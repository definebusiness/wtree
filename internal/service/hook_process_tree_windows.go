//go:build windows

package service

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type hookWindowsProcessOperations struct {
	beforeJobAssignment      func(*exec.Cmd)
	createJobObject          func(*windows.SecurityAttributes, *uint16) (windows.Handle, error)
	assignProcessToJobObject func(windows.Handle, windows.Handle) error
	createThreadSnapshot     func(uint32, uint32) (windows.Handle, error)
	thread32First            func(windows.Handle, *windows.ThreadEntry32) error
	thread32Next             func(windows.Handle, *windows.ThreadEntry32) error
	openThread               func(uint32, bool, uint32) (windows.Handle, error)
	resumeThread             func(windows.Handle) (uint32, error)
	terminateJobObject       func(windows.Handle, uint32) error
	closeHandle              func(windows.Handle) error
}

var hookWindowsProcessOps = hookWindowsProcessOperations{
	beforeJobAssignment:      func(*exec.Cmd) {},
	createJobObject:          windows.CreateJobObject,
	assignProcessToJobObject: windows.AssignProcessToJobObject,
	createThreadSnapshot:     windows.CreateToolhelp32Snapshot,
	thread32First:            windows.Thread32First,
	thread32Next:             windows.Thread32Next,
	openThread:               windows.OpenThread,
	resumeThread:             windows.ResumeThread,
	terminateJobObject:       windows.TerminateJobObject,
	closeHandle:              windows.CloseHandle,
}

// configureHookProcess is deliberately separate from configureDirectProcess:
// only hook launch owns the suspended-start/Job/resume lifecycle.
func configureHookProcess(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

// hookProcessTree is a private job object. Its membership retains process-tree
// authority after the direct leader exits, unlike taskkill by the leader PID.
type hookProcessTree struct {
	command    *exec.Cmd
	job        windows.Handle
	assigned   bool
	operations hookWindowsProcessOperations
}

func beginHookProcessTree(command *exec.Cmd) (hookProcessTree, error) {
	operations := hookWindowsProcessOps
	tree := hookProcessTree{command: command, operations: operations}
	operations.beforeJobAssignment(command)
	job, err := operations.createJobObject(nil, nil)
	if err != nil {
		return tree, fmt.Errorf("create hook process Job: %w", err)
	}
	tree.job = job
	var assignmentErr error
	handleErr := command.Process.WithHandle(func(handle uintptr) {
		assignmentErr = operations.assignProcessToJobObject(job, windows.Handle(handle))
	})
	if handleErr != nil {
		assignmentErr = handleErr
	}
	if assignmentErr != nil {
		return tree, fmt.Errorf("assign suspended hook process to Job: %w", assignmentErr)
	}
	tree.assigned = true
	thread, err := hookWindowsInitialThread(operations, uint32(command.Process.Pid))
	if err != nil {
		return tree, err
	}
	previous, resumeErr := operations.resumeThread(thread)
	closeErr := operations.closeHandle(thread)
	if resumeErr != nil {
		return tree, errors.Join(fmt.Errorf("resume suspended hook process thread: %w", resumeErr), closeErr)
	}
	if previous != 1 {
		return tree, errors.Join(fmt.Errorf("resume suspended hook process thread: unexpected suspension count %d", previous), closeErr)
	}
	if closeErr != nil {
		return tree, fmt.Errorf("close resumed hook process thread: %w", closeErr)
	}
	return tree, nil
}

func hookWindowsInitialThread(operations hookWindowsProcessOperations, processID uint32) (windows.Handle, error) {
	// os/exec closes CreateProcess's initial-thread handle before Cmd.Start
	// returns. CREATE_SUSPENDED prevents the loader or user code from creating
	// another thread, so a Toolhelp snapshot must contain exactly one thread
	// whose recorded owner is the new process before it is safe to resume.
	snapshot, err := operations.createThreadSnapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, fmt.Errorf("snapshot suspended hook process threads: %w", err)
	}
	closeSnapshot := func() error { return operations.closeHandle(snapshot) }
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := operations.thread32First(snapshot, &entry); err != nil {
		return 0, errors.Join(fmt.Errorf("enumerate suspended hook process threads: %w", err), closeSnapshot())
	}
	var threadID uint32
	for {
		if entry.OwnerProcessID == processID {
			if threadID != 0 {
				return 0, errors.Join(errors.New("suspended hook process has multiple initial threads"), closeSnapshot())
			}
			threadID = entry.ThreadID
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		err = operations.thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return 0, errors.Join(fmt.Errorf("enumerate suspended hook process threads: %w", err), closeSnapshot())
		}
	}
	if threadID == 0 {
		return 0, errors.Join(errors.New("suspended hook process initial thread not found"), closeSnapshot())
	}
	thread, openErr := operations.openThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	closeErr := closeSnapshot()
	if openErr != nil {
		return 0, errors.Join(fmt.Errorf("open suspended hook process initial thread: %w", openErr), closeErr)
	}
	if closeErr != nil {
		return 0, errors.Join(fmt.Errorf("close hook process thread snapshot: %w", closeErr), operations.closeHandle(thread))
	}
	return thread, nil
}

func (tree hookProcessTree) Terminate() error {
	var jobErr error
	if tree.assigned && tree.job != 0 {
		jobErr = tree.operations.terminateJobObject(tree.job, 1)
		if jobErr == nil {
			return nil
		}
	}
	directErr := tree.command.Process.Kill()
	if directErr == nil {
		return jobErr
	}
	if jobErr == nil {
		return fmt.Errorf("terminate suspended hook process: %w", directErr)
	}
	return fmt.Errorf("terminate suspended hook process: %v; terminate hook Job: %w", directErr, jobErr)
}

func (tree hookProcessTree) Close() error {
	if tree.job == 0 {
		return nil
	}
	return tree.operations.closeHandle(tree.job)
}
