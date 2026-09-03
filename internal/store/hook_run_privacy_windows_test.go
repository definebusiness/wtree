//go:build windows

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/fsutil"
	"github.com/definebusiness/wtree/internal/lock"
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

func TestWindowsHookRunRecordTransitionsUnderRetainedEventLock(t *testing.T) {
	dataDir := t.TempDir()
	path, err := HookRunRecordPath(dataDir, "project", "workspace", "post-create")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := (lock.Manager{}).HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Unlock()
	if _, err := ReadHookRunRecord(path); !os.IsNotExist(err) {
		t.Fatalf("initial record read = %v, want not exist", err)
	}
	_, record := hookRecordTestPathAndValue(t)
	record.ProjectID, record.WorkspaceID, record.Event = "project", "workspace", "post-create"
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatalf("write running record: %v", err)
	}
	if got, err := ReadHookRunRecord(path); err != nil || got.State != "running" {
		t.Fatalf("read running record = %#v, %v", got, err)
	}
	record.CompletedHookIDs, record.NextIndex, record.State = []string{"setup"}, 1, "finalizing"
	record.UpdatedAt = record.UpdatedAt.Add(time.Second)
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatalf("write finalizing record: %v", err)
	}
	if got, err := ReadHookRunRecord(path); err != nil || got.State != "finalizing" || got.NextIndex != 1 {
		t.Fatalf("read finalizing record = %#v, %v", got, err)
	}
	if err := RemoveHookRunRecord(path); err != nil {
		t.Fatalf("remove finalizing record: %v", err)
	}
	if _, err := ReadHookRunRecord(path); !os.IsNotExist(err) {
		t.Fatalf("final record read = %v, want not exist", err)
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
