package service_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestWorkspaceDeleterRemovesChildFirstThenBranchesAndState(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/delete", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}, {RepositoryID: "shared", Mount: "common"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/delete")
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.NewWorkspaceDeleter().Delete(context.Background(), project, workspace, data, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := []string{value.Repositories[0].ID, value.Repositories[1].ID, value.Repositories[2].ID}, []string{"shared", "backend", "root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree order = %v, want %v", got, want)
	}
	for _, path := range []string{target, filepath.Join(target, "api"), filepath.Join(target, "api", "common")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("worktree remains at %q: %v", path, err)
		}
	}
	for _, repository := range []testutil.GitRepository{root, backend, shared} {
		exists, err := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/delete")
		if err != nil || exists {
			t.Fatalf("branch at %q exists=%t error=%v", repository.Path, exists, err)
		}
	}
	if _, err := service.RequireWorkspace(project, data, "feature/delete"); !cliExitKind(t, err, service.ErrorWorkspaceNotFound) {
		t.Fatalf("workspace state after delete = %v, want not found", err)
	}
}

func TestWorkspaceDeleterRefusesUnmergedBranchUnlessForcedAndReportsOverride(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/delete", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/delete")
	if err != nil {
		t.Fatal(err)
	}
	testutil.GitRepository{Path: target}.CommitFile("unmerged.txt", "unmerged\n", "unmerged")
	if _, err := service.NewWorkspaceDeleter().PlanDelete(context.Background(), project, workspace, false); !cliExitKind(t, err, service.ErrorConflict) {
		t.Fatalf("unmerged delete = %v, want conflict", err)
	}
	value, err := service.NewWorkspaceDeleter().PlanDelete(context.Background(), project, workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	if !containsOverride(value.Overrides, "root", "unmerged branch") {
		t.Fatalf("unmerged force overrides = %#v", value.Overrides)
	}
	if _, err := service.NewWorkspaceDeleter().Delete(context.Background(), project, workspace, data, true, nil); err != nil {
		t.Fatalf("forced unmerged delete: %v", err)
	}
}

func TestWorkspaceDeleterFailureAfterBranchDeletionWritesRecoveryAndPreservesState(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/delete", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/delete")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingDeleteGit{Git: gitadapter.NewAdapter("git"), failDeleteAt: 2}
	_, err = service.NewWorkspaceDeleterWith(failing, lock.Manager{}, store.WriteRecovery, os.Remove, store.WriteRawAtomic, os.ReadFile).Delete(context.Background(), project, workspace, data, false, nil)
	if !cliExitKind(t, err, service.ErrorRollbackIncomplete) {
		t.Fatalf("delete branch failure = %v, want rollback incomplete", err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspace.ID+".json")
	record, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || record.Operation != "delete" || !containsStep(record.UnrevertedSteps, "delete_branch:backend") || record.FailedStep != "delete_branch:root" {
		t.Fatalf("delete recovery = %#v, %v", record, readErr)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("branch failure changed state: before=%q after=%q error=%v", stateBefore, stateAfter, err)
	}
}

func TestWorkspaceDeleterStateDeleteFailureWritesRecovery(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/delete", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/delete")
	if err != nil {
		t.Fatal(err)
	}
	deleter := service.NewWorkspaceDeleterWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteRecovery, func(string) error { return errors.New("injected state delete failure") }, store.WriteRawAtomic, os.ReadFile)
	_, err = deleter.Delete(context.Background(), project, workspace, data, false, nil)
	if !cliExitKind(t, err, service.ErrorRollbackIncomplete) {
		t.Fatalf("state delete failure = %v, want rollback incomplete", err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspace.ID+".json")
	record, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || record.Operation != "delete" || !containsStep(record.UnrevertedSteps, "delete_branch:root") || record.FailedStep != "delete_state" {
		t.Fatalf("state delete recovery = %#v, %v", record, readErr)
	}
}

func TestWorkspaceDeleterRemovalFailureRollsBackBeforeBranchDeletion(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/delete", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/delete")
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingDeleteGit{Git: gitadapter.NewAdapter("git"), failRemoveAt: 2}
	_, err = service.NewWorkspaceDeleterWith(failing, lock.Manager{}, store.WriteRecovery, os.Remove, store.WriteRawAtomic, os.ReadFile).Delete(context.Background(), project, workspace, data, false, nil)
	if err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("delete remove failure = %v, want clean rollback", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("remove rollback did not restore root: %v", err)
	}
	for _, repository := range project.Repositories {
		exists, err := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.SourcePath, "feature/delete")
		if err != nil || !exists {
			t.Fatalf("branch %s after remove failure exists=%t error=%v", repository.ID, exists, err)
		}
	}
}

