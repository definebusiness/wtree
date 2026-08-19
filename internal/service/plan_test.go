package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestWorkspacePlannerBuildsParentFirstCreatePlanWithIndependentHEADBases(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom mount")
	resolved, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.NewWorkspacePlanner().Plan(context.Background(), resolved.Project, service.WorkspacePlanRequest{
		Operation:     plan.Create,
		WorkspaceName: "feature/login",
		From:          "HEAD",
		WorktreeRoot:  filepath.Join(data, "worktrees"),
		DataDir:       data,
		Mounts:        []service.MountOverride{{RepositoryID: "backend", Mount: `services\..\api`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := repositoryPlanIDs(result.Repositories), []string{"root", "backend"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repository order = %v, want %v", got, want)
	}
	if result.Repositories[0].Base == result.Repositories[1].Base {
		t.Fatalf("per-repository HEAD bases are equal %q, want independently resolved commits", result.Repositories[0].Base)
	}
	if got, want := result.Repositories[1].Mount, "api"; got != want {
		t.Fatalf("backend mount = %q, want %q", got, want)
	}
	if got, want := result.Repositories[1].Path, filepath.Join(result.RootPath, "api"); got != want {
		t.Fatalf("backend path = %q, want %q", got, want)
	}
	if got, want := stepSummary(result), []string{"create_branch:root", "add_worktree:root", "create_branch:backend", "add_worktree:backend"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

func TestWorkspacePlannerAllowsMountWithoutCommittedParentGitignoreRule(t *testing.T) {
	project, root, backend, data := plannerFixture(t)
	root.Run(t, "branch", "base-without-ignore")
	backend.Run(t, "branch", "base-without-ignore")
	request := service.WorkspacePlanRequest{
		Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
	}

	value, err := service.NewWorkspacePlanner().Plan(context.Background(), project, request)
	if err != nil {
		t.Fatalf("Plan() = %v, want committed-base ignore content to be irrelevant", err)
	}
	if exists, branchErr := gitBranchExists(root, "feature"); branchErr != nil || exists {
		t.Fatalf("root feature branch exists=%t error=%v, want false nil", exists, branchErr)
	}
	if got, want := value.Repositories[1].Mount, "api"; got != want {
		t.Fatalf("planned mount = %q, want %q", got, want)
	}
	request.WorkspaceName = "from-old-base"
	request.From = "base-without-ignore"
	if _, err := service.NewWorkspacePlanner().Plan(context.Background(), project, request); err != nil {
		t.Fatalf("Plan() from base without ignore rule = %v, want success", err)
	}
}

func TestWorkspacePlanIgnoreEnsuresProjectNormalizedEntriesParentFirst(t *testing.T) {
	value := plan.WorkspacePlan{Repositories: []plan.RepositoryPlan{
		{ID: "root", Mount: ".", Path: filepath.Join("/worktrees", "feature")},
		{ID: "backend", ParentID: "root", Mount: "api", Path: filepath.Join("/worktrees", "feature", "api")},
		{ID: "docs", ParentID: "root", Mount: "docs/site", Path: filepath.Join("/worktrees", "feature", "docs", "site")},
		{ID: "shared", ParentID: "backend", Mount: "common", Path: filepath.Join("/worktrees", "feature", "api", "common")},
	}}

	ensures, err := service.WorkspacePlanIgnoreEnsures(value)
	if err != nil {
		t.Fatalf("WorkspacePlanIgnoreEnsures() = %v", err)
	}
	want := []service.IgnoreEnsure{
		{ParentRepositoryID: "root", Path: filepath.Join("/worktrees", "feature", ".gitignore"), Rules: []string{"/api/", "/docs/site/"}},
		{ParentRepositoryID: "backend", Path: filepath.Join("/worktrees", "feature", "api", ".gitignore"), Rules: []string{"/common/"}},
	}
	if !reflect.DeepEqual(ensures, want) {
		t.Fatalf("ignore ensures = %#v, want %#v", ensures, want)
	}
}

func TestWorkspacePlannerTreatsNormalizedOverrideAsConfiguredDefault(t *testing.T) {
	project, _, _, data := plannerFixture(t)
	for index := range project.Repositories {
		if project.Repositories[index].ID == "backend" {
			project.Repositories[index].DefaultMount = `services\..\api`
		}
	}

	result, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
		Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want semantically identical legacy default to skip committed-ignore preflight", err)
	}
	if got := result.Repositories[1].Mount; got != "api" {
		t.Fatalf("planned mount = %q, want normalized api", got)
	}
}

func TestWorkspacePlannerNormalizesDefaultMountBeforeSourceConflictPreflight(t *testing.T) {
	project, root, _, data := plannerFixture(t)
	for index := range project.Repositories {
		if project.Repositories[index].ID == "backend" {
			project.Repositories[index].DefaultMount = `services\..\api`
		}
	}
	if err := os.WriteFile(filepath.Join(root.Path, "api"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
		Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
	})
	if err == nil || !contains(err.Error(), "contains content") || !contains(err.Error(), "api") {
		t.Fatalf("Plan() error = %v, want normalized default mount source-content conflict", err)
	}
}

func repositoryPlanIDs(repositories []plan.RepositoryPlan) []string {
	ids := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		ids = append(ids, repository.ID)
	}
	return ids
}

func stepSummary(value plan.WorkspacePlan) []string {
	steps := make([]string, 0, len(value.Steps))
	for _, step := range value.Steps {
		steps = append(steps, string(step.Action)+":"+step.RepositoryID)
	}
	return steps
}

func TestWorkspacePlannerCheckoutRejectsBranchCheckedOutElsewhere(t *testing.T) {
	project, root, backend, data := plannerFixture(t)
	root.Run(t, "branch", "feature")
	backend.Run(t, "branch", "feature")
	root.Run(t, "checkout", "feature")
	_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
		Operation: plan.Checkout, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
	})
	if err == nil || !contains(err.Error(), "already checked out") {
		t.Fatalf("Plan() error = %v, want checked-out branch conflict", err)
	}
}

func TestWorkspacePlannerCheckoutResolvesRequestedBranchForEveryRepository(t *testing.T) {
	project, root, backend, data := plannerFixture(t)
	root.Run(t, "branch", "feature")
	backend.Run(t, "branch", "feature")
	value, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
		Operation: plan.Checkout, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepSummary(value), []string{"add_worktree:root", "add_worktree:backend"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("checkout steps = %v, want %v", got, want)
	}
	for _, repository := range value.Repositories {
		if repository.Base == "" || repository.Branch != "feature" {
			t.Fatalf("checkout repository plan = %#v", repository)
		}
	}
}

func TestWorkspacePlannerExplicitRefFailsBeforeAnyMutation(t *testing.T) {
	project, root, backend, data := plannerFixture(t)
	root.Run(t, "branch", "shared-base")
	request := service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "feature", From: "shared-base", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data}
	_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, request)
	if err == nil || !contains(err.Error(), "resolve base") || !contains(err.Error(), "backend") {
		t.Fatalf("Plan() error = %v, want missing backend base", err)
	}
	if exists, err := gitBranchExists(root, "feature"); err != nil || exists {
		t.Fatalf("root feature branch exists=%t error=%v, want false nil", exists, err)
	}
	if exists, err := gitBranchExists(backend, "feature"); err != nil || exists {
		t.Fatalf("backend feature branch exists=%t error=%v, want false nil", exists, err)
	}
}

