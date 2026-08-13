package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestWriteGlobalFilePreservesPreviousDataBeforeReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	old := GlobalConfig{Version: Version, Worktrees: Worktrees{Root: "/old"}}
	if err := WriteGlobalFile(path, old); err != nil {
		t.Fatal(err)
	}
	configAtomicStepHook = func(step string) error {
		if step == "before-rename" {
			return errors.New("injected failure")
		}
		return nil
	}
	defer func() { configAtomicStepHook = nil }()
	if err := WriteGlobalFile(path, GlobalConfig{Version: Version, Worktrees: Worktrees{Root: "/new"}}); err == nil {
		t.Fatal("WriteGlobalFile() error = nil, want injected failure")
	}
	loaded, err := ReadGlobalFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Worktrees.Root != old.Worktrees.Root {
		t.Fatalf("worktree root = %q, want %q", loaded.Worktrees.Root, old.Worktrees.Root)
	}
}
