//go:build windows

package fsutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

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

func TestWindowsAtomicProductionRenameUsesNtRelativeLeafProtocol(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	root := windows.Handle(0x1234)
	leaf := "target"
	calls := 0
	setAtomicReplacementInformation = func(handle windows.Handle, status *windows.IO_STATUS_BLOCK, buffer *byte, length, class uint32) error {
		calls++
		if handle != windows.Handle(0x5678) || status == nil {
			t.Fatalf("handle=%v status=%p", handle, status)
		}
		information := (*privateWindowsRenameInformation)(unsafe.Pointer(buffer))
		name := windows.UTF16ToString(unsafe.Slice(&information.FileName[0], int(information.FileNameLength/2)))
		wantLength := uint32(unsafe.Offsetof(privateWindowsRenameInformation{}.FileName)) + information.FileNameLength
		if class != nativeFileRenameInformationEx || information.Flags != windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS || information.RootDirectory != root || name != leaf || length != wantLength {
			t.Fatalf("class=%d flags=%#x root=%v name=%q length=%d want length=%d", class, information.Flags, information.RootDirectory, name, length, wantLength)
		}
		return nil
	}
	if err := renameAtomicReplacementRelative(windows.Handle(0x5678), root, leaf, windows.FileRenameInfoEx, windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("NtSetInformationFile calls=%d, want 1", calls)
	}
}

