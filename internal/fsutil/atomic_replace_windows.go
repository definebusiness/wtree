//go:build windows

package fsutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// nativeFileRenameInformationEx is the NtSetInformationFile information
// class for FILE_RENAME_INFORMATION_EX. x/sys exposes the Win32
// FileRenameInfoEx class (22), but not this native class (65).
const nativeFileRenameInformationEx uint32 = 65

// writeFileAtomicPlatform keeps the writer-created source handle open from
// CREATE_NEW through publication. The generic path remains for injected
// source/destination replacement seams only.
func writeFileAtomicPlatform(path string, data []byte, mode os.FileMode, hook AtomicStepHook) (bool, error) {
	directory, leaf := filepath.Dir(path), filepath.Base(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return true, err
	}
	if err := atomicStep(hook, "create-temp"); err != nil {
		return true, err
	}
	directoryHandle, err := openAtomicReplacementDirectory(directory)
	if err != nil {
		return true, err
	}
	defer windows.CloseHandle(directoryHandle)
	writerHandle, name, err := createAtomicReplacementTemp(directoryHandle, leaf)
	if err != nil {
		return true, err
	}
	writer := os.NewFile(uintptr(writerHandle), name)
	if writer == nil {
		windows.CloseHandle(writerHandle)
		return true, errors.New("adopt atomic temporary handle")
	}
	published, disposeTemporary := false, true
	publisherHandle := windows.InvalidHandle
	var publisher *os.File
	defer func() {
		if !published && disposeTemporary {
			cleanup := publisherHandle
			if cleanup == windows.InvalidHandle && writer != nil {
				cleanup, _ = reopenAtomicReplacementHandle(writerHandle)
			}
			if cleanup != windows.InvalidHandle {
				_ = removeAtomicReplacement(cleanup)
				if cleanup != publisherHandle {
					_ = windows.CloseHandle(cleanup)
				}
			}
		}
		if publisher != nil {
			_ = publisher.Close()
		}
		if writer != nil {
			_ = writer.Close()
		}
	}()
	if err := atomicStep(hook, "write"); err != nil {
		return true, err
	}
	if _, err := writer.Write(data); err != nil {
		return true, err
	}
	if err := atomicStep(hook, "sync"); err != nil {
		return true, err
	}
	if err := Sync(writer); err != nil {
		return true, err
	}
	if err := atomicStep(hook, "close"); err != nil {
		return true, err
	}
	if err := atomicStep(hook, "before-rename"); err != nil {
		return true, err
	}
	publisherHandle, err = reopenAtomicReplacementHandle(writerHandle)
	if err != nil {
		return true, err
	}
	publisher = os.NewFile(uintptr(publisherHandle), name)
	if publisher == nil {
		windows.CloseHandle(publisherHandle)
		publisherHandle = windows.InvalidHandle
		return true, errors.New("adopt atomic publication handle")
	}
	if err := writer.Close(); err != nil {
		writer = nil
		return true, err
	}
	writer = nil
	// A rename can change the namespace and still report an error. Do not
	// dispose the source by handle until we independently prove that it still
	// names the temporary leaf.
	disposeTemporary = false
	renameErr := renameAtomicReplacementRelativeHandle(publisherHandle, directoryHandle, leaf, windows.FileRenameInfoEx, windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS)
	if isUnsupportedAtomicRenameError(renameErr) {
		renameErr = renameAtomicReplacementRelativeHandle(publisherHandle, directoryHandle, leaf, windows.FileRenameInfo, windows.FILE_RENAME_REPLACE_IF_EXISTS)
	}
	if err := verifyAtomicReplacementRelative(directoryHandle, leaf, publisherHandle, false); err != nil {
		result := errors.Join(renameErr, fmt.Errorf("verify atomic replacement destination: %w", err))
		if temporaryErr := verifyAtomicReplacementRelative(directoryHandle, name, publisherHandle, false); temporaryErr == nil {
			disposeTemporary = true
		} else {
			result = errors.Join(result, fmt.Errorf("preserve atomic temporary after unproven publication: %w", temporaryErr))
		}
		return true, result
	}
	// The destination identity is now proven. Do not allow deferred cleanup to
	// dispose the delivered generation, even when the rename API reported an
	// error after changing the namespace.
	published = true
	var result error
	if renameErr != nil {
		result = errors.Join(result, renameErr)
	}
	if err := chmodAtomicReplacement(publisher, mode); err != nil {
		result = errors.Join(result, fmt.Errorf("set mode on renamed atomic replacement: %w", err))
	}
	if err := flushAtomicReplacement(publisherHandle); err != nil {
		result = errors.Join(result, fmt.Errorf("flush renamed atomic replacement source: %w", err))
	}
	if err := verifyAtomicReplacementRelative(directoryHandle, leaf, publisherHandle, false); err != nil {
		result = errors.Join(result, fmt.Errorf("verify atomic replacement destination after flush: %w", err))
	}
	if result != nil {
		return true, &postReplacementError{Err: result}
	}
	if err := publisher.Close(); err != nil {
		publisher = nil
		return true, &postReplacementError{Err: fmt.Errorf("close atomic publication handle: %w", err)}
	}
	publisher = nil
	if err := atomicStep(hook, "dir-sync"); err != nil {
		return true, &postReplacementError{Err: err}
	}
	return true, nil
}

