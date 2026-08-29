//go:build windows

package service

import (
	"errors"

	"golang.org/x/sys/windows"
)

func cloneGroupingReplacementRefused(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
