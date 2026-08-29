//go:build windows

package cli_test

import (
	"os"
	"testing"
)

// Windows does not report POSIX 0600 mode bits. The portable observable
// contract for a private backup is that its effective owner can write it and
// it has not been made read-only in place of the requested protection.
func assertPrivateBackupFile(t *testing.T, path string, info os.FileInfo) {
	t.Helper()
	if info.Mode().Perm()&0o222 == 0 {
		t.Fatalf("backup %q is read-only: mode=%o", path, info.Mode().Perm())
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open backup %q for effective-owner write: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
