package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestWorkspaceCreatorCreatesNestedWorkspaceParentFirst(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	value, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := len(value.Steps), 4; got != want || value.Steps[0].Action != plan.CreateBranch || value.Steps[2].RepositoryID != "backend" {
		t.Fatalf("plan steps = %#v", value.Steps)
	}
	root.Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
	backend.Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
	for _, path := range []string{target, filepath.Join(target, "api")} {
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("created worktree %q: %v", path, err)
		}
	}
	state, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, value.WorkspaceID))
	if err != nil {
		t.Fatalf("ReadWorkspace: %v", err)
	}
	if got, want := state.Repositories["backend"].ResolvedPath, filepath.Join(target, "api"); got != want {
		t.Fatalf("backend state path = %q, want %q", got, want)
	}
}

func TestWorkspaceCreatorCreatesThreeLevelRenamedWorkspace(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	value, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{
			{RepositoryID: "backend", Mount: "api"},
			{RepositoryID: "shared", Mount: "common"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	paths := map[string]string{
		"root":    target,
		"backend": filepath.Join(target, "api"),
		"shared":  filepath.Join(target, "api", "common"),
	}
	sources := map[string]testutil.GitRepository{"root": root, "backend": backend, "shared": shared}
	for id, path := range paths {
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("%s worktree at %q: %v", id, path, err)
		}
		identity, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), path)
		if err != nil {
			t.Fatalf("%s created identity: %v", id, err)
		}
		sourceIdentity, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), sources[id].Path)
		if err != nil || identity != sourceIdentity {
			t.Fatalf("%s identity = %q source = %q error = %v", id, identity, sourceIdentity, err)
		}
		sources[id].Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
	}
	state, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, value.WorkspaceID))
	if err != nil {
		t.Fatalf("ReadWorkspace: %v", err)
	}
	if state.Path != target {
		t.Fatalf("workspace state path = %q, want %q", state.Path, target)
	}
	for id, path := range paths {
		checkout, found := state.Repositories[id]
		if !found || checkout.Branch != "feature/login" || checkout.ResolvedPath != path {
			t.Fatalf("state %s = %#v, want branch/path %q", id, checkout, path)
		}
	}
}

func TestWorkspaceCreatorGrandchildFailureRollsBackRenamedParents(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 6}
	_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}, {RepositoryID: "shared", Mount: "common"}},
	}, nil)
	if err == nil {
		t.Fatal("Create succeeded, want grandchild add-worktree failure")
	}
	for _, repository := range []testutil.GitRepository{root, backend, shared} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("renamed workspace remains after grandchild failure: %v", statErr)
	}
	if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, pathutil.StorageName("feature/login"))); !os.IsNotExist(statErr) {
		t.Fatalf("state remains after grandchild failure: %v", statErr)
	}
}

func TestWorkspaceCreatorRollsBackEveryCreateEffectFailure(t *testing.T) {
	for failureAt := 1; failureAt <= 4; failureAt++ {
		t.Run(fmt.Sprintf("effect-%d", failureAt), func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			target := filepath.Join(t.TempDir(), "workspace")
			git := &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: failureAt}
			_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{
				WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
			}, nil)
			if err == nil {
				t.Fatal("Create succeeded, want injected effect failure")
			}
			for _, repository := range []testutil.GitRepository{root, backend} {
				exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
				if branchErr != nil || exists {
					t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
				}
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("target remains after rollback: %v", statErr)
			}
			if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, "feature-login")); !os.IsNotExist(statErr) {
				t.Fatalf("state remains after rollback: %v", statErr)
			}
		})
	}
}

func TestWorkspaceCreatorStateCommitFailureRollsBackGitEffects(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	coordinator := service.NewWorkspaceTransactionWithFiles(
		projectLocker{},
		func(string, store.WorkspaceState) error { return errors.New("injected state failure") },
		store.WriteRecovery, os.Remove, os.ReadFile, store.WriteRawAtomic,
	)
	_, err := service.NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), coordinator).Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	if err == nil {
		t.Fatal("Create succeeded, want state commit failure")
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target remains after state failure: %v", statErr)
	}
}

func TestWorkspaceCreatorResultValidationFailureRollsBackGitEffects(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := validationFailingCreateGit{Git: gitadapter.NewAdapter("git"), target: target}
	_, err := service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	if err == nil {
		t.Fatal("Create succeeded, want result validation failure")
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target remains after validation failure: %v", statErr)
	}
}

func TestWorkspaceCreatorIncompleteRollbackWritesRecovery(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := rollbackFailingCreateGit{Git: gitadapter.NewAdapter("git")}
	_, err := service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	record, readErr := store.ReadRecovery(service.RecoveryRecordPath(data, plan.WorkspacePlan{ProjectID: project.ID, WorkspaceID: pathutil.StorageName("feature/login")}))
	if readErr != nil {
		t.Fatalf("ReadRecovery: %v", readErr)
	}
	if got, want := record.UnrevertedSteps, []string{"create_branch:root"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("recovery unreverted steps = %v, want %v", got, want)
	}
}

func TestWorkspaceCreatorCancellationRollsBackCompletedGitEffects(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	git := cancelAfterWorktreeGit{Git: gitadapter.NewAdapter("git"), cancel: cancel}
	_, err := service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction()).Create(ctx, project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want context canceled", err)
	}
	if !service.HasCleanRollback(err) {
		t.Fatalf("Create error = %v, want clean rollback outcome", err)
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target remains after cancellation: %v", statErr)
	}
}

