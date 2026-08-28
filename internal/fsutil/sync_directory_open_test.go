package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenAndCloseDirectoryValidatesDirectoryHandleLifecycle(t *testing.T) {
	directory := t.TempDir()
	if err := openAndCloseDirectory(directory, openDirectoryHandle); err != nil {
		t.Fatalf("valid directory: %v", err)
	}

	missing := filepath.Join(directory, "missing")
	if err := openAndCloseDirectory(missing, openDirectoryHandle); !os.IsNotExist(err) {
		t.Fatalf("missing directory error = %v, want not exist", err)
	}

	wantOpen := errors.New("injected open failure")
	if err := openAndCloseDirectory(directory, func(string) (directoryHandle, error) {
		return nil, wantOpen
	}); !errors.Is(err, wantOpen) {
		t.Fatalf("open error = %v, want %v", err, wantOpen)
	}

	file := filepath.Join(directory, "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := openAndCloseDirectory(file, openDirectoryHandle); err == nil {
		t.Fatal("regular file was accepted as a directory")
	}
}

func TestOpenAndCloseDirectoryPropagatesStatTypeAndCloseFailures(t *testing.T) {
	wantStat := errors.New("injected stat failure")
	statHandle := &testDirectoryHandle{statErr: wantStat}
	err := openAndCloseDirectory("directory", func(string) (directoryHandle, error) {
		return statHandle, nil
	})
	if !errors.Is(err, wantStat) || !statHandle.closed {
		t.Fatalf("stat error = %v, closed = %t; want stat error and close", err, statHandle.closed)
	}

	notDirectory := &testDirectoryHandle{info: testFileInfo{mode: 0o600}}
	err = openAndCloseDirectory("file", func(string) (directoryHandle, error) {
		return notDirectory, nil
	})
	if err == nil || !notDirectory.closed {
		t.Fatalf("non-directory error = %v, closed = %t; want error and close", err, notDirectory.closed)
	}

	wantClose := errors.New("injected close failure")
	closeHandle := &testDirectoryHandle{info: testFileInfo{mode: fs.ModeDir}, closeErr: wantClose}
	err = openAndCloseDirectory("directory", func(string) (directoryHandle, error) {
		return closeHandle, nil
	})
	if !errors.Is(err, wantClose) || !closeHandle.closed {
		t.Fatalf("close error = %v, closed = %t; want close error", err, closeHandle.closed)
	}
}

type testDirectoryHandle struct {
	info     os.FileInfo
	statErr  error
	closeErr error
	closed   bool
}

func (handle *testDirectoryHandle) Stat() (os.FileInfo, error) { return handle.info, handle.statErr }

func (handle *testDirectoryHandle) Close() error {
	handle.closed = true
	return handle.closeErr
}

type testFileInfo struct{ mode os.FileMode }

func (info testFileInfo) Name() string       { return "test" }
func (info testFileInfo) Size() int64        { return 0 }
func (info testFileInfo) Mode() os.FileMode  { return info.mode }
func (info testFileInfo) ModTime() time.Time { return time.Time{} }
func (info testFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info testFileInfo) Sys() any           { return nil }
