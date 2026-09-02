//go:build !windows

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
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory %s mode=%v %v", path, info.Mode(), err)
	}
}

func TestUnixProjectAndHookRunLockUnlockRejectMissingIntermediateAncestor(t *testing.T) {
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
			if err := os.Rename(filepath.Join(dataDir, "projects"), filepath.Join(dataDir, "old-projects")); err != nil {
				t.Fatal(err)
			}
			if err := lock.Unlock(); err == nil {
				t.Fatal("Unlock accepted a missing intermediate ancestor")
			}
		})
	}
}
