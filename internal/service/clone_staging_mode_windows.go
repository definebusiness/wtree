//go:build windows

package service

import "os"

// Windows synthesizes POSIX permission bits from file attributes; MkdirTemp
// directories normally report 0777 even though their access is ACL-governed.
func cloneStagingModeIsPrivate(os.FileMode) bool { return true }

// Windows FileMode owner/group bits are synthesized and do not prove that a
// requested POSIX ACL was installed. Its write bits do expose the read-only
// file attribute, so require that observable state to match the request.
func requestedFilePermissionsMatch(actual, requested os.FileMode) bool {
	return observableFileWritabilityMatches(actual, requested)
}

// NTFS can change a directory timestamp as part of the rename itself. Accept
// only that platform-owned transition, then retain the observed timestamp so
// every later inventory revalidation still detects metadata mutation.
func reconcileCloneRootAfterRename(expected *cloneTreeEntry, actual os.FileInfo) bool {
	if expected == nil || expected.info == nil || !actual.IsDir() || actual.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(expected.info, actual) || expected.mode != actual.Mode() || expected.size != actual.Size() || expected.digest != "" {
		return false
	}
	expected.mtime = actual.ModTime().UnixNano()
	return true
}
