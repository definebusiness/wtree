//go:build windows

package fsutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsAtomicReplaceUsesRenameInfoExThenFlush(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	source := writeAtomicWindowsSource(t, "new")
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	var calls []struct{ class, flags uint32 }
	renameAtomicReplacement = func(_ windows.Handle, _ string, class, flags uint32) error {
		calls = append(calls, struct{ class, flags uint32 }{class, flags})
		return nil
	}
	verifyAtomicReplacement = func(string, windows.Handle) (bool, error) { return true, nil }
	flushed := false
	flushAtomicReplacement = func(windows.Handle) error { flushed = true; return nil }
	if err := atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].class != windows.FileRenameInfoEx || calls[0].flags != windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS || !flushed {
		t.Fatalf("rename calls=%#v flushed=%t", calls, flushed)
	}
}

func TestWindowsAtomicReplaceRejectsPreOpenSubstitution(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	source := writeAtomicWindowsSource(t, "new")
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	renameAtomicReplacement = func(windows.Handle, string, uint32, uint32) error {
		t.Fatal("rename called after source identity changed")
		return nil
	}
	err = atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected)
	if err == nil || ReplacementCompleted(err) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
}

func TestWindowsAtomicReplaceBindsPostOpenSourceSubstitutionToHandle(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	directory := t.TempDir()
	source, destination := filepath.Join(directory, "source"), filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	originalRename := renameAtomicReplacement
	renameAtomicReplacement = func(handle windows.Handle, path string, class, flags uint32) error {
		if err := os.Rename(source, source+".held"); err != nil {
			return err
		}
		if err := os.WriteFile(source, []byte("attacker"), 0o600); err != nil {
			return err
		}
		return originalRename(handle, path, class, flags)
	}
	if err := atomicReplaceWithInfo(source, destination, expected); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{source: "attacker", destination: "new"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s=%q error=%v, want %q", path, data, err, want)
		}
	}
}

func TestWindowsAtomicReplaceFailsClosedWhenDestinationDoesNotMatchHandle(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	source := writeAtomicWindowsSource(t, "new")
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	renameAtomicReplacement = func(windows.Handle, string, uint32, uint32) error { return nil }
	verifyAtomicReplacement = func(string, windows.Handle) (bool, error) { return false, nil }
	flushAtomicReplacement = func(windows.Handle) error { t.Fatal("flush after failed destination verification"); return nil }
	err = atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected)
	if err == nil || ReplacementCompleted(err) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
}

func TestWindowsAtomicReplaceFallsBackOnlyForUnsupportedInfoEx(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	source := writeAtomicWindowsSource(t, "new")
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	var calls []uint32
	renameAtomicReplacement = func(_ windows.Handle, _ string, class, _ uint32) error {
		calls = append(calls, class)
		if len(calls) == 1 {
			return windows.ERROR_INVALID_PARAMETER
		}
		return nil
	}
	verifyAtomicReplacement = func(string, windows.Handle) (bool, error) { return true, nil }
	flushAtomicReplacement = func(windows.Handle) error { return nil }
	if err := atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != windows.FileRenameInfoEx || calls[1] != windows.FileRenameInfo {
		t.Fatalf("rename classes=%v", calls)
	}
}

func TestWindowsAtomicReplaceClassifiesFlushAndReportedRenameFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		renameErr error
		flushErr  error
	}{
		{"flush", nil, os.ErrPermission},
		{"reported-after-publish", os.ErrPermission, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			restoreAtomicWindowsSeams(t)
			source := writeAtomicWindowsSource(t, "new")
			expected, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			renameAtomicReplacement = func(windows.Handle, string, uint32, uint32) error { return test.renameErr }
			verifyAtomicReplacement = func(string, windows.Handle) (bool, error) { return true, nil }
			flushAtomicReplacement = func(windows.Handle) error { return test.flushErr }
			err = atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected)
			if err == nil || !ReplacementCompleted(err) || (test.renameErr != nil && !errors.Is(err, test.renameErr)) || (test.flushErr != nil && !errors.Is(err, test.flushErr)) {
				t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
			}
		})
	}
}

