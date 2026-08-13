// Package fsutil contains small filesystem portability helpers.
package fsutil

import "os"

// Sync flushes file data when the backing filesystem supports it. Some mounted
// filesystems, including Apple VirtioFS, perform normal writes and atomic
// renames but report that fsync itself is unsupported. Those capability errors
// cannot be retried meaningfully; all other sync failures remain fatal.
func Sync(file *os.File) error {
	err := file.Sync()
	if err == nil || isUnsupportedSyncError(err) {
		return nil
	}
	return err
}
