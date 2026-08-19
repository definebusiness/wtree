//go:build windows

package fsutil

import "golang.org/x/sys/windows"

// RenameNoReplace atomically renames oldPath only when newPath is absent.
func RenameNoReplace(oldPath, newPath string) error {
	oldPointer, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPointer, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(oldPointer, newPointer, windows.MOVEFILE_WRITE_THROUGH)
}
