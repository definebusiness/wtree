//go:build !windows

package service_test

import (
	"io/fs"
	"os"
)

func inventorySnapshotModTime(_ fs.DirEntry, info os.FileInfo) int64 {
	return info.ModTime().UnixNano()
}