func TestWorkspaceDeleterScopesForceFlagsPerDirtyAndUnmergedAllowance(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		dirty, unmerged bool
		wantRemove      map[string]bool
		wantDelete      map[string]bool
	}{
		{name: "dirty only", dirty: true, wantRemove: map[string]bool{"backend": true, "root": false}, wantDelete: map[string]bool{"backend": false, "root": false}},
		{name: "unmerged only", unmerged: true, wantRemove: map[string]bool{"backend": false, "root": false}, wantDelete: map[string]bool{"backend": false, "root": true}},
		{name: "combined", dirty: true, unmerged: true, wantRemove: map[string]bool{"backend": true, "root": false}, wantDelete: map[string]bool{"backend": false, "root": true}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, _, _, data := createFixture(t)
			target := filepath.Join(t.TempDir(), "workspace")
			if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/delete", TargetPath: target, DataDir: data}, nil); err != nil {
				t.Fatal(err)
			}
			workspace, err := service.RequireWorkspace(project, data, "feature/delete")
			if err != nil {
				t.Fatal(err)
			}
			if scenario.dirty {
				backendPath, err := workspace.ResolveRepository("backend")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(backendPath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.unmerged {
				testutil.GitRepository{Path: target}.CommitFile("unmerged.txt", "unmerged\n", "unmerged")
			}
			plan, err := service.NewWorkspaceDeleter().PlanDelete(context.Background(), project, workspace, true)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Repositories[0].ForceWorktree != scenario.wantRemove["backend"] || plan.Repositories[1].ForceWorktree != scenario.wantRemove["root"] || plan.Branches[0].ForceBranch != scenario.wantDelete["backend"] || plan.Branches[1].ForceBranch != scenario.wantDelete["root"] {
				t.Fatalf("typed plan allowances = %#v branches=%#v", plan.Repositories, plan.Branches)
			}
			flags := &forceRecordingGit{Git: gitadapter.NewAdapter("git"), sourceIDs: sourceIDMap(project)}
			if _, err := service.NewWorkspaceDeleterWith(flags, lock.Manager{}, store.WriteRecovery, os.Remove, store.WriteRawAtomic, os.ReadFile).Delete(context.Background(), project, workspace, data, true, nil); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(flags.removeForce, scenario.wantRemove) {
				t.Fatalf("remove --force flags = %#v, want %#v", flags.removeForce, scenario.wantRemove)
			}
			if !reflect.DeepEqual(flags.deleteForce, scenario.wantDelete) {
				t.Fatalf("delete --force flags = %#v, want %#v", flags.deleteForce, scenario.wantDelete)
			}
		})
	}
}

func TestWorkspaceDeleterCleanUnmergedDescendantRollsBackAfterLaterRemovalFailure(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/delete", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/delete")
	if err != nil {
		t.Fatal(err)
	}
	backendPath, err := workspace.ResolveRepository("backend")
	if err != nil {
		t.Fatal(err)
	}
	testutil.GitRepository{Path: backendPath}.CommitFile("unmerged.txt", "unmerged\n", "unmerged")
	failing := &failingDeleteGit{Git: gitadapter.NewAdapter("git"), failRemoveAt: 2}
	_, err = service.NewWorkspaceDeleterWith(failing, lock.Manager{}, store.WriteRecovery, os.Remove, store.WriteRawAtomic, os.ReadFile).Delete(context.Background(), project, workspace, data, true, nil)
	if err == nil || !service.HasCleanRollback(err) || cliExitKind(t, err, service.ErrorRollbackIncomplete) {
		t.Fatalf("clean unmerged descendant failure = %v, want clean rollback", err)
	}
	if _, err := os.Stat(backendPath); err != nil {
		t.Fatalf("clean unmerged backend was not restored: %v", err)
	}
}

type failingDeleteGit struct {
	gitadapter.Git
	deleteCalls  int
	failDeleteAt int
	removeCalls  int
	failRemoveAt int
}

type forceRecordingGit struct {
	gitadapter.Git
	removeForce map[string]bool
	deleteForce map[string]bool
	sourceIDs   map[string]string
}

func (g *forceRecordingGit) RemoveWorktree(ctx context.Context, repository, path string, force bool) error {
	if g.removeForce == nil {
		g.removeForce = map[string]bool{}
	}
	g.removeForce[g.sourceIDs[repository]] = force
	return g.Git.RemoveWorktree(ctx, repository, path, force)
}

func (g *forceRecordingGit) DeleteBranch(ctx context.Context, repository, branch string, force bool) error {
	if g.deleteForce == nil {
		g.deleteForce = map[string]bool{}
	}
	g.deleteForce[g.sourceIDs[repository]] = force
	return g.Git.DeleteBranch(ctx, repository, branch, force)
}

func sourceIDMap(project domain.Project) map[string]string {
	ids := make(map[string]string, len(project.Repositories))
	for _, repository := range project.Repositories {
		ids[repository.SourcePath] = repository.ID
	}
	return ids
}

func (g *failingDeleteGit) RemoveWorktree(ctx context.Context, repository, path string, force bool) error {
	g.removeCalls++
	if g.removeCalls == g.failRemoveAt {
		return errors.New("injected remove failure")
	}
	return g.Git.RemoveWorktree(ctx, repository, path, force)
}

func (g *failingDeleteGit) DeleteBranch(ctx context.Context, repository, branch string, force bool) error {
	g.deleteCalls++
	if g.deleteCalls == g.failDeleteAt {
		return errors.New("injected delete branch failure")
	}
	return g.Git.DeleteBranch(ctx, repository, branch, force)
}

func containsOverride(overrides []service.RemovalOverride, repositoryID, reason string) bool {
	for _, override := range overrides {
		if override.RepositoryID == repositoryID && override.Reason == reason {
			return true
		}
	}
	return false
}
