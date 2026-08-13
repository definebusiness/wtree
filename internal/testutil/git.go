package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// GitRepository is a hermetic repository with a local test-only identity.
type GitRepository struct {
	Path string
}

// NewGitRepository initializes an isolated repository without relying on user
// Git configuration, credentials, hooks, or network access.
func NewGitRepository(t testing.TB) GitRepository {
	t.Helper()
	path := t.TempDir()
	runGit(t, path, "init", "--initial-branch=main")
	runGit(t, path, "config", "user.name", "wtree test")
	runGit(t, path, "config", "user.email", "wtree@example.invalid")
	return GitRepository{Path: path}
}

// CommitFile writes and commits one fixture file.
func (r GitRepository) CommitFile(name, contents, message string) {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(r.Path, name)), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(r.Path, name), []byte(contents), 0o644); err != nil {
		panic(err)
	}
	runGitPanic(r.Path, "add", "--", name)
	runGitPanic(r.Path, "commit", "-m", message)
}

// Run executes a Git command in the fixture and fails the caller on error.
func (r GitRepository) Run(t testing.TB, args ...string) {
	t.Helper()
	runGit(t, r.Path, args...)
}

func runGit(t testing.TB, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = gitFixtureEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func runGitPanic(directory string, args ...string) {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = gitFixtureEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		panic(string(output))
	}
}

func gitFixtureEnvironment() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"SystemRoot=" + os.Getenv("SystemRoot"),
		"WINDIR=" + os.Getenv("WINDIR"),
		"ComSpec=" + os.Getenv("ComSpec"),
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_ATTR_NOSYSTEM=1",
		"LC_ALL=C",
		"LANG=C",
	}
}
