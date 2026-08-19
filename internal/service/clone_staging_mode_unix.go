//go:build !windows

package service

import "os"

func cloneStagingModeIsPrivate(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}