func openAtomicDirectory(path string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := validatePrivateWindowsType(handle, true); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func createAtomicWindowsTemp(directory windows.Handle, base string) (windows.Handle, string, error) {
	for attempt := 0; attempt != 10; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return windows.InvalidHandle, "", err
		}
		name := "." + base + "-" + hex.EncodeToString(token[:])
		handle, err := openAtomicWindowsRelative(directory, name,
			windows.GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH, nil)
		if err == nil {
			return handle, name, nil
		}
		if !errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			return windows.InvalidHandle, "", err
		}
	}
	return windows.InvalidHandle, "", os.ErrExist
}

var reopenAtomicReplacementHandle = reopenAtomicReplacement

var reopenWindowsFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

func reopenAtomicReplacement(handle windows.Handle) (windows.Handle, error) {
	const access = windows.GENERIC_WRITE | windows.DELETE | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE
	return reopenWindowsHandle(handle, access)
}

// reopenWindowsHandle upgrades access through an already-held file object,
// avoiding a pathname reopen between writing and publication.
func reopenWindowsHandle(handle windows.Handle, access uint32) (windows.Handle, error) {
	value, _, callErr := reopenWindowsFileProc.Call(uintptr(handle), uintptr(access),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		uintptr(windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH))
	if windows.Handle(value) == windows.InvalidHandle {
		if callErr == syscall.Errno(0) {
			callErr = errors.New("ReOpenFile failed")
		}
		return windows.InvalidHandle, callErr
	}
	return windows.Handle(value), nil
}

func renameAtomicReplacementRelative(handle, root windows.Handle, name string, class uint32, flags uint32) error {
	switch class {
	case windows.FileRenameInfoEx:
		// Match the native pairing used by Go's Windows rename-at primitive:
		// POSIX replacement flags require FileRenameInformationEx (65).
		return renamePrivateWindowsHandleWithInformation(handle, root, name, flags, nativeFileRenameInformationEx, setAtomicReplacementInformation)
	case windows.FileRenameInfo:
		// The compatibility retry intentionally uses the basic native class
		// (10) with only REPLACE_IF_EXISTS.
		return renamePrivateWindowsHandleWithInformation(handle, root, name, flags, windows.FileRenameInformation, setAtomicReplacementInformation)
	default:
		return windows.ERROR_INVALID_PARAMETER
	}
}

// windowsRenameInformation has the layout required by
// SetFileInformationByHandle. FileName is deliberately a trailing array: the
// caller allocates the complete UTF-16 destination in one aligned buffer.
type windowsRenameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

var (
	openAtomicReplacementDirectory        = openAtomicDirectory
	createAtomicReplacementTemp           = createAtomicWindowsTemp
	openAtomicWindowsRelative             = openPrivateWindowsRelative
	renameAtomicReplacementRelativeHandle = renameAtomicReplacementRelative
	setAtomicReplacementInformation       = privateWindowsSetInformationFile(windows.NtSetInformationFile)
	verifyAtomicReplacementRelative       = validatePrivateWindowsRelativeIdentity
	chmodAtomicReplacement                = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
	openAtomicReplacementSource           = openAtomicReplacementSourceHandle
	renameAtomicReplacement               = renameAtomicReplacementHandle
	verifyAtomicReplacement               = verifyAtomicReplacementDestination
	flushAtomicReplacement                = windows.FlushFileBuffers
	removeAtomicReplacement               = removeAtomicReplacementHandle
	atomicBeforeTemporaryCleanupIdentity  func()
)

