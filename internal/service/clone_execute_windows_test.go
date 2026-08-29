//go:build windows

package service

import (
	"errors"
	"os"
	"os/exec"
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
	staging, owned, stagingParent, leaseValue, err := createCloneStaging(parent, ".clone.wtree-clone-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*windowsCloneStagingLease)
	t.Cleanup(func() {
		_ = os.RemoveAll(staging)
		_ = lease.closeAll()
	})
	if owned != nil {
		t.Fatal("Git destination child exists before Git")
	}
	if err := validateWindowsPrivateDirectoryHandle(windows.Handle(lease.container.Fd()), lease.user, true); err != nil {
		t.Fatalf("staging container DACL: %v", err)
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	createWindowsCloneGitObjects(t, staging)
	owned, err = lease.captureChild(staging, staging, nil, stagingParent, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsPrivateDirectoryHandle(windows.Handle(lease.child.Fd()), lease.user, false); err != nil {
		t.Fatalf("staging child DACL: %v", err)
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
	if err := lease.releaseChild(staging, owned, stagingParent, os.Lstat); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsCloneStagingLetsGitCreateAbsentDestination(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	staging, owned, stagingParent, leaseValue, err := createCloneStaging(parent, ".clone.wtree-clone-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*windowsCloneStagingLease)
	t.Cleanup(func() {
		_ = os.RemoveAll(staging)
		_ = lease.closeAll()
	})
	if owned != nil {
		t.Fatal("Git destination child exists before Git")
	}
	command := exec.Command("git", "init", "--quiet", "--", staging)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init absent staging child: %v\n%s", err, output)
	}
	owned, err = lease.captureChild(staging, staging, nil, stagingParent, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	replacement := staging + "-replacement"
	if err := os.Rename(staging, replacement); err == nil {
		t.Fatal("retained staging handle allowed rename before Git operation")
	}
	command = exec.Command("git", "-C", staging, "status", "--porcelain")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git operation with captured staging child: %v\n%s", err, output)
	}
	if err := os.Rename(staging, replacement); err == nil {
		t.Fatal("retained staging handle allowed rename after Git operation")
	}
	if err := lease.releaseChild(staging, owned, stagingParent, os.Lstat); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsCloneStagingLeaseRejectsDACLMutationAndRenameSubstitution(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	staging, owned, stagingParent, leaseValue, err := createCloneStaging(parent, ".clone.wtree-clone-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*windowsCloneStagingLease)
	t.Cleanup(func() {
		_ = os.RemoveAll(staging)
		_ = lease.closeAll()
	})
	if owned != nil {
		t.Fatal("Git destination child exists before Git")
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	createWindowsCloneGitObjects(t, staging)
	owned, err = lease.captureChild(staging, staging, nil, stagingParent, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, staging+"-replacement"); err == nil {
		t.Fatal("retained staging handle unexpectedly allowed rename substitution")
	}
	setWindowsTestDACL(t, staging, "D:P(A;OICI;FA;;;WD)")
	if err := lease.releaseChild(staging, owned, stagingParent, os.Lstat); err == nil {
		t.Fatal("staging lease accepted a changed DACL")
	}
}

func TestWindowsCloneStagingGuardBindsAuthorizedCheckoutGitObjects(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	staging, _, stagingParent, leaseValue, err := createCloneStaging(parent, ".clone.wtree-clone-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*windowsCloneStagingLease)
	t.Cleanup(func() {
		_ = os.RemoveAll(staging)
		_ = lease.closeAll()
	})
	checkout := filepath.Join(staging, "services", "base")
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staging, "decoy", ".git", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.openGuardRelative(staging, filepath.Join(parent, "outside")); err == nil {
		t.Fatal("accepted Git object authority outside private clone staging")
	}
	if _, err := lease.captureChild(staging, checkout, nil, stagingParent, os.Lstat); err != nil {
		t.Fatalf("capture exact checkout Git authority: %v", err)
	}
	if lease.guardPath != filepath.Join(checkout, ".git", "objects") {
		t.Fatalf("guard path = %q, want exact checkout authority", lease.guardPath)
	}
}

func createWindowsCloneGitObjects(t *testing.T, staging string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(staging, ".git", "objects"), 0o700); err != nil {
		t.Fatal(err)
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
	if _, _, _, _, err := createCloneStaging(parent, prefix, parentInfo, os.MkdirTemp, lstat); err == nil {
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

func TestWindowsCloneStagingDispositionFailurePreservesReplacement(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, leaseValue, err := createCloneStaging(parent, ".clone.wtree-clone-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*windowsCloneStagingLease)
	container := lease.containerPath
	lease.deleteHandle = func(windows.Handle) error { return errors.New("injected exact disposition failure") }
	if err := lease.closeAll(); err == nil || !strings.Contains(err.Error(), "injected exact disposition failure") {
		t.Fatalf("closeAll() error = %v", err)
	}
	if _, err := os.Lstat(container); err != nil {
		t.Fatalf("owned container was not preserved: %v", err)
	}
	preserved := container + "-preserved"
	if err := os.Rename(container, preserved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(container, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(container, "replacement")
	if err := os.WriteFile(marker, []byte("unowned replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.closeAll(); err != nil {
		t.Fatalf("repeat closeAll() after disposition failure: %v", err)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "unowned replacement" {
		t.Fatalf("replacement after disposition failure = %q, %v", contents, err)
	}
	if _, err := os.Lstat(preserved); err != nil {
		t.Fatalf("preserved owned container disappeared: %v", err)
	}
	_ = os.RemoveAll(container)
	_ = os.RemoveAll(preserved)
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
