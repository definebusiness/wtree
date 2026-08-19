package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAndCloseDirectoryValidatesPathAndClose(t *testing.T) {
	if err := openAndCloseDirectory(t.TempDir(), openDirectoryHandle); err != nil {
		t.Fatalf("valid directory: %v", err)
	}
	if err := openAndCloseDirectory(filepath.Join(t.TempDir(), "missing"), openDirectoryHandle); !os.IsNotExist(err) {
		t.Fatalf("missing directory error = %v, want not exist", err)
	}
	if err := openAndCloseDirectory("denied", func(string) (directoryHandle, error) {
		return nil, os.ErrPermission
	}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("permission error = %v, want permission denied", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := openAndCloseDirectory(file, openDirectoryHandle); err == nil {
		t.Fatal("regular file was accepted as a directory")
	}
	want := errors.New("injected close failure")
	err := openAndCloseDirectory(t.TempDir(), func(path string) (directoryHandle, error) {
		directory, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return closeFailureHandle{File: directory, err: want}, nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("close error = %v, want %v", err, want)
	}
}

type closeFailureHandle struct {
	*os.File
	err error
}

func (handle closeFailureHandle) Close() error {
	return errors.Join(handle.File.Close(), handle.err)
}