func TestWindowsAtomicReplaceContinuesProvenPublicationAfterReportedRenameFailure(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	source := writeAtomicWindowsSource(t, "new")
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	renameFailure, flushFailure, secondFailure := errors.New("rename reported failure"), errors.New("flush failure"), errors.New("second verification failure")
	renameAtomicReplacement = func(windows.Handle, string, uint32, uint32) error { return renameFailure }
	verificationCalls := 0
	verifyAtomicReplacement = func(string, windows.Handle) (bool, error) {
		verificationCalls++
		if verificationCalls == 1 {
			return true, nil
		}
		return false, secondFailure
	}
	flushCalls := 0
	flushAtomicReplacement = func(windows.Handle) error { flushCalls++; return flushFailure }
	err = atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected)
	if err == nil || !ReplacementCompleted(err) || !errors.Is(err, renameFailure) || !errors.Is(err, flushFailure) || !errors.Is(err, secondFailure) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
	if flushCalls != 1 || verificationCalls != 2 {
		t.Fatalf("flush calls=%d verification calls=%d, want 1 and 2", flushCalls, verificationCalls)
	}
}

func TestWindowsAtomicReplaceKeepsCompletedStateWhenSecondVerificationMismatches(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	source := writeAtomicWindowsSource(t, "new")
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	renameAtomicReplacement = func(windows.Handle, string, uint32, uint32) error { return nil }
	verificationCalls := 0
	verifyAtomicReplacement = func(string, windows.Handle) (bool, error) {
		verificationCalls++
		return verificationCalls == 1, nil
	}
	flushAtomicReplacement = func(windows.Handle) error { return nil }
	err = atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected)
	if err == nil || !ReplacementCompleted(err) || verificationCalls != 2 {
		t.Fatalf("error=%v completed=%t verification calls=%d", err, ReplacementCompleted(err), verificationCalls)
	}
}

func TestWindowsAtomicReplaceKeepsCompletedStateWhenSecondVerificationFails(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	source := writeAtomicWindowsSource(t, "new")
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	verificationFailure := errors.New("second verification failed")
	renameAtomicReplacement = func(windows.Handle, string, uint32, uint32) error { return nil }
	verificationCalls := 0
	verifyAtomicReplacement = func(string, windows.Handle) (bool, error) {
		verificationCalls++
		if verificationCalls == 1 {
			return true, nil
		}
		return false, verificationFailure
	}
	flushAtomicReplacement = func(windows.Handle) error { return nil }
	err = atomicReplaceWithInfo(source, filepath.Join(filepath.Dir(source), "destination"), expected)
	if err == nil || !ReplacementCompleted(err) || !errors.Is(err, verificationFailure) || verificationCalls != 2 {
		t.Fatalf("error=%v completed=%t verification calls=%d", err, ReplacementCompleted(err), verificationCalls)
	}
}

func TestWindowsAtomicTemporaryCleanupPreservesSubstitutedPath(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	path := writeAtomicWindowsSource(t, "expected")
	expected, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	atomicBeforeTemporaryCleanupIdentity = func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("unrelated"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err = removeAtomicTemporary(path, expected)
	if err != nil {
		t.Fatalf("identity-bound cleanup = %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "unrelated" {
		t.Fatalf("path=%q error=%v", data, readErr)
	}
}

func TestWindowsAtomicReplaceAllowsRetainedDeleteShareGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generation")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	retained := os.NewFile(uintptr(handle), path)
	if retained == nil {
		windows.CloseHandle(handle)
		t.Fatal("adopt retained generation handle")
	}
	defer retained.Close()
	before, err := retained.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomicMode(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicMode() = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || os.SameFile(before, after) {
		t.Fatalf("replacement identity after=%v error=%v", after, err)
	}
	if _, err := retained.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	old, err := io.ReadAll(retained)
	if err != nil || string(old) != "old" {
		t.Fatalf("retained generation=%q error=%v", old, err)
	}
}

func writeAtomicWindowsSource(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func restoreAtomicWindowsSeams(t *testing.T) {
	t.Helper()
	open, rename, verify, flush, remove, before := openAtomicReplacementSource, renameAtomicReplacement, verifyAtomicReplacement, flushAtomicReplacement, removeAtomicReplacement, atomicBeforeTemporaryCleanupIdentity
	t.Cleanup(func() {
		openAtomicReplacementSource, renameAtomicReplacement, verifyAtomicReplacement, flushAtomicReplacement, removeAtomicReplacement, atomicBeforeTemporaryCleanupIdentity = open, rename, verify, flush, remove, before
	})
}
