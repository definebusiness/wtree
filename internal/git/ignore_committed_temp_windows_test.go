//go:build windows

package git

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCommittedIgnoreExcludeUsesProtectedUserOnlyWindowsBoundary(t *testing.T) {
	path, cleanup, err := committedIgnoreExclude([]byte("/child/\n"))
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("effective user = %#v, %v", user, err)
	}
	containerPointer, err := windows.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	containerHandle, err := windows.CreateFile(containerPointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsCommittedIgnoreHandle(containerHandle, user.User.Sid.String(), true); err != nil {
		windows.CloseHandle(containerHandle)
		t.Fatalf("container privacy: %v", err)
	}
	if err := windows.CloseHandle(containerHandle); err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	fileHandle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsCommittedIgnoreHandle(fileHandle, user.User.Sid.String(), false); err != nil {
		windows.CloseHandle(fileHandle)
		t.Fatalf("file privacy: %v", err)
	}
	if err := windows.CloseHandle(fileHandle); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "/child/\n" {
		t.Fatalf("exclude contents = %q, %v", contents, err)
	}
	container := filepath.Dir(path)
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exclude remains after cleanup: %v", err)
	}
	if _, err := os.Lstat(container); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private container remains after cleanup: %v", err)
	}
}
