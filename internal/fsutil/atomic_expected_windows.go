//go:build windows

package fsutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

// expectedAtomicBeforeExchange is a test seam at the actual conditional
// publication boundary. Production leaves it nil.
var expectedAtomicBeforeExchange func()

// replaceExpectedAtomic uses ReplaceFile's backup generation as the Windows
// equivalent of Unix exchange: the current destination is atomically moved to
// a private recovery name, then its identity is checked before accepting the
// new destination. A changed destination is restored atomically when possible
// and otherwise retained at the reported recovery path.
func replaceExpectedAtomic(source, destination string, temporary, expected os.FileInfo, replacementData, expectedData []byte) error {
	backup, err := conditionalReplacementBackupPath(destination)
	if err != nil {
		return err
	}
	if expectedAtomicBeforeExchange != nil {
		expectedAtomicBeforeExchange()
	}
	if err := replaceWindowsFile(destination, source, backup); err != nil {
		return err
	}
	displaced, validationErr := validateExpectedAtomicGeneration(backup, expected, expectedData)
	if validationErr == nil {
		if err := removeAtomicTemporary(backup, displaced); err != nil {
			return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{backup}, Err: errors.Join(errors.New("remove displaced expected generation"), err)}}
		}
		return nil
	}
	// Source no longer names writer data after ReplaceFile. Roll back with the
	// backup as replacement and source as the backup name for our generation.
	if restoreErr := replaceWindowsFile(destination, backup, source); restoreErr != nil {
		return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{backup}, Err: &preservedConditionalReplacementError{
			RecoveryPath: backup,
			Err:          errors.Join(errors.New("conditional replacement destination changed and could not be restored"), restoreErr),
		}}}
	}
	if syncErr := syncDirectory(filepath.Dir(destination)); syncErr != nil {
		return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{source}, Err: &preservedConditionalReplacementError{
			RecoveryPath: source,
			Err:          errors.Join(errors.New("conditional replacement restored destination but directory sync failed"), validationErr, syncErr),
		}}}
	}
	// The second ReplaceFile placed the writer generation at source. The
	// writer now removes it only after proving exact identity and bytes.
	writer, writerErr := validateExpectedAtomicGeneration(source, temporary, replacementData)
	if writerErr != nil {
		return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{source}, Err: &preservedConditionalReplacementError{
			RecoveryPath: source,
			Err:          errors.Join(errors.New("conditional replacement restored destination but writer recovery identity is unproven"), validationErr, writerErr),
		}}}
	}
	if cleanupErr := removeAtomicTemporary(source, writer); cleanupErr != nil {
		return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{source}, Err: &preservedConditionalReplacementError{
			RecoveryPath: source,
			Err:          errors.Join(errors.New("conditional replacement restored destination but writer generation cleanup failed"), validationErr, cleanupErr),
		}}}
	}
	return errors.Join(errors.New("conditional replacement destination changed"), validationErr)
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
	var auxiliary *atomicAuxiliaryError
	return errors.As(err, &preserved) || errors.As(err, &auxiliary)
}
