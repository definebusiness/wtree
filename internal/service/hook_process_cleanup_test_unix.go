//go:build !windows

package service

import (
	"errors"
	"os/exec"
	"testing"
)

func installHookWriterCleanupFailure(t *testing.T) func() int {
	t.Helper()
	original, attempts := directProcessTerminate, 0
	directProcessTerminate = func(*exec.Cmd) error {
		attempts++
		return errors.New("injected termination failure")
	}
	t.Cleanup(func() { directProcessTerminate = original })
	return func() int { return attempts }
}
