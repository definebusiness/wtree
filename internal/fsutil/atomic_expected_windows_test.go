//go:build windows

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicModeExpectedWindowsRestoresInPlaceContentChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil || !os.SameFile(expected, expected) {
		t.Fatalf("capture expected identity: %v", err)
	}
	previousHook := expectedAtomicBeforeExchange
	expectedAtomicBeforeExchange = func() {
		if err := os.WriteFile(path, []byte("intervening"), 0o600); err != nil {
			t.Fatalf("write intervening generation: %v", err)
		}
	}
	t.Cleanup(func() { expectedAtomicBeforeExchange = previousHook })

	err = WriteFileAtomicModeExpected(path, []byte("new"), 0o600, expected, []byte("expected"))
	if err == nil {
		t.Fatal("conditional replacement succeeded after destination content changed in place")
	}
	if ReplacementCompleted(err) {
		t.Fatalf("ReplacementCompleted(%v) = true, want false after successful restore", err)
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "intervening" {
		t.Fatalf("destination after restore = %q, %v; want intervening", data, readErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("entries after restore = %v; want target only", entries)
	}
}

func TestWriteFileAtomicModeExpectedWindowsReportsWriterCleanupAfterContentRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil || !os.SameFile(expected, expected) {
		t.Fatalf("capture expected identity: %v", err)
	}
	previousHook := expectedAtomicBeforeExchange
	expectedAtomicBeforeExchange = func() {
		if err := os.WriteFile(path, []byte("intervening"), 0o600); err != nil {
			t.Fatalf("write intervening generation: %v", err)
		}
	}
	previousRemove := removeAtomicTemporary
	removeCalls := 0
	removeAtomicTemporary = func(string, os.FileInfo) error {
		removeCalls++
		return errors.New("injected restored-writer cleanup failure")
	}
	t.Cleanup(func() {
		expectedAtomicBeforeExchange = previousHook
		removeAtomicTemporary = previousRemove
	})

	err = WriteFileAtomicModeExpected(path, []byte("new"), 0o600, expected, []byte("expected"))
	if err == nil {
		t.Fatal("writer cleanup failure was lost")
	}
	outcome := AtomicOutcome(err)
	if !outcome.ReplacementCompleted || len(outcome.AuxiliaryPaths) != 1 {
		t.Fatalf("cleanup outcome = %#v", outcome)
	}
	if removeCalls != 1 {
		t.Fatalf("cleanup calls = %d; want exactly one", removeCalls)
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "intervening" {
		t.Fatalf("restored destination = %q, %v; want intervening", data, readErr)
	}
	if data, readErr := os.ReadFile(outcome.AuxiliaryPaths[0]); readErr != nil || string(data) != "new" {
		t.Fatalf("writer recovery generation = %q, %v; want new", data, readErr)
	}
}
