//go:build windows

package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsCloneStagingPathRejectsUnprovenInheritedPrivacy(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ".clone.wtree-clone-"
	staging, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		t.Fatal(err)
	}
	staging = filepath.Clean(filepath.ToSlash(swapWindowsDriveCase(staging)))
	stagingInfo, err := os.Lstat(staging)
	if err != nil {
		t.Fatal(err)
	}
	if cloneStagingPathIsSafe(staging, prefix, stagingInfo, parentInfo, cloneStagingModeIsPrivate(stagingInfo.Mode()), os.Lstat) {
		t.Fatalf("MkdirTemp staging with unproven inherited DACL was accepted: %q", staging)
	}
}

func TestWindowsRequestedFileModeUsesObservableWritability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 || requestedFilePermissionsMatch(info.Mode(), 0o600) || !requestedFilePermissionsMatch(info.Mode(), 0o400) {
		t.Fatalf("read-only Windows mode handling = %o", info.Mode().Perm())
	}
}

func TestWindowsCloneStagingCreationOverridesPermissiveParentDACL(t *testing.T) {
	parent := t.TempDir()
	setWindowsTestDACL(t, parent, "D:P(A;OICI;FA;;;WD)")
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	staging, owned, leaseValue, err := createCloneStaging(parent, ".clone.wtree-clone-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*windowsCloneStagingLease)
	t.Cleanup(func() {
		_ = lease.closeAll()
		_ = os.RemoveAll(staging)
	})
	if err := validateWindowsPrivateDirectoryHandle(windows.Handle(lease.child.Fd()), lease.user, true); err != nil {
		t.Fatalf("staging root DACL: %v", err)
	}
	descendant := filepath.Join(staging, "descendant")
	if err := os.Mkdir(descendant, 0o700); err != nil {
		t.Fatal(err)
	}
	descendantHandle := openWindowsTestDirectory(t, descendant)
	if err := validateWindowsPrivateDirectoryHandle(descendantHandle, lease.user, false); err != nil {
		windows.CloseHandle(descendantHandle)
		t.Fatalf("descendant inherited private DACL: %v", err)
	}
	if err := windows.CloseHandle(descendantHandle); err != nil {
		t.Fatal(err)
	}
	if err := lease.releaseChild(staging, owned, parentInfo, os.Lstat); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsCloneStagingLeaseRejectsDACLMutationAndRenameSubstitution(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	staging, owned, leaseValue, err := createCloneStaging(parent, ".clone.wtree-clone-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*windowsCloneStagingLease)
	t.Cleanup(func() {
		_ = lease.closeAll()
		_ = os.RemoveAll(staging)
	})
	if err := os.Rename(staging, staging+"-replacement"); err == nil {
		t.Fatal("retained staging handle unexpectedly allowed rename substitution")
	}
	setWindowsTestDACL(t, staging, "D:P(A;OICI;FA;;;WD)")
	if err := lease.releaseChild(staging, owned, parentInfo, os.Lstat); err == nil {
		t.Fatal("staging lease accepted a changed DACL")
	}
}

func TestWindowsCloneStagingCreationFailureDeletesOnlyThroughOwnedHandle(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ".clone.wtree-clone-"
	lstat := func(path string) (os.FileInfo, error) {
		if strings.HasPrefix(filepath.Base(path), prefix) {
			return nil, errors.New("injected staging path observation failure")
		}
		return os.Lstat(path)
	}
	if _, _, _, err := createCloneStaging(parent, prefix, parentInfo, os.MkdirTemp, lstat); err == nil {
		t.Fatal("staging creation unexpectedly survived path observation failure")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Fatalf("exact-handle cleanup left staging residue %q", entry.Name())
		}
	}
}

func TestWindowsCloneRootRenameDistinguishesOwnedTimestampFromInjectedMutation(t *testing.T) {
	parent := t.TempDir()
	staging := filepath.Join(parent, "staging")
	destination := filepath.Join(parent, "destination")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	inventory, err := captureCloneTree(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, destination); err != nil {
		t.Fatal(err)
	}
	renameInfo, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	genuine := inventory
	if err := translateCloneRootAfterRename(destination, &genuine, renameInfo); err != nil {
		t.Fatalf("genuine Windows rename transition: %v", err)
	}
	changed := time.Now().Add(time.Hour).Round(0)
	if err := os.Chtimes(destination, changed, changed); err != nil {
		t.Fatal(err)
	}
	if err := translateCloneRootAfterRename(destination, &inventory, nil); err == nil {
		t.Fatal("injected rename-seam timestamp mutation was accepted")
	}
}

func setWindowsTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func openWindowsTestDirectory(t *testing.T, path string) windows.Handle {
	t.Helper()
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(pathPointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func swapWindowsDriveCase(path string) string {
	volume := filepath.VolumeName(path)
	if len(volume) < 2 || volume[1] != ':' {
		return path
	}
	first := volume[:1]
	if first == strings.ToLower(first) {
		first = strings.ToUpper(first)
	} else {
		first = strings.ToLower(first)
	}
	return first + path[1:]
}
