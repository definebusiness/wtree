//go:build !windows

package fsutil

import (
	"errors"
	"syscall"
)

func isUnsupportedSyncError(err error) bool {
	return errors.Is(err, syscall.ENOTTY) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP)
}
