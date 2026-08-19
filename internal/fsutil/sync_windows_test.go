//go:build windows

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsUnsupportedSyncErrors(t *testing.T) {
	for _, err := range []error{
		errorInvalidFunction,
		syscall.Errno(50),  // ERROR_NOT_SUPPORTED
		syscall.Errno(120), // ERROR_CALL_NOT_IMPLEMENTED
		fmt.Errorf("sync directory: %w", errorInvalidFunction),
	} {
		if !isUnsupportedSyncError(err) {
			t.Errorf("isUnsupportedSyncError(%v) = false", err)
		}
	}
	for _, err := range []error{
		syscall.ERROR_ACCESS_DENIED,
		syscall.Errno(6),    // ERROR_INVALID_HANDLE
		syscall.Errno(1117), // ERROR_IO_DEVICE
		errors.New("disk failure"),
	} {
		if isUnsupportedSyncError(err) {
			t.Errorf("isUnsupportedSyncError(%v) = true", err)
		}
	}
}

func TestWindowsContainingDirectorySyncIsSupportedAsCapabilityBoundary(t *testing.T) {
	if err := syncDirectory(t.TempDir()); err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
}

func TestWindowsContainingDirectorySyncRejectsUnavailablePaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := syncDirectory(missing); !os.IsNotExist(err) {
		t.Fatalf("syncDirectory missing error = %v, want not exist", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(file); err == nil {
		t.Fatal("syncDirectory accepted a regular file")
	}
	if err := openAndCloseDirectory("denied", func(string) (directoryHandle, error) {
		return nil, syscall.ERROR_ACCESS_DENIED
	}); !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("access error = %v, want ERROR_ACCESS_DENIED", err)
	}
}

func TestWindowsContainingDirectorySyncPropagatesCloseFailure(t *testing.T) {
	want := errors.New("injected directory close failure")
	err := openAndCloseDirectory(t.TempDir(), func(path string) (directoryHandle, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return closeFailureDirectory{File: file, err: want}, nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("close error = %v, want %v", err, want)
	}
}

func TestWindowsAtomicReplacementReportsRemovedContainingDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := writeFileAtomicMode(target, []byte("new"), 0o600, nil, func(source, destination string) error {
		if err := os.Rename(source, destination); err != nil {
			return err
		}
		return os.RemoveAll(directory)
	})
	if err == nil || !ReplacementCompleted(err) {
		t.Fatalf("atomic write error = %v, want post-replacement failure", err)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Fatalf("removed target stat error = %v, want not exist", statErr)
	}
}

type closeFailureDirectory struct {
	*os.File
	err error
}

func (directory closeFailureDirectory) Close() error {
	return errors.Join(directory.File.Close(), directory.err)
}
