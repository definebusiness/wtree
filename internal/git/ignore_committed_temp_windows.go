//go:build windows

package git

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsCommittedIgnoreAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func createPrivateCommittedIgnoreTemp(pattern string) (*committedIgnoreTemp, error) {
	temporaryRoot := filepath.Clean(os.TempDir())
	rootInfo, err := os.Lstat(temporaryRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(errors.New("committed ignore temporary root is not a plain directory"), err)
	}
	rootPointer, err := windows.UTF16PtrFromString(temporaryRoot)
	if err != nil {
		return nil, err
	}
	rootHandle, err := windows.CreateFile(
		rootPointer,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open committed ignore temporary root: %w", err)
	}
	root := os.NewFile(uintptr(rootHandle), temporaryRoot)
	if root == nil {
		windows.CloseHandle(rootHandle)
		return nil, errors.New("adopt committed ignore temporary root handle")
	}
	rootHandleInfo, err := root.Stat()
	if err != nil || !windowsCommittedIgnoreHandleIsPlain(rootHandle, true) || !os.SameFile(rootInfo, rootHandleInfo) {
		return nil, errors.Join(errors.New("committed ignore temporary root handle differs"), err, root.Close())
	}
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, errors.Join(errors.New("read effective Windows token user"), err, root.Close())
	}
	userString := user.User.Sid.String()
	containerDescriptor, err := windows.SecurityDescriptorFromString("O:" + userString + "D:P(A;OICI;FA;;;" + userString + ")")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build committed ignore container descriptor: %w", err), root.Close())
	}
	fileDescriptor, err := windows.SecurityDescriptorFromString("O:" + userString + "D:P(A;;FA;;;" + userString + ")")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build committed ignore file descriptor: %w", err), root.Close())
	}
	var container *os.File
	var containerHandle windows.Handle
	var containerPath string
	closeLease := func(delete bool) error {
		var result error
		if container != nil {
			if delete {
				result = markWindowsCommittedIgnoreHandleDeleted(containerHandle)
			}
			result = errors.Join(result, container.Close())
			container = nil
		}
		result = errors.Join(result, root.Close())
		return result
	}
	for attempt := 0; attempt < 128; attempt++ {
		leaf, randomErr := windowsCommittedIgnoreLeaf(pattern)
		if randomErr != nil {
			return nil, errors.Join(randomErr, root.Close())
		}
		var status windows.IO_STATUS_BLOCK
		containerHandle, status, err = createWindowsCommittedIgnoreObject(rootHandle, leaf, true, containerDescriptor)
		if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) || errors.Is(err, windows.STATUS_OBJECT_NAME_EXISTS) {
			continue
		}
		if err != nil {
			return nil, errors.Join(fmt.Errorf("create private committed ignore container: %w", err), root.Close())
		}
		if status.Information != 2 {
			windows.CloseHandle(containerHandle)
			return nil, errors.Join(fmt.Errorf("committed ignore container creation status = %d, want created", status.Information), root.Close())
		}
		containerPath = filepath.Join(temporaryRoot, leaf)
		container = os.NewFile(uintptr(containerHandle), containerPath)
		if container == nil {
			markErr := markWindowsCommittedIgnoreHandleDeleted(containerHandle)
			return nil, errors.Join(errors.New("adopt committed ignore container handle"), markErr, windows.CloseHandle(containerHandle), root.Close())
		}
		break
	}
	if container == nil {
		return nil, errors.Join(errors.New("allocate collision-free committed ignore container name"), root.Close())
	}
	if err := validateWindowsCommittedIgnoreHandle(containerHandle, userString, true); err != nil {
		return nil, errors.Join(err, closeLease(true))
	}
	containerInfo, err := container.Stat()
	containerPathInfo, pathErr := os.Lstat(containerPath)
	rootPathInfo, rootPathErr := os.Lstat(temporaryRoot)
	if err != nil || pathErr != nil || rootPathErr != nil || !os.SameFile(containerInfo, containerPathInfo) || !os.SameFile(rootInfo, rootPathInfo) || !os.SameFile(rootHandleInfo, rootPathInfo) {
		return nil, errors.Join(errors.New("committed ignore container or root path identity differs"), err, pathErr, rootPathErr, closeLease(true))
	}
	filePath := filepath.Join(containerPath, "exclude")
	fileHandle, status, err := createWindowsCommittedIgnoreObject(containerHandle, "exclude", false, fileDescriptor)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create private committed ignore exclude: %w", err), closeLease(true))
	}
	if status.Information != 2 {
		markErr := markWindowsCommittedIgnoreHandleDeleted(fileHandle)
		return nil, errors.Join(fmt.Errorf("committed ignore file creation status = %d, want created", status.Information), markErr, windows.CloseHandle(fileHandle), closeLease(true))
	}
	file := os.NewFile(uintptr(fileHandle), filePath)
	if file == nil {
		markErr := markWindowsCommittedIgnoreHandleDeleted(fileHandle)
		return nil, errors.Join(errors.New("adopt private committed ignore exclude handle"), markErr, windows.CloseHandle(fileHandle), closeLease(true))
	}
	if err := validateWindowsCommittedIgnoreHandle(fileHandle, userString, false); err != nil {
		markErr := markWindowsCommittedIgnoreHandleDeleted(fileHandle)
		return nil, errors.Join(err, markErr, file.Close(), closeLease(true))
	}
	fileInfo, err := file.Stat()
	filePathInfo, pathErr := os.Lstat(filePath)
	if err != nil || pathErr != nil || !os.SameFile(fileInfo, filePathInfo) || !filePathInfo.Mode().IsRegular() || filePathInfo.Mode()&os.ModeSymlink != 0 {
		markErr := markWindowsCommittedIgnoreHandleDeleted(fileHandle)
		return nil, errors.Join(errors.New("committed ignore file path identity or type differs"), err, pathErr, markErr, file.Close(), closeLease(true))
	}
	cleanup := func() error {
		if err := os.Remove(filePath); err != nil {
			return errors.Join(err, closeLease(false))
		}
		return closeLease(true)
	}
	return &committedIgnoreTemp{file: file, cleanup: cleanup}, nil
}

