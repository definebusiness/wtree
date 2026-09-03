//go:build !windows

package fsutil

import "os"

func atomicReplaceWithInfo(source, destination string, _ os.FileInfo) error {
	return os.Rename(source, destination)
}

func removeAtomicTemporary(path string, _ os.FileInfo) error { return os.Remove(path) }
