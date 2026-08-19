//go:build windows

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsCloneStagingPathAcceptsEquivalentParentSpelling(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ".clone.wtree-clone-"
	staging, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		t.Fatal(err)
	}
	staging = filepath.Clean(filepath.ToSlash(swapWindowsDriveCase(staging)))
	stagingInfo, err := os.Lstat(staging)
	if err != nil {
		t.Fatal(err)
	}
	if stagingInfo.Mode().Perm()&0o077 == 0 {
		t.Fatal("Windows unexpectedly exposed enforceable POSIX-private directory permissions")
	}
	if !cloneStagingPathIsSafe(staging, prefix, stagingInfo, parentInfo, os.Lstat) {
		t.Fatalf("equivalent Windows staging spelling was rejected: %q", staging)
	}
}

func swapWindowsDriveCase(path string) string {
	volume := filepath.VolumeName(path)
	if len(volume) < 2 || volume[1] != ':' {
		return path
	}
	first := volume[:1]
	if first == strings.ToLower(first) {
		first = strings.ToUpper(first)
	} else {
		first = strings.ToLower(first)
	}
	return first + path[1:]
}
