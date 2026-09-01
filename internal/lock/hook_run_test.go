package lock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHookRunLockUsesPrivateEventSiblingPath(t *testing.T) {
	dataDir := t.TempDir()
	path, err := HookRunLockPath(dataDir, "project", "workspace", "post-create")
	if err != nil || path != filepath.Join(dataDir, "projects", "project", "hooks", "workspace", "post-create.lock") {
		t.Fatalf("HookRunLockPath()=%q %v", path, err)
	}
	handle, err := (Manager{}).HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Unlock()
	for _, directory := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path)), filepath.Dir(filepath.Dir(filepath.Dir(path)))} {
		info, statErr := os.Stat(directory)
		if statErr != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s mode=%v %v", directory, info.Mode(), statErr)
		}
	}
	if _, err := HookRunLockPath(dataDir, "../project", "workspace", "post-create"); err == nil {
		t.Fatal("accepted unsafe id")
	}
}

func TestHookRunLockRejectsHeldOwnershipImmediately(t *testing.T) {
	dataDir := t.TempDir()
	manager := Manager{}
	first, err := manager.HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Unlock()
	started := time.Now()
	second, err := manager.HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
	if second != nil || err == nil || time.Since(started) > 200*time.Millisecond {
		t.Fatalf("HookRunLock() handle=%v error=%v elapsed=%s", second, err, time.Since(started))
	}
}
