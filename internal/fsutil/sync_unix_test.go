//go:build !windows

package fsutil

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestUnsupportedSyncErrors(t *testing.T) {
	for _, err := range []error{
		syscall.ENOTTY,
		syscall.EINVAL,
		syscall.ENOTSUP,
		fmt.Errorf("sync file: %w", syscall.ENOTTY),
	} {
		if !isUnsupportedSyncError(err) {
			t.Errorf("isUnsupportedSyncError(%v) = false", err)
		}
	}
	if isUnsupportedSyncError(errors.New("disk failure")) {
		t.Error("ordinary I/O error treated as unsupported sync")
	}
}
