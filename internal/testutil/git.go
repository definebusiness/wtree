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

// RequireIntegration marks the first boundary that would start a real Git
// command or a process-heavy test fixture.  Short mode is deliberately a
// fast, deterministic lane: it retains pure and fake-adapter tests while it
// skips before an integration process can be created.
//
// This helper lives with the fixture rather than in callers so a new fixture
// operation cannot accidentally start its first Git process before checking
// the lane policy.
func RequireIntegration(t testing.TB, capability string) {
	t.Helper()
	if testing.Short() {
		t.Skipf("short mode skips %s integration fixture", capability)
	}
}

// GitCommand classifies a direct Git command used by tests that construct a
// fixture outside GitRepository. Callers use it at the command-creation
// boundary so short mode cannot start the process before skipping.
func GitCommand(t testing.TB, args ...string) *exec.Cmd {
	t.Helper()
	RequireIntegration(t, "real Git")
	command := exec.Command("git", args...)
	command.Env = gitFixtureEnvironment()
	return command
}

// NewGitRepository initializes an isolated repository without relying on user
// Git configuration, credentials, hooks, or network access.
func NewGitRepository(t testing.TB) GitRepository {
	t.Helper()
	RequireIntegration(t, "real Git")
	path := t.TempDir()
	runGit(t, path, "init", "--initial-branch=main")
	return GitRepository{Path: path}
}

// PushedGitRepository is an explicit fixture for consumers that require a
// configured, published upstream. Ordinary GitRepository behavior stays local.
type PushedGitRepository struct{ GitRepository }

func NewPushedGitRepository(t testing.TB) PushedGitRepository {
	t.Helper()
	repository := NewGitRepository(t)
	remote := NewBareGitRemote(t)
	repository.Run(t, "remote", "add", "origin", remote)
	return PushedGitRepository{repository}
}

func (r PushedGitRepository) CommitFile(name, contents, message string) {
	r.GitRepository.CommitFile(name, contents, message)
	r.RunPanic("push", "-u", "origin", "main")
}

func (r GitRepository) RunPanic(args ...string) { runGitPanic(r.Path, args...) }

// NewBareGitRemote creates an isolated bare remote for clone and advertised
// reference tests. It shares the fixture's no-network, no-user-config setup.
func NewBareGitRemote(t testing.TB) string {
	t.Helper()
	RequireIntegration(t, "real Git")
	parent := t.TempDir()
	path := filepath.Join(parent, "remote.git")
	runGit(t, parent, "init", "--bare", path)
	return path
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
		"GIT_AUTHOR_NAME=wtree test",
		"GIT_AUTHOR_EMAIL=wtree@example.invalid",
		"GIT_COMMITTER_NAME=wtree test",
		"GIT_COMMITTER_EMAIL=wtree@example.invalid",
		"LC_ALL=C",
		"LANG=C",
	}
}
