package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/marcel/wtree/internal/domain"
	gitadapter "github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/lock"
	"github.com/marcel/wtree/internal/store"
	"github.com/marcel/wtree/internal/transaction"
)

// DeletionPlan extends remove preflight with the exact branches that will be
// deleted after every worktree is gone. Repositories remain child-first.
type DeletionPlan struct {
	RemovalPlan
	Branches []DeletionBranch `json:"branches"`
}

type DeletionBranch struct {
	RepositoryID string `json:"repositoryId"`
	Branch       string `json:"branch"`
	Merged       bool   `json:"merged"`
	ForceBranch  bool   `json:"forceBranch,omitempty"`
}

// WorkspaceDeleter owns the destructive branch/state boundary. Its recovery
// metadata is intentionally retained when an irreversible effect completed.
type WorkspaceDeleter struct {
	git           gitadapter.Git
	locker        ProjectLocker
	writeRecovery func(string, store.RecoveryRecord) error
	removeState   func(string) error
	writeRaw      func(string, []byte) error
	readFile      func(string) ([]byte, error)
	lockTimeout   time.Duration
}

func NewWorkspaceDeleter() *WorkspaceDeleter {
	return NewWorkspaceDeleterWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteRecovery, os.Remove, store.WriteRawAtomic, os.ReadFile)
}

func NewWorkspaceDeleterWith(git gitadapter.Git, locker ProjectLocker, writeRecovery func(string, store.RecoveryRecord) error, removeState func(string) error, writeRaw func(string, []byte) error, readFile func(string) ([]byte, error)) *WorkspaceDeleter {
	return &WorkspaceDeleter{git: git, locker: locker, writeRecovery: writeRecovery, removeState: removeState, writeRaw: writeRaw, readFile: readFile, lockTimeout: time.Second}
}

func (d *WorkspaceDeleter) PlanDelete(ctx context.Context, project domain.Project, workspace domain.Workspace, force bool) (DeletionPlan, error) {
	if d == nil || d.git == nil {
		return DeletionPlan{}, NewError(ErrorInternal, errors.New("workspace deleter is not configured"))
	}
	removal, err := NewWorkspaceRemoverWith(d.git, d.locker, d.writeRecovery).PlanRemove(ctx, project, workspace, force)
	if err != nil {
		return DeletionPlan{}, err
	}
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	value := DeletionPlan{RemovalPlan: removal, Branches: make([]DeletionBranch, 0, len(removal.Repositories))}
	for _, item := range removal.Repositories {
		repository := repositories[item.ID]
		exists, err := d.git.BranchExists(ctx, repository.SourcePath, item.Branch)
		if err != nil {
			return DeletionPlan{}, NewError(ErrorGit, fmt.Errorf("check branch %q for repository %q: %w", item.Branch, item.ID, err))
		}
		if !exists {
			return DeletionPlan{}, NewError(ErrorValidation, fmt.Errorf("retained branch %q for repository %q is missing", item.Branch, item.ID))
		}
		worktrees, err := d.git.ListWorktrees(ctx, repository.SourcePath)
		if err != nil {
			return DeletionPlan{}, NewError(ErrorGit, fmt.Errorf("list branch worktrees for repository %q: %w", item.ID, err))
		}
		for _, worktree := range worktrees {
			if worktree.Branch == item.Branch && !sameCheckoutPath(worktree.Path, item.Path) {
				return DeletionPlan{}, NewError(ErrorConflict, fmt.Errorf("branch %q for repository %q is checked out at %q", item.Branch, item.ID, worktree.Path))
			}
		}
		merged, err := d.git.BranchMerged(ctx, repository.SourcePath, item.Branch)
		if err != nil {
			return DeletionPlan{}, NewError(ErrorGit, fmt.Errorf("check whether branch %q for repository %q is merged: %w", item.Branch, item.ID, err))
		}
		if !merged && !force {
			return DeletionPlan{}, NewError(ErrorConflict, fmt.Errorf("branch %q for repository %q is not merged; use --force to delete this unmerged branch", item.Branch, item.ID))
		}
		if !merged {
			value.Overrides = append(value.Overrides, RemovalOverride{RepositoryID: item.ID, Reason: "unmerged branch"})
		}
		value.Branches = append(value.Branches, DeletionBranch{RepositoryID: item.ID, Branch: item.Branch, Merged: merged, ForceBranch: !merged && force})
	}
	return value, nil
}