func windowsCommittedIgnoreLeaf(pattern string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate committed ignore container name: %w", err)
	}
	return pattern + hex.EncodeToString(random[:]), nil
}

func createWindowsCommittedIgnoreObject(parent windows.Handle, leaf string, directory bool, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, windows.IO_STATUS_BLOCK, error) {
	name, err := windows.NewNTUnicodeString(leaf)
	if err != nil {
		return 0, windows.IO_STATUS_BLOCK{}, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      parent,
		ObjectName:         name,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	fileAttributes := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	options := uint32(windows.FILE_NON_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		fileAttributes = windows.FILE_ATTRIBUTE_DIRECTORY
		options = windows.FILE_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	allocationSize := int64(0)
	err = windows.NtCreateFile(
		&handle,
		uint32(windowsCommittedIgnoreAllAccess),
		attributes,
		&status,
		&allocationSize,
		fileAttributes,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_CREATE,
		options,
		0,
		0,
	)
	return handle, status, err
}

func windowsCommittedIgnoreHandleIsPlain(handle windows.Handle, directory bool) bool {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return false
	}
	return information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 == directory && information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

func validateWindowsCommittedIgnoreHandle(handle windows.Handle, user string, directory bool) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return fmt.Errorf("read committed ignore handle information: %w", err)
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory || information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("committed ignore temporary object has unsafe type")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read committed ignore temporary security: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.Join(errors.New("committed ignore temporary DACL is absent or unprotected"), err)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil || owner == nil || defaulted || owner.String() != user {
		return errors.Join(errors.New("committed ignore temporary owner differs from effective user"), err)
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || defaulted {
		return errors.Join(errors.New("committed ignore temporary DACL is null or defaulted"), err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil {
		return errors.Join(errors.New("committed ignore temporary DACL has no allow entry"), err)
	}
	var extra *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 1, &extra); err == nil {
		return errors.New("committed ignore temporary DACL contains more than one entry")
	} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return fmt.Errorf("inspect committed ignore temporary DACL entry count: %w", err)
	}
	wantFlags := uint8(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != wantFlags || ace.Mask != windowsCommittedIgnoreAllAccess {
		return errors.New("committed ignore temporary DACL entry has unsafe type, inheritance, or access")
	}
	trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if trustee == nil || trustee.String() != user {
		return errors.New("committed ignore temporary DACL trustee differs from effective user")
	}
	return nil
}

func markWindowsCommittedIgnoreHandleDeleted(handle windows.Handle) error {
	information := struct{ DeleteFile byte }{DeleteFile: 1}
	return windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	)
}
