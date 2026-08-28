//go:build !windows

package service

import (
	"errors"
	"os"
)

func protectPrivateUpdateDirectory(_ string, info os.FileInfo) error {
	return validatePrivateUpdateDirectory("", info)
}

func validatePrivateUpdateDirectory(_ string, info os.FileInfo) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("update directory is not private")
	}
	return nil
}
