//go:build !windows

package fsutil

import (
	"os"
	"testing"
)

func assertAtomicRequestedMode(t *testing.T, actual, requested os.FileMode) {
	t.Helper()
	if actual.Perm() != requested.Perm() {
		t.Fatalf("mode = %o, want %o", actual.Perm(), requested.Perm())
	}
}
