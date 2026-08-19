//go:build darwin

package fsutil

import "golang.org/x/sys/unix"

// RenameNoReplace atomically renames oldPath only when newPath is absent.
func RenameNoReplace(oldPath, newPath string) error {
	return unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
}
