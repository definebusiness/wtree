package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
	"github.com/definebusiness/wtree/internal/transaction"
)

func TestWorkspaceCreatorCreatesForestWithGroupedTopLevelsAndNestedChildren(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "workspace forest")
	created, err := NewWorkspaceCreator().CreateWithResult(context.Background(), project.Project, WorkspacePlanRequest{WorkspaceName: "feature/forest", TargetPath: target, DataDir: data}, nil)
	if err != nil {
		t.Fatalf("Create forest: %v", err)
	}
	value := created.Plan
	if value.LogicalRoot != target || value.BaseRepository != "api" {
		t.Fatalf("workspace topology = %#v", value)
	}
	if created.LogicalRoot != target || created.BaseRepository != "api" {
		t.Fatalf("create result topology = %#v", created)
	}
	for _, repository := range value.Repositories {
		if _, err := os.Stat(filepath.Join(repository.Path, ".git")); err != nil {
			t.Fatalf("%s worktree at %q: %v", repository.ID, repository.Path, err)
		}
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(data, project.Project.ID, value.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Path != target || len(state.Repositories) != len(value.Repositories) {
		t.Fatalf("workspace state = %#v", state)
	}
	for _, repository := range value.Repositories {
		checkout, found := state.Repositories[repository.ID]
		if !found || checkout.Mount != repository.Mount || checkout.ResolvedPath != repository.Path || checkout.Branch != "feature/forest" {
			t.Fatalf("state checkout %q = %#v, found %v", repository.ID, checkout, found)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("plain logical root unexpectedly owns ignore: %v", err)
	}
	for _, parent := range []struct {
		id    string
		child string
	}{
		{id: "api", child: "alpha"},
		{id: "alpha", child: "beta"},
		{id: "beta", child: "gamma"},
	} {
		path := filepath.Join(repositoryPath(value, parent.id), ".gitignore")
		contents, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(contents), "/"+parent.child+"/") {
			t.Fatalf("immediate parent ignore %q = %q, %v", parent.id, contents, err)
		}
	}
	markerPath := filepath.Join(target, "notes.txt")
	marker := []byte("preserve this logical-root note\n")
	if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceFromState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkspaceRemover().Remove(context.Background(), project.Project, workspace, data, true, nil); err != nil {
		t.Fatalf("remove forest before same-root checkout: %v", err)
	}
	if contents, err := os.ReadFile(markerPath); err != nil || !bytes.Equal(contents, marker) {
		t.Fatalf("preserved logical-root marker = %q, %v", contents, err)
	}
	restored, err := NewWorkspaceCreator().Checkout(context.Background(), project.Project, WorkspacePlanRequest{WorkspaceName: "feature/forest", TargetPath: target, DataDir: data}, nil)
	if err != nil {
		t.Fatalf("checkout into retained plain logical root: %v", err)
	}
	for _, repository := range restored.Repositories {
		if _, err := os.Stat(filepath.Join(repository.Path, ".git")); err != nil {
			t.Fatalf("restored %s worktree at %q: %v", repository.ID, repository.Path, err)
		}
	}
	if contents, err := os.ReadFile(markerPath); err != nil || !bytes.Equal(contents, marker) {
		t.Fatalf("restored checkout changed logical-root marker = %q, %v", contents, err)
	}
}

func repositoryPath(value plan.WorkspacePlan, id string) string {
	for _, repository := range value.Repositories {
		if repository.ID == id {
			return repository.Path
		}
	}
	return ""
}

func TestWorkspaceCreatorChecksOutExistingForestBranchAtNewLogicalRoot(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	creator := NewWorkspaceCreator()
	first, err := creator.Create(context.Background(), resolution.Project, WorkspacePlanRequest{WorkspaceName: "feature/checkout", TargetPath: filepath.Join(t.TempDir(), "first"), DataDir: data}, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := gitadapter.NewAdapter("git")
	for _, repository := range resolution.Project.ChildFirst() {
		if err := adapter.RemoveWorktree(context.Background(), repository.SourcePath, repositoryPath(first, repository.ID), true); err != nil {
			t.Fatalf("remove first %s worktree: %v", repository.ID, err)
		}
	}
	secondTarget := filepath.Join(t.TempDir(), "second")
	second, err := creator.Checkout(context.Background(), resolution.Project, WorkspacePlanRequest{WorkspaceName: "feature/checkout", TargetPath: secondTarget, DataDir: data}, nil)
	if err != nil {
		t.Fatalf("Checkout forest: %v", err)
	}
	if second.LogicalRoot != secondTarget || second.BaseRepository != "api" || len(second.Repositories) != len(first.Repositories) {
		t.Fatalf("checkout plan = %#v", second)
	}
	for _, repository := range second.Repositories {
		if _, err := os.Stat(filepath.Join(repository.Path, ".git")); err != nil {
			t.Fatalf("checked-out %s worktree: %v", repository.ID, err)
		}
	}
}

func TestWorkspaceCreatorForestCancellationRemovesOwnedGroupingDirectories(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "owned-root")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = NewWorkspaceCreator().Create(ctx, resolution.Project, WorkspacePlanRequest{WorkspaceName: "feature/cancel", TargetPath: target, DataDir: data}, func(event transaction.Event) {
		if event.Kind == transaction.ExecuteSucceeded && event.Step == "add_worktree:api" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("forest cancellation error = %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("owned logical root remains after rollback: %v", err)
	}
	if _, err := os.Lstat(WorkspaceStatePath(data, resolution.Project.ID, "feature-cancel")); !os.IsNotExist(err) {
		t.Fatalf("state remains after rollback: %v", err)
	}
}

func TestWorkspaceCreatorReceiptCaptureFailureRollsBackFailedAddWorktree(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation plan.Operation
		forest    bool
	}{
		{name: "create-root-git", operation: plan.Create},
		{name: "checkout-root-git", operation: plan.Checkout},
		{name: "create-grouped-forest", operation: plan.Create, forest: true},
		{name: "checkout-grouped-forest", operation: plan.Checkout, forest: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, data := rootWorkspaceProject(t)
			request := WorkspacePlanRequest{WorkspaceName: "feature/receipt-" + test.name, TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}
			failedRepositoryID := "root"
			if test.forest {
				project, data = forestWorkspaceProject(t)
				request = forestWorkspaceRequest(filepath.Join(t.TempDir(), "workspace"), data, "feature/receipt-"+test.name)
				failedRepositoryID = "api"
			}
			request.Operation = test.operation
			if test.operation == plan.Checkout {
				createWorkspaceBranches(t, project, request.WorkspaceName)
			}
			value, err := NewWorkspacePlanner().Plan(context.Background(), project, request)
			if err != nil {
				t.Fatal(err)
			}
			failedPath := repositoryPath(value, failedRepositoryID)
			creator := NewWorkspaceCreator()
			filesystem := creator.filesystem
			injected := false
			filesystem.lstat = func(path string) (os.FileInfo, error) {
				info, err := os.Lstat(path)
				if filepath.Clean(path) == filepath.Clean(failedPath) && err == nil {
					injected = true
					return nil, errors.New("injected post-add receipt capture failure")
				}
				return info, err
			}
			creator.filesystem = filesystem
			var progress []transaction.Event
			if test.operation == plan.Create {
				_, err = creator.Create(context.Background(), project, request, func(event transaction.Event) { progress = append(progress, event) })
			} else {
				_, err = creator.Checkout(context.Background(), project, request, func(event transaction.Event) { progress = append(progress, event) })
			}
			if !injected || err == nil {
				t.Fatalf("operation error = %v, injected = %t", err, injected)
			}
			var application *Error
			if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || HasCleanRollback(err) {
				t.Fatalf("operation error = %v, want incomplete rollback", err)
			}
			if _, statErr := os.Stat(filepath.Join(failedPath, ".git")); statErr != nil {
				t.Fatalf("unreceipted worktree was not preserved: %v", statErr)
			}
			if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, value.WorkspaceID)); !os.IsNotExist(statErr) {
				t.Fatalf("workspace state exists: %v", statErr)
			}
			record, readErr := store.ReadRecovery(RecoveryRecordPath(data, value))
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantFirst := "add_worktree:" + failedRepositoryID
			if len(record.UnrevertedSteps) == 0 || record.UnrevertedSteps[0] != wantFirst || len(record.RollbackFailures) == 0 || record.RollbackFailures[0].Step != wantFirst {
				t.Fatalf("recovery = %#v, want %q first", record, wantFirst)
			}
			assertFailedStepCleanupEvents(t, progress, wantFirst, transaction.RollbackFailed)
			targetRepository := project.Repositories[0]
			for _, repository := range project.Repositories {
				if repository.ID == failedRepositoryID {
					targetRepository = repository
				}
			}
			exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), targetRepository.SourcePath, request.WorkspaceName)
			if branchErr != nil || !exists {
				t.Fatalf("preserved branch = exists:%t error:%v", exists, branchErr)
			}
		})
	}
}

