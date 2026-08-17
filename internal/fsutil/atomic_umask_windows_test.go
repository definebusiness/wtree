//go:build windows

package fsutil

import "testing"

func TestWriteFileAtomicCreateModeUmaskIsPlatformManaged(t *testing.T) {
	t.Skip("Windows does not expose POSIX process umask semantics")
}
