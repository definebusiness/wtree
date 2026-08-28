//go:build windows

package service

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

const windowsFileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

type windowsCloneStagingLease struct {
	parent        *os.File
	container     *os.File
	child         *os.File
	parentInfo    os.FileInfo
	containerInfo os.FileInfo
	containerPath string
	prefix        string
	user          string
	deleteHandle  func(windows.Handle) error
}

func createCloneStaging(parent, prefix string, parentInfo os.FileInfo, _ func(string, string) (string, error), lstat func(string) (os.FileInfo, error)) (string, os.FileInfo, os.FileInfo, cloneStagingLease, error) {
	if lstat == nil || parentInfo == nil {
		return "", nil, nil, nil, errors.New("clone staging dependencies are not configured")
	}
	parentPath, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		return "", nil, nil, nil, err
	}
	parentHandle, err := windows.CreateFile(
		parentPath,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("open clone staging parent: %w", err)
	}
	parentFile := os.NewFile(uintptr(parentHandle), parent)
	if parentFile == nil {
		windows.CloseHandle(parentHandle)
		return "", nil, nil, nil, errors.New("adopt clone staging parent handle")
	}
	lease := &windowsCloneStagingLease{
		parent:       parentFile,
		parentInfo:   parentInfo,
		prefix:       prefix,
		deleteHandle: deleteWindowsCloneStagingHandle,
	}
	fail := func(cause error) (string, os.FileInfo, os.FileInfo, cloneStagingLease, error) {
		var cleanupErr error
		if lease.container != nil {
			cleanupErr = lease.deleteHandle(windows.Handle(lease.container.Fd()))
			if cleanupErr != nil {
				cleanupErr = fmt.Errorf("preserve clone staging container after exact handle deletion failed: %w", cleanupErr)
			}
		}
		closeErr := lease.closeHandles()
		return "", nil, nil, nil, errors.Join(cause, closeErr, cleanupErr)
	}

	parentHandleInfo, err := parentFile.Stat()
	if err != nil || !windowsDirectoryHandleIsPlain(parentHandle) || !os.SameFile(parentInfo, parentHandleInfo) {
		return fail(errors.Join(errors.New("clone staging parent handle identity or type differs"), err))
	}
	parentPathInfo, err := lstat(parent)
	if err != nil || !parentPathInfo.IsDir() || parentPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(parentInfo, parentPathInfo) || !os.SameFile(parentHandleInfo, parentPathInfo) {
		return fail(errors.Join(errors.New("clone staging parent path identity differs"), err))
	}

	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return fail(errors.Join(errors.New("read effective Windows token user"), err))
	}
	lease.user = user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + lease.user + "D:P(A;OICI;FA;;;" + lease.user + ")")
	if err != nil {
		return fail(fmt.Errorf("build private clone staging descriptor: %w", err))
	}

	for attempt := 0; attempt < 128; attempt++ {
		leaf, err := windowsCloneStagingLeaf(prefix)
		if err != nil {
			return fail(err)
		}
		containerHandle, status, err := openWindowsCloneStagingDirectory(parentHandle, leaf, windows.FILE_CREATE, descriptor)
		if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) || errors.Is(err, windows.STATUS_OBJECT_NAME_EXISTS) {
			continue
		}
		if err != nil {
			return fail(fmt.Errorf("create private clone staging container relative to parent: %w", err))
		}
		lease.containerPath = filepath.Clean(filepath.Join(parent, leaf))
		lease.container = os.NewFile(uintptr(containerHandle), lease.containerPath)
		if lease.container == nil {
			windows.CloseHandle(containerHandle)
			return fail(errors.New("adopt private clone staging container handle"))
		}
		lease.containerInfo, err = lease.container.Stat()
		if err != nil {
			return fail(fmt.Errorf("stat private clone staging container handle: %w", err))
		}
		if status.Information != 2 {
			return fail(fmt.Errorf("private clone staging container creation status = %d, want created", status.Information))
		}
		if err := lease.validateContainer(lstat); err != nil {
			return fail(err)
		}
		staging := filepath.Clean(filepath.Join(lease.containerPath, prefix+"root"))
		if _, err := lstat(staging); err == nil || !os.IsNotExist(err) {
			return fail(errors.Join(errors.New("private clone staging child unexpectedly exists"), err))
		}
		return staging, nil, lease.containerInfo, lease, nil
	}
	return fail(errors.New("allocate collision-free private clone staging container name"))
}