func TestWorkspaceCreatorReceiptCaptureReplacementWritesExactRecovery(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation plan.Operation
		forest    bool
		wantOther string
	}{
		{name: "create-root-git", operation: plan.Create, wantOther: "create_branch:root"},
		{name: "checkout-grouped-forest", operation: plan.Checkout, forest: true, wantOther: "prepare_grouping:api"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, data := rootWorkspaceProject(t)
			request := WorkspacePlanRequest{WorkspaceName: "feature/replaced-receipt-" + test.name, TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}
			failedRepositoryID := "root"
			if test.forest {
				project, data = forestWorkspaceProject(t)
				request = forestWorkspaceRequest(filepath.Join(t.TempDir(), "workspace"), data, "feature/replaced-receipt-"+test.name)
				failedRepositoryID = "api"
			}
			request.Operation = test.operation
			if test.operation == plan.Checkout {
				createWorkspaceBranches(t, project, request.WorkspaceName)
			}
			value, err := NewWorkspacePlanner().Plan(context.Background(), project, request)
			if err != nil {
				t.Fatal(err)
			}
			failedPath := repositoryPath(value, failedRepositoryID)
			capturedPath := failedPath + ".captured"
			marker := []byte("replacement remains\n")
			creator := NewWorkspaceCreator()
			filesystem := creator.filesystem
			injected := false
			filesystem.lstat = func(path string) (os.FileInfo, error) {
				info, err := os.Lstat(path)
				if filepath.Clean(path) != filepath.Clean(failedPath) || err != nil || injected {
					return info, err
				}
				injected = true
				if renameErr := os.Rename(failedPath, capturedPath); renameErr != nil {
					return nil, renameErr
				}
				if mkdirErr := os.Mkdir(failedPath, 0o755); mkdirErr != nil {
					return nil, mkdirErr
				}
				if writeErr := os.WriteFile(filepath.Join(failedPath, "marker"), marker, 0o600); writeErr != nil {
					return nil, writeErr
				}
				return nil, errors.New("injected post-add receipt replacement")
			}
			creator.filesystem = filesystem
			if test.operation == plan.Create {
				_, err = creator.Create(context.Background(), project, request, nil)
			} else {
				_, err = creator.Checkout(context.Background(), project, request, nil)
			}
			var application *Error
			if !injected || !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || HasCleanRollback(err) {
				t.Fatalf("operation error = %v, injected = %t", err, injected)
			}
			if got, readErr := os.ReadFile(filepath.Join(failedPath, "marker")); readErr != nil || string(got) != string(marker) {
				t.Fatalf("replacement marker = %q, %v", got, readErr)
			}
			if _, statErr := os.Stat(filepath.Join(capturedPath, ".git")); statErr != nil {
				t.Fatalf("captured transaction worktree = %v", statErr)
			}
			if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, value.WorkspaceID)); !os.IsNotExist(statErr) {
				t.Fatalf("workspace state exists: %v", statErr)
			}
			record, readErr := store.ReadRecovery(RecoveryRecordPath(data, value))
			if readErr != nil {
				t.Fatal(readErr)
			}
			failedStep := "add_worktree:" + failedRepositoryID
			if record.FailedStep != failedStep || len(record.UnrevertedSteps) < 2 || record.UnrevertedSteps[0] != failedStep || record.UnrevertedSteps[1] != test.wantOther {
				t.Fatalf("recovery unreverted steps = %v, failed = %q", record.UnrevertedSteps, record.FailedStep)
			}
			if len(record.RollbackFailures) < 2 || record.RollbackFailures[0].Step != failedStep || record.RollbackFailures[1].Step != test.wantOther {
				t.Fatalf("recovery rollback failures = %#v", record.RollbackFailures)
			}
		})
	}
}

