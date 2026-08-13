package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPreReplacementFailuresPreserveOldState(t *testing.T) {
	for _, step := range []string{"write", "sync", "close", "before-rename"} {
		t.Run(step, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := WriteWorkspace(path, WorkspaceState{Version: 1, ID: "old"}); err != nil {
				t.Fatal(err)
			}
			oldBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			atomicStepHook = func(got string) error {
				if got == step {
					return errors.New("injected")
				}
				return nil
			}
			defer func() { atomicStepHook = nil }()
			if err := WriteWorkspace(path, WorkspaceState{Version: 1, ID: "new"}); err == nil {
				t.Fatal("write succeeded")
			}
			got, err := ReadWorkspace(path)
			if err != nil || got.ID != "old" {
				t.Fatalf("old state lost %#v %v", got, err)
			}
			currentBytes, err := os.ReadFile(path)
			if err != nil || string(currentBytes) != string(oldBytes) {
				t.Fatalf("authoritative bytes changed: %q %v", currentBytes, err)
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".state.json-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary files remain: %v %v", matches, err)
			}
		})
	}
}
