//go:build !windows

package fsutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPrivatePathCreatesExactOwnedModesBeneathPermissiveAnchor(t *testing.T) {
	anchor := t.TempDir()
	if err := os.Chmod(anchor, 0o777); err != nil {
		t.Fatal(err)
	}
	authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if err := authority.WriteFileAtomicModeWithHook([]byte("private"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(anchor, "projects"),
		filepath.Join(anchor, "projects", "project"),
		filepath.Join(anchor, "projects", "project", "hooks"),
		filepath.Join(anchor, "projects", "project", "hooks", "workspace"),
	} {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("owned directory %q = %v, %v", path, info, statErr)
		}
	}
	if info, err := os.Lstat(filepath.Join(anchor, "projects", "project", "hooks", "workspace", "event.json")); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("owned leaf = %v, %v", info, err)
	}
}

func TestPrivatePathReadOnlyValidationRejectsWithoutRepair(t *testing.T) {
	anchor := t.TempDir()
	directory := filepath.Join(anchor, "projects")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		authority.Close()
		t.Fatal("read-only validation accepted an unsafe directory")
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("read-only validation mutated mode: %v, %v", info, err)
	}
}

func TestPrivatePathRejectsDirectoryAndLeafTypeSwaps(t *testing.T) {
	anchor := t.TempDir()
	if err := os.WriteFile(filepath.Join(anchor, "projects"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true); err == nil {
		t.Fatal("accepted file in an owned directory position")
	}
	if err := os.Remove(filepath.Join(anchor, "projects")); err != nil {
		t.Fatal(err)
	}
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	authority.Close()
	if err := os.Mkdir(filepath.Join(anchor, "projects", "event.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		t.Fatal("accepted directory in a private leaf position")
	}
}

func TestPrivateLockReportsLeafReplacementDuringLease(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := authority.TryLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(anchor, "projects", "event.lock")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	replacementAuthority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	defer replacementAuthority.Close()
	if replacement, err := replacementAuthority.TryLock(context.Background()); err == nil {
		replacement.Unlock()
		t.Fatal("replacement leaf became a concurrent private lock lease")
	}
	if err := lock.Unlock(); err == nil {
		t.Fatal("lock lease accepted a replacement leaf")
	}
}

func TestPrivateDirectoryEnumerationRejectsRetainedGenerationReplacement(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if err := os.Rename(filepath.Join(anchor, "projects"), filepath.Join(anchor, "old-projects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(anchor, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ReadDirectory(); err == nil {
		t.Fatal("enumeration accepted a replaced private directory generation")
	}
}

func TestPrivatePathRejectsIntermediateAncestorReplacementAcrossOperations(t *testing.T) {
	operations := []struct {
		name string
		run  func(*PrivatePath) error
	}{
		{"read", func(path *PrivatePath) error { _, err := path.ReadFile(); return err }},
		{"write", func(path *PrivatePath) error { return path.WriteFileAtomicModeWithHook([]byte("detached"), 0o600, nil) }},
		{"remove", func(path *PrivatePath) error { return path.Remove() }},
		{"enumerate", func(path *PrivatePath) error { _, err := path.ReadDirectory(); return err }},
		{"sync", func(path *PrivatePath) error { return path.SyncDirectory() }},
		{"try-lock", func(path *PrivatePath) error {
			lock, err := path.TryLock(context.Background())
			if lock != nil {
				_ = lock.Unlock()
			}
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			anchor := t.TempDir()
			components := []string{"projects", "project", "hooks", "workspace"}
			leaf := "event.json"
			if operation.name == "try-lock" {
				leaf = "event.lock"
			}
			authority, err := OpenPrivatePath(anchor, components, leaf, true)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			if operation.name != "try-lock" {
				if err := authority.WriteFileAtomicModeWithHook([]byte("old"), 0o600, nil); err != nil {
					t.Fatal(err)
				}
			}
			oldProjects := filepath.Join(anchor, "old-projects")
			if err := os.Rename(filepath.Join(anchor, "projects"), oldProjects); err != nil {
				t.Fatal(err)
			}
			fresh, err := OpenPrivatePath(anchor, components, leaf, true)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if operation.name != "try-lock" {
				if err := fresh.WriteFileAtomicModeWithHook([]byte("replacement"), 0o600, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := operation.run(authority); err == nil {
				t.Fatalf("%s accepted authority detached by an intermediate ancestor replacement", operation.name)
			}
			if operation.name != "try-lock" {
				data, err := fresh.ReadFile()
				if err != nil || string(data) != "replacement" {
					t.Fatalf("fresh generation=%q %v", data, err)
				}
			}
		})
	}
}

func TestPrivateLockReportsIntermediateAncestorReplacementDuringLease(t *testing.T) {
	anchor := t.TempDir()
	components := []string{"projects", "project", "hooks", "workspace"}
	authority, err := OpenPrivatePath(anchor, components, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := authority.TryLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(anchor, "projects"), filepath.Join(anchor, "old-projects")); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenPrivatePath(anchor, components, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	freshLock, err := fresh.TryLock(context.Background())
	if err != nil {
		fresh.Close()
		t.Fatal(err)
	}
	if err := lock.Unlock(); err == nil {
		t.Fatal("lock lease accepted an intermediate ancestor replacement")
	}
	if err := freshLock.Unlock(); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestPrivatePathClosesCompleteAuthorityChainExactlyOnce(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	fds := make([]int, len(authority.platform.chain))
	for index, handle := range authority.platform.chain {
		fds[index] = int(handle.Fd())
	}
	if len(fds) != 5 {
		t.Fatalf("retained handles=%d, want anchor plus four components", len(fds))
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("second Close()=%v", err)
	}
	for _, fd := range fds {
		if err := unix.Fstat(fd, &unix.Stat_t{}); !errors.Is(err, unix.EBADF) {
			t.Fatalf("retained fd %d remains open: %v", fd, err)
		}
	}
}

func TestPrivateLockClosesTransferredAuthorityChainExactlyOnce(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	fds := make([]int, len(authority.platform.chain))
	for index, handle := range authority.platform.chain {
		fds[index] = int(handle.Fd())
	}
	lock, err := authority.TryLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("wrapper Close after transfer=%v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("second Unlock()=%v", err)
	}
	for _, fd := range fds {
		if err := unix.Fstat(fd, &unix.Stat_t{}); !errors.Is(err, unix.EBADF) {
			t.Fatalf("transferred fd %d remains open: %v", fd, err)
		}
	}
}

func TestPrivatePathRemovalRetainsVerifiedQuarantineWithoutNameUnlink(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if err := authority.WriteFileAtomicModeWithHook([]byte("verified"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	err = authority.Remove()
	if !errors.Is(err, ErrPrivateRemovalAmbiguous) || !errors.Is(err, ErrPrivateRemovalQuarantined) {
		t.Fatalf("Remove()=%v", err)
	}
	directory := filepath.Join(anchor, "projects", "project", "hooks", "workspace")
	if _, err := os.Lstat(filepath.Join(directory, "event.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authoritative leaf remains: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries=%v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	if err != nil || string(data) != "verified" {
		t.Fatalf("retained quarantine=%q %v", data, err)
	}
}

func TestPrivatePathMissingIntermediateAncestorIsNotLeafAbsence(t *testing.T) {
	operations := []struct {
		name string
		run  func(*PrivatePath) error
	}{
		{"read", func(path *PrivatePath) error { _, err := path.ReadFile(); return err }},
		{"write", func(path *PrivatePath) error { return path.WriteFileAtomicModeWithHook([]byte("detached"), 0o600, nil) }},
		{"remove", func(path *PrivatePath) error { return path.Remove() }},
		{"enumerate", func(path *PrivatePath) error { _, err := path.ReadDirectory(); return err }},
		{"sync", func(path *PrivatePath) error { return path.SyncDirectory() }},
		{"try-lock", func(path *PrivatePath) error {
			lock, err := path.TryLock(context.Background())
			if lock != nil {
				_ = lock.Unlock()
			}
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			anchor := t.TempDir()
			leaf := "event.json"
			if operation.name == "try-lock" {
				leaf = "event.lock"
			}
			authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, leaf, true)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			if operation.name != "try-lock" {
				if err := authority.WriteFileAtomicModeWithHook([]byte("old"), 0o600, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(filepath.Join(anchor, "projects"), filepath.Join(anchor, "old-projects")); err != nil {
				t.Fatal(err)
			}
			err = operation.run(authority)
			if err == nil {
				t.Fatalf("%s treated a missing intermediate ancestor as an absent leaf", operation.name)
			}
		})
	}
}
