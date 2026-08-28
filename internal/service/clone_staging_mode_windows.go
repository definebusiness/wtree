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
	parent *os.File
	child  *os.File
	user   string
}

func createCloneStaging(parent, prefix string, parentInfo os.FileInfo, _ func(string, string) (string, error), lstat func(string) (os.FileInfo, error)) (string, os.FileInfo, cloneStagingLease, error) {
	if lstat == nil || parentInfo == nil {
		return "", nil, nil, errors.New("clone staging dependencies are not configured")
	}
	parentPath, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		return "", nil, nil, err
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
		return "", nil, nil, fmt.Errorf("open clone staging parent: %w", err)
	}
	parentFile := os.NewFile(uintptr(parentHandle), parent)
	if parentFile == nil {
		windows.CloseHandle(parentHandle)
		return "", nil, nil, errors.New("adopt clone staging parent handle")
	}
	lease := &windowsCloneStagingLease{parent: parentFile}
	fail := func(cause error, path string, _ os.FileInfo) (string, os.FileInfo, cloneStagingLease, error) {
		var cleanupErr error
		if path != "" && lease.child != nil {
			cleanupErr = deleteWindowsCloneStagingHandle(windows.Handle(lease.child.Fd()))
			if cleanupErr != nil {
				cleanupErr = fmt.Errorf("preserve clone staging after exact handle deletion failed: %w", cleanupErr)
			}
		}
		closeErr := lease.closeAll()
		return "", nil, nil, errors.Join(cause, closeErr, cleanupErr)
	}

	parentHandleInfo, err := parentFile.Stat()
	if err != nil || !windowsDirectoryHandleIsPlain(parentHandle) || !os.SameFile(parentInfo, parentHandleInfo) {
		return fail(errors.Join(errors.New("clone staging parent handle identity or type differs"), err), "", nil)
	}
	parentPathInfo, err := lstat(parent)
	if err != nil || !parentPathInfo.IsDir() || parentPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(parentInfo, parentPathInfo) || !os.SameFile(parentHandleInfo, parentPathInfo) {
		return fail(errors.Join(errors.New("clone staging parent path identity differs"), err), "", nil)
	}

	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return fail(errors.Join(errors.New("read effective Windows token user"), err), "", nil)
	}
	lease.user = user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + lease.user + "D:P(A;OICI;FA;;;" + lease.user + ")")
	if err != nil {
		return fail(fmt.Errorf("build private clone staging descriptor: %w", err), "", nil)
	}

	for attempt := 0; attempt < 128; attempt++ {
		leaf, err := windowsCloneStagingLeaf(prefix)
		if err != nil {
			return fail(err, "", nil)
		}
		name, err := windows.NewNTUnicodeString(leaf)
		if err != nil {
			return fail(err, "", nil)
		}
		attributes := &windows.OBJECT_ATTRIBUTES{
			Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
			RootDirectory:      parentHandle,
			ObjectName:         name,
			Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
			SecurityDescriptor: descriptor,
		}
		var childHandle windows.Handle
		var status windows.IO_STATUS_BLOCK
		allocationSize := int64(0)
		err = windows.NtCreateFile(
			&childHandle,
			uint32(windowsFileAllAccess),
			attributes,
			&status,
			&allocationSize,
			windows.FILE_ATTRIBUTE_DIRECTORY,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			windows.FILE_CREATE,
			windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
			0,
			0,
		)
		if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) || errors.Is(err, windows.STATUS_OBJECT_NAME_EXISTS) {
			continue
		}
		if err != nil {
			return fail(fmt.Errorf("create private clone staging relative to parent: %w", err), "", nil)
		}
		staging := filepath.Clean(filepath.Join(parent, leaf))
		lease.child = os.NewFile(uintptr(childHandle), staging)
		if lease.child == nil {
			windows.CloseHandle(childHandle)
			return fail(errors.New("adopt private clone staging handle"), staging, nil)
		}
		owned, err := lease.child.Stat()
		if err != nil {
			return fail(fmt.Errorf("stat private clone staging handle: %w", err), staging, nil)
		}
		if status.Information != 2 {
			return fail(fmt.Errorf("private clone staging creation status = %d, want created", status.Information), staging, owned)
		}
		if err := lease.validate(staging, owned, parentInfo, lstat); err != nil {
			return fail(err, staging, owned)
		}
		if !cloneStagingPathIsSafe(staging, prefix, owned, parentInfo, true, lstat) {
			return fail(errors.New("private clone staging path is unsafe"), staging, owned)
		}
		return staging, owned, lease, nil
	}
	return fail(errors.New("allocate collision-free private clone staging name"), "", nil)
}

func windowsCloneStagingLeaf(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate private clone staging name: %w", err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func (lease *windowsCloneStagingLease) validate(staging string, owned, parentInfo os.FileInfo, lstat func(string) (os.FileInfo, error)) error {
	if lease == nil || lease.parent == nil || lease.child == nil || owned == nil || parentInfo == nil || lstat == nil {
		return errors.New("private clone staging lease is incomplete")
	}
	parentHandleInfo, err := lease.parent.Stat()
	if err != nil || !windowsDirectoryHandleIsPlain(windows.Handle(lease.parent.Fd())) || !os.SameFile(parentInfo, parentHandleInfo) {
		return errors.Join(errors.New("clone staging parent handle changed"), err)
	}
	parentPathInfo, err := lstat(filepath.Dir(staging))
	if err != nil || !parentPathInfo.IsDir() || parentPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(parentInfo, parentPathInfo) || !os.SameFile(parentHandleInfo, parentPathInfo) {
		return errors.Join(errors.New("clone staging parent path changed"), err)
	}
	childHandleInfo, err := lease.child.Stat()
	if err != nil || !windowsDirectoryHandleIsPlain(windows.Handle(lease.child.Fd())) || !os.SameFile(owned, childHandleInfo) {
		return errors.Join(errors.New("clone staging handle identity or type changed"), err)
	}
	pathInfo, err := lstat(staging)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, pathInfo) || !os.SameFile(childHandleInfo, pathInfo) {
		return errors.Join(errors.New("clone staging path identity or type changed"), err)
	}
	return validateWindowsPrivateDirectoryHandle(windows.Handle(lease.child.Fd()), lease.user, true)
}

func (lease *windowsCloneStagingLease) releaseChild(staging string, owned, parentInfo os.FileInfo, lstat func(string) (os.FileInfo, error)) error {
	if lease == nil || lease.child == nil {
		return nil
	}
	if err := lease.validate(staging, owned, parentInfo, lstat); err != nil {
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
	var result error
	if lease.child != nil {
		result = errors.Join(result, lease.child.Close())
		lease.child = nil
	}
	if lease.parent != nil {
		result = errors.Join(result, lease.parent.Close())
		lease.parent = nil
	}
	return result
}

func windowsDirectoryHandleIsPlain(handle windows.Handle) bool {
	var information windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(handle, &information) == nil &&
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 &&
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
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