func (d *WorkspaceDeleter) Delete(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir string, force bool, progress func(transaction.Event)) (DeletionPlan, error) {
	value, err := d.PlanDelete(ctx, project, workspace, force)
	if err != nil {
		return DeletionPlan{}, err
	}
	if dataDir == "" {
		return DeletionPlan{}, NewError(ErrorValidation, errors.New("data directory is required"))
	}
	if d.locker == nil || d.writeRecovery == nil || d.removeState == nil || d.writeRaw == nil || d.readFile == nil {
		return DeletionPlan{}, NewError(ErrorInternal, errors.New("workspace deleter is not configured"))
	}
	handle, err := d.locker.ProjectLock(ctx, dataDir, project.ID, d.lockTimeout)
	if err != nil {
		return DeletionPlan{}, NewError(ErrorConflict, fmt.Errorf("acquire project mutation lock: %w", err))
	}
	defer handle.Unlock()
	revalidated, err := d.PlanDelete(ctx, project, workspace, force)
	if err != nil {
		return DeletionPlan{}, err
	}
	if !reflect.DeepEqual(value, revalidated) {
		return DeletionPlan{}, NewError(ErrorConflict, errors.New("workspace deletion plan changed during locked revalidation"))
	}
	recoveryPath := removalRecoveryPath(dataDir, value.RemovalPlan)
	if _, err := os.Stat(recoveryPath); err == nil {
		return DeletionPlan{}, NewError(ErrorConflict, fmt.Errorf("workspace %q has unresolved recovery metadata at %q", workspace.Name, recoveryPath))
	} else if !os.IsNotExist(err) {
		return DeletionPlan{}, NewError(ErrorInternal, fmt.Errorf("inspect deletion recovery metadata: %w", err))
	}
	statePath := WorkspaceStatePath(dataDir, project.ID, workspace.ID)
	state, err := d.readFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return DeletionPlan{}, NewError(ErrorWorkspaceNotFound, fmt.Errorf("workspace state %q was not found", workspace.Name))
	}
	if err != nil {
		return DeletionPlan{}, NewError(ErrorInternal, fmt.Errorf("read workspace state before delete: %w", err))
	}
	steps := d.deletionSteps(project, value, statePath, state)
	result := (transaction.Runner{Progress: progress}).Run(ctx, steps)
	if result.Succeeded() {
		return value, nil
	}
	if result.RollbackFailure == nil {
		err := classifyTransactionError("delete workspace", result.Failure)
		if len(result.Completed) > 0 {
			err = withCleanRollback(err)
		}
		return DeletionPlan{}, err
	}
	record := store.RecoveryRecord{ProjectID: value.ProjectID, WorkspaceID: value.WorkspaceID, Operation: "delete", FailedStep: result.FailedStep, CompletedSteps: result.Completed, UnrevertedSteps: result.UnrevertedSteps, RollbackFailures: recoveryRollbackFailures(result.RollbackFailures)}
	if err := d.writeRecovery(recoveryPath, record); err != nil {
		return DeletionPlan{}, NewError(ErrorRollbackIncomplete, fmt.Errorf("workspace deletion rollback is incomplete after %q: %w; also write recovery metadata: %v", result.FailedStep, result.RollbackFailure, err))
	}
	return DeletionPlan{}, NewError(ErrorRollbackIncomplete, fmt.Errorf("workspace deletion rollback is incomplete after %q; recovery metadata: %q: %w", result.FailedStep, recoveryPath, result.RollbackFailure))
}

func (d *WorkspaceDeleter) deletionSteps(project domain.Project, value DeletionPlan, statePath string, state []byte) []transaction.Step {
	remover := NewWorkspaceRemoverWith(d.git, d.locker, d.writeRecovery)
	steps := remover.removalSteps(project, value.RemovalPlan)
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	for _, branch := range value.Branches {
		repository := repositories[branch.RepositoryID]
		branch := branch
		steps = append(steps, transaction.Step{Name: "delete_branch:" + branch.RepositoryID, Execute: func(ctx context.Context) error {
			if err := d.git.DeleteBranch(ctx, repository.SourcePath, branch.Branch, branch.ForceBranch); err != nil {
				return NewError(ErrorGit, fmt.Errorf("delete branch %q for repository %q: %w", branch.Branch, branch.RepositoryID, err))
			}
			return nil
		}})
	}
	steps = append(steps, transaction.Step{Name: "delete_state", Execute: func(context.Context) error {
		if err := d.removeState(statePath); err != nil {
			return NewError(ErrorInternal, fmt.Errorf("delete workspace state: %w", err))
		}
		return nil
	}, Rollback: func(context.Context) error {
		if err := d.writeRaw(statePath, state); err != nil {
			return NewError(ErrorInternal, fmt.Errorf("restore workspace state: %w", err))
		}
		return nil
	}})
	return steps
}

// deletionRecoveryPath is retained for callers/tests that need the common
// operation recovery location without reconstructing it from filesystem paths.
func deletionRecoveryPath(dataDir string, value DeletionPlan) string {
	return filepath.Join(dataDir, "projects", value.ProjectID, "recovery", value.WorkspaceID+".json")
}