func TestWorkspacePlannerRejectsExistingBranchAndExistingPath(t *testing.T) {
	project, root, backend, data := plannerFixture(t)
	root.Run(t, "branch", "feature")
	backend.Run(t, "branch", "feature")
	_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data})
	if err == nil || !contains(err.Error(), "already exists") {
		t.Fatalf("existing branch error = %v", err)
	}
	target := filepath.Join(data, "existing-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "another", TargetPath: target, DataDir: data})
	if err == nil || !contains(err.Error(), "already exists") {
		t.Fatalf("existing path error = %v", err)
	}
}

func TestWorkspacePlannerRejectsWorkspaceStorageAndTrackedMountConflicts(t *testing.T) {
	t.Run("registered storage collision", func(t *testing.T) {
		project, _, _, data := plannerFixture(t)
		name := "feature/login"
		if err := store.WriteWorkspace(service.WorkspaceStatePath(data, project.ID, "other"), store.WorkspaceState{ID: pathutil.StorageName(name), Name: "other", Path: filepath.Join(data, "other")}); err != nil {
			t.Fatal(err)
		}
		_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: name, WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data})
		if err == nil || !contains(err.Error(), "already registered") {
			t.Fatalf("Plan() error = %v, want storage collision", err)
		}
	})

	for _, content := range []struct {
		name string
		make func(string) error
	}{
		{name: "file", make: func(path string) error { return os.WriteFile(path, []byte("tracked\n"), 0o600) }},
		{name: "directory", make: func(path string) error {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("tracked\n"), 0o600)
		}},
	} {
		t.Run("tracked "+content.name, func(t *testing.T) {
			project, root, _, data := plannerFixture(t)
			mount := filepath.Join(root.Path, "api")
			if err := content.make(mount); err != nil {
				t.Fatal(err)
			}
			root.Run(t, "add", "api")
			root.Run(t, "commit", "-m", "tracked api")
			_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
				Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
				Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
			})
			if err == nil || !contains(err.Error(), "contains content") {
				t.Fatalf("Plan() error = %v, want tracked mount conflict", err)
			}
		})
	}
}

