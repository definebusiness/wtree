//go:build windows

package fsutil

func isUnsupportedSyncError(error) bool { return false }
