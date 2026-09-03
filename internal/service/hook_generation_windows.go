//go:build windows

package service

import (
	"os"
	"syscall"
)

// openHookGenerationFile retains a read handle while allowing another handle
// to atomically replace its name. os.Open omits FILE_SHARE_DELETE on Windows,
// which would otherwise make the generation guard prevent its own publication.
func openHookGenerationFile(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, syscall.EINVAL
	}
	return file, nil
}
