//go:build windows

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
	openAtomicReplacementSource          = openAtomicReplacementSourceHandle
	renameAtomicReplacement              = renameAtomicReplacementHandle
	verifyAtomicReplacement              = verifyAtomicReplacementDestination
	flushAtomicReplacement               = windows.FlushFileBuffers
	removeAtomicReplacement              = removeAtomicReplacementHandle
	atomicBeforeTemporaryCleanupIdentity func()
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
