package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCloneStagingPathRequiresActualParentIdentity(t *testing.T) {
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
	stagingInfo, err := os.Lstat(staging)
	if err != nil {
		t.Fatal(err)
	}
	if !cloneStagingPathIsSafe(staging, prefix, stagingInfo, parentInfo, true, os.Lstat) {
		t.Fatal("owned staging under its actual parent was rejected")
	}
	otherInfo, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cloneStagingPathIsSafe(staging, prefix, stagingInfo, otherInfo, true, os.Lstat) {
		t.Fatal("staging was accepted under an unrelated parent identity")
	}
}

func TestRequestedFilePermissionsMatchPreservesWritabilityBoundary(t *testing.T) {
	if !requestedFilePermissionsMatch(0o600, 0o600) {
		t.Fatal("private writable request was rejected")
	}
	if requestedFilePermissionsMatch(0o400, 0o600) {
		t.Fatal("read-only file satisfied writable request")
	}
}

func TestCloneRootRenamePreservesSubsequentMetadataDetection(t *testing.T) {
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
	if err := translateCloneRootAfterRename(destination, &inventory, renameInfo); err != nil {
		t.Fatalf("rename transition: %v", err)
	}
	if err := os.Chmod(destination, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := revalidateCloneTree(destination, inventory); err == nil {
		t.Fatal("post-rename root metadata change was accepted")
	}
}
