//go:build !windows

package fsutil

import (
	"errors"
	"os"
	"syscall"
)

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return Sync(directory)
}

func isUnsupportedSyncError(err error) bool {
	return errors.Is(err, syscall.ENOTTY) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP)
}
