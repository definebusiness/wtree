//go:build windows

package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestWindowsAutomaticIgnoreAliasPlanApplyAndRollback(t *testing.T) {
	root, backend, _ := threeLevelIgnoreFixture(t)
	data := t.TempDir()
	rootAlias := alternateAutomaticIgnoreWindowsSpelling(root.Path)
	rootInfo, err := os.Lstat(root.Path)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Lstat(rootAlias)
	if err != nil || !os.SameFile(rootInfo, aliasInfo) {
		t.Fatalf("project alias identity = %v, %v", aliasInfo, err)
	}

	if result := testutil.RunCommand(t, cli.Execute, "init", rootAlias, "--data-dir", data); result.Err != nil {
		t.Fatalf("init through alias = %#v", result)
	}
	initial := []byte("/.wtree.yml\n/backend/\n")
	canonicalIgnore := filepath.Join(root.Path, ".gitignore")
	aliasIgnore := filepath.Join(rootAlias, ".gitignore")
	canonicalInfo, err := os.Lstat(canonicalIgnore)
	if err != nil {
		t.Fatal(err)
	}
	aliasFileInfo, err := os.Lstat(aliasIgnore)
	if err != nil || !os.SameFile(canonicalInfo, aliasFileInfo) {
		t.Fatalf("ignore alias identity = %v, %v", aliasFileInfo, err)
	}
	if got, err := os.ReadFile(canonicalIgnore); err != nil || string(got) != string(initial) {
		t.Fatalf("canonical init generation = %q, %v; want %q", got, err, initial)
	}
	unrelated := filepath.Join(root.Path, "unrelated")
	if err := os.WriteFile(unrelated, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	canonicalBackend, err := filepath.EvalSymlinks(backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	buildAutomaticIgnoreGitFailureHelper(t, shimDir, realGit, canonicalBackend, false)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	target := filepath.Join(t.TempDir(), "rollback")
	targetAlias := alternateAutomaticIgnoreWindowsSpelling(target)
	result := testutil.RunCommand(t, cli.Execute, "create", "--project", rootAlias, "feature/rollback-alias", "--data-dir", data, "--path", targetAlias)
	assertCLIErrorContract(t, result, 6, "git", false, "Rollback complete.")
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("rollback retained target generation at canonical path: %v", err)
	}
	if got, err := os.ReadFile(canonicalIgnore); err != nil || string(got) != string(initial) {
		t.Fatalf("source ignore generation after rollback = %q, %v; want %q", got, err, initial)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "preserve" {
		t.Fatalf("unrelated generation after rollback = %q, %v", got, err)
	}
}

func alternateAutomaticIgnoreWindowsSpelling(path string) string {
	volume := filepath.VolumeName(path)
	if len(volume) >= 2 && volume[1] == ':' {
		first := volume[:1]
		if first == strings.ToLower(first) {
			first = strings.ToUpper(first)
		} else {
			first = strings.ToLower(first)
		}
		return first + path[1:]
	}
	return filepath.ToSlash(path)
}
