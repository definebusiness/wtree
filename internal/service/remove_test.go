package service_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestWorkspaceRemoverRemovesNestedWorktreesChildFirstAndRetainsState(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/remove", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}, {RepositoryID: "shared", Mount: "common"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/remove")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := []string{value.Repositories[0].ID, value.Repositories[1].ID, value.Repositories[2].ID}, []string{"shared", "backend", "root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removal order = %v, want %v", got, want)
	}
	for _, path := range []string{target, filepath.Join(target, "api"), filepath.Join(target, "api", "common")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("removed worktree remains at %q: %v", path, err)
		}
	}
	for _, repository := range []testutil.GitRepository{root, backend, shared} {
		exists, err := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/remove")
		if err != nil || !exists {
			t.Fatalf("retained branch at %q = %t, %v", repository.Path, exists, err)
		}
	}
	if _, err := service.RequireWorkspace(project, data, "feature/remove"); err != nil {
		t.Fatalf("retained workspace state: %v", err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("remove changed retained state: before=%q after=%q error=%v", stateBefore, stateAfter, err)
	}
	if _, err := service.NewWorkspaceCreator().CheckoutWorkspace(context.Background(), project, service.WorkspaceCheckoutRequest{WorkspaceName: "feature/remove", DataDir: data}, nil); err != nil {
		t.Fatalf("checkout after remove: %v", err)
	}
}

func TestWorkspaceRemoverDoesNotMutateWhenProjectLockIsContended(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/remove", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/remove")
	if err != nil {
		t.Fatal(err)
	}
	remover := service.NewWorkspaceRemoverWith(gitadapter.NewAdapter("git"), contendedProjectLocker{}, store.WriteRecovery)
	if _, err := remover.Remove(context.Background(), project, workspace, data, false, nil); !cliExitKind(t, err, service.ErrorConflict) {
		t.Fatalf("contended remove error = %v, want conflict", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("contended remove mutated workspace: %v", err)
	}
}

