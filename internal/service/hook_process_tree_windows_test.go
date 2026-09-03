//go:build windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestHookProcessWindowsSuspendedUntilJobOwnership(t *testing.T) {
	if marker := os.Getenv("WTREE_HOOK_WINDOWS_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("executed"), 0o600); err != nil {
			os.Exit(2)
		}
		return
	}

	original := hookWindowsProcessOps
	defer func() { hookWindowsProcessOps = original }()
	reached := make(chan struct{})
	release := make(chan struct{})
	ops := original
	ops.beforeJobAssignment = func(*exec.Cmd) {
		close(reached)
		<-release
	}
	hookWindowsProcessOps = ops

	marker := filepath.Join(t.TempDir(), "executed")
	request := HookProcessRequest{
		Program:     os.Args[0],
		Arguments:   []string{"-test.run=^TestHookProcessWindowsSuspendedUntilJobOwnership$"},
		Directory:   mustHookTestDirectory(t),
		Environment: append(os.Environ(), "WTREE_HOOK_WINDOWS_MARKER="+marker),
		Timeout:     5 * time.Second,
		Event:       "post-create",
		HookID:      "setup",
	}
	ctx := t.Context()
	done := make(chan struct {
		result HookProcessResult
		err    error
	}, 1)
	go func() {
		result, err := newHookProcessAdapter().Run(ctx, request)
		done <- struct {
			result HookProcessResult
			err    error
		}{result, err}
	}()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("hook process did not reach the pre-assignment barrier")
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook user code ran before Job assignment and initial-thread resume")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	close(release)
	select {
	case outcome := <-done:
		if outcome.err != nil || !outcome.result.Started || outcome.result.ExitCode != 0 {
			t.Fatalf("Run() = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resumed hook process did not finish")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("hook user code did not run after Job assignment and resume: %v", err)
	}
}

func TestHookProcessWindowsSuspensionIsHookSpecific(t *testing.T) {
	direct := &exec.Cmd{}
	configureDirectProcess(direct)
	if direct.SysProcAttr != nil && direct.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED != 0 {
		t.Fatal("shared aggregate process configuration unexpectedly suspends its process")
	}
	hook := &exec.Cmd{}
	configureHookProcess(hook)
	if hook.SysProcAttr == nil || hook.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatal("hook process configuration did not request suspended creation")
	}
}

func TestHookProcessTreeWindowsJobTerminationFailureFallsBackOnce(t *testing.T) {
	jobFailure := errors.New("injected Job termination failure")
	jobCalls, leaderCalls := 0, 0
	tree := hookProcessTree{
		command:  &exec.Cmd{},
		job:      windows.Handle(1),
		assigned: true,
		operations: hookWindowsProcessOperations{
			terminateJobObject: func(windows.Handle, uint32) error {
				jobCalls++
				return jobFailure
			},
			killProcess: func(*exec.Cmd) error {
				leaderCalls++
				return nil
			},
		},
	}
	if err := tree.Terminate(); !errors.Is(err, jobFailure) {
		t.Fatalf("Terminate() = %v, want Job termination failure", err)
	}
	if jobCalls != 1 || leaderCalls != 1 {
		t.Fatalf("Job calls=%d leader-kill calls=%d, want one each", jobCalls, leaderCalls)
	}
}

