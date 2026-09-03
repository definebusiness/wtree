package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/discovery"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestDiscoverFindsRootAndIndependentNestedRepositories(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	nested := filepath.Join(root.Path, "backend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
		t.Fatalf("init nested: %v %s", err, output)
	}
	got, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotIDs := []string{got[0].ID, got[1].ID}; !reflect.DeepEqual(gotIDs, []string{"root", "backend"}) {
		t.Fatalf("ids=%v", gotIDs)
	}
}

func TestDiscoverUsesExplicitPlainLogicalRootForGroupedTopLevelRepositories(t *testing.T) {
	logicalRoot := t.TempDir()
	for _, relative := range []string{"services/api", "services/web"} {
		path := filepath.Join(logicalRoot, relative)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
			t.Fatalf("init %q: %v %s", relative, err, output)
		}
	}

	got, err := discovery.DiscoverContext(context.Background(), logicalRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ParentID != "" || got[0].Mount != "services/api" || got[1].ParentID != "" || got[1].Mount != "services/web" {
		t.Fatalf("repositories = %#v", got)
	}
}

func TestDiscoverDerivesNearestParentsAcrossThreeLevelsBelowPlainBoundary(t *testing.T) {
	logicalRoot := t.TempDir()
	for _, relative := range []string{"api", "api/shared", "api/shared/tools", "web"} {
		path := filepath.Join(logicalRoot, relative)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
			t.Fatalf("init %q: %v %s", relative, err, output)
		}
	}

	got, err := discovery.DiscoverContext(context.Background(), logicalRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]discovery.Repository{}
	for _, repository := range got {
		byID[repository.ID] = repository
	}
	if len(got) != 4 || byID["shared"].ParentID != "api" || byID["shared"].Mount != "shared" || byID["tools"].ParentID != "shared" || byID["tools"].Mount != "tools" || byID["web"].ParentID != "" {
		t.Fatalf("repositories = %#v", got)
	}
}

func TestDiscoverDoesNotFollowRepositorySymlinkOutsideBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks requires privileges on some Windows hosts")
	}
	logicalRoot := t.TempDir()
	inside := filepath.Join(logicalRoot, "inside")
	outside := testutil.NewGitRepository(t)
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", inside).CombinedOutput(); err != nil {
		t.Fatalf("init inside: %v %s", err, output)
	}
	if err := os.Symlink(outside.Path, filepath.Join(logicalRoot, "escape")); err != nil {
		t.Fatal(err)
	}

	got, err := discovery.Discover(logicalRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "inside" {
		t.Fatalf("repositories = %#v", got)
	}
}

func TestDiscoverContextHonorsCancellation(t *testing.T) {
	root := testutil.NewGitRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discovery.DiscoverContext(ctx, root.Path, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverContext() error = %v, want context cancellation", err)
	}
}

func TestDiscoverContextCancelsInFlightSubmoduleInspection(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDirectory := t.TempDir()
	entered, release := filepath.Join(shimDirectory, "entered"), filepath.Join(shimDirectory, "release")
	buildBlockingSubmoduleGit(t, shimDirectory, realGit, entered, release)
	t.Setenv("PATH", shimDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The cleanup release is only a bounded RED-path escape hatch. A passing
	// cancellation must return while the release file is still absent, which
	// proves CommandContext terminated the blocked helper rather than relying
	// on the test to let it complete.
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release\n"), 0o600) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := discovery.DiscoverContext(ctx, repository.Path, nil)
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("blocking ls-files helper was not entered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DiscoverContext() error = %v, want context cancellation", err)
		}
		if _, err := os.Stat(release); !os.IsNotExist(err) {
			t.Fatalf("blocked helper needed test release to exit: %v", err)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("DiscoverContext did not terminate in-flight ls-files after cancellation")
	}
}

