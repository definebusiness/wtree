package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
	"github.com/definebusiness/wtree/internal/transaction"
)

var m07RealGitTestSlots = make(chan struct{}, 4)

func parallelM07RealGitTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	m07RealGitTestSlots <- struct{}{}
	t.Cleanup(func() { <-m07RealGitTestSlots })
}

func TestWorkspaceRemovalForestSafetyMatrix(t *testing.T) {
	parallelM07RealGitTest(t)
	project, _ := forestWorkspaceProject(t)
	tests := []struct {
		name string
		run  func(*testing.T, domain.Project, string)
	}{
		{"remove child first", testWorkspaceRemoverRemovesPlainForestChildFirstAndRetainsState},
		{"remove preserves ambiguous directories", testWorkspaceRemoverPreservesPreexistingLogicalAndGroupingDirectories},
		{"replace grouping before first removal", testWorkspaceRemoverRejectsGroupingReplacementBeforeFirstRemoval},
		{"replace grouping before later removal", testWorkspaceRemoverPreservesLaterGroupingReplacementAndWritesRecovery},
		{"forced remove mutates then errors", testWorkspaceRemoverInventoriesForcedRemoveThatMutatesThenErrors},
		{"delete child first", testWorkspaceDeleterDeletesPlainForestChildFirstAfterSafeRemoval},
		{"delete preserves ambiguous directories", testWorkspaceDeleterPreservesPreexistingLogicalAndGroupingDirectories},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.run(t, project, t.TempDir()) })
		pruneSharedM07Worktrees(t, project)
	}
}

func testWorkspaceRemoverPreservesPreexistingLogicalAndGroupingDirectories(t *testing.T, project domain.Project, data string) {
	testWorkspaceTeardownPreservesPreexistingDirectories(t, project, data, false)
}

func testWorkspaceDeleterPreservesPreexistingLogicalAndGroupingDirectories(t *testing.T, project domain.Project, data string) {
	testWorkspaceTeardownPreservesPreexistingDirectories(t, project, data, true)
}

func testWorkspaceTeardownPreservesPreexistingDirectories(t *testing.T, project domain.Project, data string, deleteWorkspace bool) {
	t.Helper()
	project = forestWorkspaceProjectWithGroupedMounts(project)
	target := filepath.Join(t.TempDir(), "preexisting logical root")
	services := filepath.Join(target, "services")
	workspaceName := "feature/remove-preserve-directories"
	if deleteWorkspace {
		workspaceName = "feature/delete-preserve-directories"
	}
	rootMarker := filepath.Join(target, "notes.txt")
	servicesMarker := filepath.Join(services, "README")
	rootMarkerBytes := []byte("unrelated root notes remain\n")
	servicesMarkerBytes := []byte("unrelated grouping notes remain\n")
	var rootBefore, servicesBefore, rootMarkerBefore, servicesMarkerBefore os.FileInfo
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, workspaceName), func(event transaction.Event) {
		if rootBefore != nil || event.Kind != transaction.ExecuteStarted || event.Step != "prepare_grouping:api" {
			return
		}
		if err := os.MkdirAll(services, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rootMarker, rootMarkerBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(servicesMarker, servicesMarkerBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		var statErr error
		rootBefore, statErr = os.Lstat(target)
		if statErr != nil {
			t.Fatal(statErr)
		}
		servicesBefore, statErr = os.Lstat(services)
		if statErr != nil {
			t.Fatal(statErr)
		}
		rootMarkerBefore, statErr = os.Lstat(rootMarker)
		if statErr != nil {
			t.Fatal(statErr)
		}
		servicesMarkerBefore, statErr = os.Lstat(servicesMarker)
		if statErr != nil {
			t.Fatal(statErr)
		}
	}); err != nil {
		t.Fatalf("create forest workspace: %v", err)
	}
	if rootBefore == nil || servicesBefore == nil || rootMarkerBefore == nil || servicesMarkerBefore == nil {
		t.Fatal("creation did not observe pre-existing directory race fixture")
	}
	workspace, err := RequireWorkspace(project, data, workspaceName)
	if err != nil {
		t.Fatal(err)
	}
	if deleteWorkspace {
		_, err = NewWorkspaceDeleter().Delete(context.Background(), project, workspace, data, true, nil)
	} else {
		_, err = NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, true, nil)
	}
	if err != nil {
		t.Fatalf("teardown forest workspace: %v", err)
	}
	rootAfter, err := os.Lstat(target)
	if err != nil || !os.SameFile(rootBefore, rootAfter) {
		t.Fatalf("pre-existing logical root changed: before=%v after=%v err=%v", rootBefore, rootAfter, err)
	}
	servicesAfter, err := os.Lstat(services)
	if err != nil || !os.SameFile(servicesBefore, servicesAfter) {
		t.Fatalf("pre-existing grouping changed: before=%v after=%v err=%v", servicesBefore, servicesAfter, err)
	}
	rootMarkerAfter, err := os.Lstat(rootMarker)
	if err != nil || !os.SameFile(rootMarkerBefore, rootMarkerAfter) {
		t.Fatalf("root marker identity changed: before=%v after=%v err=%v", rootMarkerBefore, rootMarkerAfter, err)
	}
	if got, err := os.ReadFile(rootMarker); err != nil || !bytes.Equal(got, rootMarkerBytes) {
		t.Fatalf("root marker bytes = %q, err=%v", got, err)
	}
	servicesMarkerAfter, err := os.Lstat(servicesMarker)
	if err != nil || !os.SameFile(servicesMarkerBefore, servicesMarkerAfter) {
		t.Fatalf("grouping marker identity changed: before=%v after=%v err=%v", servicesMarkerBefore, servicesMarkerAfter, err)
	}
	if got, err := os.ReadFile(servicesMarker); err != nil || !bytes.Equal(got, servicesMarkerBytes) {
		t.Fatalf("grouping marker bytes = %q, err=%v", got, err)
	}
}

