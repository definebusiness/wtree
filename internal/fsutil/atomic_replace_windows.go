//go:build windows

package fsutil

import "golang.org/x/sys/windows"

// atomicReplace is the replacement boundary used by the portable atomic
// writer. os.Rename does not replace an existing destination on Windows.
func atomicReplace(source, destination string) error {
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePointer, destinationPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
