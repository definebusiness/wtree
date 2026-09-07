//go:build windows

package service

import (
	"os"
	"path/filepath"
	"testing"
)

// Windows Lstat FileInfo values resolve their file ID lazily. A snapshot must
// bind that ID at capture, otherwise an identical-byte replacement can make a
// stale snapshot appear to describe the new pathname generation.
func TestSecureCloneFileSnapshotBindsWindowsIdentityBeforeReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	if err := os.WriteFile(path, []byte("same bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := secureCloneFileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("same bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := revalidateCloneFileSnapshot(snapshot); err == nil {
		t.Fatal("identical-byte replacement was accepted by a stale snapshot")
	}
}
