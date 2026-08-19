//go:build !windows

package service

import "os"

func cloneStagingModeIsPrivate(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}

func requestedFilePermissionsMatch(actual, requested os.FileMode) bool {
	return actual.Perm() == requested.Perm()
}

func reconcileCloneRootAfterRename(expected *cloneTreeEntry, actual os.FileInfo) bool {
	return expected != nil && expected.info != nil && actual.IsDir() && actual.Mode()&os.ModeSymlink == 0 &&
		os.SameFile(expected.info, actual) && expected.mode == actual.Mode() && expected.size == actual.Size() &&
		expected.mtime == actual.ModTime().UnixNano() && expected.digest == ""
}
