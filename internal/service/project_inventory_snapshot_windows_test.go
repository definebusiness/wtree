//go:build windows

package service_test

import (
	"io/fs"
	"os"
)

func inventorySnapshotModTime(entry fs.DirEntry, info os.FileInfo) int64 {
	if entry.IsDir() {
		// Directory mtimes are updated lazily by NTFS and can settle after a
		// read-only traversal. Membership plus file bytes, mode, and mtime
		// remain the no-mutation authority on Windows.
		return 0
	}
	return info.ModTime().UnixNano()
}