func TestWindowsAtomicProductionRenameFallsBackFromUnsupportedNtFlags(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	originalSet := setAtomicReplacementInformation
	var calls []struct {
		class uint32
		flags uint32
	}
	setAtomicReplacementInformation = func(handle windows.Handle, status *windows.IO_STATUS_BLOCK, buffer *byte, length, class uint32) error {
		information := (*privateWindowsRenameInformation)(unsafe.Pointer(buffer))
		calls = append(calls, struct {
			class uint32
			flags uint32
		}{class: class, flags: information.Flags})
		if len(calls) == 1 {
			return windows.STATUS_INVALID_PARAMETER
		}
		return originalSet(handle, status, buffer, length, class)
	}

	path := filepath.Join(t.TempDir(), "target")
	if err := WriteFileAtomicMode(path, []byte("new generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].class != nativeFileRenameInformationEx || calls[0].flags != windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS || calls[1].class != windows.FileRenameInformation || calls[1].flags != windows.FILE_RENAME_REPLACE_IF_EXISTS {
		t.Fatalf("NtSetInformationFile calls=%#v", calls)
	}
}

func TestWindowsAtomicProductionAuthorityUsesRelativeHandlesAndFinalModeBeforeFlush(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	originalOpen := openAtomicWindowsRelative
	originalRename := renameAtomicReplacementRelativeHandle
	originalSet := setAtomicReplacementInformation
	originalVerify := verifyAtomicReplacementRelative
	originalChmod := chmodAtomicReplacement
	originalFlush := flushAtomicReplacement
	var temporaryParent, renameParent windows.Handle
	var temporaryName, renameName string
	var nativeRoot windows.Handle
	var nativeName string
	var nativeClass, nativeFlags uint32
	var access windows.ACCESS_MASK
	var share, disposition, options uint32
	var order []string
	verifyCalls := 0
	openAtomicWindowsRelative = func(parent windows.Handle, name string, gotAccess windows.ACCESS_MASK, gotShare, gotDisposition, gotOptions uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
		temporaryParent, temporaryName, access, share, disposition, options = parent, name, gotAccess, gotShare, gotDisposition, gotOptions
		return originalOpen(parent, name, gotAccess, gotShare, gotDisposition, gotOptions, descriptor)
	}
	renameAtomicReplacementRelativeHandle = func(handle, root windows.Handle, name string, class, flags uint32) error {
		renameParent, renameName = root, name
		return originalRename(handle, root, name, class, flags)
	}
	setAtomicReplacementInformation = func(handle windows.Handle, status *windows.IO_STATUS_BLOCK, buffer *byte, length, class uint32) error {
		information := (*privateWindowsRenameInformation)(unsafe.Pointer(buffer))
		nativeRoot, nativeClass, nativeFlags = information.RootDirectory, class, information.Flags
		nativeName = windows.UTF16ToString(unsafe.Slice(&information.FileName[0], int(information.FileNameLength/2)))
		return originalSet(handle, status, buffer, length, class)
	}
	verifyAtomicReplacementRelative = func(root windows.Handle, name string, handle windows.Handle, directory bool) error {
		verifyCalls++
		return originalVerify(root, name, handle, directory)
	}
	chmodAtomicReplacement = func(file *os.File, mode os.FileMode) error {
		order = append(order, "chmod")
		return originalChmod(file, mode)
	}
	flushAtomicReplacement = func(handle windows.Handle) error {
		order = append(order, "flush")
		return originalFlush(handle)
	}

	path := filepath.Join(t.TempDir(), "target")
	if err := WriteFileAtomicMode(path, []byte("new generation"), 0o400); err != nil {
		t.Fatal(err)
	}
	if temporaryParent == windows.InvalidHandle || temporaryParent != renameParent || renameName != filepath.Base(path) || filepath.Base(temporaryName) != temporaryName {
		t.Fatalf("temporary parent=%v name=%q rename parent=%v name=%q", temporaryParent, temporaryName, renameParent, renameName)
	}
	if nativeRoot != renameParent || nativeName != filepath.Base(path) || nativeClass != nativeFileRenameInformationEx || nativeFlags != windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS {
		t.Fatalf("native rename root=%v name=%q class=%d flags=%#x", nativeRoot, nativeName, nativeClass, nativeFlags)
	}
	wantAccess := windows.ACCESS_MASK(windows.GENERIC_WRITE | windows.DELETE | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	wantShare := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	wantOptions := uint32(windows.FILE_NON_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_WRITE_THROUGH)
	if access != wantAccess || share != wantShare || disposition != windows.FILE_CREATE || options != wantOptions {
		t.Fatalf("temporary access=%#x share=%#x disposition=%#x options=%#x", access, share, disposition, options)
	}
	if len(order) != 2 || order[0] != "chmod" || order[1] != "flush" || verifyCalls != 2 {
		t.Fatalf("post-publication order=%v verification calls=%d", order, verifyCalls)
	}
}

func TestWindowsAtomicProductionKeepsCompletedResultWhenRenameReportsAfterEffect(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	originalRename := renameAtomicReplacementRelativeHandle
	originalVerify := verifyAtomicReplacementRelative
	originalFlush := flushAtomicReplacement
	reported := errors.New("rename reported after publication")
	verifyCalls, flushCalls := 0, 0
	renameAtomicReplacementRelativeHandle = func(handle, root windows.Handle, name string, class, flags uint32) error {
		if err := originalRename(handle, root, name, class, flags); err != nil {
			return err
		}
		return reported
	}
	verifyAtomicReplacementRelative = func(root windows.Handle, name string, handle windows.Handle, directory bool) error {
		verifyCalls++
		return originalVerify(root, name, handle, directory)
	}
	flushAtomicReplacement = func(handle windows.Handle) error {
		flushCalls++
		return originalFlush(handle)
	}

	path := filepath.Join(t.TempDir(), "target")
	err := WriteFileAtomicMode(path, []byte("published despite error"), 0o600)
	if err == nil || !ReplacementCompleted(err) || !errors.Is(err, reported) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "published despite error" {
		t.Fatalf("destination=%q error=%v", data, readErr)
	}
	if flushCalls != 1 || verifyCalls != 2 {
		t.Fatalf("flush calls=%d verification calls=%d", flushCalls, verifyCalls)
	}
}

func TestWindowsAtomicProductionKeepsCompletedResultWhenSecondVerificationFails(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	originalVerify := verifyAtomicReplacementRelative
	verificationFailure := errors.New("second verification failed")
	verifyCalls := 0
	verifyAtomicReplacementRelative = func(root windows.Handle, name string, handle windows.Handle, directory bool) error {
		verifyCalls++
		if verifyCalls == 2 {
			return verificationFailure
		}
		return originalVerify(root, name, handle, directory)
	}

	err := WriteFileAtomicMode(filepath.Join(t.TempDir(), "target"), []byte("new generation"), 0o600)
	if err == nil || !ReplacementCompleted(err) || !errors.Is(err, verificationFailure) || verifyCalls != 2 {
		t.Fatalf("error=%v completed=%t verification calls=%d", err, ReplacementCompleted(err), verifyCalls)
	}
}

func TestWindowsAtomicProductionPreservesDeliveredGenerationWhenFirstVerificationFails(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	originalVerify := verifyAtomicReplacementRelative
	originalRemove := removeAtomicReplacement
	firstVerification := errors.New("first destination verification failed")
	verifyCalls, removeCalls := 0, 0
	verifyAtomicReplacementRelative = func(root windows.Handle, name string, handle windows.Handle, directory bool) error {
		verifyCalls++
		if verifyCalls == 1 {
			return firstVerification
		}
		return originalVerify(root, name, handle, directory)
	}
	removeAtomicReplacement = func(handle windows.Handle) error {
		removeCalls++
		return originalRemove(handle)
	}

	path := filepath.Join(t.TempDir(), "target")
	err := WriteFileAtomicMode(path, []byte("delivered generation"), 0o600)
	if err == nil || ReplacementCompleted(err) || !errors.Is(err, firstVerification) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "delivered generation" {
		t.Fatalf("destination=%q error=%v", data, readErr)
	}
	if removeCalls != 0 || verifyCalls != 2 {
		t.Fatalf("disposition calls=%d verification calls=%d", removeCalls, verifyCalls)
	}
}

func TestWindowsAtomicProductionCleansVerifiedUnpublishedTemporary(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	originalOpen := openAtomicWindowsRelative
	originalRemove := removeAtomicReplacement
	var temporaryName string
	openAtomicWindowsRelative = func(parent windows.Handle, name string, access windows.ACCESS_MASK, share, disposition, options uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
		temporaryName = name
		return originalOpen(parent, name, access, share, disposition, options, descriptor)
	}
	renameFailure := errors.New("rename definitely did not publish")
	renameAtomicReplacementRelativeHandle = func(windows.Handle, windows.Handle, string, uint32, uint32) error { return renameFailure }
	removeCalls := 0
	removeAtomicReplacement = func(handle windows.Handle) error {
		removeCalls++
		return originalRemove(handle)
	}

	path := filepath.Join(t.TempDir(), "target")
	err := WriteFileAtomicMode(path, []byte("unpublished generation"), 0o600)
	if err == nil || ReplacementCompleted(err) || !errors.Is(err, renameFailure) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
	if removeCalls != 1 {
		t.Fatalf("disposition calls=%d, want 1", removeCalls)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), temporaryName)); !os.IsNotExist(statErr) {
		t.Fatalf("temporary still exists or stat failed: %v", statErr)
	}
}

