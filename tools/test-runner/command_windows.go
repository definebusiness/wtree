//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var assignOwnedProcessToJob = windows.AssignProcessToJobObject

type ownedPipe struct {
	read   windows.Handle
	write  windows.Handle
	buffer bytes.Buffer
	done   chan error
}

type ownedProcessWait struct {
	exitCode uint32
	err      error
}

// runOwnedCommand creates the command suspended, assigns that suspended leader
// to a runner-owned kill-on-close Job, and only then resumes its first thread.
// Windows 8 and later support assigning a process inherited from an outer Job
// to this nested Job; an unsupported/restricted inherited-Job configuration
// fails before any user instruction can run.
func runOwnedCommand(ctx context.Context, name string, args ...string) commandResult {
	started := time.Now()
	fail := func(err error) commandResult {
		return commandResult{ErrorOutput: []byte(err.Error()), Elapsed: time.Since(started), ExitCode: 1}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	command := exec.Command(name, args...)
	if command.Err != nil {
		return fail(command.Err)
	}
	application, err := windows.UTF16PtrFromString(command.Path)
	if err != nil {
		return fail(err)
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(command.Args))
	if err != nil {
		return fail(err)
	}

	job, err := createOwnedJob()
	if err != nil {
		return fail(fmt.Errorf("create owned command Job: %w", err))
	}
	jobOpen := true
	defer func() {
		if jobOpen {
			_ = windows.CloseHandle(job)
		}
	}()

	stdout, err := createOwnedPipe()
	if err != nil {
		return fail(fmt.Errorf("create owned command stdout: %w", err))
	}
	defer stdout.close()
	stderr, err := createOwnedPipe()
	if err != nil {
		return fail(fmt.Errorf("create owned command stderr: %w", err))
	}
	defer stderr.close()
	stdin, err := openInheritedNull()
	if err != nil {
		return fail(fmt.Errorf("open owned command stdin: %w", err))
	}
	defer windows.CloseHandle(stdin)

	inherited := []windows.Handle{stdin, stdout.write, stderr.write}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return fail(fmt.Errorf("create owned command handle list: %w", err))
	}
	defer attributes.Delete()
	if err := attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&inherited[0]),
		uintptr(len(inherited))*unsafe.Sizeof(inherited[0]),
	); err != nil {
		return fail(fmt.Errorf("set owned command handle list: %w", err))
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  stdin,
			StdOutput: stdout.write,
			StdErr:    stderr.write,
		},
		ProcThreadAttributeList: attributes.List(),
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcess(
		application,
		commandLine,
		nil,
		nil,
		true,
		windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT,
		nil,
		nil,
		&startup.StartupInfo,
		&process,
	); err != nil {
		return fail(err)
	}
	runtime.KeepAlive(inherited)
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)

	// The child owns duplicates of these handles now. Closing the parent write
	// ends makes captured output reach EOF as soon as the complete Job dies.
	stdout.closeWrite()
	stderr.closeWrite()
	stdout.startReader()
	stderr.startReader()

	if err := assignOwnedProcessToJob(job, process.Process); err != nil {
		// The leader is still suspended and cannot have created a descendant.
		_ = windows.TerminateProcess(process.Process, 1)
		_, _ = windows.WaitForSingleObject(process.Process, windows.INFINITE)
		return capturedFailure(started, stdout, stderr, fmt.Errorf("assign suspended command to owned Job: %w", err))
	}
	if err := ctx.Err(); err != nil {
		_ = windows.CloseHandle(job)
		jobOpen = false
		_, _ = windows.WaitForSingleObject(process.Process, windows.INFINITE)
		return capturedFailure(started, stdout, stderr, err)
	}
	if count, err := windows.ResumeThread(process.Thread); err != nil || count != 1 {
		// Assignment succeeded, so kill-on-close covers the suspended leader and
		// anything the platform could have associated with this Job.
		_ = windows.CloseHandle(job)
		jobOpen = false
		_, _ = windows.WaitForSingleObject(process.Process, windows.INFINITE)
		if err == nil {
			err = fmt.Errorf("unexpected previous suspend count %d", count)
		}
		return capturedFailure(started, stdout, stderr, fmt.Errorf("resume owned command: %w", err))
	}

	wait := make(chan ownedProcessWait, 1)
	go func() {
		state, waitErr := windows.WaitForSingleObject(process.Process, windows.INFINITE)
		if waitErr == nil && state != windows.WAIT_OBJECT_0 {
			waitErr = fmt.Errorf("unexpected process wait state %#x", state)
		}
		outcome := ownedProcessWait{err: waitErr}
		if waitErr == nil {
			outcome.err = windows.GetExitCodeProcess(process.Process, &outcome.exitCode)
		}
		wait <- outcome
	}()

	var outcome ownedProcessWait
	select {
	case outcome = <-wait:
	case <-ctx.Done():
		// Close is the authoritative tree operation: KILL_ON_JOB_CLOSE applies
		// only to this runner-created Job and never selects unrelated processes.
		if closeErr := windows.CloseHandle(job); closeErr != nil {
			outcome.err = errors.Join(outcome.err, fmt.Errorf("close cancelled owned command Job: %w", closeErr))
		}
		jobOpen = false
		waited := <-wait
		outcome.exitCode = waited.exitCode
		outcome.err = errors.Join(outcome.err, waited.err)
	}
	if jobOpen {
		// The leader may have exited while descendants still hold inherited
		// output handles. Closing the kill-on-close Job terminates those
		// descendants before the readers are drained.
		if closeErr := windows.CloseHandle(job); closeErr != nil && outcome.err == nil {
			outcome.err = fmt.Errorf("close owned command Job: %w", closeErr)
		}
		jobOpen = false
	}

	stdoutErr := stdout.waitReader()
	stderrErr := stderr.waitReader()
	result := commandResult{
		Output:      append([]byte(nil), stdout.buffer.Bytes()...),
		ErrorOutput: append([]byte(nil), stderr.buffer.Bytes()...),
		Elapsed:     time.Since(started),
		ExitCode:    int(outcome.exitCode),
	}
	if ctx.Err() != nil && result.ExitCode == 0 {
		result.ExitCode = 1
	}
	joined := errors.Join(outcome.err, stdoutErr, stderrErr)
	if joined != nil {
		result.ExitCode = 1
		result.ErrorOutput = appendDiagnostic(result.ErrorOutput, joined)
	}
	return result
}

func createOwnedJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func createOwnedPipe() (*ownedPipe, error) {
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	pipe := &ownedPipe{}
	if err := windows.CreatePipe(&pipe.read, &pipe.write, &security, 0); err != nil {
		return nil, err
	}
	if err := windows.SetHandleInformation(pipe.read, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		pipe.close()
		return nil, err
	}
	return pipe, nil
}

func openInheritedNull() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return 0, err
	}
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	return windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		&security,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
}

func (pipe *ownedPipe) closeWrite() {
	if pipe.write != 0 {
		_ = windows.CloseHandle(pipe.write)
		pipe.write = 0
	}
}

func (pipe *ownedPipe) startReader() {
	pipe.done = make(chan error, 1)
	file := os.NewFile(uintptr(pipe.read), "owned-command-output")
	pipe.read = 0
	go func() {
		_, err := io.Copy(&pipe.buffer, file)
		closeErr := file.Close()
		pipe.done <- errors.Join(err, closeErr)
	}()
}

func (pipe *ownedPipe) waitReader() error {
	if pipe.done == nil {
		return nil
	}
	err := <-pipe.done
	pipe.done = nil
	return err
}

func (pipe *ownedPipe) close() {
	pipe.closeWrite()
	if pipe.read != 0 {
		_ = windows.CloseHandle(pipe.read)
		pipe.read = 0
	}
}

func capturedFailure(started time.Time, stdout, stderr *ownedPipe, cause error) commandResult {
	stdoutErr := stdout.waitReader()
	stderrErr := stderr.waitReader()
	return commandResult{
		Output:      append([]byte(nil), stdout.buffer.Bytes()...),
		ErrorOutput: appendDiagnostic(append([]byte(nil), stderr.buffer.Bytes()...), errors.Join(cause, stdoutErr, stderrErr)),
		Elapsed:     time.Since(started),
		ExitCode:    1,
	}
}

func appendDiagnostic(output []byte, err error) []byte {
	if err == nil {
		return output
	}
	if len(output) != 0 && output[len(output)-1] != '\n' {
		output = append(output, '\n')
	}
	return append(output, err.Error()...)
}
