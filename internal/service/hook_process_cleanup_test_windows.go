//go:build windows

package service

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func installHookWriterCleanupFailure(t *testing.T) func() int {
	t.Helper()
	original, attempts := hookWindowsProcessOps, 0
	ops := original
	ops.terminateJobObject = func(windows.Handle, uint32) error {
		attempts++
		return errors.New("injected termination failure")
	}
	hookWindowsProcessOps = ops
	t.Cleanup(func() { hookWindowsProcessOps = original })
	return func() int { return attempts }
}

// hookWriterCleanupTerminationAttempts reflects the Windows contract: a Job
// termination failure falls back to a direct leader kill, rather than retrying
// the same Job termination primitive.
func hookWriterCleanupTerminationAttempts() int { return 1 }