func windowsCloneStagingLeaf(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate private clone staging name: %w", err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func openWindowsCloneStagingDirectory(parent windows.Handle, leaf string, disposition uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, windows.IO_STATUS_BLOCK, error) {
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
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	allocationSize := int64(0)
	err = windows.NtCreateFile(
		&handle,
		uint32(windowsFileAllAccess),
		attributes,
		&status,
		&allocationSize,
		windows.FILE_ATTRIBUTE_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		disposition,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	return handle, status, err
}

func (lease *windowsCloneStagingLease) validateContainer(lstat func(string) (os.FileInfo, error)) error {
	if lease == nil || lease.parent == nil || lease.container == nil || lease.parentInfo == nil || lease.containerInfo == nil || lease.containerPath == "" || lstat == nil {
		return errors.New("private clone staging container lease is incomplete")
	}
	parentHandleInfo, err := lease.parent.Stat()
	if err != nil || !windowsDirectoryHandleIsPlain(windows.Handle(lease.parent.Fd())) || !os.SameFile(lease.parentInfo, parentHandleInfo) {
		return errors.Join(errors.New("clone staging parent handle changed"), err)
	}
	parentPathInfo, err := lstat(filepath.Dir(lease.containerPath))
	if err != nil || !parentPathInfo.IsDir() || parentPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(lease.parentInfo, parentPathInfo) || !os.SameFile(parentHandleInfo, parentPathInfo) {
		return errors.Join(errors.New("clone staging parent path changed"), err)
	}
	containerHandleInfo, err := lease.container.Stat()
	if err != nil || !windowsDirectoryHandleIsPlain(windows.Handle(lease.container.Fd())) || !os.SameFile(lease.containerInfo, containerHandleInfo) {
		return errors.Join(errors.New("clone staging container handle changed"), err)
	}
	containerPathInfo, err := lstat(lease.containerPath)
	if err != nil || !containerPathInfo.IsDir() || containerPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(lease.containerInfo, containerPathInfo) || !os.SameFile(containerHandleInfo, containerPathInfo) {
		return errors.Join(errors.New("clone staging container path changed"), err)
	}
	return validateWindowsPrivateDirectoryHandle(windows.Handle(lease.container.Fd()), lease.user, true)
}

func (lease *windowsCloneStagingLease) validateChild(staging string, owned, parentInfo os.FileInfo, lstat func(string) (os.FileInfo, error)) error {
	if lease == nil || lease.child == nil || owned == nil || parentInfo == nil || !os.SameFile(parentInfo, lease.containerInfo) || filepath.Dir(staging) != lease.containerPath {
		return errors.New("private clone staging child lease is incomplete")
	}
	if err := lease.validateContainer(lstat); err != nil {
		return err
	}
	childHandleInfo, err := lease.child.Stat()
	if err != nil || !windowsDirectoryHandleIsPlain(windows.Handle(lease.child.Fd())) || !os.SameFile(owned, childHandleInfo) {
		return errors.Join(errors.New("clone staging handle identity or type changed"), err)
	}
	pathInfo, err := lstat(staging)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, pathInfo) || !os.SameFile(childHandleInfo, pathInfo) {
		return errors.Join(errors.New("clone staging path identity or type changed"), err)
	}
	if err := validateWindowsPrivateDirectoryHandle(windows.Handle(lease.child.Fd()), lease.user, false); err != nil {
		return err
	}
	if !cloneStagingPathIsSafe(staging, lease.prefix, owned, parentInfo, true, lstat) {
		return errors.New("private clone staging child path is unsafe")
	}
	return nil
}

func (lease *windowsCloneStagingLease) prepareChild(staging, checkout string, owned, parentInfo os.FileInfo, _ func(string, os.FileMode) error, lstat func(string) (os.FileInfo, error)) (os.FileInfo, error) {
	if owned != nil {
		return owned, lease.validateChild(staging, owned, parentInfo, lstat)
	}
	if err := lease.validateContainer(lstat); err != nil {
		return nil, err
	}
	if _, err := lstat(staging); err == nil || !os.IsNotExist(err) {
		return nil, errors.Join(errors.New("private clone staging child unexpectedly exists before creation"), err)
	}
	if filepath.Clean(checkout) == filepath.Clean(staging) {
		return nil, nil
	}
	handle, status, err := openWindowsCloneStagingDirectory(windows.Handle(lease.container.Fd()), filepath.Base(staging), windows.FILE_CREATE, nil)
	if err != nil {
		return nil, fmt.Errorf("create private clone logical root: %w", err)
	}
	if status.Information != 2 {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("private clone logical root creation status = %d, want created", status.Information)
	}
	return lease.adoptChild(handle, staging, parentInfo, lstat)
}

func (lease *windowsCloneStagingLease) captureChild(staging string, owned, parentInfo os.FileInfo, lstat func(string) (os.FileInfo, error)) (os.FileInfo, error) {
	if owned != nil {
		return owned, lease.validateChild(staging, owned, parentInfo, lstat)
	}
	if err := lease.validateContainer(lstat); err != nil {
		return nil, err
	}
	handle, _, err := openWindowsCloneStagingDirectory(windows.Handle(lease.container.Fd()), filepath.Base(staging), windows.FILE_OPEN, nil)
	if err != nil {
		return nil, fmt.Errorf("open Git-created private clone staging root: %w", err)
	}
	return lease.adoptChild(handle, staging, parentInfo, lstat)
}

