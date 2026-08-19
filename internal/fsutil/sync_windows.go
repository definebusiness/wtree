//go:build windows

package fsutil

import (
	"errors"
	"syscall"
)

const errorInvalidFunction syscall.Errno = 1

// Windows has no portable equivalent of POSIX directory fsync. Opening a
// directory and calling FlushFileBuffers can return ERROR_ACCESS_DENIED even
// after a successful atomic replacement. The temporary regular file is still
// flushed before rename, and injected directory-boundary failures are handled
// by the caller before this capability boundary.
func syncDirectory(path string) error {
	return openAndCloseDirectory(path, openDirectoryHandle)
}

// Some Windows-backed filesystems reject FlushFileBuffers with
// ERROR_INVALID_FUNCTION. That is a capability limitation; permission,
// handle, and I/O failures remain fatal. Directory durability is handled at
// the separate syncDirectory capability boundary above.
func isUnsupportedSyncError(err error) bool {
	return errors.Is(err, errorInvalidFunction) || errors.Is(err, errors.ErrUnsupported)
}
