//go:build windows

package service

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateUpdateDirectoryProtectionAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	setWindowsTestDACL(t, path, "D:P(A;OICI;FA;;;WD)")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateUpdateDirectory(path, info); err != nil {
		t.Fatalf("protect private update directory: %v", err)
	}
	if err := validatePrivateUpdateDirectory(path, info); err != nil {
		t.Fatalf("validate protected update directory: %v", err)
	}
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("effective user = %#v, %v", user, err)
	}
	handle := openWindowsTestDirectory(t, path)
	if err := validateWindowsPrivateDirectoryHandle(handle, user.User.Sid.String(), true); err != nil {
		windows.CloseHandle(handle)
		t.Fatalf("protected DACL: %v", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove after helper handle closure: %v", err)
	}
}

func TestWindowsPrivateUpdateDirectoryRejectsUnsafeDACLAndWrongOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateUpdateDirectory(path, info); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("effective user = %#v, %v", user, err)
	}
	setWindowsTestDACL(t, path, "O:"+user.User.Sid.String()+"D:P(A;OICI;FA;;;"+user.User.Sid.String()+")(A;OICI;FA;;;WD)")
	if err := validatePrivateUpdateDirectory(path, info); err == nil {
		t.Fatal("update directory with extra broad ACE was accepted")
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateUpdateDirectory(path, info); err == nil {
		t.Fatal("update directory with an unprotected/inherited DACL was accepted")
	}
	if err := withPrivateUpdateDirectoryHandleForUser(path, info, false, "S-1-5-18"); err == nil {
		t.Fatal("update directory owned by a different user was accepted")
	}
}

func TestWindowsPrivateUpdateDirectoryRejectsTypeReparseAndIdentityMismatch(t *testing.T) {
	parent := t.TempDir()
	first := filepath.Join(parent, "first")
	second := filepath.Join(parent, "second")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateUpdateDirectory(second, firstInfo); err == nil {
		t.Fatal("path/handle identity mismatch was accepted")
	}
	secondInfo, err := os.Lstat(second)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateUpdateDirectory(file, fileInfo); err == nil {
		t.Fatal("regular file was accepted as update directory")
	}
	reparseAttributes := uint32(windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT)
	if windowsDirectoryAttributesArePlain(reparseAttributes) {
		t.Fatal("directory reparse attributes were classified as plain")
	}
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("effective user = %#v, %v", user, err)
	}
	reparseHandleCheck := func(windows.Handle) bool {
		return windowsDirectoryAttributesArePlain(reparseAttributes)
	}
	if err := withPrivateUpdateDirectoryHandleForUserAndTypeCheck(second, secondInfo, false, user.User.Sid.String(), reparseHandleCheck); err == nil {
		t.Fatal("directory reparse handle was accepted")
	}
}
