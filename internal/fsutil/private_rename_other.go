//go:build !linux && !darwin && !windows

package fsutil

import "errors"

func privateRenameNoReplace(int, string, string) error { return errors.ErrUnsupported }