func TestWorkspacePlannerRejectsEscapingAndSymlinkedTargets(t *testing.T) {
	project, _, _, data := plannerFixture(t)
	_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
		Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "../../outside"}},
	})
	if err == nil || !contains(err.Error(), "escapes") {
		t.Fatalf("escaping mount error = %v", err)
	}

	worktreeRoot := filepath.Join(data, "symlink-worktrees")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(worktreeRoot, project.ID)); err != nil {
		t.Fatal(err)
	}
	_, err = service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
		Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: worktreeRoot, DataDir: data,
	})
	if err == nil || !contains(err.Error(), "escapes") {
		t.Fatalf("symlinked target error = %v", err)
	}
}

func TestWorkspacePlannerRejectsInvalidAndDuplicateMountOverrides(t *testing.T) {
	project, _, _, data := plannerFixture(t)
	for _, mounts := range [][]service.MountOverride{
		{{RepositoryID: "unknown", Mount: "api"}},
		{{RepositoryID: "backend", Mount: "api"}, {RepositoryID: "backend", Mount: "other"}},
	} {
		_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
			Operation: plan.Create, WorkspaceName: "feature", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data, Mounts: mounts,
		})
		if err == nil {
			t.Fatalf("Plan(%#v) succeeded", mounts)
		}
	}
}

func TestWorkspacePlannerUsesGitBranchGrammarBeforePlanSuccess(t *testing.T) {
	project, root, backend, data := plannerFixture(t)
	for _, branch := range []string{".feature", "feature/", "foo/.bar", "foo..bar", "foo.lock", "foo@{bar", "feature//child"} {
		t.Run(branch, func(t *testing.T) {
			_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
				Operation: plan.Create, WorkspaceName: branch, WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
			})
			if err == nil || !contains(err.Error(), "invalid branch") {
				t.Fatalf("Plan(%q) error = %v, want invalid branch", branch, err)
			}
			for _, repository := range []testutil.GitRepository{root, backend} {
				if exists, err := gitBranchExists(repository, branch); err != nil || exists {
					t.Fatalf("branch %q exists=%t error=%v, want false nil", branch, exists, err)
				}
			}
		})
	}
	for _, branch := range []string{"feature/login", "agent/task-123"} {
		t.Run("valid/"+branch, func(t *testing.T) {
			if _, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
				Operation: plan.Create, WorkspaceName: branch, WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
			}); err != nil {
				t.Fatalf("Plan(%q) error = %v", branch, err)
			}
		})
	}
}

func TestWorkspacePlannerClassifiesBranchValidationRepositoryFailureAsGitError(t *testing.T) {
	project, _, _, data := plannerFixture(t)
	project.Repositories[0].SourcePath = filepath.Join(t.TempDir(), "missing")
	_, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{
		Operation: plan.Create, WorkspaceName: "feature/login", WorktreeRoot: filepath.Join(data, "worktrees"), DataDir: data,
	})
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorGit {
		t.Fatalf("Plan() error = %v, want Git application error", err)
	}
}

func plannerFixture(t *testing.T) (domain.Project, testutil.GitRepository, testutil.GitRepository, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}
	backend.Path = filepath.Join(root.Path, "backend")
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	return resolution.Project, root.GitRepository, backend.GitRepository, data
}

func gitBranchExists(repository testutil.GitRepository, branch string) (bool, error) {
	return gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, branch)
}

func contains(value, substring string) bool { return strings.Contains(value, substring) }
