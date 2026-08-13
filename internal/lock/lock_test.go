package lock_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcel/wtree/internal/lock"
)

func TestManagerReportsContentionBeforeTimeout(t *testing.T) {
	manager := lock.Manager{}
	path := filepath.Join(t.TempDir(), "project.lock")
	first, err := manager.Acquire(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Unlock()
	if _, err := manager.Acquire(context.Background(), path, 20*time.Millisecond); err == nil {
		t.Fatal("second lock acquired")
	}
}

func TestNamedRegistryAndProjectLocks(t *testing.T) {
	manager := lock.Manager{}
	if handle, err := manager.RegistryLock(context.Background(), t.TempDir(), time.Second); err != nil {
		t.Fatal(err)
	} else {
		defer handle.Unlock()
	}
	if handle, err := manager.ProjectLock(context.Background(), t.TempDir(), "project-id", time.Second); err != nil {
		t.Fatal(err)
	} else {
		defer handle.Unlock()
	}
}

func TestProjectLockRejectsTraversalID(t *testing.T) {
	if _, err := (lock.Manager{}).ProjectLock(context.Background(), t.TempDir(), "../escape", time.Second); err == nil {
		t.Fatal("traversal project ID accepted")
	}
}
