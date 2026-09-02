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
		assertPrivateHookLockDirectory(t, directory)
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

func TestProjectLockCreationConvergesWithLaterHookRunAuthority(t *testing.T) {
	dataDir := t.TempDir()
	manager := Manager{}
	project, err := manager.ProjectLock(context.Background(), dataDir, "project", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}
	hook, err := manager.HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
	if err != nil {
		t.Fatalf("HookRunLock after ProjectLock: %v", err)
	}
	if err := hook.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectLockMutationRepairsOwnedPreExistingHierarchy(t *testing.T) {
	dataDir := t.TempDir()
	directory := filepath.Join(dataDir, "projects", "project")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "project.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := (Manager{}).ProjectLock(context.Background(), dataDir, "project", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}
	hook, err := (Manager{}).HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
	if err != nil {
		t.Fatalf("HookRunLock after pre-existing ProjectLock hierarchy: %v", err)
	}
	if err := hook.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAndHookRunLocksRejectIntermediateAncestorReplacementBeforeLease(t *testing.T) {
	for _, test := range []struct {
		name string
		step string
		lock func(Manager, string) (Handle, error)
	}{
		{
			name: "project",
			step: "project-after-open",
			lock: func(manager Manager, dataDir string) (Handle, error) {
				return manager.ProjectLock(context.Background(), dataDir, "project", time.Second)
			},
		},
		{
			name: "hook-run",
			step: "hook-after-open",
			lock: func(manager Manager, dataDir string) (Handle, error) {
				return manager.HookRunLock(context.Background(), dataDir, "project", "workspace", "post-create", time.Second)
			},
		},
	} {
		for _, scenario := range []string{"missing", "replacement"} {
			t.Run(test.name+"/"+scenario, func(t *testing.T) {
				dataDir := t.TempDir()
				manager := Manager{}
				var fresh Handle
				privateLockAuthorityStepHook = func(step string) error {
					if step != test.step {
						return nil
					}
					privateLockAuthorityStepHook = nil
					if err := os.Rename(filepath.Join(dataDir, "projects"), filepath.Join(dataDir, "old-projects")); err != nil {
						return err
					}
					if scenario == "replacement" {
						var err error
						fresh, err = test.lock(manager, dataDir)
						return err
					}
					return nil
				}
				defer func() { privateLockAuthorityStepHook = nil }()
				detached, err := test.lock(manager, dataDir)
				if detached != nil {
					_ = detached.Unlock()
				}
				if err == nil {
					t.Fatal("lock acquired beneath an intermediate ancestor detached after authority open")
				}
				if scenario == "replacement" {
					if fresh == nil {
						t.Fatal("fresh authoritative lock was not acquired")
					}
					if err := fresh.Unlock(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