func TestWorkspaceRemoverRefusesEveryDirtyCategoryUnlessForcedAndReportsOverrides(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		prepare    func(t *testing.T, checkout testutil.GitRepository)
		wantReason string
	}{
		{name: "staged", prepare: func(t *testing.T, checkout testutil.GitRepository) {
			checkout.CommitFile("staged.txt", "one\n", "one")
			if err := os.WriteFile(filepath.Join(checkout.Path, "staged.txt"), []byte("two\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			checkout.Run(t, "add", "staged.txt")
		}, wantReason: "staged changes"},
		{name: "modified", prepare: func(t *testing.T, checkout testutil.GitRepository) {
			if err := os.WriteFile(filepath.Join(checkout.Path, "root.txt"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, wantReason: "modified files"},
		{name: "untracked", prepare: func(t *testing.T, checkout testutil.GitRepository) {
			if err := os.WriteFile(filepath.Join(checkout.Path, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, wantReason: "untracked files"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, _, _, data := createFixture(t)
			target := filepath.Join(t.TempDir(), "workspace")
			if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/remove", TargetPath: target, DataDir: data}, nil); err != nil {
				t.Fatal(err)
			}
			workspace, err := service.RequireWorkspace(project, data, "feature/remove")
			if err != nil {
				t.Fatal(err)
			}
			scenario.prepare(t, testutil.GitRepository{Path: target})
			if _, err := service.NewWorkspaceRemover().PlanRemove(context.Background(), project, workspace, false); !cliExitKind(t, err, service.ErrorDirtyWorkspace) {
				t.Fatalf("remove preflight error = %v, want dirty workspace", err)
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("dirty refusal removed workspace: %v", err)
			}
			value, err := service.NewWorkspaceRemover().PlanRemove(context.Background(), project, workspace, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(value.Overrides) != 1 || value.Overrides[0].RepositoryID != "root" || value.Overrides[0].Reason != scenario.wantReason {
				t.Fatalf("force overrides = %#v", value.Overrides)
			}
		})
	}
}

func TestWorkspaceRemoverRefusesManualMissingOrUnregisteredCheckoutWithoutRepair(t *testing.T) {
	project, root, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/remove", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/remove")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		if _, err := service.NewWorkspaceRemover().PlanRemove(context.Background(), project, workspace, force); !cliExitKind(t, err, service.ErrorValidation) {
			t.Fatalf("manual missing force=%t error = %v, want validation", force, err)
		}
	}
	worktrees, err := gitadapter.NewAdapter("git").ListWorktrees(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsWorktreePath(worktrees, target) {
		t.Fatalf("remove preflight pruned manual registration: %#v", worktrees)
	}
}

func TestWorkspaceRemoverRefusesPresentButUnregisteredCheckoutWithoutRepair(t *testing.T) {
	source := testutil.NewGitRepository(t)
	source.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: source.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: source.Path, ProjectPath: source.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), resolution.Project, service.WorkspacePlanRequest{WorkspaceName: "feature/remove", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(resolution.Project, data, "feature/remove")
	if err != nil {
		t.Fatal(err)
	}
	source.Run(t, "worktree", "remove", "--force", target)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewWorkspaceRemover().PlanRemove(context.Background(), resolution.Project, workspace, false); !cliExitKind(t, err, service.ErrorValidation) {
		t.Fatalf("unregistered checkout error = %v, want validation", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("remove preflight repaired or removed replacement directory: %v", err)
	}
}

func TestWorkspaceRemoverRollsBackMidRemovalAndWritesRecoveryOnlyWhenUndoFails(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/remove", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/remove")
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingRemoveGit{Git: gitadapter.NewAdapter("git"), failAt: 2}
	_, err = service.NewWorkspaceRemoverWith(failing, lock.Manager{}, store.WriteRecovery).Remove(context.Background(), project, workspace, data, false, nil)
	if err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("mid-remove error = %v, want clean rollback", err)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("rollback did not restore worktree: %v", statErr)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspace.ID+".json")
	if _, statErr := os.Stat(recoveryPath); !os.IsNotExist(statErr) {
		t.Fatalf("clean rollback wrote recovery: %v", statErr)
	}

	failing = &failingRemoveGit{Git: gitadapter.NewAdapter("git"), failAt: 2, failRestore: true}
	_, err = service.NewWorkspaceRemoverWith(failing, lock.Manager{}, store.WriteRecovery).Remove(context.Background(), project, workspace, data, false, nil)
	if !cliExitKind(t, err, service.ErrorRollbackIncomplete) {
		t.Fatalf("failed undo error = %v, want rollback incomplete", err)
	}
	record, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || record.Operation != "remove" || len(record.UnrevertedSteps) != 1 {
		t.Fatalf("remove recovery = %#v, %v", record, readErr)
	}
}

func TestWorkspaceRemoverForcedDirtyDescendantFailureLeavesActionableRecovery(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/remove", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/remove")
	if err != nil {
		t.Fatal(err)
	}
	backendPath, err := workspace.ResolveRepository("backend")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendPath, "lost-if-restored.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failing := &failingRemoveGit{Git: gitadapter.NewAdapter("git"), failAt: 2}
	_, err = service.NewWorkspaceRemoverWith(failing, lock.Manager{}, store.WriteRecovery).Remove(context.Background(), project, workspace, data, true, nil)
	if !cliExitKind(t, err, service.ErrorRollbackIncomplete) || service.HasCleanRollback(err) {
		t.Fatalf("dirty remove failure = %v, want rollback-incomplete without clean rollback", err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspace.ID+".json")
	record, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || record.Operation != "remove" || !containsStep(record.UnrevertedSteps, "remove_worktree:backend") || !containsStep(record.CompletedSteps, "remove_worktree:backend") || record.FailedStep != "remove_worktree:root" {
		t.Fatalf("dirty remove recovery = %#v, %v", record, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(backendPath, "lost-if-restored.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("forced dirty worktree unexpectedly restored or retained dirty content: %v", statErr)
	}
}

type failingRemoveGit struct {
	gitadapter.Git
	failAt      int
	calls       int
	failRestore bool
}

type contendedProjectLocker struct{}

func (contendedProjectLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	return nil, errors.New("held by another operation")
}

func (g *failingRemoveGit) RemoveWorktree(ctx context.Context, repository, path string, force bool) error {
	g.calls++
	if g.calls == g.failAt {
		return errors.New("injected remove failure")
	}
	return g.Git.RemoveWorktree(ctx, repository, path, force)
}

func (g *failingRemoveGit) AddWorktree(ctx context.Context, repository, path, branch string) error {
	if g.failRestore {
		return errors.New("injected restore failure")
	}
	return g.Git.AddWorktree(ctx, repository, path, branch)
}

func cliExitKind(t *testing.T, err error, want service.ErrorKind) bool {
	t.Helper()
	var application *service.Error
	return errors.As(err, &application) && application.Kind == want
}

func containsWorktreePath(worktrees []gitadapter.Worktree, path string) bool {
	canonical := canonicalMissingPath(path)
	for _, worktree := range worktrees {
		candidate := canonicalMissingPath(worktree.Path)
		if candidate == canonical {
			return true
		}
	}
	return false
}

func canonicalMissingPath(path string) string {
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		return canonical
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(parent, filepath.Base(path))
	}
	return filepath.Clean(path)
}

func containsStep(steps []string, want string) bool {
	for _, step := range steps {
		if step == want {
			return true
		}
	}
	return false
}
