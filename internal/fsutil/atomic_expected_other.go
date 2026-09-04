//go:build !darwin && !linux && !windows

package fsutil

import (
	"errors"
	"os"
)

func replaceExpectedAtomic(string, string, os.FileInfo, os.FileInfo) error {
	return errors.New("conditional atomic replacement is unsupported on this platform")
}

func preserveAtomicTemporary(error) bool { return false }
