//go:build darwin || linux || windows

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func openExpectedRemovalTestPath(t *testing.T, path string) *ExpectedRemovalPath {
	t.Helper()
	authority, err := OpenExpectedRemovalPath(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authority.Close() })
	return authority
}

func TestExpectedRemovalPathRemovesOrRetainsExactGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	err = openExpectedRemovalTestPath(t, path).RemoveExpectedWithHook(expected, []byte("owned"), nil)
	outcome := AtomicOutcome(err)
	if runtime.GOOS == "windows" {
		if err != nil || outcome.ReplacementCompleted || len(outcome.AuxiliaryPaths) != 0 {
			t.Fatalf("Windows exact removal outcome = %#v, %v", outcome, err)
		}
	} else {
		if err == nil || !errors.Is(err, ErrPrivateRemovalQuarantined) || !outcome.ReplacementCompleted || len(outcome.AuxiliaryPaths) != 1 {
			t.Fatalf("Unix exact removal outcome = %#v, %v", outcome, err)
		}
		if data, readErr := os.ReadFile(outcome.AuxiliaryPaths[0]); readErr != nil || string(data) != "owned" {
			t.Fatalf("retained exact generation = %q, %v", data, readErr)
		}
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed public target = %v", err)
	}
}

func TestExpectedRemovalPathRestoresFinalInPlaceChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := []byte("foreign")
	err = openExpectedRemovalTestPath(t, path).RemoveExpectedWithHook(expected, []byte("owned"), func(step string) error {
		if step != "before-quarantine" {
			return nil
		}
		return os.WriteFile(path, foreign, 0o600)
	})
	if err == nil {
		t.Fatal("final in-place change was removed")
	}
	if ReplacementCompleted(err) {
		t.Fatalf("ReplacementCompleted(%v) = true after unchanged public path", err)
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != string(foreign) {
		t.Fatalf("public target = %q, %v; want foreign", data, readErr)
	}
}

func TestExpectedRemovalPathRejectsAncestorReplacement(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	detached := filepath.Join(root, "detached")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "target")
	if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	authority := openExpectedRemovalTestPath(t, path)
	err = authority.RemoveExpectedWithHook(expected, []byte("owned"), func(step string) error {
		if step != "before-quarantine" {
			return nil
		}
		if err := os.Rename(parent, detached); err != nil {
			return err
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(parent, "target"), []byte("foreign"), 0o600)
	})
	if err == nil {
		t.Fatal("ancestor replacement was accepted")
	}
	if data, readErr := os.ReadFile(filepath.Join(parent, "target")); readErr != nil || string(data) != "foreign" {
		t.Fatalf("replacement ancestor target = %q, %v", data, readErr)
	}
	if data, readErr := os.ReadFile(filepath.Join(detached, "target")); readErr != nil || string(data) != "owned" {
		t.Fatalf("detached owned target = %q, %v", data, readErr)
	}
}