func TestWorkspaceRemovalRootSafetyMatrix(t *testing.T) {
	parallelM07RealGitTest(t)
	project, _ := rootWorkspaceProject(t)
	tests := []struct {
		name string
		run  func(*testing.T, domain.Project, string)
	}{
		{"remove mutates then errors", testWorkspaceRemoverRollsBackRemoveWorktreeThatMutatesThenErrors},
		{"replace branch after callback", testWorkspaceDeleterPreservesBranchReplacementAfterProgressCallback},
		{"replace state after callback", testWorkspaceDeleterPreservesConcurrentStateReplacement},
		{"state remove and publication rollback", testWorkspaceDeleterRestoresStateWhenRemovalMutatesThenErrors},
		{"branch delete mutates then errors", testWorkspaceDeleterRestoresBranchWhenDeletionMutatesThenErrors},
		{"state replacement after removal error", testWorkspaceDeleterPreservesStateReplacementAfterRemovalError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.run(t, project, t.TempDir()) })
		pruneSharedM07Worktrees(t, project)
	}
}

func pruneSharedM07Worktrees(t *testing.T, project domain.Project) {
	t.Helper()
	for _, repository := range project.Repositories {
		testutil.GitRepository{Path: repository.SourcePath}.Run(t, "worktree", "prune")
	}
}

