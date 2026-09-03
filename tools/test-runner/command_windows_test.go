//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsOwnedCommandHelper(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	args := os.Args[separator+1:]
	switch args[0] {
	case "mark":
		if err := os.WriteFile(args[1], []byte("executed"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "child":
		fmt.Fprintln(os.Stdout, "owned-child-started")
		fmt.Fprintln(os.Stderr, "owned-child-stderr")
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		if err := os.WriteFile(args[2], []byte("escaped"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "spawn-exit", "spawn-wait":
		command := exec.Command(os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "child", args[1], args[2])
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, args[1], 5*time.Second)
		fmt.Fprintln(os.Stdout, "owned-leader-exiting")
		if args[0] == "spawn-wait" {
			if err := command.Wait(); err != nil {
				t.Fatal(err)
			}
		}
	case "nested-controller":
		result := runOwnedCommand(context.Background(), os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "nested-child")
		if result.ExitCode != 0 || !strings.Contains(string(result.Output), "nested-owned-child") {
			t.Fatalf("nested result=%#v", result)
		}
		fmt.Fprintln(os.Stdout, "nested-controller-ok")
	case "nested-child":
		fmt.Fprintln(os.Stdout, "nested-owned-child")
	case "exit-seven":
		fmt.Fprintln(os.Stdout, "exit-seven-output")
		fmt.Fprintln(os.Stderr, "exit-seven-error")
		os.Exit(7)
	case "control":
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
	default:
		t.Fatalf("unknown helper mode %q", args[0])
	}
}

func TestWindowsOwnedCommandAssignsBeforeUserCode(t *testing.T) {
	originalAssign := assignOwnedProcessToJob
	assignOwnedProcessToJob = func(windows.Handle, windows.Handle) error { return windows.ERROR_ACCESS_DENIED }
	defer func() { assignOwnedProcessToJob = originalAssign }()

	marker := filepath.Join(t.TempDir(), "executed")
	result := runOwnedCommand(context.Background(), os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "mark", marker)
	if result.ExitCode == 0 || !strings.Contains(string(result.ErrorOutput), "assign suspended command") {
		t.Fatalf("assignment failure result=%#v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("user command executed before assignment: %v", err)
	}
}

func TestWindowsOwnedCommandFastLeaderExitKillsDescendantAndDrainsOutput(t *testing.T) {
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "child.pid")
	escapePath := filepath.Join(directory, "escaped")
	started := time.Now()
	result := runOwnedCommand(context.Background(), os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "spawn-exit", pidPath, escapePath)
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("fast leader waited for long descendant: %v", elapsed)
	}
	if result.ExitCode != 0 || !strings.Contains(string(result.Output), "owned-leader-exiting") || !strings.Contains(string(result.Output), "owned-child-started") || !strings.Contains(string(result.ErrorOutput), "owned-child-stderr") {
		t.Fatalf("output/exit result=%#v", result)
	}
	assertRecordedProcessExited(t, pidPath, 5*time.Second)
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(escapePath); !os.IsNotExist(err) {
		t.Fatalf("owned descendant escaped Job: %v", err)
	}
}

func TestWindowsOwnedCommandCancellationKillsTreeNotUnrelatedProcess(t *testing.T) {
	directory := t.TempDir()
	controlPID := filepath.Join(directory, "control.pid")
	control := exec.Command(os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "control", controlPID)
	if err := control.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = control.Process.Kill()
		_ = control.Wait()
	})
	waitForFile(t, controlPID, 5*time.Second)

	ownedPID := filepath.Join(directory, "owned.pid")
	escapePath := filepath.Join(directory, "escaped")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan commandResult, 1)
	go func() {
		done <- runOwnedCommand(ctx, os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "spawn-wait", ownedPID, escapePath)
	}()
	waitForFile(t, ownedPID, 5*time.Second)
	cancel()
	select {
	case result := <-done:
		if result.ExitCode == 0 {
			t.Fatalf("cancelled result=%#v", result)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled owned tree did not drain")
	}
	assertRecordedProcessExited(t, ownedPID, 5*time.Second)
	controlHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(control.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(controlHandle)
	if state, err := windows.WaitForSingleObject(controlHandle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("unrelated control was targeted: state=%#x error=%v", state, err)
	}
}

func TestWindowsOwnedCommandSupportsNestedJob(t *testing.T) {
	result := runOwnedCommand(context.Background(), os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "nested-controller")
	if result.ExitCode != 0 || !strings.Contains(string(result.Output), "nested-controller-ok") {
		t.Fatalf("nested Job result=%#v", result)
	}
}

func TestWindowsOwnedCommandPreservesExitAndOutput(t *testing.T) {
	result := runOwnedCommand(context.Background(), os.Args[0], "-test.run=^TestWindowsOwnedCommandHelper$", "--", "exit-seven")
	if result.ExitCode != 7 || !strings.Contains(string(result.Output), "exit-seven-output") || !strings.Contains(string(result.ErrorOutput), "exit-seven-error") || result.Elapsed <= 0 {
		t.Fatalf("exit/output/timing result=%#v", result)
	}
}

func TestWindowsOwnedCommandLaunchFailure(t *testing.T) {
	result := runOwnedCommand(context.Background(), filepath.Join(t.TempDir(), "missing-command.exe"))
	if result.ExitCode == 0 || len(result.ErrorOutput) == 0 {
		t.Fatalf("launch failure result=%#v", result)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q", path)
}

func assertRecordedProcessExited(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, uint32(timeout/time.Millisecond))
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("process %d survived: state=%#x error=%v", pid, state, err)
	}
}
