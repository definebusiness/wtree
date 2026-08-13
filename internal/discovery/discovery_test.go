package discovery_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/marcel/wtree/internal/discovery"
	"github.com/marcel/wtree/internal/testutil"
)

func TestDiscoverFindsRootAndIndependentNestedRepositories(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	nested := filepath.Join(root.Path, "backend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
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

func TestDiscoverDerivesParentsAndMountsForTwoLevelsAndSiblings(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	for _, relative := range []string{"backend", "backend/shared", "frontend"} {
		path := filepath.Join(root.Path, relative)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("git", "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
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

func TestDiscoverFindsRepositoryRootFromAnOrdinaryDescendant(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	ordinaryDirectory := filepath.Join(root.Path, "cmd", "wtree")
	if err := os.MkdirAll(ordinaryDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := discovery.Discover(ordinaryDirectory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "root" || got[0].Path != want {
		t.Fatalf("repositories=%#v", got)
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
	if output, err := exec.Command("git", "init", ignored).CombinedOutput(); err != nil {
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
	if output, err := exec.Command("git", "init", ignored).CombinedOutput(); err != nil {
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

func TestDiscoverSkipsBuiltInDependencyAndBuildDirectories(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	for _, relative := range []string{"node_modules/dependency", ".venv/environment", "target/artifact", "build/generated"} {
		directory := filepath.Join(root.Path, relative)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("git", "init", "--initial-branch=main", directory).CombinedOutput(); err != nil {
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
	if output, err := exec.Command("git", "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	dependency := filepath.Join(nested, "node_modules", "dependency")
	if err := os.MkdirAll(dependency, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--initial-branch=main", dependency).CombinedOutput(); err != nil {
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
	if output, err := exec.Command("git", "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
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
	if output, err := exec.Command("git", "init", nested).CombinedOutput(); err != nil {
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
	if output, err := exec.Command("git", "init", "--initial-branch=main", nested).CombinedOutput(); err != nil {
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
		if output, err := exec.Command("git", "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
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