func testWorkspaceRemoverRemovesPlainForestChildFirstAndRetainsState(t *testing.T, project domain.Project, data string) {
	project = forestWorkspaceProjectWithGroupedMounts(project)
	target := filepath.Join(t.TempDir(), "plain removal root")
	created, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/remove-forest"), nil)
	if err != nil {
		t.Fatalf("create forest workspace: %v", err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/remove-forest")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, true, nil)
	if err != nil {
		t.Fatalf("remove forest workspace: %v", err)
	}
	wantOrder := []string{"gamma", "beta", "alpha", "web", "api"}
	gotOrder := make([]string, 0, len(removed.Repositories))
	for _, repository := range removed.Repositories {
		gotOrder = append(gotOrder, repository.ID)
		if _, statErr := os.Lstat(repository.Path); !os.IsNotExist(statErr) {
			t.Fatalf("removed checkout %q remains: %v", repository.ID, statErr)
		}
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("remove order = %v, want %v", gotOrder, wantOrder)
	}
	if info, statErr := os.Lstat(target); statErr != nil || !info.IsDir() {
		t.Fatalf("ambiguous logical root was not preserved: %v, %v", info, statErr)
	}
	if info, statErr := os.Lstat(filepath.Join(target, "services")); statErr != nil || !info.IsDir() {
		t.Fatalf("ambiguous grouping was not preserved: %v, %v", info, statErr)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("remove changed retained state: before=%q after=%q err=%v", stateBefore, stateAfter, err)
	}
	if removed.LogicalRoot != target || removed.BaseRepository != project.BaseRepository || created.LogicalRoot != target {
		t.Fatalf("remove topology = %#v", removed)
	}
}

func testWorkspaceRemoverRejectsGroupingReplacementBeforeFirstRemoval(t *testing.T, project domain.Project, data string) {
	project = forestWorkspaceProjectWithGroupedMounts(project)
	target := filepath.Join(t.TempDir(), "plain removal root")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/remove-replaced-first"), nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/remove-replaced-first")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	services := filepath.Join(target, "services")
	displaced := filepath.Join(target, "services-owned")
	external := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(external, "marker")
	if err := os.WriteFile(marker, []byte("external remains\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	_, err = NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, true, func(event transaction.Event) {
		if swapped || event.Kind != transaction.ExecuteStarted || event.Step != "remove_worktree:gamma" {
			return
		}
		if renameErr := os.Rename(services, displaced); renameErr != nil {
			t.Fatalf("displace grouping: %v", renameErr)
		}
		if linkErr := os.Symlink(external, services); linkErr != nil {
			t.Fatalf("replace grouping with symlink: %v", linkErr)
		}
		swapped = true
	})
	if !swapped || err == nil || HasCleanRollback(err) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("replacement remove error = %v, swapped=%t", err, swapped)
	}
	stateAfter, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("replacement changed state: before=%q after=%q error=%v", stateBefore, stateAfter, readErr)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "external remains\n" {
		t.Fatalf("external marker = %q, %v", got, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(external, "api")); !os.IsNotExist(statErr) {
		t.Fatalf("removal touched external target: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(displaced, "api", "components", "alpha", "deep", "beta", "tools", "gamma")); statErr != nil {
		t.Fatalf("first removal mutated displaced owned tree: %v", statErr)
	}
	recoveryPath := removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID})
	recovery, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || recovery.FailedStep != "remove_worktree:gamma" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"remove_worktree:gamma"}) {
		t.Fatalf("replacement recovery = %#v, %v", recovery, readErr)
	}
}

func testWorkspaceRemoverPreservesLaterGroupingReplacementAndWritesRecovery(t *testing.T, project domain.Project, data string) {
	project = forestWorkspaceProjectWithGroupedMounts(project)
	target := filepath.Join(t.TempDir(), "plain removal root")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/remove-replaced-later"), nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/remove-replaced-later")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	services := filepath.Join(target, "services")
	displaced := filepath.Join(target, "services-owned")
	external := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(external, "marker")
	if err := os.WriteFile(marker, []byte("external remains\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	_, err = NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, true, func(event transaction.Event) {
		if swapped || event.Kind != transaction.ExecuteStarted || event.Step != "remove_worktree:api" {
			return
		}
		if renameErr := os.Rename(services, displaced); renameErr != nil {
			t.Fatalf("displace grouping: %v", renameErr)
		}
		if linkErr := os.Symlink(external, services); linkErr != nil {
			t.Fatalf("replace grouping with symlink: %v", linkErr)
		}
		swapped = true
	})
	if !swapped || err == nil || !errors.As(err, new(*Error)) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("later replacement remove error = %v, swapped=%t", err, swapped)
	}
	stateAfter, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("later replacement changed state: before=%q after=%q error=%v", stateBefore, stateAfter, readErr)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "external remains\n" {
		t.Fatalf("external marker = %q, %v", got, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(external, "api")); !os.IsNotExist(statErr) {
		t.Fatalf("rollback touched external target: %v", statErr)
	}
	if info, statErr := os.Lstat(services); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("concurrent replacement was not preserved: %v, %v", info, statErr)
	}
	recoveryPath := removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID})
	recovery, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || recovery.Operation != "remove" || recovery.FailedStep != "remove_worktree:api" || len(recovery.UnrevertedSteps) == 0 {
		t.Fatalf("later replacement recovery = %#v, %v", recovery, readErr)
	}
}

