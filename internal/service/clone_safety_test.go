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
	if err := translateCloneRootAfterRename(destination, &inventory); err != nil {
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
	if err := translateCloneRootAfterRename(destination, &inventory); err != nil {
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

func TestPrimeDirectoryIdentitySurvivesRename(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	destination := filepath.Join(parent, "destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !primeFileIdentity(before) {
		t.Fatal("failed to capture source directory identity")
	}
	if err := os.Rename(source, destination); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("primed directory identity did not survive rename")
	}
}

func TestObservableFileWritabilityMatchesRequestedMode(t *testing.T) {
	tests := []struct {
		name      string
		actual    os.FileMode
		requested os.FileMode
		want      bool
	}{
		{name: "writable synthesized mode satisfies private writable request", actual: 0o666, requested: 0o600, want: true},
		{name: "read-only mode rejects writable request", actual: 0o444, requested: 0o600},
		{name: "read-only mode satisfies read-only request", actual: 0o444, requested: 0o400, want: true},
		{name: "writable mode rejects read-only request", actual: 0o666, requested: 0o400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := observableFileWritabilityMatches(test.actual, test.requested); got != test.want {
				t.Fatalf("observableFileWritabilityMatches(%o, %o) = %t, want %t", test.actual.Perm(), test.requested.Perm(), got, test.want)
			}
		})
	}
}
