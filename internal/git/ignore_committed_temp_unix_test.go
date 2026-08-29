//go:build !windows

package git

import (
	"errors"
	"os"
	"testing"
)

func TestCommittedIgnoreExcludeIsPrivateAndCleansOnUnix(t *testing.T) {
	path, cleanup, err := committedIgnoreExclude([]byte("/child/\n"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("exclude permissions = %o, want 600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "/child/\n" {
		t.Fatalf("exclude contents = %q, %v", contents, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exclude remains after cleanup: %v", err)
	}
}