func testWorkspaceRemoverRollsBackRemoveWorktreeThatMutatesThenErrors(t *testing.T, project domain.Project, data string) {
	target := filepath.Join(t.TempDir(), "root workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/remove-post-error", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/remove-post-error")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	git := &removeThenErrorGit{Git: gitadapter.NewAdapter("git"), path: target}
	_, err = NewWorkspaceRemoverWith(git, lock.Manager{}, store.WriteRecovery).Remove(context.Background(), project, workspace, data, false, nil)
	if err == nil || !HasCleanRollback(err) || !git.failed {
		t.Fatalf("post-remove error = %v, clean=%t injected=%t", err, HasCleanRollback(err), git.failed)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("failed remove was not restored: %v", statErr)
	}
	stateAfter, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("post-remove error changed state: before=%q after=%q error=%v", stateBefore, stateAfter, readErr)
	}
	recoveryPath := removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID})
	if _, statErr := os.Lstat(recoveryPath); !os.IsNotExist(statErr) {
		t.Fatalf("clean failed-execute rollback wrote recovery: %v", statErr)
	}
}

func testWorkspaceRemoverInventoriesForcedRemoveThatMutatesThenErrors(t *testing.T, project domain.Project, data string) {
	project = forestWorkspaceProjectWithGroupedMounts(project)
	target := filepath.Join(t.TempDir(), "plain removal root")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/remove-post-error-forest"), nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/remove-post-error-forest")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := workspace.ResolveRepository("beta")
	if err != nil {
		t.Fatal(err)
	}
	git := &removeThenErrorGit{Git: gitadapter.NewAdapter("git"), path: beta}
	_, err = NewWorkspaceRemoverWith(git, lock.Manager{}, store.WriteRecovery).Remove(context.Background(), project, workspace, data, true, nil)
	if err == nil || HasCleanRollback(err) || !strings.Contains(err.Error(), "rollback is incomplete") || !git.failed {
		t.Fatalf("forced post-remove error = %v, clean=%t injected=%t", err, HasCleanRollback(err), git.failed)
	}
	if _, statErr := os.Lstat(beta); !os.IsNotExist(statErr) {
		t.Fatalf("forced removed checkout unexpectedly restored: %v", statErr)
	}
	recoveryPath := removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID})
	recovery, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || recovery.FailedStep != "remove_worktree:beta" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"remove_worktree:beta", "remove_worktree:gamma"}) {
		t.Fatalf("forced post-remove recovery = %#v, %v", recovery, readErr)
	}
}

func testWorkspaceRemoverPreservesGroupingReplacementAtCleanupBoundary(t *testing.T, project domain.Project, data string) {
	project = forestWorkspaceProjectWithGroupedMounts(project)
	target := filepath.Join(t.TempDir(), "plain removal root")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/remove-grouping-cleanup"), nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/remove-grouping-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	services := filepath.Join(target, "services")
	displaced := filepath.Join(target, "services-owned")
	external := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(external, "marker")
	if err := os.WriteFile(marker, []byte("external remains\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	_, err = NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, true, func(event transaction.Event) {
		if swapped || event.Kind != transaction.ExecuteStarted || event.Step != "remove_grouping:services" {
			return
		}
		if renameErr := os.Rename(services, displaced); renameErr != nil {
			t.Fatalf("displace empty grouping: %v", renameErr)
		}
		if linkErr := os.Symlink(external, services); linkErr != nil {
			t.Fatalf("replace empty grouping with symlink: %v", linkErr)
		}
		swapped = true
	})
	if !swapped || err == nil || HasCleanRollback(err) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("grouping cleanup replacement error = %v, swapped=%t", err, swapped)
	}
	if info, statErr := os.Lstat(services); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("cleanup replacement was not preserved: %v, %v", info, statErr)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "external remains\n" {
		t.Fatalf("external marker = %q, %v", got, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(external, "api")); !os.IsNotExist(statErr) {
		t.Fatalf("cleanup touched external target: %v", statErr)
	}
	stateAfter, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("cleanup replacement changed state: before=%q after=%q error=%v", stateBefore, stateAfter, readErr)
	}
	recoveryPath := removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID})
	recovery, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || recovery.FailedStep != "remove_grouping:services" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"remove_worktree:api", "remove_worktree:alpha", "remove_worktree:beta", "remove_worktree:gamma"}) {
		t.Fatalf("cleanup replacement recovery = %#v, %v", recovery, readErr)
	}
}

