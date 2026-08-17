package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicModePreservesModeAndPreReplacementFailures(t *testing.T) {
	for _, step := range []string{"write", "sync", "close", "before-rename"} {
		t.Run(step, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := WriteFileAtomicModeWithHook(path, []byte("new"), 0o600, func(got string) error {
				if got == step {
					return errors.New("injected")
				}
				return nil
			}); err == nil {
				t.Fatal("write succeeded")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "old" {
				t.Fatalf("replacement changed old data: %q, %v", data, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "target")
	if err := WriteFileAtomicMode(path, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
}
