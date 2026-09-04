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

func replaceExpectedAtomic(source, destination string, _ os.FileInfo, expected os.FileInfo) error {
	if expectedAtomicBeforeExchange != nil {
		expectedAtomicBeforeExchange()
	}
	if err := expectedAtomicExchange(source, destination); err != nil {
		return err
	}
	displaced, err := os.Lstat(source)
	if err == nil && displaced.Mode().IsRegular() && os.SameFile(expected, displaced) {
		if err := removeAtomicTemporary(source, expected); err != nil {
			return &postReplacementError{Err: errors.Join(errors.New("remove displaced expected generation"), err)}
		}
		return nil
	}
	if restoreErr := expectedAtomicExchange(source, destination); restoreErr != nil {
		syncErr := syncDirectory(filepath.Dir(destination))
		return &postReplacementError{Err: &preservedConditionalReplacementError{
			RecoveryPath: source,
			Err:          errors.Join(errors.New("conditional replacement destination changed and could not be restored"), restoreErr, syncErr),
		}}
	}
	return errors.Join(errors.New("conditional replacement destination changed"), syncDirectory(filepath.Dir(destination)))
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
	return errors.As(err, &preserved)
}