func TestWindowsAtomicProductionCleanupPreservesSubstitutedTemporaryPath(t *testing.T) {
	restoreAtomicWindowsSeams(t)
	originalOpen := openAtomicWindowsRelative
	var temporaryName string
	openAtomicWindowsRelative = func(parent windows.Handle, name string, access windows.ACCESS_MASK, share, disposition, options uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
		temporaryName = name
		return originalOpen(parent, name, access, share, disposition, options, descriptor)
	}
	stop := errors.New("stop before write")
	path := filepath.Join(t.TempDir(), "target")
	err := WriteFileAtomicModeWithHook(path, []byte("ignored"), 0o600, func(step string) error {
		if step != "write" {
			return nil
		}
		temporaryPath := filepath.Join(filepath.Dir(path), temporaryName)
		if removeErr := os.Remove(temporaryPath); removeErr != nil {
			t.Fatal(removeErr)
		}
		if writeErr := os.WriteFile(temporaryPath, []byte("unrelated"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return stop
	})
	if !errors.Is(err, stop) || ReplacementCompleted(err) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
	data, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), temporaryName))
	if readErr != nil || string(data) != "unrelated" {
		t.Fatalf("substituted temporary=%q error=%v", data, readErr)
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
	directory, temporary, relativeOpen, relativeRename, relativeSet, relativeVerify, chmod := openAtomicReplacementDirectory, createAtomicReplacementTemp, openAtomicWindowsRelative, renameAtomicReplacementRelativeHandle, setAtomicReplacementInformation, verifyAtomicReplacementRelative, chmodAtomicReplacement
	open, rename, verify, flush, remove, before := openAtomicReplacementSource, renameAtomicReplacement, verifyAtomicReplacement, flushAtomicReplacement, removeAtomicReplacement, atomicBeforeTemporaryCleanupIdentity
	t.Cleanup(func() {
		openAtomicReplacementDirectory, createAtomicReplacementTemp, openAtomicWindowsRelative, renameAtomicReplacementRelativeHandle, setAtomicReplacementInformation, verifyAtomicReplacementRelative, chmodAtomicReplacement = directory, temporary, relativeOpen, relativeRename, relativeSet, relativeVerify, chmod
		openAtomicReplacementSource, renameAtomicReplacement, verifyAtomicReplacement, flushAtomicReplacement, removeAtomicReplacement, atomicBeforeTemporaryCleanupIdentity = open, rename, verify, flush, remove, before
	})
}