func testWorkspaceDeleterDeletesPlainForestChildFirstAfterSafeRemoval(t *testing.T, project domain.Project, data string) {
	project = forestWorkspaceProjectWithGroupedMounts(project)
	target := filepath.Join(t.TempDir(), "plain deletion root")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/delete-forest"), nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/delete-forest")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := NewWorkspaceDeleter().Delete(context.Background(), project, workspace, data, true, nil)
	if err != nil {
		t.Fatalf("delete forest workspace: %v", err)
	}
	gotOrder := make([]string, 0, len(deleted.Repositories))
	for _, repository := range deleted.Repositories {
		gotOrder = append(gotOrder, repository.ID)
		if _, statErr := os.Lstat(repository.Path); !os.IsNotExist(statErr) {
			t.Fatalf("deleted checkout %q remains: %v", repository.ID, statErr)
		}
	}
	if want := []string{"gamma", "beta", "alpha", "web", "api"}; !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("delete order = %v, want %v", gotOrder, want)
	}
	if deleted.LogicalRoot != target || deleted.BaseRepository != project.BaseRepository {
		t.Fatalf("delete topology = %#v", deleted)
	}
	if info, statErr := os.Lstat(target); statErr != nil || !info.IsDir() {
		t.Fatalf("ambiguous logical root was not preserved: %v, %v", info, statErr)
	}
	if info, statErr := os.Lstat(filepath.Join(target, "services")); statErr != nil || !info.IsDir() {
		t.Fatalf("ambiguous grouping was not preserved: %v, %v", info, statErr)
	}
	if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, workspace.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("deleted state remains: %v", statErr)
	}
	adapter := gitadapter.NewAdapter("git")
	for _, repository := range project.Repositories {
		exists, branchErr := adapter.BranchExists(context.Background(), repository.SourcePath, "feature/delete-forest")
		if branchErr != nil || exists {
			t.Fatalf("deleted branch %q exists=%t err=%v", repository.ID, exists, branchErr)
		}
	}
}

func testWorkspaceDeleterPreservesBranchReplacementAfterProgressCallback(t *testing.T, project domain.Project, data string) {
	target := filepath.Join(t.TempDir(), "root workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/delete-branch-replaced", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/delete-branch-replaced")
	if err != nil {
		t.Fatal(err)
	}
	repository := project.Repositories[0]
	testutil.GitRepository{Path: repository.SourcePath}.CommitFile("later.txt", "later\n", "later")
	adapter := gitadapter.NewAdapter("git")
	alternateHead, err := adapter.Head(context.Background(), repository.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	_, err = NewWorkspaceDeleter().Delete(context.Background(), project, workspace, data, false, func(event transaction.Event) {
		if replaced || event.Kind != transaction.ExecuteStarted || event.Step != "delete_branch:root" {
			return
		}
		if deleteErr := adapter.DeleteBranch(context.Background(), repository.SourcePath, workspace.Name, true); deleteErr != nil {
			t.Fatalf("delete original branch in callback: %v", deleteErr)
		}
		if createErr := adapter.CreateBranch(context.Background(), repository.SourcePath, workspace.Name, alternateHead); createErr != nil {
			t.Fatalf("create replacement branch in callback: %v", createErr)
		}
		replaced = true
	})
	if !replaced || err == nil || HasCleanRollback(err) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("branch replacement delete error = %v, replaced=%t", err, replaced)
	}
	current, resolveErr := adapter.ResolveRef(context.Background(), repository.SourcePath, "refs/heads/"+workspace.Name)
	if resolveErr != nil || current != alternateHead {
		t.Fatalf("replacement branch = %q, want %q, err=%v", current, alternateHead, resolveErr)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Fatalf("branch replacement caused an unsafe worktree restore: %v", statErr)
	}
	recovery, readErr := store.ReadRecovery(removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID}))
	if readErr != nil || recovery.FailedStep != "delete_branch:root" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"delete_branch:root", "remove_worktree:root"}) {
		t.Fatalf("branch replacement recovery = %#v, %v", recovery, readErr)
	}
}

