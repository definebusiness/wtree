//go:build windows

package store_test

import (
	"os"
	"testing"
)

// Windows does not preserve POSIX 0600 bits. Its observable equivalent for a
// state file is that the effective owner can open it for write and that the
// read-only attribute has not replaced the requested owner-write permission.
func assertPrivateStoreFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 == 0 {
		t.Fatalf("state file is read-only: mode=%o", info.Mode().Perm())
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open state file for effective-owner write: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
