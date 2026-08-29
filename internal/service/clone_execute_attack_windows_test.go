//go:build windows

package service

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func cloneGroupingReplacementRefused(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

func cloneTestLogicalRoot(container, prefix string) string {
	return filepath.Join(container, prefix+"root")
}
