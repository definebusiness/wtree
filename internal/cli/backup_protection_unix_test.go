//go:build !windows

package cli_test

import (
	"os"
	"testing"
)

func assertPrivateBackupFile(t *testing.T, path string, info os.FileInfo) {
	t.Helper()
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup %q mode=%o, want 600", path, info.Mode().Perm())
	}
}