func TestWorkspaceCreatorReceiptCaptureRejectsPlannedBranchWithWrongHead(t *testing.T) {
	project, _ := rootWorkspaceProject(t)
	repository := project.Repositories[0]
	adapter := gitadapter.NewAdapter("git")
	head, err := adapter.Head(context.Background(), repository.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	branch := "receipt-wrong-head"
	if err := adapter.CreateBranch(context.Background(), repository.SourcePath, branch, head); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worktree")
	if err := adapter.AddWorktree(context.Background(), repository.SourcePath, path, branch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "changed.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", path, "add", "changed.txt")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("add: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", path, "commit", "-m", "change")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, output)
	}
	receipt, err := NewWorkspaceCreator().captureWorktreeReceipt(context.Background(), path, repository.CommonGitDir, branch, head)
	if err == nil || receipt != nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("receipt = %#v, err = %v", receipt, err)
	}
}

func TestWorkspaceCreatorPreservesCleanLinkedWorktreeReplacementAtCleanupBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation plan.Operation
		forest    bool
		phase     string
		detached  bool
	}{
		{name: "create-root-receipt", operation: plan.Create, phase: "receipt"},
		{name: "checkout-root-receipt", operation: plan.Checkout, phase: "receipt"},
		{name: "create-forest-receipt", operation: plan.Create, forest: true, phase: "receipt"},
		{name: "checkout-forest-receipt", operation: plan.Checkout, forest: true, phase: "receipt"},
		{name: "create-root-return-boundary", operation: plan.Create, phase: "return"},
		{name: "checkout-root-return-boundary", operation: plan.Checkout, phase: "return"},
		{name: "create-forest-return-boundary", operation: plan.Create, forest: true, phase: "return"},
		{name: "checkout-forest-return-boundary", operation: plan.Checkout, forest: true, phase: "return"},
		{name: "create-root-completed", operation: plan.Create, phase: "completed"},
		{name: "checkout-root-completed-detached", operation: plan.Checkout, phase: "completed", detached: true},
		{name: "create-forest-completed", operation: plan.Create, forest: true, phase: "completed"},
		{name: "checkout-forest-completed", operation: plan.Checkout, forest: true, phase: "completed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parallelM07RealGitTest(t)
			project, data := rootWorkspaceProject(t)
			request := WorkspacePlanRequest{WorkspaceName: "feature/linked-" + test.name, TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data, Operation: test.operation}
			targetID := "root"
			if test.forest {
				project, data = forestWorkspaceProject(t)
				request = forestWorkspaceRequest(filepath.Join(t.TempDir(), "workspace"), data, "feature/linked-"+test.name)
				request.Operation = test.operation
				targetID = "web"
			}
			if test.operation == plan.Checkout {
				createWorkspaceBranches(t, project, request.WorkspaceName)
			}
			value, err := NewWorkspacePlanner().Plan(context.Background(), project, request)
			if err != nil {
				t.Fatal(err)
			}
			targetPath := repositoryPath(value, targetID)
			sourcePath := ""
			for _, repository := range project.Repositories {
				if repository.ID == targetID {
					sourcePath = repository.SourcePath
					break
				}
			}
			if sourcePath == "" {
				t.Fatalf("repository %q not found", targetID)
			}
			alternatePath := filepath.Join(filepath.Dir(request.TargetPath), "alternate-"+test.name)
			displacedPath := targetPath + ".transaction"
			alternateBranch := "alternate/" + test.name
			replacement := newLinkedWorktreeReplacement(t, sourcePath, targetPath, displacedPath, alternatePath, alternateBranch, test.detached)
			git := &cleanupBoundaryLinkedReplacementGit{Git: gitadapter.NewAdapter("git"), replacement: replacement, t: t, swapAfterAdd: test.phase == "return"}
			transactionService := NewWorkspaceTransaction()
			if test.phase == "completed" {
				transactionService = NewWorkspaceTransactionWith(lock.Manager{}, func(string, store.WorkspaceState) error {
					return errors.New("injected post-completion state failure")
				}, store.WriteRecovery, os.Remove)
			}
			creator := NewWorkspaceCreatorWith(git, transactionService)
			if test.phase == "receipt" {
				filesystem := creator.filesystem
				filesystem.lstat = func(path string) (os.FileInfo, error) {
					info, statErr := os.Lstat(path)
					if filepath.Clean(path) == filepath.Clean(targetPath) && statErr == nil {
						replacement.swapInto(t, path)
						return nil, errors.New("injected post-add receipt failure")
					}
					return info, statErr
				}
				creator.filesystem = filesystem
			}
			if test.operation == plan.Create {
				_, err = creator.Create(context.Background(), project, request, nil)
			} else {
				_, err = creator.Checkout(context.Background(), project, request, nil)
			}
			var application *Error
			if !replacement.swapped || !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || HasCleanRollback(err) {
				t.Fatalf("operation error = %v, swapped = %t", err, replacement.swapped)
			}
			if got, want := replacement.swappedInQuarantine, test.phase == "completed"; got != want {
				t.Fatalf("post-quarantine substitution = %t, want %t", got, want)
			}
			assertLinkedWorktreeReplacementPreserved(t, replacement)
			if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, value.WorkspaceID)); !os.IsNotExist(statErr) {
				t.Fatalf("workspace state exists: %v", statErr)
			}
			record, readErr := store.ReadRecovery(RecoveryRecordPath(data, value))
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantFirst := "add_worktree:" + targetID
			wantRecovery := []string{wantFirst}
			if test.forest {
				wantRecovery = append(wantRecovery, "prepare_grouping:"+targetID)
			}
			if test.operation == plan.Create {
				wantRecovery = append(wantRecovery, "create_branch:"+targetID)
			}
			if test.forest {
				wantRecovery = append(wantRecovery, "prepare_grouping:api")
			}
			failureSteps := make([]string, len(record.RollbackFailures))
			for index, failure := range record.RollbackFailures {
				failureSteps[index] = failure.Step
			}
			if strings.Join(record.UnrevertedSteps, "\x00") != strings.Join(wantRecovery, "\x00") || strings.Join(failureSteps, "\x00") != strings.Join(wantRecovery, "\x00") {
				t.Fatalf("recovery = %#v, want exact steps %v", record, wantRecovery)
			}
			branchExists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), sourcePath, request.WorkspaceName)
			if branchErr != nil || !branchExists {
				t.Fatalf("transaction branch = exists:%t error:%v", branchExists, branchErr)
			}
		})
	}
}

