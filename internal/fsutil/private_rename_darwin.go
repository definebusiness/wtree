//go:build darwin

package fsutil

import "golang.org/x/sys/unix"

func privateRenameNoReplace(directory int, oldName, newName string) error {
	return unix.RenameatxNp(directory, oldName, directory, newName, unix.RENAME_EXCL)
}
