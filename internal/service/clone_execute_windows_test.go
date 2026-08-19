//go:build windows

package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
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

func TestWindowsRequestedFileModeDoesNotUseSynthesizedPermissionBits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o600 {
		t.Fatal("Windows unexpectedly exposed requested POSIX permission bits")
	}
	if !requestedFilePermissionsMatch(info.Mode(), 0o600) {
		t.Fatalf("synthesized mode %o rejected requested mode 0600", info.Mode().Perm())
	}
	if requestedFilePermissionsMatch(info.Mode(), 0o400) {
		t.Fatalf("writable synthesized mode %o accepted read-only request", info.Mode().Perm())
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("Windows read-only file exposed writable mode %o", info.Mode().Perm())
	}
	if requestedFilePermissionsMatch(info.Mode(), 0o600) {
		t.Fatalf("read-only mode %o accepted writable request", info.Mode().Perm())
	}
	if !requestedFilePermissionsMatch(info.Mode(), 0o400) {
		t.Fatalf("read-only mode %o rejected read-only request", info.Mode().Perm())
	}
	if snapshot := (cloneFileSnapshot{exists: true, data: []byte("exact"), mode: info.Mode()}); cloneSnapshotHasExactBytes(snapshot, []byte("exact"), 0o600) {
		t.Fatal("read-only exact-byte publication snapshot accepted as writable")
	}
}

func TestWindowsCloneExecuteRejectsExactReadOnlyConfig(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	writer := func(path string, value config.ProjectConfig) error {
		data, err := config.MarshalProject(value)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
		return os.Chmod(path, 0o400)
	}
	_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, WriteConfig: writer}).Execute(context.Background(), plan, nil)
	if err == nil || !hasCloneErrorKind(err, ErrorInternal) {
		t.Fatalf("error = %v, want internal rejection of exact read-only config", err)
	}
	if _, statErr := os.Lstat(plan.Destination.Path); !os.IsNotExist(statErr) {
		t.Fatalf("destination published after read-only config rejection: %v", statErr)
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