func TestWorkspaceCreatorRejectsDetachedOwnershipReceipt(t *testing.T) {
	project, _ := rootWorkspaceProject(t)
	sourcePath := project.Repositories[0].SourcePath
	path := filepath.Join(t.TempDir(), "detached")
	adapter := gitadapter.NewAdapter("git")
	head, err := adapter.Head(context.Background(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateBranch(context.Background(), sourcePath, "receipt-detached", head); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddWorktree(context.Background(), sourcePath, path, "receipt-detached"); err != nil {
		t.Fatal(err)
	}
	command := testutil.GitCommand(t, "-C", path, "checkout", "--detach")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("detach worktree: %v: %s", err, output)
	}
	receipt, err := NewWorkspaceCreator().captureWorktreeReceipt(context.Background(), path, "", "", "")
	if err == nil || receipt != nil {
		t.Fatalf("detached receipt = %#v, %v, want explicit failure", receipt, err)
	}
}

type linkedWorktreeReplacement struct {
	sourcePath, publicPath, displacedPath, alternatePath string
	alternateBranch                                      string
	detached                                             bool
	swapped                                              bool
	swappedInQuarantine                                  bool
	originalGitDir, alternateGitDir                      string
	originalHead, alternateHead                          string
	originalGitFile, alternateGitFile                    []byte
}

func newLinkedWorktreeReplacement(t *testing.T, sourcePath, publicPath, displacedPath, alternatePath, alternateBranch string, detached bool) *linkedWorktreeReplacement {
	t.Helper()
	adapter := gitadapter.NewAdapter("git")
	head, err := adapter.Head(context.Background(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateBranch(context.Background(), sourcePath, alternateBranch, head); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddWorktree(context.Background(), sourcePath, alternatePath, alternateBranch); err != nil {
		t.Fatal(err)
	}
	if detached {
		command := testutil.GitCommand(t, "-C", alternatePath, "checkout", "--detach")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("detach alternate worktree: %v: %s", err, output)
		}
	}
	alternateGitDir, err := adapter.GitDir(context.Background(), alternatePath)
	if err != nil {
		t.Fatal(err)
	}
	alternateGitFile, err := os.ReadFile(filepath.Join(alternatePath, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	alternateHead, err := adapter.Head(context.Background(), alternatePath)
	if err != nil {
		t.Fatal(err)
	}
	return &linkedWorktreeReplacement{
		sourcePath: sourcePath, publicPath: publicPath, displacedPath: displacedPath, alternatePath: alternatePath,
		alternateBranch: alternateBranch, detached: detached, alternateGitDir: alternateGitDir, alternateHead: alternateHead, alternateGitFile: alternateGitFile,
	}
}

func (replacement *linkedWorktreeReplacement) swapInto(t *testing.T, ownedPath string) {
	t.Helper()
	adapter := gitadapter.NewAdapter("git")
	var err error
	replacement.originalGitDir, err = adapter.GitDir(context.Background(), ownedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement.originalHead, err = adapter.Head(context.Background(), ownedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement.originalGitFile, err = os.ReadFile(filepath.Join(ownedPath, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ownedPath, replacement.displacedPath); err != nil {
		t.Fatal(err)
	}
	if err := adapter.WorktreeRepair(context.Background(), replacement.displacedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement.alternatePath, ownedPath); err != nil {
		t.Fatal(err)
	}
	if err := adapter.WorktreeRepair(context.Background(), ownedPath); err != nil {
		t.Fatal(err)
	}
	replacement.swapped = true
}

type cleanupBoundaryLinkedReplacementGit struct {
	gitadapter.Git
	replacement  *linkedWorktreeReplacement
	t            *testing.T
	swapAfterAdd bool
}

func (git *cleanupBoundaryLinkedReplacementGit) AddWorktree(ctx context.Context, source, path, branch string) error {
	if err := git.Git.AddWorktree(ctx, source, path, branch); err != nil {
		return err
	}
	if git.swapAfterAdd && filepath.Clean(path) == filepath.Clean(git.replacement.publicPath) {
		git.replacement.swapInto(git.t, path)
	}
	return nil
}

func (git *cleanupBoundaryLinkedReplacementGit) StatusIncludingIgnored(ctx context.Context, path string) (gitadapter.Status, error) {
	if !git.replacement.swapped && filepath.Base(path) == "owned" && strings.HasPrefix(filepath.Base(filepath.Dir(path)), ".wtree-worktree-rollback-") {
		gotCommon, gotErr := git.Git.CommonGitDir(ctx, path)
		wantCommon, wantErr := git.Git.CommonGitDir(ctx, git.replacement.sourcePath)
		if gotErr == nil && wantErr == nil && gotCommon == wantCommon {
			git.replacement.swapInto(git.t, path)
			git.replacement.swappedInQuarantine = true
		}
	}
	return git.Git.StatusIncludingIgnored(ctx, path)
}

func assertLinkedWorktreeReplacementPreserved(t *testing.T, replacement *linkedWorktreeReplacement) {
	t.Helper()
	adapter := gitadapter.NewAdapter("git")
	for _, checkout := range []struct {
		path, gitDir, head string
		gitFile            []byte
	}{
		{path: replacement.publicPath, gitDir: replacement.alternateGitDir, head: replacement.alternateHead, gitFile: replacement.alternateGitFile},
		{path: replacement.displacedPath, gitDir: replacement.originalGitDir, head: replacement.originalHead, gitFile: replacement.originalGitFile},
	} {
		gotGitFile, err := os.ReadFile(filepath.Join(checkout.path, ".git"))
		if err != nil || !bytes.Equal(gotGitFile, checkout.gitFile) {
			t.Fatalf("checkout %q .git bytes = %q, %v, want %q", checkout.path, gotGitFile, err, checkout.gitFile)
		}
		gotGitDir, err := adapter.GitDir(context.Background(), checkout.path)
		if err != nil || gotGitDir != checkout.gitDir {
			t.Fatalf("checkout %q GitDir = %q, %v, want %q", checkout.path, gotGitDir, err, checkout.gitDir)
		}
		gotHead, err := adapter.Head(context.Background(), checkout.path)
		if err != nil || gotHead != checkout.head {
			t.Fatalf("checkout %q HEAD = %q, %v, want %q", checkout.path, gotHead, err, checkout.head)
		}
	}
	branch, detached, err := adapter.CurrentBranch(context.Background(), replacement.publicPath)
	if err != nil || detached != replacement.detached || (!replacement.detached && branch != replacement.alternateBranch) {
		t.Fatalf("public replacement branch = %q detached:%t error:%v", branch, detached, err)
	}
	worktrees, err := adapter.ListWorktrees(context.Background(), replacement.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{replacement.publicPath, replacement.displacedPath} {
		canonical, canonicalErr := filepath.EvalSymlinks(path)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		found := false
		for _, worktree := range worktrees {
			found = found || filepath.Clean(worktree.Path) == filepath.Clean(canonical)
		}
		if !found {
			t.Fatalf("registered worktrees = %#v, want %q", worktrees, path)
		}
	}
}

func assertFailedStepCleanupEvents(t *testing.T, events []transaction.Event, step string, rollbackResult transaction.EventKind) {
	t.Helper()
	want := []transaction.EventKind{transaction.ExecuteStarted, transaction.ExecuteFailed, transaction.RollbackStarted, rollbackResult}
	var got []transaction.EventKind
	for _, event := range events {
		if event.Step == step {
			got = append(got, event.Kind)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("events for %s = %v, want %v", step, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("events for %s = %v, want %v", step, got, want)
		}
	}
}

func TestWorkspaceCreatorForestRejectsCallbackSymlinkBeforeGroupingMutation(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	marker := filepath.Join(external, "marker")
	if err := os.WriteFile(marker, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	_, err = NewWorkspaceCreator().Create(context.Background(), resolution.Project, WorkspacePlanRequest{WorkspaceName: "feature/symlink", TargetPath: target, DataDir: data}, func(event transaction.Event) {
		if event.Kind == transaction.ExecuteStarted && event.Step == "prepare_grouping:api" {
			if err := os.Symlink(external, target); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err == nil || !strings.Contains(err.Error(), "without symlinks") {
		t.Fatalf("callback symlink error = %v", err)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "untouched\n" {
		t.Fatalf("external marker = %q, %v", contents, err)
	}
	if _, err := os.Lstat(filepath.Join(external, "services")); !os.IsNotExist(err) {
		t.Fatalf("grouping touched external target: %v", err)
	}
	if _, err := os.Lstat(WorkspaceStatePath(data, resolution.Project.ID, "feature-symlink")); !os.IsNotExist(err) {
		t.Fatalf("state exists after callback rejection: %v", err)
	}
}

func TestWorkspaceCreatorForestRejectsGroupingReplacementAfterMkdir(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	target := filepath.Join(t.TempDir(), "workspace")
	request := WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "feature/replace", TargetPath: target, DataDir: data, Mounts: []MountOverride{{RepositoryID: "api", Mount: "services/api"}}}
	value, err := NewWorkspacePlanner().Plan(context.Background(), resolution.Project, request)
	if err != nil {
		t.Fatal(err)
	}
	creator := NewWorkspaceCreator()
	filesystem := creator.filesystem
	filesystem.mkdir = func(path string, mode os.FileMode) error {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		if path == filepath.Join(target, "services") {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink(external, path)
		}
		return nil
	}
	creator.filesystem = filesystem
	_, err = creator.Create(context.Background(), resolution.Project, request, nil)
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || HasCleanRollback(err) {
		t.Fatalf("grouping replacement error = %v", err)
	}
	record, readErr := store.ReadRecovery(RecoveryRecordPath(data, value))
	if readErr != nil || record.FailedStep != "prepare_grouping:api" || len(record.UnrevertedSteps) != 1 || record.UnrevertedSteps[0] != "prepare_grouping:api" {
		t.Fatalf("recovery = %#v, %v", record, readErr)
	}
	if destination, linkErr := os.Readlink(filepath.Join(target, "services")); linkErr != nil || destination != external {
		t.Fatalf("replacement symlink = %q, %v", destination, linkErr)
	}
	if _, err := os.Lstat(filepath.Join(external, "api")); !os.IsNotExist(err) {
		t.Fatalf("replacement reached external path: %v", err)
	}
	if _, err := os.Lstat(WorkspaceStatePath(data, resolution.Project.ID, "feature-replace")); !os.IsNotExist(err) {
		t.Fatalf("state exists after grouping replacement: %v", err)
	}
}

func TestWorkspaceCreatorForestUsesTopLevelAndChildMountOverridesWithSpaces(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "workspace with spaces")
	value, err := NewWorkspaceCreator().Create(context.Background(), resolution.Project, WorkspacePlanRequest{
		WorkspaceName: "feature/overrides",
		TargetPath:    target,
		DataDir:       data,
		Mounts: []MountOverride{
			{RepositoryID: "api", Mount: "services/api"},
			{RepositoryID: "web", Mount: "web space"},
			{RepositoryID: "alpha", Mount: "components/alpha"},
			{RepositoryID: "beta", Mount: "deep/beta"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(value.Repositories))
	for index, repository := range value.Repositories {
		ids[index] = repository.ID
	}
	if got, want := strings.Join(ids, ","), "api,web,alpha,beta,gamma"; got != want {
		t.Fatalf("parent-first repositories = %q, want %q", got, want)
	}
	paths := map[string]string{
		"api":   filepath.Join(target, "services", "api"),
		"web":   filepath.Join(target, "web space"),
		"alpha": filepath.Join(target, "services", "api", "components", "alpha"),
		"beta":  filepath.Join(target, "services", "api", "components", "alpha", "deep", "beta"),
		"gamma": filepath.Join(target, "services", "api", "components", "alpha", "deep", "beta", "gamma"),
	}
	for id, path := range paths {
		if got := repositoryPath(value, id); got != path {
			t.Fatalf("%s path = %q, want %q", id, got, path)
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("%s checkout: %v", id, err)
		}
	}
}

func TestWorkspaceCreatorForestPreflightsExistingLogicalRootSymlinkWithoutMutation(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	marker := filepath.Join(external, "marker")
	if err := os.WriteFile(marker, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(external, target); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	_, err = NewWorkspaceCreator().Create(context.Background(), resolution.Project, WorkspacePlanRequest{WorkspaceName: "feature/preflight", TargetPath: target, DataDir: data}, nil)
	if err == nil || !strings.Contains(err.Error(), "existing symlink") {
		t.Fatalf("preflight error = %v", err)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "unchanged\n" {
		t.Fatalf("external marker = %q, %v", contents, err)
	}
	if _, err := os.Lstat(filepath.Join(external, "api")); !os.IsNotExist(err) {
		t.Fatalf("preflight touched external root: %v", err)
	}
	if _, err := os.Lstat(WorkspaceStatePath(data, resolution.Project.ID, "feature-preflight")); !os.IsNotExist(err) {
		t.Fatalf("preflight wrote workspace state: %v", err)
	}
}

// The domain EffectivePaths table owns mount grammar, escape, overlap, reserved
// Git-path, and case/canonical-alias rules. These integration rows prove the
// create boundary returns those failures before branch/worktree/state effects.
func TestWorkspaceCreatorForestPreflightRejectsBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, target string)
		mounts  []MountOverride
	}{
		{name: "occupied-logical-root-file", prepare: func(t *testing.T, target string) {
			if err := os.WriteFile(target, []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "logical-root-symlink", prepare: func(t *testing.T, target string) {
			if err := os.Symlink(t.TempDir(), target); err != nil {
				t.Skipf("symlink fixture unavailable: %v", err)
			}
		}},
		{name: "invalid-child-override", mounts: []MountOverride{{RepositoryID: "alpha", Mount: "."}}},
		{name: "reserved-top-level-override", mounts: []MountOverride{{RepositoryID: "api", Mount: ".git"}}},
		{name: "undeclared-top-level-overlap", mounts: []MountOverride{{RepositoryID: "api", Mount: "api"}, {RepositoryID: "web", Mount: "api/web"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, data := forestWorkspaceProject(t)
			target := filepath.Join(t.TempDir(), "workspace")
			if test.prepare != nil {
				test.prepare(t, target)
			}
			request := WorkspacePlanRequest{WorkspaceName: "feature/preflight-table", TargetPath: target, DataDir: data, Mounts: test.mounts}
			_, err := NewWorkspaceCreator().Create(context.Background(), project, request, nil)
			if err == nil {
				t.Fatal("Create succeeded, want preflight rejection")
			}
			if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, "feature-preflight-table")); !os.IsNotExist(statErr) {
				t.Fatalf("state changed: %v", statErr)
			}
			for _, repository := range project.Repositories {
				exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.SourcePath, "feature/preflight-table")
				if branchErr != nil || exists {
					t.Fatalf("branch preflight effect for %s: exists=%v err=%v", repository.ID, exists, branchErr)
				}
			}
		})
	}
}

func TestWorkspaceCreatorForestPreflightFailureOmitsTopologyResultFacts(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	result, err := NewWorkspaceCreator().CreateWithResult(context.Background(), project, WorkspacePlanRequest{
		WorkspaceName: "feature/preflight-result", TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data,
		Mounts: []MountOverride{{RepositoryID: "alpha", Mount: "."}},
	}, nil)
	if err == nil {
		t.Fatal("CreateWithResult succeeded, want validation failure")
	}
	if result.LogicalRoot != "" || result.BaseRepository != "" || result.Plan.Version != 0 || len(result.Plan.Repositories) != 0 {
		t.Fatalf("preflight result exposes unvalidated topology: %#v", result)
	}
}

func TestWorkspaceCreatorForestGroupingFailuresRollBackOwnedDirectories(t *testing.T) {
	for _, failAt := range []int{1, 3, 6} {
		t.Run(fmt.Sprintf("mkdir-%d", failAt), func(t *testing.T) {
			project, data := forestWorkspaceProject(t)
			target := filepath.Join(t.TempDir(), "workspace")
			creator := NewWorkspaceCreator()
			filesystem := creator.filesystem
			calls := 0
			filesystem.mkdir = func(path string, mode os.FileMode) error {
				calls++
				if calls == failAt {
					return errors.New("injected grouping mkdir failure")
				}
				return os.Mkdir(path, mode)
			}
			creator.filesystem = filesystem
			_, err := creator.Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/grouping"), nil)
			if err == nil || !strings.Contains(err.Error(), "injected grouping mkdir failure") {
				t.Fatalf("grouping failure = %v", err)
			}
			assertForestWorkspaceAbsent(t, target, data, project.ID, "feature-grouping")
		})
	}
}

func TestWorkspaceCreatorForestPartialGroupingCleanupFailureWritesRecovery(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	target := filepath.Join(t.TempDir(), "workspace")
	request := forestWorkspaceRequest(target, data, "feature/partial-recovery")
	request.Operation = plan.Create
	value, planErr := NewWorkspacePlanner().Plan(context.Background(), project, request)
	if planErr != nil {
		t.Fatal(planErr)
	}
	creator := NewWorkspaceCreator()
	filesystem := creator.filesystem
	calls := 0
	filesystem.mkdir = func(path string, mode os.FileMode) error {
		calls++
		if calls == 2 {
			return errors.New("later grouping mkdir")
		}
		return os.Mkdir(path, mode)
	}
	filesystem.remove = func(path string) error {
		if path == target {
			return errors.New("retain receipt")
		}
		return os.Remove(path)
	}
	creator.filesystem = filesystem
	_, err := creator.Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/partial-recovery"), nil)
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || HasCleanRollback(err) {
		t.Fatalf("error = %v", err)
	}
	record, readErr := store.ReadRecovery(RecoveryRecordPath(data, value))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if record.FailedStep != "prepare_grouping:api" || len(record.UnrevertedSteps) != 1 || record.UnrevertedSteps[0] != "prepare_grouping:api" {
		t.Fatalf("record = %#v", record)
	}
	if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, value.WorkspaceID)); !os.IsNotExist(statErr) {
		t.Fatalf("state = %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(target, "services")); !os.IsNotExist(statErr) {
		t.Fatalf("child receipt = %v", statErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("retained receipt = %v", statErr)
	}
}

func TestWorkspaceCreatorForestReplacementBeforeAddWorktreeWritesRecovery(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	target := filepath.Join(t.TempDir(), "workspace")
	request := forestWorkspaceRequest(target, data, "feature/replacement-recovery")
	request.Operation = plan.Create
	value, err := NewWorkspacePlanner().Plan(context.Background(), project, request)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(target, "services")
	_, err = NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/replacement-recovery"), func(event transaction.Event) {
		if event.Kind == transaction.ExecuteSucceeded && event.Step == "prepare_grouping:api" {
			if err := os.Remove(replacement); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(replacement, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || HasCleanRollback(err) {
		t.Fatalf("error = %v", err)
	}
	record, readErr := store.ReadRecovery(RecoveryRecordPath(data, value))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if record.FailedStep != "add_worktree:api" || len(record.UnrevertedSteps) != 1 || record.UnrevertedSteps[0] != "prepare_grouping:api" {
		t.Fatalf("record = %#v", record)
	}
	if _, statErr := os.Stat(replacement); statErr != nil {
		t.Fatalf("replacement removed: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(replacement, "api")); !os.IsNotExist(statErr) {
		t.Fatalf("Git touched replacement: %v", statErr)
	}
	if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, value.WorkspaceID)); !os.IsNotExist(statErr) {
		t.Fatalf("state = %v", statErr)
	}
}

func TestWorkspaceCreatorForestRejectsReplacedParentBeforeChildGrouping(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	target := filepath.Join(t.TempDir(), "workspace")
	request := forestWorkspaceRequest(target, data, "feature/parent-replaced")
	_, err := NewWorkspaceCreator().Create(context.Background(), project, request, func(event transaction.Event) {
		if event.Kind == transaction.ExecuteSucceeded && event.Step == "add_worktree:api" {
			api := filepath.Join(target, "services", "api")
			if err := os.Rename(api, api+"-owned"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(api, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(api, "marker"), []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("error = %v", err)
	}
	api := filepath.Join(target, "services", "api")
	if marker, readErr := os.ReadFile(filepath.Join(api, "marker")); readErr != nil || string(marker) != "replacement" {
		t.Fatalf("marker = %q, %v", marker, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(api, "components", "alpha", ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("child Git mutation: %v", statErr)
	}
	if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, "feature-parent-replaced")); !os.IsNotExist(statErr) {
		t.Fatalf("state = %v", statErr)
	}
}

func TestWorkspaceCreatorForestRejectsReplacedSharedGroupingBeforeLaterTopLevel(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	target := filepath.Join(t.TempDir(), "workspace")
	request := forestWorkspaceRequest(target, data, "feature/shared-replaced")
	request.Mounts[1] = MountOverride{RepositoryID: "web", Mount: "services/web"}
	_, err := NewWorkspaceCreator().Create(context.Background(), project, request, func(event transaction.Event) {
		if event.Kind == transaction.ExecuteStarted && event.Step == "prepare_grouping:web" {
			services := filepath.Join(target, "services")
			if err := os.Rename(services, services+"-owned"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(services, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(services, "marker"), []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("error = %v", err)
	}
	services := filepath.Join(target, "services")
	if marker, readErr := os.ReadFile(filepath.Join(services, "marker")); readErr != nil || string(marker) != "replacement" {
		t.Fatalf("marker = %q, %v", marker, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(services, "web", ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("web Git mutation: %v", statErr)
	}
	if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, "feature-shared-replaced")); !os.IsNotExist(statErr) {
		t.Fatalf("state = %v", statErr)
	}
}

func TestWorkspaceCreatorForestRejectsReplacedParentBeforeDirectChildAddWorktree(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	target := filepath.Join(t.TempDir(), "workspace")
	_, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/direct-parent", TargetPath: target, DataDir: data}, func(event transaction.Event) {
		if event.Kind == transaction.ExecuteStarted && event.Step == "add_worktree:alpha" {
			api := filepath.Join(target, "api")
			if err := os.Rename(api, api+"-owned"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(api, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(api, "marker"), []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("error = %v", err)
	}
	api := filepath.Join(target, "api")
	if marker, readErr := os.ReadFile(filepath.Join(api, "marker")); readErr != nil || string(marker) != "replacement" {
		t.Fatalf("marker = %q, %v", marker, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(api, "alpha", ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("child Git mutation: %v", statErr)
	}
	if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, "feature-direct-parent")); !os.IsNotExist(statErr) {
		t.Fatalf("state = %v", statErr)
	}
}

func TestWorkspaceCreatorForestGitAndIgnoreFailuresRollBackEveryTree(t *testing.T) {
	tests := []struct {
		name       string
		createID   string
		addID      string
		ignoreCall int
	}{
		{name: "branch-base", createID: "api"},
		{name: "branch-sibling", createID: "web"},
		{name: "branch-deep", createID: "gamma"},
		{name: "add-base", addID: "api"},
		{name: "add-sibling", addID: "web"},
		{name: "add-deep", addID: "gamma"},
		{name: "ignore-first", ignoreCall: 1},
		{name: "ignore-middle", ignoreCall: 2},
		{name: "ignore-last", ignoreCall: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parallelM07RealGitTest(t)
			project, data := forestWorkspaceProject(t)
			target := filepath.Join(t.TempDir(), "workspace")
			git := &forestFailureGit{Git: gitadapter.NewAdapter("git"), sources: forestSourceIDs(project), createID: test.createID, addID: test.addID, ignoreCall: test.ignoreCall}
			creator := NewWorkspaceCreatorWith(git, NewWorkspaceTransaction())
			_, err := creator.Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/failure"), nil)
			if err == nil || !strings.Contains(err.Error(), "injected forest failure") {
				t.Fatalf("forest %s failure = %v", test.name, err)
			}
			assertForestWorkspaceAbsent(t, target, data, project.ID, "feature-failure")
		})
	}
}

func TestWorkspaceCreatorForestStateFailureAndCancellationCleanUp(t *testing.T) {
	t.Run("state", func(t *testing.T) {
		project, data := forestWorkspaceProject(t)
		target := filepath.Join(t.TempDir(), "workspace")
		transaction := NewWorkspaceTransactionWith(lock.Manager{}, func(string, store.WorkspaceState) error {
			return errors.New("injected workspace state failure")
		}, store.WriteRecovery, os.Remove)
		_, err := NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), transaction).Create(context.Background(), project, forestWorkspaceRequest(target, data, "feature/state"), nil)
		if err == nil || !strings.Contains(err.Error(), "injected workspace state failure") {
			t.Fatalf("state failure = %v", err)
		}
		assertForestWorkspaceAbsent(t, target, data, project.ID, "feature-state")
	})
	t.Run("cancel-before-later-tree", func(t *testing.T) {
		project, data := forestWorkspaceProject(t)
		target := filepath.Join(t.TempDir(), "workspace")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := NewWorkspaceCreator().Create(ctx, project, forestWorkspaceRequest(target, data, "feature/cancel-later"), func(event transaction.Event) {
			if event.Kind == transaction.ExecuteSucceeded && event.Step == "add_worktree:web" {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
		assertForestWorkspaceAbsent(t, target, data, project.ID, "feature-cancel-later")
	})
}

func TestWorkspaceCreatorForestGroupingRollbackFailureRecordsOnlyOwnedForestStep(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	target := filepath.Join(t.TempDir(), "workspace")
	request := forestWorkspaceRequest(target, data, "feature/recovery")
	request.Operation = plan.Create
	value, err := NewWorkspacePlanner().Plan(context.Background(), project, request)
	if err != nil {
		t.Fatal(err)
	}
	git := &forestFailureGit{Git: gitadapter.NewAdapter("git"), sources: forestSourceIDs(project), addID: "api"}
	creator := NewWorkspaceCreatorWith(git, NewWorkspaceTransaction())
	filesystem := creator.filesystem
	filesystem.remove = func(path string) error {
		if path == target {
			return errors.New("injected owned-root removal failure")
		}
		return os.Remove(path)
	}
	creator.filesystem = filesystem
	_, err = creator.Create(context.Background(), project, request, nil)
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("error = %v, want rollback incomplete", err)
	}
	record, readErr := store.ReadRecovery(RecoveryRecordPath(data, value))
	if readErr != nil {
		t.Fatalf("read forest recovery: %v", readErr)
	}
	if record.FailedStep != "add_worktree:api" || len(record.UnrevertedSteps) != 1 || record.UnrevertedSteps[0] != "prepare_grouping:api" {
		t.Fatalf("recovery record = %#v", record)
	}
	if _, statErr := os.Lstat(target); statErr != nil {
		t.Fatalf("owned root retained for recovery: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(target, "services")); !os.IsNotExist(statErr) {
		t.Fatalf("child-first grouping cleanup left services: %v", statErr)
	}
	if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, value.WorkspaceID)); !os.IsNotExist(statErr) {
		t.Fatalf("state published despite failed forest: %v", statErr)
	}
}

func forestWorkspaceProject(t *testing.T) (domain.Project, string) {
	t.Helper()
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: fixture.paths["api"], ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	return resolution.Project, data
}

func rootWorkspaceProject(t *testing.T) (domain.Project, string) {
	t.Helper()
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: repository.Path, ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	return resolution.Project, data
}

func createWorkspaceBranches(t *testing.T, project domain.Project, branch string) {
	t.Helper()
	adapter := gitadapter.NewAdapter("git")
	for _, repository := range project.Repositories {
		head, err := adapter.Head(context.Background(), repository.SourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.CreateBranch(context.Background(), repository.SourcePath, branch, head); err != nil {
			t.Fatal(err)
		}
	}
}

func forestWorkspaceRequest(target, data, name string) WorkspacePlanRequest {
	return WorkspacePlanRequest{WorkspaceName: name, TargetPath: target, DataDir: data, Mounts: []MountOverride{
		{RepositoryID: "api", Mount: "services/api"}, {RepositoryID: "web", Mount: "grouped/web"}, {RepositoryID: "alpha", Mount: "components/alpha"}, {RepositoryID: "beta", Mount: "deep/beta"}, {RepositoryID: "gamma", Mount: "tools/gamma"},
	}}
}

func assertForestWorkspaceAbsent(t *testing.T, target, data, projectID, workspaceID string) {
	t.Helper()
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("logical root remains: %v", err)
	}
	if _, err := os.Lstat(WorkspaceStatePath(data, projectID, strings.ReplaceAll(workspaceID, "/", "-"))); !os.IsNotExist(err) {
		t.Fatalf("workspace state remains: %v", err)
	}
}

type forestFailureGit struct {
	gitadapter.Git
	sources                        map[string]string
	createID, addID                string
	ignoreCall, observedIgnoreCall int
}

func forestSourceIDs(project domain.Project) map[string]string {
	result := make(map[string]string, len(project.Repositories))
	for _, repository := range project.Repositories {
		result[repository.SourcePath] = repository.ID
	}
	return result
}

func (git *forestFailureGit) CreateBranch(ctx context.Context, repository, branch, base string) error {
	if git.sources[repository] == git.createID {
		return errors.New("injected forest failure")
	}
	return git.Git.CreateBranch(ctx, repository, branch, base)
}

func (git *forestFailureGit) AddWorktree(ctx context.Context, repository, path, branch string) error {
	if git.sources[repository] == git.addID {
		return errors.New("injected forest failure")
	}
	return git.Git.AddWorktree(ctx, repository, path, branch)
}

func (git *forestFailureGit) InspectWorkingTreeIgnore(ctx context.Context, repository, mount string) (gitadapter.WorkingTreeIgnoreEvidence, error) {
	git.observedIgnoreCall++
	if git.ignoreCall == git.observedIgnoreCall {
		return gitadapter.WorkingTreeIgnoreEvidence{}, errors.New("injected forest failure")
	}
	return git.Git.InspectWorkingTreeIgnore(ctx, repository, mount)
}
