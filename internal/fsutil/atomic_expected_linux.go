//go:build linux

package fsutil

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

var expectedAtomicExchange = func(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE)
}

// expectedAtomicBeforeExchange is a test seam at the actual conditional
// publication boundary. Production leaves it nil.
var expectedAtomicBeforeExchange func()

func replaceExpectedAtomic(source, destination string, temporary, expected os.FileInfo, replacementData, expectedData []byte) error {
	if expectedAtomicBeforeExchange != nil {
		expectedAtomicBeforeExchange()
	}
	if err := expectedAtomicExchange(source, destination); err != nil {
		return err
	}
	displaced, validationErr := validateExpectedAtomicGeneration(source, expected, expectedData)
	if validationErr == nil {
		if err := removeAtomicTemporary(source, displaced); err != nil {
			return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{source}, Err: errors.Join(errors.New("remove displaced expected generation"), err)}}
		}
		return nil
	}
	if restoreErr := expectedAtomicExchange(source, destination); restoreErr != nil {
		syncErr := syncDirectory(filepath.Dir(destination))
		return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{source}, Err: &preservedConditionalReplacementError{
			RecoveryPath: source,
			Err:          errors.Join(errors.New("conditional replacement destination changed and could not be restored"), restoreErr, syncErr),
		}}}
	}
	if syncErr := syncDirectory(filepath.Dir(destination)); syncErr != nil {
		return &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{source}, Err: &preservedConditionalReplacementError{
			RecoveryPath: source,
			Err:          errors.Join(errors.New("conditional replacement restored destination but directory sync failed"), validationErr, syncErr),
		}}}
	}
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
