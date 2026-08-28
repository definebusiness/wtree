//go:build windows

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func assertAtomicRequestedMode(t *testing.T, actual, requested os.FileMode) {
	t.Helper()
	if got, want := actual.Perm()&0o222 != 0, requested.Perm()&0o222 != 0; got != want {
		t.Fatalf("observable writability = %t for mode %o, want %t for requested mode %o", got, actual.Perm(), want, requested.Perm())
	}
}

func TestWindowsAtomicRequestedModesPreserveObservableWritability(t *testing.T) {
	for _, writer := range []struct {
		name  string
		write func(string, []byte, os.FileMode) error
	}{
		{name: "replace", write: WriteFileAtomicMode},
		{name: "create", write: WriteFileAtomicCreateMode},
	} {
		for _, existing := range []bool{false, true} {
			for _, requested := range []os.FileMode{0o600, 0o400} {
				t.Run(writer.name+"/"+map[bool]string{false: "missing", true: "existing"}[existing]+"/"+requested.String(), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "target")
					if existing {
						if err := os.WriteFile(path, []byte("old generation"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if err := writer.write(path, []byte("new generation"), requested); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(path)
					if err != nil || string(data) != "new generation" {
						t.Fatalf("generation = %q, %v", data, err)
					}
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					assertAtomicRequestedMode(t, info.Mode(), requested)
				})
			}
		}
	}
}