func testWorkspaceDeleterPreservesConcurrentStateReplacement(t *testing.T, project domain.Project, data string) {
	target := filepath.Join(t.TempDir(), "root workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/delete-state-replaced", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/delete-state-replaced")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	replacement := []byte("{\"concurrent\":true}\n")
	replaced := false
	_, err = NewWorkspaceDeleter().Delete(context.Background(), project, workspace, data, false, func(event transaction.Event) {
		if replaced || event.Kind != transaction.ExecuteStarted || event.Step != "delete_state" {
			return
		}
		if writeErr := store.WriteRawAtomic(statePath, replacement); writeErr != nil {
			t.Fatalf("replace state in callback: %v", writeErr)
		}
		replaced = true
	})
	if !replaced || err == nil || HasCleanRollback(err) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("state replacement delete error = %v, replaced=%t", err, replaced)
	}
	got, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("state replacement = %q, want %q, err=%v", got, replacement, readErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("worktree was not restored after state replacement: %v", statErr)
	}
	recovery, readErr := store.ReadRecovery(removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID}))
	if readErr != nil || recovery.FailedStep != "delete_state" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"delete_state"}) {
		t.Fatalf("state replacement recovery = %#v, %v", recovery, readErr)
	}
}

func testWorkspaceDeleterRestoresStateWhenRemovalMutatesThenErrors(t *testing.T, project domain.Project, data string) {
	target := filepath.Join(t.TempDir(), "root workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/delete-state-post-error", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/delete-state-post-error")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	removeState := func(path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return errors.New("injected error after exact state removal")
	}
	deleter := NewWorkspaceDeleterWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteRecovery, removeState, store.WriteRawCAS, os.ReadFile)
	_, err = deleter.Delete(context.Background(), project, workspace, data, false, nil)
	if err == nil || !HasCleanRollback(err) {
		t.Fatalf("post-state-remove error = %v, want clean rollback", err)
	}
	stateAfter, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("post-error state = %q, want %q, err=%v", stateAfter, stateBefore, readErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("worktree was not restored after state remove error: %v", statErr)
	}
	if _, statErr := os.Lstat(removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID})); !os.IsNotExist(statErr) {
		t.Fatalf("clean post-state-remove rollback wrote recovery: %v", statErr)
	}

	publicationTarget := filepath.Join(t.TempDir(), "publication-boundary workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/delete-state-publication-race", TargetPath: publicationTarget, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	publicationWorkspace, err := RequireWorkspace(project, data, "feature/delete-state-publication-race")
	if err != nil {
		t.Fatal(err)
	}
	publicationStatePath := WorkspaceStatePath(data, project.ID, publicationWorkspace.ID)
	replacement := []byte("{\"concurrent\":\"publication-boundary\"}\n")
	publicationWriteRawCAS := func(path string, data []byte, compare func() error) error {
		if err := store.WriteRawAtomic(path, replacement); err != nil {
			return err
		}
		return store.WriteRawCAS(path, data, compare)
	}
	publicationDeleter := NewWorkspaceDeleterWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteRecovery, removeState, publicationWriteRawCAS, os.ReadFile)
	_, err = publicationDeleter.Delete(context.Background(), project, publicationWorkspace, data, false, nil)
	if err == nil || HasCleanRollback(err) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("publication-boundary state replacement error = %v", err)
	}
	got, readErr := os.ReadFile(publicationStatePath)
	if readErr != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("publication-boundary replacement = %q, want %q, err=%v", got, replacement, readErr)
	}
	recovery, readErr := store.ReadRecovery(removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: publicationWorkspace.ID}))
	if readErr != nil || recovery.FailedStep != "delete_state" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"delete_state"}) {
		t.Fatalf("publication-boundary replacement recovery = %#v, %v", recovery, readErr)
	}
}

