//go:build !darwin && !linux && !windows

package fsutil

import (
	"errors"
	"runtime"
)

// RenameNoReplace is unavailable on unsupported targets because an Lstat plus
// ordinary rename would reintroduce the overwrite race this operation closes.
func RenameNoReplace(_, _ string) error {
	return errors.New("atomic no-replace rename is unsupported on " + runtime.GOOS)
}
