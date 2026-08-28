//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixPrivateUpdateDirectoryRequiresExactPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateUpdateDirectory(path, info); err != nil {
		t.Fatalf("protect private directory: %v", err)
	}
	if err := validatePrivateUpdateDirectory(path, info); err != nil {
		t.Fatalf("validate private directory: %v", err)
	}
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateUpdateDirectory(path, changed); err == nil {
		t.Fatal("group-accessible update directory was accepted")
	}
}
