//go:build windows

package fsutil

import (
	"errors"
	"syscall"
)

const errorInvalidFunction syscall.Errno = 1

// Windows filesystems can reject FlushFileBuffers for a directory with
// ERROR_INVALID_FUNCTION. That is a capability limitation; permission,
// handle, and I/O failures remain fatal.
func isUnsupportedSyncError(err error) bool {
	return errors.Is(err, errorInvalidFunction) || errors.Is(err, errors.ErrUnsupported)
}
