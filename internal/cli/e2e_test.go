package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marcel/wtree/internal/cli"
	"github.com/marcel/wtree/internal/domain"
	"github.com/marcel/wtree/internal/pathutil"
	"github.com/marcel/wtree/internal/service"
	"github.com/marcel/wtree/internal/store"
	"github.com/marcel/wtree/internal/testutil"
)

// This black-box command test uses real nested repositories through the public
// Execute boundary and validates the normal lifecycle without package internals.
func TestEndToEndNestedWorkspaceLifecycle(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "create", "feature/e2e", "--data-dir", data, "--path", target, "--mount", "backend=api"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	path := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "path", "feature/e2e", "--data-dir", data)
	if path.Err != nil || path.Stdout != target+"\n" {
		t.Fatalf("path = %#v", path)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	repoPath := testutil.RunCommand(t, cli.Execute, "repo", "path", "backend", "--data-dir", data)
	if repoPath.Err != nil || repoPath.Stdout != filepath.Join(target, "api")+"\n" {
		t.Fatalf("repo path = %#v", repoPath)
	}
	if err := os.Chdir(previous); err != nil {
		t.Fatal(err)
	}
	status := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "status", "feature/e2e", "--data-dir", data, "--json")
	var report struct {
		Repositories []struct{ ID, Status string } `json:"repositories"`
	}
	if status.Err != nil || json.Unmarshal([]byte(status.Stdout), &report) != nil || len(report.Repositories) != 2 {
		t.Fatalf("status = %#v report=%#v", status, report)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "remove", "feature/e2e", "--data-dir", data); result.Err != nil {
		t.Fatalf("remove = %#v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("remove retained target: %v", err)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "checkout", "feature/e2e", "--data-dir", data); result.Err != nil {
		t.Fatalf("checkout = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "delete", "feature/e2e", "--data-dir", data); result.Err != nil {
		t.Fatalf("delete = %#v", result)
	}
	if _, err := service.RequireWorkspace(mustProject(t, root.Path, data), data, "feature/e2e"); err == nil {
		t.Fatal("delete retained workspace state")
	}
}

func TestEndToEndImportWorkflow(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "imported")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.Run(t, "branch", "feature/import")
	root.Run(t, "worktree", "add", target, "feature/import")
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "import", target, "--name", "imported", "--data-dir", data); result.Err != nil {
		t.Fatalf("import = %#v", result)
	}
	workspace, err := service.RequireWorkspace(mustProject(t, root.Path, data), data, "imported")
	canonicalTarget, canonicalErr := filepath.EvalSymlinks(target)
	if err != nil || canonicalErr != nil || workspace.RootPath != canonicalTarget {
		t.Fatalf("imported workspace = %#v, %v", workspace, err)
	}
}

func TestEndToEndImportMapsRenamedNestedCheckoutByIdentity(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "external")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.Run(t, "branch", "feature/import")
	root.Run(t, "worktree", "add", target, "feature/import")
	backend.Run(t, "branch", "feature/import")
	backend.Run(t, "worktree", "add", filepath.Join(target, "api"), "feature/import")
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "import", target, "--name", "renamed", "--data-dir", data, "--json"); result.Err != nil {
		t.Fatalf("import = %#v", result)
	}
	workspace, err := service.RequireWorkspace(mustProject(t, root.Path, data), data, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	path, err := workspace.ResolveRepository("backend")
	canonicalTarget, canonicalErr := filepath.EvalSymlinks(filepath.Join(target, "api"))
	if err != nil || canonicalErr != nil || path != canonicalTarget {
		t.Fatalf("renamed backend = %q, %v", path, err)
	}
}

func TestEndToEndCreateRollbackIncompleteWritesRecovery(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "rollback")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	canonicalBackend, err := filepath.EvalSymlinks(backendPath)
	if err != nil {
		t.Fatal(err)
	}
	buildGitFailureHelper(t, shimDir, realGit, canonicalBackend)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "create", "feature/recovery", "--data-dir", data, "--path", target, "--json")
	if result.Err == nil || cli.ExitCode(result.Err) != 9 || !strings.Contains(result.Stdout, `"code":"rollback_incomplete"`) || result.Stderr != "" {
		t.Fatalf("create recovery = %#v", result)
	}
	project := mustProject(t, root.Path, data)
	workspaceID := pathutil.StorageName("feature/recovery")
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspaceID+".json")
	if _, err := store.ReadRecovery(recoveryPath); err != nil {
		t.Fatalf("recovery record = %v", err)
	}
	if _, err := os.Stat(service.WorkspaceStatePath(data, project.ID, workspaceID)); !os.IsNotExist(err) {
		t.Fatalf("state persisted after failed create: %v", err)
	}
}

// buildGitFailureHelper creates a native executable named exactly as Git on
// the current platform. It is a test-only PATH fixture; production code keeps
// invoking the normal Git adapter without hooks or altered behavior.
func buildGitFailureHelper(t *testing.T, directory, realGit, failedRepository string) {
	t.Helper()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source := filepath.Join(directory, "main.go")
	program := fmt.Sprintf(`package main
import (
  "fmt"
  "os"
  "os/exec"
  "path/filepath"
)
func main() {
  args := os.Args[1:]
  repo, rest := "", args
  if len(args) >= 2 && args[0] == "-C" { repo, rest = args[1], args[2:] }
  resolved, _ := filepath.EvalSymlinks(repo)
  if resolved == %q && len(rest) >= 2 && ((rest[0] == "worktree" && rest[1] == "add") || (rest[0] == "branch" && rest[1] == "-D")) {
    fmt.Fprintln(os.Stderr, "injected Git failure")
    os.Exit(1)
  }
  command := exec.Command(%q, args...)
  command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
  if err := command.Run(); err != nil {
    if exit, ok := err.(*exec.ExitError); ok { os.Exit(exit.ExitCode()) }
    fmt.Fprintln(os.Stderr, err)
    os.Exit(1)
  }
}
`, failedRepository, realGit)
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("go", "build", "-o", filepath.Join(directory, name), source).CombinedOutput()
	if err != nil {
		t.Fatalf("build Git failure helper: %v\n%s", err, output)
	}
}

func TestEndToEndDoctorSurfacesRecoveryRecordWithoutMutatingState(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "recovery")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "create", "feature/recovery", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	project := mustProject(t, root.Path, data)
	workspace, err := service.RequireWorkspace(project, data, "feature/recovery")
	if err != nil {
		t.Fatal(err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspace.ID+".json")
	if err := store.WriteRecovery(recoveryPath, store.RecoveryRecord{ProjectID: project.ID, WorkspaceID: workspace.ID, Operation: "create", FailedStep: "add-root", CompletedSteps: []string{"create-root"}}); err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, workspace.ID)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "doctor", "feature/recovery", "--data-dir", data, "--json")
	if result.Err != nil || !strings.Contains(result.Stdout, `"code":"recovery-record"`) {
		t.Fatalf("doctor recovery = %#v", result)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("doctor altered recoverable state: %v", err)
	}
}

func mustProject(t *testing.T, path, data string) domain.Project {
	t.Helper()
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: path, ProjectPath: path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	return resolution.Project
}