// atomicReplaceWithInfo publishes the exact already-flushed temporary opened
// by handle. A pathname substitution after the open cannot change the source
// generation that is renamed.
func atomicReplaceWithInfo(source, destination string, expected os.FileInfo) error {
	file, handle, err := openAtomicReplacementSource(source)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat atomic replacement source handle: %w", err)
	}
	if expected == nil || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return errors.New("atomic replacement source identity changed before handle-bound publication")
	}

	flags := uint32(windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS)
	renameErr := renameAtomicReplacement(handle, destination, windows.FileRenameInfoEx, flags)
	if isUnsupportedAtomicRenameError(renameErr) {
		renameErr = renameAtomicReplacement(handle, destination, windows.FileRenameInfo, windows.FILE_RENAME_REPLACE_IF_EXISTS)
	}
	matched, verifyErr := verifyAtomicReplacement(destination, handle)
	if verifyErr != nil || !matched {
		if renameErr != nil {
			return errors.Join(renameErr, verifyErr)
		}
		return errors.Join(errors.New("atomic replacement destination does not name source handle"), verifyErr)
	}
	flushErr := flushAtomicReplacement(handle)
	matched, verifyErr = verifyAtomicReplacement(destination, handle)
	var result error
	if renameErr != nil {
		result = errors.Join(result, renameErr)
	}
	if flushErr != nil {
		result = errors.Join(result, fmt.Errorf("flush renamed atomic replacement source: %w", flushErr))
	}
	if verifyErr != nil {
		result = errors.Join(result, fmt.Errorf("verify atomic replacement destination after flush: %w", verifyErr))
	} else if !matched {
		result = errors.Join(result, errors.New("atomic replacement destination changed after flush"))
	}
	if result != nil {
		return &postReplacementError{Err: result}
	}
	return nil
}

func openAtomicReplacementSourceHandle(path string) (*os.File, windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_WRITE|windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return nil, windows.InvalidHandle, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		windows.CloseHandle(handle)
		return nil, windows.InvalidHandle, errors.New("adopt atomic replacement source handle")
	}
	return file, handle, nil
}

func renameAtomicReplacementHandle(handle windows.Handle, destination string, class uint32, flags uint32) error {
	encoded, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	length := len(encoded)*2 - 2
	var layout windowsRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+length)
	information := (*windowsRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.Flags = flags
	information.RootDirectory = 0
	information.FileNameLength = uint32(length)
	copy(unsafe.Slice(&information.FileName[0], length/2), encoded[:len(encoded)-1])
	return windows.SetFileInformationByHandle(handle, class, &buffer[0], uint32(len(buffer)))
}

func verifyAtomicReplacementDestination(path string, source windows.Handle) (bool, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	destination, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(destination)
	var sourceInfo, destinationInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(source, &sourceInfo); err != nil {
		return false, err
	}
	if err := windows.GetFileInformationByHandle(destination, &destinationInfo); err != nil {
		return false, err
	}
	return samePrivateWindowsIdentity(sourceInfo, destinationInfo), nil
}

func isUnsupportedAtomicRenameError(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.STATUS_INVALID_PARAMETER) ||
		errors.Is(err, windows.STATUS_INVALID_INFO_CLASS) ||
		errors.Is(err, windows.STATUS_NOT_SUPPORTED) ||
		errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, errors.ErrUnsupported)
}

// removeAtomicTemporary deletes only the generation identified before close.
// If another process replaces the pathname, it is deliberately preserved.
func removeAtomicTemporary(path string, expected os.FileInfo) error {
	file, handle, err := openAtomicReplacementSource(path)
	if os.IsNotExist(err) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if atomicBeforeTemporaryCleanupIdentity != nil {
		atomicBeforeTemporaryCleanupIdentity()
	}
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, actual) {
		return errors.New("atomic temporary pathname no longer names the expected generation")
	}
	return removeAtomicReplacement(handle)
}

func removeAtomicReplacementHandle(handle windows.Handle) error {
	information := struct{ DeleteFile byte }{DeleteFile: 1}
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information)))
}
