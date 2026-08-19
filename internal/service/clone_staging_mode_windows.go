//go:build windows

package service

import "os"

// Windows synthesizes POSIX permission bits from file attributes; MkdirTemp
// directories normally report 0777 even though their access is ACL-governed.
func cloneStagingModeIsPrivate(os.FileMode) bool { return true }
