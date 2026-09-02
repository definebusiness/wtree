//go:build windows

package lock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertPrivateHookLockDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("directory %s type=%v %v", path, info, err)
	}
}

func TestWindowsProjectAndHookRunLockLeasePreventsIntermediateAncestorReplacement(t *testing.T) {
	for _, test := range []struct {
		name string
		lock func(Manager, string) (Handle, error)
	}{
		{"project", func(manager Manager, dataDir string) (Handle, error) {
			return manager.ProjectLock(context.Background(), dataDir, "project", time.Second)
		}},
		{"hook-run", func(manager Manager, dataDir string) (Handle, error) {
			return manager.HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			lock, err := test.lock(Manager{}, dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(dataDir, "projects"), filepath.Join(dataDir, "old-projects")); err == nil {
				_ = lock.Unlock()
				t.Fatal("active Windows lock lease allowed intermediate ancestor replacement")
			}
			if err := lock.Unlock(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
