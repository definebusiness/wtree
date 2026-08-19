//go:build linux

package fsutil

import "golang.org/x/sys/unix"

// RenameNoReplace atomically renames oldPath only when newPath is absent.
func RenameNoReplace(oldPath, newPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
}
