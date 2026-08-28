package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTranslateCloneRootAfterRenamePreservesExactRootMetadata(t *testing.T) {
	parent := t.TempDir()
	staging := filepath.Join(parent, ".clone.wtree-clone-staging")
	destination := filepath.Join(parent, "clone")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "README.md"), []byte("root\n"), 0o600); err != nil {
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
		t.Fatalf("translateCloneRootAfterRename() error = %v", err)
	}
	if err := revalidateCloneTree(destination, inventory); err != nil {
		t.Fatalf("revalidateCloneTree() error = %v", err)
	}
}

func TestTranslateCloneRootAfterRenameStillRejectsLaterRootMetadataMutation(t *testing.T) {
	parent := t.TempDir()
	staging := filepath.Join(parent, ".clone.wtree-clone-staging")
	destination := filepath.Join(parent, "clone")
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
		t.Fatal(err)
	}
	changed := time.Now().Add(-time.Hour).Round(0)
	if err := os.Chtimes(destination, changed, changed); err != nil {
		t.Fatal(err)
	}
	if err := revalidateCloneTree(destination, inventory); err == nil {
		t.Fatal("revalidateCloneTree() accepted a concurrent root metadata mutation")
	}
}