func buildBlockingSubmoduleGit(t *testing.T, directory, realGit, entered, release string) {
	t.Helper()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source := filepath.Join(directory, "main.go")
	program := `package main
import (
  "os"
  "os/exec"
  "time"
)
func main() {
  for _, argument := range os.Args[1:] {
    if argument == "ls-files" {
      if err := os.WriteFile(` + quoteGoString(entered) + `, []byte("entered\\n"), 0600); err != nil { os.Exit(2) }
      for {
        if _, err := os.Stat(` + quoteGoString(release) + `); err == nil { os.Exit(0) }
        time.Sleep(10 * time.Millisecond)
      }
    }
  }
  command := exec.Command(` + quoteGoString(realGit) + `, os.Args[1:]...)
  command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
  if err := command.Run(); err != nil {
    if exit, ok := err.(*exec.ExitError); ok { os.Exit(exit.ExitCode()) }
    os.Exit(1)
  }
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", filepath.Join(directory, name), source).CombinedOutput(); err != nil {
		t.Fatalf("build fake git: %v\n%s", err, output)
	}
}

func quoteGoString(value string) string {
	return fmt.Sprintf("%q", value)
}

func TestDiscoverDerivesParentsAndMountsForTwoLevelsAndSiblings(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	for _, relative := range []string{"backend", "backend/shared", "frontend"} {
		path := filepath.Join(root.Path, relative)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
			t.Fatalf("init %q: %v %s", relative, err, output)
		}
	}

	got, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]discovery.Repository, len(got))
	for _, repository := range got {
		byID[repository.ID] = repository
	}
	if len(got) != 4 || byID["backend"].ParentID != "root" || byID["backend"].Mount != "backend" || byID["shared"].ParentID != "backend" || byID["shared"].Mount != "shared" || byID["frontend"].ParentID != "root" || byID["frontend"].Mount != "frontend" {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestDiscoverDoesNotGuessAnEnclosingRepositoryFromAnOrdinaryBoundary(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	ordinaryDirectory := filepath.Join(root.Path, "cmd", "wtree")
	if err := os.MkdirAll(ordinaryDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := discovery.Discover(ordinaryDirectory, nil); err == nil {
		t.Fatal("discovery guessed enclosing repository outside explicit boundary")
	}
}

func TestDiscoverCanonicalizesASymlinkedRepositoryRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks requires privileges on some Windows hosts")
	}
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	link := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(root.Path, link); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := discovery.Discover(link, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != want {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestDiscoverSkipsIgnoredDirectories(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	ignored := filepath.Join(root.Path, "vendor", "dependency")
	if err := os.MkdirAll(ignored, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", ignored).CombinedOutput(); err != nil {
		t.Fatalf("init ignored: %v %s", err, output)
	}
	got, err := discovery.Discover(root.Path, []string{"vendor"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "root" {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestDiscoverSupportsGlobIgnores(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	ignored := filepath.Join(root.Path, "build", "generated")
	if err := os.MkdirAll(ignored, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", ignored).CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	got, err := discovery.Discover(root.Path, []string{"build/**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestShouldIgnorePathUsesDefaultAndConfiguredPatterns(t *testing.T) {
	for _, value := range []struct {
		relative string
		name     string
		ignores  []string
		want     bool
	}{
		{relative: "node_modules/unrelated", name: "unrelated", want: true},
		{relative: "generated/cache", name: "cache", ignores: []string{"generated/**"}, want: true},
		{relative: "generated", name: "generated", ignores: []string{"generated"}, want: true},
		{relative: "outside", name: "outside", ignores: []string{"generated/**"}, want: false},
	} {
		if got := discovery.ShouldIgnorePath(value.relative, value.name, value.ignores); got != value.want {
			t.Fatalf("ShouldIgnorePath(%q, %q, %q) = %t, want %t", value.relative, value.name, value.ignores, got, value.want)
		}
	}
}

func TestDiscoverSkipsBuiltInDependencyAndBuildDirectories(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	for _, relative := range []string{"node_modules/dependency", ".venv/environment", "target/artifact", "build/generated"} {
		directory := filepath.Join(root.Path, relative)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", directory).CombinedOutput(); err != nil {
			t.Fatalf("init %q: %v %s", relative, err, output)
		}
	}

	got, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "root" {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestDiscoverSkipsBuiltInDependencyDirectoriesBelowNestedRepositories(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	nested := filepath.Join(root.Path, "backend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	dependency := filepath.Join(nested, "node_modules", "dependency")
	if err := os.MkdirAll(dependency, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", dependency).CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}

	got, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].ID != "backend" {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestDiscoverRejectsSubmoduleConfiguration(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	if err := os.WriteFile(filepath.Join(root.Path, ".gitmodules"), []byte("[submodule \"x\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := discovery.Discover(root.Path, nil); err == nil {
		t.Fatal("submodule configuration accepted")
	}
}

func TestDiscoverRejectsSubmoduleConfigurationInsideNestedRepository(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	nested := filepath.Join(root.Path, "backend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	if err := os.WriteFile(filepath.Join(nested, ".gitmodules"), []byte("[submodule \"shared\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := discovery.Discover(root.Path, nil); err == nil {
		t.Fatal("nested submodule configuration accepted")
	}
}

func TestDiscoverRejectsTrackedSubmoduleWithoutWorkingTreeGitmodulesFile(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	child := testutil.NewGitRepository(t)
	child.CommitFile("child.txt", "child\n", "child")
	root.Run(t, "-c", "protocol.file.allow=always", "submodule", "add", child.Path, "modules/child")
	if err := os.Remove(filepath.Join(root.Path, ".gitmodules")); err != nil {
		t.Fatal(err)
	}

	if _, err := discovery.Discover(root.Path, nil); err == nil {
		t.Fatal("tracked submodule without .gitmodules was accepted")
	}
}

func TestDiscoverAcceptsWorktreeGitFile(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	root.Run(t, "branch", "worktree")
	path := filepath.Join(t.TempDir(), "linked worktree")
	root.Run(t, "worktree", "add", path, "worktree")
	got, err := discovery.Discover(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "root" {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestDiscoverRejectsInvalidGitFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, ".git"), []byte("not a gitdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := discovery.Discover(directory, nil); err == nil {
		t.Fatal("invalid .git file accepted")
	}
}

func TestDiscoverUsesSafeNonEmptyIDForUnicodeOnlyDirectory(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	nested := filepath.Join(root.Path, "登录")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", nested).CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	got, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[1].ID == "" || got[1].ID == "root" {
		t.Fatalf("unsafe id=%q", got[1].ID)
	}
}

func TestDiscoverAssignsCollisionSafeIDWhenNestedDirectoryIsNamedRoot(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	nested := filepath.Join(root.Path, "root")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}

	got, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "root" || got[1].ID == "root" || got[1].ParentID != "root" {
		t.Fatalf("repositories=%#v", got)
	}
}

func TestDiscoverAssignsDeterministicDistinctIDsForSlugCollisions(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	for _, directory := range []string{"foo bar", "foo-bar"} {
		path := filepath.Join(root.Path, directory)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := testutil.GitCommand(t, "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
			t.Fatal(string(output))
		}
	}

	first, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := discovery.Discover(root.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first) != 3 || first[1].ID == first[2].ID || first[1].ID == "root" || first[2].ID == "root" {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}