func testWorkspaceDeleterRestoresBranchWhenDeletionMutatesThenErrors(t *testing.T, project domain.Project, data string) {
	target := filepath.Join(t.TempDir(), "root workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/delete-branch-post-error", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/delete-branch-post-error")
	if err != nil {
		t.Fatal(err)
	}
	git := &deleteBranchThenErrorGit{Git: gitadapter.NewAdapter("git"), branch: workspace.Name}
	deleter := NewWorkspaceDeleterWith(git, lock.Manager{}, store.WriteRecovery, os.Remove, store.WriteRawCAS, os.ReadFile)
	_, err = deleter.Delete(context.Background(), project, workspace, data, false, nil)
	if err == nil || !HasCleanRollback(err) || !git.failed {
		t.Fatalf("post-branch-delete error = %v, clean=%t injected=%t", err, HasCleanRollback(err), git.failed)
	}
	exists, branchErr := git.BranchExists(context.Background(), project.Repositories[0].SourcePath, workspace.Name)
	if branchErr != nil || !exists {
		t.Fatalf("post-error branch was not restored: exists=%t err=%v", exists, branchErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("post-error worktree was not restored: %v", statErr)
	}
	if _, statErr := os.Lstat(removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID})); !os.IsNotExist(statErr) {
		t.Fatalf("clean post-branch-delete rollback wrote recovery: %v", statErr)
	}
}

func testWorkspaceDeleterPreservesStateReplacementAfterRemovalError(t *testing.T, project domain.Project, data string) {
	target := filepath.Join(t.TempDir(), "root workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/delete-state-post-replacement", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/delete-state-post-replacement")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	replacement := []byte("{\"concurrent\":true}\n")
	removeState := func(path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := store.WriteRawAtomic(path, replacement); err != nil {
			return err
		}
		return errors.New("injected error after state replacement")
	}
	deleter := NewWorkspaceDeleterWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteRecovery, removeState, store.WriteRawCAS, os.ReadFile)
	_, err = deleter.Delete(context.Background(), project, workspace, data, false, nil)
	if err == nil || HasCleanRollback(err) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("post-state-replacement error = %v", err)
	}
	got, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("post-error state replacement = %q, want %q, err=%v", got, replacement, readErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("worktree was not restored after state replacement error: %v", statErr)
	}
	recovery, readErr := store.ReadRecovery(removalRecoveryPath(data, RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID}))
	if readErr != nil || recovery.FailedStep != "delete_state" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"delete_state"}) {
		t.Fatalf("post-state-replacement recovery = %#v, %v", recovery, readErr)
	}
}

type removeThenErrorGit struct {
	gitadapter.Git
	path   string
	failed bool
}

type deleteBranchThenErrorGit struct {
	gitadapter.Git
	branch string
	failed bool
}

func (git *deleteBranchThenErrorGit) DeleteBranch(ctx context.Context, repository, branch string, force bool) error {
	if err := git.Git.DeleteBranch(ctx, repository, branch, force); err != nil {
		return err
	}
	if !git.failed && branch == git.branch {
		git.failed = true
		return errors.New("injected error after real branch deletion")
	}
	return nil
}

func (git *removeThenErrorGit) RemoveWorktree(ctx context.Context, repository, path string, force bool) error {
	if err := git.Git.RemoveWorktree(ctx, repository, path, force); err != nil {
		return err
	}
	if !git.failed && filepath.Clean(path) == filepath.Clean(git.path) {
		git.failed = true
		return errors.New("injected error after real worktree removal")
	}
	return nil
}
