//go:build !windows

package service

import (
	"errors"
	"os"
	"path/filepath"
)

type unixCloneStagingLease struct{}

func createCloneStaging(parent, prefix string, parentInfo os.FileInfo, mkdirTemp func(string, string) (string, error), lstat func(string) (os.FileInfo, error)) (string, os.FileInfo, os.FileInfo, cloneStagingLease, error) {
	if mkdirTemp == nil || lstat == nil {
		return "", nil, nil, nil, errors.New("clone staging dependencies are not configured")
	}
	staging, err := mkdirTemp(parent, prefix)
	if err != nil {
		return "", nil, nil, nil, err
	}
	staging = filepath.Clean(staging)
	owned, statErr := lstat(staging)
	if statErr != nil || owned == nil || !cloneStagingPathIsSafe(staging, prefix, owned, parentInfo, cloneStagingModeIsPrivate(owned.Mode()), lstat) {
		return "", nil, nil, nil, errors.Join(errors.New("staging creator returned an unsafe path"), statErr)
	}
	return staging, owned, parentInfo, unixCloneStagingLease{}, nil
}

func (unixCloneStagingLease) prepareChild(_ string, _ string, owned, _ os.FileInfo, _ func(string, os.FileMode) error, _ func(string) (os.FileInfo, error)) (os.FileInfo, error) {
	return owned, nil
}

func (unixCloneStagingLease) captureChild(_, _ string, owned, _ os.FileInfo, _ func(string) (os.FileInfo, error)) (os.FileInfo, error) {
	return owned, nil
}

func (unixCloneStagingLease) releaseChild(staging string, owned, parent os.FileInfo, lstat func(string) (os.FileInfo, error)) error {
	if !clonePathHasParentIdentity(staging, parent, lstat) {
		return errors.New("clone staging parent identity changed")
	}
	actual, err := lstat(staging)
	if err != nil || actual == nil || !actual.IsDir() || actual.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, actual) || !cloneStagingModeIsPrivate(actual.Mode()) {
		return errors.Join(errors.New("clone staging identity, type, or private mode changed"), err)
	}
	return nil
}

func (unixCloneStagingLease) closeAll() error { return nil }

func cloneStagingModeIsPrivate(mode os.FileMode) bool { return mode.Perm()&0o077 == 0 }

func requestedFilePermissionsMatch(actual, requested os.FileMode) bool {
	return actual.Perm() == requested.Perm()
}

func reconcileCloneRootAfterRename(expected *cloneTreeEntry, actual os.FileInfo, _ bool) bool {
	return expected != nil && expected.info != nil && actual.IsDir() && actual.Mode()&os.ModeSymlink == 0 &&
		os.SameFile(expected.info, actual) && expected.mode == actual.Mode() && expected.size == actual.Size() &&
		expected.mtime == actual.ModTime().UnixNano() && expected.digest == ""
}