func (lease *windowsCloneStagingLease) adoptChild(handle windows.Handle, staging string, parentInfo os.FileInfo, lstat func(string) (os.FileInfo, error)) (os.FileInfo, error) {
	lease.child = os.NewFile(uintptr(handle), staging)
	if lease.child == nil {
		windows.CloseHandle(handle)
		return nil, errors.New("adopt private clone staging child handle")
	}
	owned, err := lease.child.Stat()
	if err != nil {
		_ = lease.child.Close()
		lease.child = nil
		return nil, fmt.Errorf("stat private clone staging child handle: %w", err)
	}
	if err := lease.validateChild(staging, owned, parentInfo, lstat); err != nil {
		_ = lease.child.Close()
		lease.child = nil
		return nil, err
	}
	return owned, nil
}

func (lease *windowsCloneStagingLease) releaseChild(staging string, owned, parentInfo os.FileInfo, lstat func(string) (os.FileInfo, error)) error {
	if lease == nil || lease.child == nil {
		return nil
	}
	if err := lease.validateChild(staging, owned, parentInfo, lstat); err != nil {
		return err
	}
	child := lease.child
	lease.child = nil
	return child.Close()
}

func (lease *windowsCloneStagingLease) closeAll() error {
	if lease == nil {
		return nil
	}
	if lease.child != nil {
		child := lease.child
		lease.child = nil
		if err := child.Close(); err != nil {
			return errors.Join(fmt.Errorf("close clone staging child before container disposition: %w", err), lease.closeHandles())
		}
	}
	if lease.container != nil && lease.containerPath != "" {
		if err := lease.validateContainer(os.Lstat); err != nil {
			return errors.Join(fmt.Errorf("preserve clone staging container after validation failed: %w", err), lease.closeHandles())
		}
		if lease.deleteHandle == nil {
			return errors.Join(errors.New("preserve clone staging container without exact handle deletion capability"), lease.closeHandles())
		}
		if err := lease.deleteHandle(windows.Handle(lease.container.Fd())); err != nil {
			return errors.Join(fmt.Errorf("preserve clone staging container after exact handle deletion failed: %w", err), lease.closeHandles())
		}
		lease.containerPath = ""
	}
	return lease.closeHandles()
}

func (lease *windowsCloneStagingLease) closeHandles() error {
	if lease == nil {
		return nil
	}
	var result error
	if lease.child != nil {
		result = errors.Join(result, lease.child.Close())
		lease.child = nil
	}
	if lease.parent != nil {
		result = errors.Join(result, lease.parent.Close())
		lease.parent = nil
	}
	if lease.container != nil {
		result = errors.Join(result, lease.container.Close())
		lease.container = nil
	}
	return result
}

func windowsDirectoryHandleIsPlain(handle windows.Handle) bool {
	var information windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(handle, &information) == nil && windowsDirectoryAttributesArePlain(information.FileAttributes)
}

func windowsDirectoryAttributesArePlain(attributes uint32) bool {
	return attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

func deleteWindowsCloneStagingHandle(handle windows.Handle) error {
	information := struct{ DeleteFile byte }{DeleteFile: 1}
	return windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	)
}

func validateWindowsPrivateDirectoryHandle(handle windows.Handle, user string, requireProtected bool) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read private clone staging security: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || requireProtected && control&windows.SE_DACL_PROTECTED == 0 {
		return errors.Join(errors.New("private clone staging DACL is absent or unprotected"), err)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil || owner == nil || defaulted || owner.String() != user {
		return errors.Join(errors.New("private clone staging owner differs from effective user"), err)
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || defaulted {
		return errors.Join(errors.New("private clone staging DACL is null or defaulted"), err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil {
		return errors.Join(errors.New("private clone staging DACL has no allow entry"), err)
	}
	var extra *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 1, &extra); err == nil {
		return errors.New("private clone staging DACL contains more than one entry")
	} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return fmt.Errorf("inspect private clone staging DACL entry count: %w", err)
	}
	wantFlags := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&wantFlags != wantFlags ||
		requireProtected && ace.Header.AceFlags&windows.INHERITED_ACE != 0 || ace.Mask != windowsFileAllAccess {
		return errors.New("private clone staging DACL entry has unsafe type, inheritance, or access")
	}
	trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if trustee == nil || trustee.String() != user {
		return errors.New("private clone staging DACL trustee differs from effective user")
	}
	return nil
}

// POSIX permission bits are synthesized on Windows and never prove staging
// privacy. Production privacy is established by the retained handle and DACL.
func cloneStagingModeIsPrivate(os.FileMode) bool { return false }

func requestedFilePermissionsMatch(actual, requested os.FileMode) bool {
	return (actual.Perm()&0o222 != 0) == (requested.Perm()&0o222 != 0)
}

// A timestamp transition is accepted only when it was captured immediately
// by the non-injected production rename boundary. Injected rename seams and
// all later validations must retain exact timestamps.
func reconcileCloneRootAfterRename(expected *cloneTreeEntry, actual os.FileInfo, renameOwned bool) bool {
	if expected == nil || expected.info == nil || actual == nil || !actual.IsDir() || actual.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(expected.info, actual) || expected.mode != actual.Mode() || expected.size != actual.Size() || expected.digest != "" {
		return false
	}
	if !renameOwned {
		return expected.mtime == actual.ModTime().UnixNano()
	}
	expected.mtime = actual.ModTime().UnixNano()
	return true
}