func TestHookProcessWindowsSetupFailuresTerminateAndReap(t *testing.T) {
	switch os.Getenv("WTREE_HOOK_WINDOWS_FAILURE_HELPER") {
	case "leader":
		if err := os.WriteFile(os.Getenv("WTREE_HOOK_WINDOWS_START_MARKER"), []byte("started"), 0o600); err != nil {
			os.Exit(2)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestHookProcessWindowsSetupFailuresTerminateAndReap$")
		child.Env = hookWindowsTestEnvironment("child")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		time.Sleep(2 * time.Second)
		return
	case "child":
		time.Sleep(250 * time.Millisecond)
		if err := os.WriteFile(os.Getenv("WTREE_HOOK_WINDOWS_ESCAPE_MARKER"), []byte("escaped"), 0o600); err != nil {
			os.Exit(4)
		}
		return
	}

	injected := errors.New("injected hook process setup failure")
	tests := []struct {
		name           string
		expectedCloses int
		inject         func(*hookWindowsProcessOperations)
	}{
		{name: "create-job", inject: func(ops *hookWindowsProcessOperations) {
			ops.createJobObject = func(*windows.SecurityAttributes, *uint16) (windows.Handle, error) { return 0, injected }
		}},
		{name: "assign-job", expectedCloses: 1, inject: func(ops *hookWindowsProcessOperations) {
			ops.assignProcessToJobObject = func(windows.Handle, windows.Handle) error { return injected }
		}},
		{name: "enumerate-thread", expectedCloses: 2, inject: func(ops *hookWindowsProcessOperations) {
			ops.thread32First = func(windows.Handle, *windows.ThreadEntry32) error { return injected }
		}},
		{name: "open-thread", expectedCloses: 2, inject: func(ops *hookWindowsProcessOperations) {
			ops.openThread = func(uint32, bool, uint32) (windows.Handle, error) { return 0, injected }
		}},
		{name: "resume-thread", expectedCloses: 3, inject: func(ops *hookWindowsProcessOperations) {
			ops.resumeThread = func(windows.Handle) (uint32, error) { return ^uint32(0), injected }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := hookWindowsProcessOps
			originalWait := hookProcessWait
			defer func() {
				hookWindowsProcessOps = original
				hookProcessWait = originalWait
			}()
			started := make(chan *exec.Cmd, 1)
			closed := 0
			waits := 0
			ops := original
			ops.beforeJobAssignment = func(command *exec.Cmd) { started <- command }
			ops.closeHandle = func(handle windows.Handle) error {
				closed++
				return windows.CloseHandle(handle)
			}
			test.inject(&ops)
			hookWindowsProcessOps = ops
			hookProcessWait = func(command *exec.Cmd) error {
				waits++
				return originalWait(command)
			}

			directory := t.TempDir()
			startMarker := filepath.Join(directory, "started")
			escapeMarker := filepath.Join(directory, "escaped")
			before := time.Now()
			result, err := newHookProcessAdapter().Run(t.Context(), HookProcessRequest{
				Program:   os.Args[0],
				Arguments: []string{"-test.run=^TestHookProcessWindowsSetupFailuresTerminateAndReap$"},
				Directory: mustHookTestDirectory(t),
				Environment: append(os.Environ(),
					"WTREE_HOOK_WINDOWS_FAILURE_HELPER=leader",
					"WTREE_HOOK_WINDOWS_START_MARKER="+startMarker,
					"WTREE_HOOK_WINDOWS_ESCAPE_MARKER="+escapeMarker,
				),
				Timeout: 5 * time.Second,
				Event:   "post-create",
				HookID:  "setup",
			})
			if err == nil || !result.Started || time.Since(before) > 2*time.Second {
				t.Fatalf("Run() = %#v, %v after %s", result, err, time.Since(before))
			}
			command := <-started
			if command.ProcessState == nil {
				t.Fatal("failed suspended hook process was not reaped")
			}
			if waits != 1 {
				t.Fatalf("Wait calls = %d, want exactly one", waits)
			}
			if closed != test.expectedCloses {
				t.Fatalf("closed handles = %d, want %d", closed, test.expectedCloses)
			}
			time.Sleep(500 * time.Millisecond)
			for _, marker := range []string{startMarker, escapeMarker} {
				if _, statErr := os.Stat(marker); statErr == nil {
					t.Fatalf("user code escaped failed suspended launch: %s", filepath.Base(marker))
				} else if !os.IsNotExist(statErr) {
					t.Fatal(statErr)
				}
			}
			if strings.Contains(err.Error(), directory) {
				t.Fatalf("launch failure leaks hook path: %v", err)
			}
		})
	}
}

func hookWindowsTestEnvironment(mode string) []string {
	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "WTREE_HOOK_WINDOWS_FAILURE_HELPER=") &&
			!strings.HasPrefix(entry, "WTREE_HOOK_WINDOWS_START_MARKER=") &&
			!strings.HasPrefix(entry, "WTREE_HOOK_WINDOWS_ESCAPE_MARKER=") {
			environment = append(environment, entry)
		}
	}
	return append(environment,
		"WTREE_HOOK_WINDOWS_FAILURE_HELPER="+mode,
		"WTREE_HOOK_WINDOWS_START_MARKER="+os.Getenv("WTREE_HOOK_WINDOWS_START_MARKER"),
		"WTREE_HOOK_WINDOWS_ESCAPE_MARKER="+os.Getenv("WTREE_HOOK_WINDOWS_ESCAPE_MARKER"),
	)
}

// These are native Windows lifecycle checks. The common hook-process tests
// exercise the same leader-exit/inherited-writer sequence on every platform;
// this test specifically proves that a Job survives leader Wait, Close alone
// does not kill a normally released job, and explicit Terminate kills it.
func TestHookProcessTreeWindowsLifecycleAfterLeaderExit(t *testing.T) {
	start := func(marker string) (*exec.Cmd, hookProcessTree) {
		t.Helper()
		command := exec.Command(os.Args[0], "-test.run=TestHookProcessStreamsContextualOutput")
		command.Env = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=exit-with-output-child", "WTREE_HOOK_PROCESS_MARKER="+marker)
		configureHookProcess(command)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		tree, err := beginHookProcessTree(command)
		if err != nil {
			_ = command.Process.Kill()
			t.Fatal(err)
		}
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
		return command, tree
	}

	t.Run("close-releases-without-termination", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "completed")
		_, tree := start(marker)
		if err := tree.Close(); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(marker); err == nil {
				return
			} else if time.Now().After(deadline) {
				t.Fatalf("child did not continue after normal Close: %v", err)
			}
		}
	})

	t.Run("terminate-remains-authoritative-after-leader-exit", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "escaped")
		_, tree := start(marker)
		defer tree.Close()
		if err := tree.Terminate(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1700 * time.Millisecond)
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("job child survived termination after leader exit")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})
}
