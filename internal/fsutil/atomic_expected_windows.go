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

var replaceFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

// replaceExpectedAtomic uses ReplaceFile's backup generation as the Windows
// equivalent of Unix exchange: the current destination is atomically moved to
// a private recovery name, then its identity is checked before accepting the
// new destination. A changed destination is restored atomically when possible
// and otherwise retained at the reported recovery path.
func replaceExpectedAtomic(source, destination string, temporary, expected os.FileInfo) error {
	backup, err := conditionalReplacementBackupPath(destination)
	if err != nil {
		return err
	}
	if err := replaceWindowsFile(destination, source, backup); err != nil {
		return err
	}
	displaced, err := os.Lstat(backup)
	if err == nil && displaced.Mode().IsRegular() && os.SameFile(expected, displaced) {
		if err := removeAtomicTemporary(backup, expected); err != nil {
			return &postReplacementError{Err: errors.Join(errors.New("remove displaced expected generation"), err)}
		}
		return nil
	}
	// Source no longer names writer data after ReplaceFile. Roll back with the
	// backup as replacement and source as the backup name for our generation.
	if restoreErr := replaceWindowsFile(destination, backup, source); restoreErr != nil {
		return &postReplacementError{Err: &preservedConditionalReplacementError{
			RecoveryPath: backup,
			Err:          errors.Join(errors.New("conditional replacement destination changed and could not be restored"), restoreErr),
		}}
	}
	// The second ReplaceFile placed the writer generation at source. The
	// generic cleanup will remove it only if it still has this exact identity.
	if actual, statErr := os.Lstat(source); statErr != nil || !os.SameFile(temporary, actual) {
		identityErr := statErr
		if identityErr == nil {
			identityErr = errors.New("writer recovery pathname no longer names writer generation")
		}
		return &postReplacementError{Err: &preservedConditionalReplacementError{
			RecoveryPath: source,
			Err:          fmt.Errorf("conditional replacement restored destination but writer recovery identity is unproven: %w", identityErr),
		}}
	}
	return errors.New("conditional replacement destination changed")
}

func conditionalReplacementBackupPath(destination string) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+"-recovery-"+hex.EncodeToString(token[:])), nil
}

func replaceWindowsFile(destination, source, backup string) error {
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	backupPointer, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	r1, _, callErr := replaceFileProc.Call(uintptr(unsafePointer(destinationPointer)), uintptr(unsafePointer(sourcePointer)), uintptr(unsafePointer(backupPointer)), 0, 0, 0)
	if r1 != 0 {
		return nil
	}
	if callErr == syscall.Errno(0) {
		return errors.New("ReplaceFileW failed")
	}
	return callErr
}

// unsafePointer is isolated to keep the syscall argument conversion explicit.
func unsafePointer(pointer *uint16) unsafe.Pointer { return unsafe.Pointer(pointer) }

type preservedConditionalReplacementError struct {
	RecoveryPath string
	Err          error
}

func (e *preservedConditionalReplacementError) Error() string {
	return "conditional replacement preserved intervening generation at " + e.RecoveryPath + ": " + e.Err.Error()
}

func (e *preservedConditionalReplacementError) Unwrap() error { return e.Err }

func preserveAtomicTemporary(err error) bool {
	var preserved *preservedConditionalReplacementError
	return errors.As(err, &preserved)
}
