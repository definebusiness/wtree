//go:build !windows

package fsutil

import (
	"errors"
	"os"
)

func atomicReplaceWithInfo(source, destination string, _ os.FileInfo) error {
	return os.Rename(source, destination)
}

// removeAtomicTemporary removes a path only after proving it still identifies
// the generation that the caller created or intentionally displaced.
func removeAtomicTemporary(path string, expected os.FileInfo) error {
	actual, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, actual) {
		return errors.New("atomic temporary pathname no longer names the expected generation")
	}
	return os.Remove(path)
}

func writeFileAtomicPlatform(string, []byte, os.FileMode, AtomicStepHook) (bool, error) {
	return false, nil
}
