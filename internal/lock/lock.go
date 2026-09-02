// Package lock wraps cross-platform advisory locks for registry and projects.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/fsutil"
	"github.com/gofrs/flock"
)

type Handle interface{ Unlock() error }
type Manager struct{}

var privateLockAuthorityStepHook fsutil.AtomicStepHook

func (Manager) Acquire(ctx context.Context, path string, timeout time.Duration) (Handle, error) {
	context, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	file := flock.New(path)
	locked, err := file.TryLockContext(context, 50*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("acquire lock %q: %w", path, err)
	}
	if !locked {
		return nil, fmt.Errorf("lock %q is held", path)
	}
	return file, nil
}

// AcquireImmediate takes an advisory lock without allowing a concurrent
// operation to wait behind the current owner. Hook execution uses this to
// prevent a stale overlapping invocation from becoming a new run after the
// owner has durably removed its finalizing record.
func (Manager) AcquireImmediate(ctx context.Context, path string) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file := flock.New(path)
	locked, err := file.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire lock %q: %w", path, err)
	}
	if !locked {
		return nil, fmt.Errorf("lock %q is held", path)
	}
	return file, nil
}
func (manager Manager) RegistryLock(ctx context.Context, dataDir string, timeout time.Duration) (Handle, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	return manager.Acquire(ctx, filepath.Join(dataDir, "registry.lock"), timeout)
}
func (manager Manager) ProjectLock(ctx context.Context, dataDir, projectID string, timeout time.Duration) (Handle, error) {
	if projectID == "" || projectID == "." || projectID == ".." || filepath.IsAbs(projectID) || projectID != filepath.Base(projectID) || filepath.Clean(projectID) != projectID || strings.ContainsAny(projectID, "/\\\x00") {
		return nil, fmt.Errorf("unsafe project ID %q", projectID)
	}
	path := filepath.Join(dataDir, "projects", projectID, "project.lock")
	authority, err := fsutil.OpenPrivatePathForMutation(dataDir, []string{"projects", projectID}, "project.lock")
	if err != nil {
		return nil, err
	}
	if err := privateLockAuthorityStepHookCall("project-after-open"); err != nil {
		authority.Close()
		return nil, err
	}
	lockContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		lock, err := authority.TryLock(lockContext)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, fsutil.ErrPrivateLockHeld) {
			authority.Close()
			return nil, fmt.Errorf("acquire lock %q: %w", path, err)
		}
		select {
		case <-lockContext.Done():
			authority.Close()
			return nil, fmt.Errorf("acquire lock %q: %w", path, lockContext.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// HookRunLock serializes one project/workspace/event hook execution without
// taking the project mutation lock.
func (manager Manager) HookRunLock(ctx context.Context, dataDir, projectID, workspaceID, event string, _ time.Duration) (Handle, error) {
	path, err := HookRunLockPath(dataDir, projectID, workspaceID, event)
	if err != nil {
		return nil, err
	}
	authority, err := fsutil.OpenPrivatePath(dataDir, []string{"projects", projectID, "hooks", workspaceID}, event+".lock", true)
	if err != nil {
		return nil, err
	}
	if err := privateLockAuthorityStepHookCall("hook-after-open"); err != nil {
		authority.Close()
		return nil, err
	}
	lock, err := authority.TryLock(ctx)
	if err != nil {
		authority.Close()
		return nil, fmt.Errorf("acquire lock %q: %w", path, err)
	}
	return lock, nil
}

func privateLockAuthorityStepHookCall(step string) error {
	if privateLockAuthorityStepHook == nil {
		return nil
	}
	return privateLockAuthorityStepHook(step)
}

func HookRunLockPath(dataDir, projectID, workspaceID, event string) (string, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir || !safeID(projectID) || !safeID(workspaceID) || !safeID(event) {
		return "", fmt.Errorf("unsafe hook run lock path")
	}
	return filepath.Join(dataDir, "projects", projectID, "hooks", workspaceID, event+".lock"), nil
}

func safeID(value string) bool {
	return config.ValidatePortableID(value) == nil
}
