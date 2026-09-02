//go:build !windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/fsutil"
)

func assertPrivateHookRecord(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode=%v %v", info, err)
	}
}

func assertRejectsUnsafePrivateHookRecordComponents(t *testing.T, path string, record HookRunRecord) {
	t.Helper()
	assertRejectsUnsafePrivateHookRecordComponentsUnix(t, path, record)
}

func TestHookRunRecordRemovalNeverDeletesSubstitutedGeneration(t *testing.T) {
	testHookRunRecordRemovalNeverDeletesSubstitutedGeneration(t)
}

func TestHookRunRecordRemovalNeverOverwritesConcurrentRestoreTarget(t *testing.T) {
	testHookRunRecordRemovalNeverOverwritesConcurrentRestoreTarget(t)
}

func TestHookRunRecordRemovalRestoresExactGenerationAfterQuarantineFailure(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hookRunRemoveStepHook = func(step string) error {
		if step == "after-quarantine" {
			return os.ErrPermission
		}
		return nil
	}
	defer func() { hookRunRemoveStepHook = nil }()
	if err := RemoveHookRunRecord(path); !errors.Is(err, fsutil.ErrPrivateRemovalAmbiguous) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("RemoveHookRunRecord()=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("restored record=%q %v, want %q", after, err, before)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("restored entries=%v %v", entries, err)
	}
}

func TestHookRunRecordSuccessfulRemovalRetainsOnePrivateQuarantineGeneration(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	want, err := HookRunRecordBytes(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHookRunRecord(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authoritative record remains: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Name(), ".post-create.json.remove-") {
		t.Fatalf("quarantine evidence=%v %v", entries, err)
	}
	evidencePath := filepath.Join(filepath.Dir(path), entries[0].Name())
	got, err := os.ReadFile(evidencePath)
	if err != nil || string(got) != string(want) {
		t.Fatalf("quarantine bytes=%q %v, want %q", got, err, want)
	}
	info, err := os.Lstat(evidencePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("quarantine mode=%v %v", info, err)
	}
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHookRunRecord(path); !errors.Is(err, fsutil.ErrPrivateRemovalAmbiguous) || errors.Is(err, fsutil.ErrPrivateRemovalQuarantined) {
		t.Fatalf("second removal with retained evidence=%v", err)
	}
	entries, err = os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("bounded evidence entries=%v %v", entries, err)
	}
	if got, err := ReadHookRunRecord(path); err != nil || got.WorkspaceName != record.WorkspaceName {
		t.Fatalf("second authoritative generation=%#v %v", got, err)
	}
}

func TestHookRunRecordQuarantinedRemovalIsLogicalCompletionWithoutStoreSync(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	called := 0
	oldSync := hookRunRemoveSync
	hookRunRemoveSync = func(*fsutil.PrivatePath) error {
		called++
		return os.ErrPermission
	}
	defer func() { hookRunRemoveSync = oldSync }()

	// RED before the R10c-n2 fix: RemoveHookRunRecord returned durability
	// uncertainty here and a runner consequently re-published finalizing even
	// though fsutil had already synced and revalidated the exact quarantine.
	if err := RemoveHookRunRecord(path); err != nil {
		t.Fatalf("RemoveHookRunRecord()=%v", err)
	}
	if called != 0 {
		t.Fatalf("redundant store sync calls=%d, want 0", called)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authoritative record remains: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Name(), ".remove-") {
		t.Fatalf("verified quarantine evidence=%v %v", entries, err)
	}
}

func TestHookRunRecordRemovalNeverDeletesFinalQuarantineSubstitution(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	var displaced string
	hookRunRemoveStepHook = func(step string) error {
		if step != "before-delete" {
			return nil
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.Contains(entry.Name(), ".remove-") {
				continue
			}
			quarantine := filepath.Join(filepath.Dir(path), entry.Name())
			displaced = quarantine + ".opened"
			if err := os.Rename(quarantine, displaced); err != nil {
				return err
			}
			return os.WriteFile(quarantine, []byte("replacement"), 0o600)
		}
		return errors.New("quarantine entry not found")
	}
	defer func() { hookRunRemoveStepHook = nil }()
	if err := RemoveHookRunRecord(path); !errors.Is(err, fsutil.ErrPrivateRemovalAmbiguous) {
		t.Fatalf("RemoveHookRunRecord()=%v", err)
	}
	if displaced == "" {
		t.Fatal("final deletion seam was not reached")
	}
	replacement, err := os.ReadFile(strings.TrimSuffix(displaced, ".opened"))
	if err != nil || string(replacement) != "replacement" {
		t.Fatalf("replacement quarantine generation=%q %v", replacement, err)
	}
	original, err := os.ReadFile(displaced)
	if err != nil || len(original) == 0 {
		t.Fatalf("verified quarantine generation=%q %v", original, err)
	}
}