func TestWorkspaceCreatorConcurrentSameWorkspaceHasOneWinner(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	request := service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}
	errors := runConcurrentCreates(project, request, request)
	successes := 0
	for _, err := range errors {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("same-workspace successes = %d, errors = %v", successes, errors)
	}
	if _, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, pathutil.StorageName("feature/login"))); err != nil {
		t.Fatalf("ReadWorkspace winner state: %v", err)
	}
}

func TestWorkspaceCreatorConcurrentDifferentWorkspacesLeaveConsistentState(t *testing.T) {
	project, root, backend, data := createFixture(t)
	one := service.WorkspacePlanRequest{WorkspaceName: "feature/one", TargetPath: filepath.Join(t.TempDir(), "one"), DataDir: data}
	two := service.WorkspacePlanRequest{WorkspaceName: "feature/two", TargetPath: filepath.Join(t.TempDir(), "two"), DataDir: data}
	for _, err := range runConcurrentCreates(project, one, two) {
		if err != nil {
			t.Fatalf("concurrent different create: %v", err)
		}
	}
	for _, request := range []service.WorkspacePlanRequest{one, two} {
		workspaceID := pathutil.StorageName(request.WorkspaceName)
		if _, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, workspaceID)); err != nil {
			t.Fatalf("ReadWorkspace %q: %v", request.WorkspaceName, err)
		}
		if _, err := os.Stat(request.TargetPath); err != nil {
			t.Fatalf("workspace target %q: %v", request.TargetPath, err)
		}
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		for _, branch := range []string{"feature/one", "feature/two"} {
			exists, err := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, branch)
			if err != nil || !exists {
				t.Fatalf("branch %q in %q = exists:%t error:%v", branch, repository.Path, exists, err)
			}
		}
	}
}

func runConcurrentCreates(project domain.Project, first, second service.WorkspacePlanRequest) []error {
	requests := []service.WorkspacePlanRequest{first, second}
	start := make(chan struct{})
	results := make(chan error, len(requests))
	var group sync.WaitGroup
	for _, request := range requests {
		request := request
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := service.NewWorkspaceCreator().Create(context.Background(), project, request, nil)
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	errors := make([]error, 0, len(requests))
	for err := range results {
		errors = append(errors, err)
	}
	return errors
}

func createFixture(t *testing.T) (domain.Project, testutil.GitRepository, testutil.GitRepository, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, AddIgnore: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	root.CommitFile(".gitignore", "/api/\n/api space/\n", "ignore custom mounts")
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolution.Project, root.GitRepository, backend.GitRepository, data
}

func createThreeLevelFixture(t *testing.T) (domain.Project, testutil.GitRepository, testutil.GitRepository, testutil.GitRepository, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("shared.txt", "shared\n", "shared")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	sharedPath := filepath.Join(backendPath, "shared")
	if err := os.Rename(shared.Path, sharedPath); err != nil {
		t.Fatal(err)
	}
	backend.Path, shared.Path = backendPath, sharedPath
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, AddIgnore: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom backend mount")
	backend.CommitFile(".gitignore", "/common/\n", "ignore custom shared mount")
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolution.Project, root.GitRepository, backend.GitRepository, shared.GitRepository, data
}

type failingCreateGit struct {
	gitadapter.Git
	failAt int
	calls  int
}

func (g *failingCreateGit) CreateBranch(ctx context.Context, repository, branch, base string) error {
	g.calls++
	if g.calls == g.failAt {
		return errors.New("injected Git mutation failure")
	}
	return g.Git.CreateBranch(ctx, repository, branch, base)
}

func (g *failingCreateGit) AddWorktree(ctx context.Context, repository, path, branch string) error {
	g.calls++
	if g.calls == g.failAt {
		return errors.New("injected Git mutation failure")
	}
	return g.Git.AddWorktree(ctx, repository, path, branch)
}

type projectLocker struct{}

func (projectLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	return noOpLock{}, nil
}

type noOpLock struct{}

func (noOpLock) Unlock() error { return nil }

type rollbackFailingCreateGit struct{ gitadapter.Git }

func (g *rollbackFailingCreateGit) AddWorktree(context.Context, string, string, string) error {
	return errors.New("injected add worktree failure")
}

func (g *rollbackFailingCreateGit) DeleteBranch(context.Context, string, string, bool) error {
	return errors.New("injected delete branch rollback failure")
}

type validationFailingCreateGit struct {
	gitadapter.Git
	target string
}

type cancelAfterWorktreeGit struct {
	gitadapter.Git
	cancel func()
	once   sync.Once
}

func (g *cancelAfterWorktreeGit) AddWorktree(ctx context.Context, repository, path, branch string) error {
	if err := g.Git.AddWorktree(ctx, repository, path, branch); err != nil {
		return err
	}
	g.once.Do(g.cancel)
	return nil
}

func (g *validationFailingCreateGit) CurrentBranch(ctx context.Context, repository string) (string, bool, error) {
	branch, detached, err := g.Git.CurrentBranch(ctx, repository)
	if servicePathEqual(repository, g.target) {
		return "unexpected", detached, err
	}
	return branch, detached, err
}

func servicePathEqual(left, right string) bool {
	canonicalLeft, leftErr := filepath.EvalSymlinks(left)
	canonicalRight, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil && rightErr == nil {
		return canonicalLeft == canonicalRight
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
