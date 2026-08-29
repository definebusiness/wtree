//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func protectPrivateUpdateDirectory(path string, info os.FileInfo) error {
	return withPrivateUpdateDirectoryHandle(path, info, true)
}

func validatePrivateUpdateDirectory(path string, info os.FileInfo) error {
	return withPrivateUpdateDirectoryHandle(path, info, false)
}

func withPrivateUpdateDirectoryHandle(path string, expected os.FileInfo, protect bool) error {
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return errors.Join(errors.New("read effective Windows token user"), err)
	}
	return withPrivateUpdateDirectoryHandleForUser(path, expected, protect, user.User.Sid.String())
}

func withPrivateUpdateDirectoryHandleForUser(path string, expected os.FileInfo, protect bool, userString string) error {
	return withPrivateUpdateDirectoryHandleForUserAndTypeCheck(path, expected, protect, userString, windowsDirectoryHandleIsPlain)
}

func withPrivateUpdateDirectoryHandleForUserAndTypeCheck(path string, expected os.FileInfo, protect bool, userString string, handleIsPlain func(windows.Handle) bool) error {
	if expected == nil || !expected.IsDir() || expected.Mode()&os.ModeSymlink != 0 {
		return errors.New("update directory is not a plain directory")
	}
	if handleIsPlain == nil {
		return errors.New("update directory type validator is not configured")
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL)
	if protect {
		access |= windows.WRITE_DAC
	}
	handle, err := windows.CreateFile(
		pointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("open private update directory: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		windows.CloseHandle(handle)
		return errors.New("adopt private update directory handle")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !handleIsPlain(handle) || !os.SameFile(expected, actual) {
		return errors.Join(errors.New("private update directory handle identity or type differs"), err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, pathInfo) || !os.SameFile(actual, pathInfo) {
		return errors.Join(errors.New("private update directory path identity or type differs"), err)
	}
	if protect {
		currentDescriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			return fmt.Errorf("read private update directory owner: %w", err)
		}
		currentOwner, defaulted, err := currentDescriptor.Owner()
		if err != nil || currentOwner == nil || defaulted || currentOwner.String() != userString {
			return errors.Join(errors.New("private update directory owner differs from effective user"), err)
		}
		descriptor, err := windows.SecurityDescriptorFromString("O:" + userString + "D:P(A;OICI;FA;;;" + userString + ")")
		if err != nil {
			return fmt.Errorf("build private update directory descriptor: %w", err)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil {
			return fmt.Errorf("read private update directory DACL: %w", err)
		}
		if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
			return fmt.Errorf("protect private update directory: %w", err)
		}
	}
	return validateWindowsPrivateDirectoryHandle(handle, userString, true)
}
