//go:build windows

package fsutil

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestWindowsUnsupportedSyncErrors(t *testing.T) {
	for _, err := range []error{
		errorInvalidFunction,
		syscall.Errno(50),  // ERROR_NOT_SUPPORTED
		syscall.Errno(120), // ERROR_CALL_NOT_IMPLEMENTED
		fmt.Errorf("sync directory: %w", errorInvalidFunction),
	} {
		if !isUnsupportedSyncError(err) {
			t.Errorf("isUnsupportedSyncError(%v) = false", err)
		}
	}
	for _, err := range []error{
		syscall.ERROR_ACCESS_DENIED,
		syscall.Errno(6),    // ERROR_INVALID_HANDLE
		syscall.Errno(1117), // ERROR_IO_DEVICE
		errors.New("disk failure"),
	} {
		if isUnsupportedSyncError(err) {
			t.Errorf("isUnsupportedSyncError(%v) = true", err)
		}
	}
}
