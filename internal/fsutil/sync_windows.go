//go:build windows

package fsutil

import (
	"errors"
	"syscall"
)

const errorInvalidFunction syscall.Errno = 1

// Windows has no portable equivalent of POSIX directory fsync. The temporary
// regular file is already flushed before rename; the publication boundary here
// validates the containing directory handle without FlushFileBuffers.
func syncDirectory(path string) error {
	return openAndCloseDirectory(path, openDirectoryHandle)
}

// Some Windows-backed filesystems reject FlushFileBuffers with
// ERROR_INVALID_FUNCTION. That is a capability limitation; permission,
// handle, and I/O failures remain fatal.
func isUnsupportedSyncError(err error) bool {
	return errors.Is(err, errorInvalidFunction) || errors.Is(err, errors.ErrUnsupported)
}
