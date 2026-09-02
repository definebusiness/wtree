//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/fsutil"
)

func assertPrivateHookRecord(t *testing.T, path string) {
	t.Helper()
	authority, err := openHookRecordPath(path, false)
	if err != nil {
		t.Fatalf("record privacy: %v", err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsHookRunRemovalBindsExactOpenGeneration(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	var renameErr error
	hookRunRemoveStepHook = func(step string) error {
		if step == "before-quarantine" {
			renameErr = os.Rename(path, filepath.Join(filepath.Dir(path), "replacement.json"))
		}
		return nil
	}
	defer func() { hookRunRemoveStepHook = nil }()
	if err := RemoveHookRunRecord(path); err != nil {
		t.Fatal(err)
	}
	if renameErr == nil {
		t.Fatal("active removal handle allowed generation replacement")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("record remains: %v", err)
	}
}

func TestWindowsHookRunRemovalStillReportsPostDeleteDurabilityUncertainty(t *testing.T) {
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
	if err := RemoveHookRunRecord(path); err != ErrHookRunRemovalDurabilityUnconfirmed {
		t.Fatalf("RemoveHookRunRecord()=%v", err)
	}
	if called != 1 {
		t.Fatalf("store sync calls=%d, want 1", called)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("record remains: %v", err)
	}
}

func assertRejectsUnsafePrivateHookRecordComponents(t *testing.T, path string, record HookRunRecord) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteHookRunRecord(path, record); err == nil {
		t.Fatal("accepted directory in hook record leaf position")
	}
}
