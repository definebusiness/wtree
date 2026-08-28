//go:build !windows

package store_test

import (
	"os"
	"testing"
)

func assertPrivateStoreFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("mode = %o, want no group or other access", info.Mode().Perm())
	}
}
