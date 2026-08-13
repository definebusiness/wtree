// Package lock wraps cross-platform advisory locks for registry and projects.
package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

type Handle interface{ Unlock() error }
type Manager struct{}

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
func (manager Manager) RegistryLock(ctx context.Context, dataDir string, timeout time.Duration) (Handle, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	return manager.Acquire(ctx, filepath.Join(dataDir, "registry.lock"), timeout)
}
func (manager Manager) ProjectLock(ctx context.Context, dataDir, projectID string, timeout time.Duration) (Handle, error) {
	if projectID == "" || projectID != filepath.Base(projectID) || projectID == "." {
		return nil, fmt.Errorf("unsafe project ID %q", projectID)
	}
	directory := filepath.Join(dataDir, "projects", projectID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	return manager.Acquire(ctx, filepath.Join(directory, "project.lock"), timeout)
}
