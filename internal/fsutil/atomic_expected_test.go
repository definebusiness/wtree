//go:build darwin || linux

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteFileAtomicModeExpectedReplacesExpectedGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("expected"), 0o640); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomicModeExpected(path, []byte("new"), 0o600, expected); err != nil {
		t.Fatalf("WriteFileAtomicModeExpected() = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("destination = %q, %v; want new", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicRequestedMode(t, info.Mode(), 0o600)
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("entries = %v; want target only", entries)
	}
}

func TestAtomicOutcomeReportsAuxiliaryGeneration(t *testing.T) {
	err := &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{"/tmp/retained-generation"}, Err: errors.New("retained")}}
	outcome := AtomicOutcome(err)
	if !outcome.ReplacementCompleted || !reflect.DeepEqual(outcome.AuxiliaryPaths, []string{"/tmp/retained-generation"}) {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestAtomicOutcomeReportsDisplacedCleanupGeneration(t *testing.T) {
	err := &postReplacementError{Err: &atomicAuxiliaryError{Paths: []string{"/tmp/displaced-expected"}, Err: errors.New("remove displaced expected generation")}}
	outcome := AtomicOutcome(err)
	if !outcome.ReplacementCompleted || !reflect.DeepEqual(outcome.AuxiliaryPaths, []string{"/tmp/displaced-expected"}) {
		t.Fatalf("cleanup outcome = %#v", outcome)
	}
}

func TestWriteFileAtomicModeExpectedCleanupFailureRetainsAdvertisedGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := removeAtomicTemporary
	calls := 0
	removeAtomicTemporary = func(string, os.FileInfo) error {
		calls++
		return errors.New("injected displaced cleanup failure")
	}
	t.Cleanup(func() { removeAtomicTemporary = previous })
	err = WriteFileAtomicModeExpected(path, []byte("new"), 0o600, expected)
	if err == nil {
		t.Fatal("cleanup failure was lost")
	}
	outcome := AtomicOutcome(err)
	if !outcome.ReplacementCompleted || len(outcome.AuxiliaryPaths) != 1 {
		t.Fatalf("cleanup outcome=%#v", outcome)
	}
	if calls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly one", calls)
	}
	if got, readErr := os.ReadFile(outcome.AuxiliaryPaths[0]); readErr != nil || string(got) != "expected" {
		t.Fatalf("retained displaced generation=%q err=%v", got, readErr)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "new" {
		t.Fatalf("destination=%q err=%v", got, readErr)
	}
	if err := os.Remove(outcome.AuxiliaryPaths[0]); err != nil {
		t.Fatalf("explicit reconciliation of retained generation: %v", err)
	}
	if _, err := os.Lstat(outcome.AuxiliaryPaths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained generation after explicit reconciliation = %v", err)
	}
}

func TestWriteFileAtomicModeExpectedRestoresInterveningGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	intervening := filepath.Join(filepath.Dir(path), "intervening")
	if err := os.WriteFile(intervening, []byte("intervening"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousHook := expectedAtomicBeforeExchange
	expectedAtomicBeforeExchange = func() {
		if err := os.Rename(intervening, path); err != nil {
			t.Fatalf("install intervening generation: %v", err)
		}
	}
	t.Cleanup(func() { expectedAtomicBeforeExchange = previousHook })

	err = WriteFileAtomicModeExpected(path, []byte("new"), 0o600, expected)
	if err == nil {
		t.Fatal("conditional replacement succeeded after destination changed")
	}
	if ReplacementCompleted(err) {
		t.Fatalf("ReplacementCompleted(%v) = true, want false after successful restore", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "intervening" {
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

func TestWriteFileAtomicModeExpectedPreservesInterveningGenerationWhenRestoreFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	intervening := filepath.Join(filepath.Dir(path), "intervening")
	if err := os.WriteFile(intervening, []byte("intervening"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousHook, previousExchange := expectedAtomicBeforeExchange, expectedAtomicExchange
	expectedAtomicBeforeExchange = func() {
		if err := os.Rename(intervening, path); err != nil {
			t.Fatalf("install intervening generation: %v", err)
		}
	}
	calls := 0
	expectedAtomicExchange = func(source, destination string) error {
		calls++
		if calls == 2 {
			return errors.New("injected rollback failure")
		}
		return previousExchange(source, destination)
	}
	t.Cleanup(func() {
		expectedAtomicBeforeExchange = previousHook
		expectedAtomicExchange = previousExchange
	})

	err = WriteFileAtomicModeExpected(path, []byte("new"), 0o600, expected)
	if err == nil {
		t.Fatal("conditional replacement succeeded after rollback failure")
	}
	if !ReplacementCompleted(err) {
		t.Fatalf("ReplacementCompleted(%v) = false, want true", err)
	}
	var preserved *preservedConditionalReplacementError
	if !errors.As(err, &preserved) {
		t.Fatalf("error %v does not identify preserved recovery generation", err)
	}
	if outcome := AtomicOutcome(err); !outcome.ReplacementCompleted || !reflect.DeepEqual(outcome.AuxiliaryPaths, []string{preserved.RecoveryPath}) {
		t.Fatalf("preserved outcome = %#v", outcome)
	}
	if preserved.RecoveryPath == path {
		t.Fatal("recovery path aliases destination")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "new" {
		t.Fatalf("published destination = %q, %v; want new", data, readErr)
	}
	recovery, readErr := os.ReadFile(preserved.RecoveryPath)
	if readErr != nil || string(recovery) != "intervening" {
		t.Fatalf("recovery generation = %q, %v; want intervening", recovery, readErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 2 {
		t.Fatalf("entries after failed restore = %v; want destination and recovery", entries)
	}
}
