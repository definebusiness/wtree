//go:build windows

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsAtomicReplacementPublishesWithoutDirectoryFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("old generation"), 0o600); err != nil {
		t.Fatal(err)
	}

	// On Windows the prior implementation called File.Sync on the containing
	// directory after replacing this target. Native directory handles can reject
	// FlushFileBuffers even though the new complete generation is published.
	if err := WriteFileAtomicMode(path, []byte("new generation"), 0o600); err != nil {
		t.Fatalf("atomic replacement after directory flush capability rejection: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new generation" {
		t.Fatalf("target generation = %q, %v; want complete new generation", data, err)
	}
}

func TestWindowsAtomicReplacementClassifiesContainingDirectoryFailureAfterRename(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("old generation"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeFileAtomicMode(target, []byte("new generation"), 0o600, nil, func(source, destination string) error {
		if err := os.Rename(source, destination); err != nil {
			return err
		}
		return os.RemoveAll(directory)
	})
	if err == nil || !ReplacementCompleted(err) {
		t.Fatalf("atomic write error = %v, want post-replacement failure", err)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Fatalf("removed target stat error = %v, want not exist", statErr)
	}
}
