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

// RemovalPlan is the immutable, read-only result of remove preflight. Its
// repository order is the execution order: descendants before parents.
type RemovalPlan struct {
	ProjectID     string              `json:"projectId"`
	WorkspaceID   string              `json:"workspaceId"`
	WorkspaceName string              `json:"workspaceName"`
	RootPath      string              `json:"rootPath"`
	Force         bool                `json:"force"`
	Repositories  []RemovalRepository `json:"repositories"`
	Overrides     []RemovalOverride   `json:"overrides,omitempty"`
}

type RemovalRepository struct {
	ID            string `json:"id"`
	Branch        string `json:"branch"`
	Mount         string `json:"mount"`
	Path          string `json:"path"`
	ForceWorktree bool   `json:"forceWorktree,omitempty"`
}

// RemovalOverride records exactly which dirty-worktree protections --force
// permitted. It never represents an identity, registration, or state bypass.
type RemovalOverride struct {
	RepositoryID string `json:"repositoryId"`
	Reason       string `json:"reason"`
}

// WorkspaceRemover removes only Git worktrees. The authoritative workspace
// state and all branches remain so checkout can restore the same mounts.
type WorkspaceRemover struct {
	git           gitadapter.Git
	locker        ProjectLocker
	writeRecovery func(string, store.RecoveryRecord) error
	lockTimeout   time.Duration
}

func NewWorkspaceRemover() *WorkspaceRemover {
	return NewWorkspaceRemoverWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteRecovery)
}

func NewWorkspaceRemoverWith(git gitadapter.Git, locker ProjectLocker, writeRecovery func(string, store.RecoveryRecord) error) *WorkspaceRemover {
	return &WorkspaceRemover{git: git, locker: locker, writeRecovery: writeRecovery, lockTimeout: time.Second}
}

// PlanRemove verifies every persisted checkout before any removal. Missing or
// stale registrations are deliberately errors: remove never prunes or repairs
// worktrees outside this exact workspace mapping.
func (r *WorkspaceRemover) PlanRemove(ctx context.Context, project domain.Project, workspace domain.Workspace, force bool) (RemovalPlan, error) {
	if r == nil || r.git == nil {
		return RemovalPlan{}, NewError(ErrorInternal, errors.New("workspace remover is not configured"))
	}
	if err := project.Validate(); err != nil {
		return RemovalPlan{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	if err := workspace.Validate(project); err != nil {
		return RemovalPlan{}, NewError(ErrorValidation, fmt.Errorf("validate workspace state: %w", err))
	}
	if workspace.Partial {
		return RemovalPlan{}, NewError(ErrorValidation, fmt.Errorf("workspace %q is partial; repair its repository mapping before removal", workspace.Name))
	}
	checkouts := make(map[string]domain.Checkout, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	value := RemovalPlan{ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, RootPath: workspace.RootPath, Force: force, Repositories: make([]RemovalRepository, 0, len(project.Repositories))}
	ordered := project.ParentFirst()
	for index := len(ordered) - 1; index >= 0; index-- {
		repository := ordered[index]
		checkout := checkouts[repository.ID]
		if checkout.Detached {
			return RemovalPlan{}, NewError(ErrorValidation, fmt.Errorf("repository %q has a detached checkout that cannot be rolled back safely", repository.ID))
		}
		childPaths := managedChildPaths(project, workspace, repository.ID)
		overrides, err := r.preflightRemovalRepository(ctx, repository, checkout, childPaths, force)
		if err != nil {
			return RemovalPlan{}, err
		}
		value.Repositories = append(value.Repositories, RemovalRepository{ID: repository.ID, Branch: checkout.Branch, Mount: checkout.Mount, Path: checkout.ResolvedPath, ForceWorktree: len(overrides) != 0})
		value.Overrides = append(value.Overrides, overrides...)
	}
	return value, nil
}

func managedChildPaths(project domain.Project, workspace domain.Workspace, parentID string) []string {
	paths := make([]string, 0)
	for _, repository := range project.Repositories {
		if repository.ParentID != parentID {
			continue
		}
		if path, err := workspace.ResolveRepository(repository.ID); err == nil {
			paths = append(paths, path)
		}
	}
	return paths
}

func (r *WorkspaceRemover) preflightRemovalRepository(ctx context.Context, repository domain.Repository, checkout domain.Checkout, childPaths []string, force bool) ([]RemovalOverride, error) {
	if checkout.Branch == "" || checkout.ResolvedPath == "" {
		return nil, NewError(ErrorValidation, fmt.Errorf("repository %q has incomplete retained checkout state", repository.ID))
	}
	if _, err := os.Stat(checkout.ResolvedPath); err != nil {
		if os.IsNotExist(err) {
			return nil, NewError(ErrorValidation, fmt.Errorf("workspace checkout for repository %q at %q is missing; it may have been removed manually and still registered", repository.ID, checkout.ResolvedPath))
		}
		return nil, NewError(ErrorInternal, fmt.Errorf("inspect workspace checkout for repository %q: %w", repository.ID, err))
	}
	worktrees, err := r.git.ListWorktrees(ctx, repository.SourcePath)
	if err != nil {
		return nil, NewError(ErrorGit, fmt.Errorf("list worktrees for repository %q: %w", repository.ID, err))
	}
	registered := false
	for _, worktree := range worktrees {
		if sameCheckoutPath(worktree.Path, checkout.ResolvedPath) {
			registered = true
			break
		}
	}
	if !registered {
		return nil, NewError(ErrorValidation, fmt.Errorf("workspace checkout for repository %q at %q is not registered by Git", repository.ID, checkout.ResolvedPath))
	}
	identity, err := r.git.CommonGitDir(ctx, checkout.ResolvedPath)
	if err != nil {
		return nil, NewError(ErrorGit, fmt.Errorf("verify checkout identity for repository %q: %w", repository.ID, err))
	}
	if identity != repository.CommonGitDir {
		return nil, NewError(ErrorValidation, fmt.Errorf("workspace checkout for repository %q has unexpected Git identity", repository.ID))
	}
	topLevel, err := r.git.TopLevel(ctx, checkout.ResolvedPath)
	if err != nil || !sameCheckoutPath(topLevel, checkout.ResolvedPath) {
		return nil, NewError(ErrorValidation, fmt.Errorf("workspace checkout for repository %q is mounted at an unexpected path", repository.ID))
	}
	branch, detached, err := r.git.CurrentBranch(ctx, checkout.ResolvedPath)
	if err != nil {
		return nil, NewError(ErrorGit, fmt.Errorf("read checkout branch for repository %q: %w", repository.ID, err))
	}
	if detached || branch != checkout.Branch {
		return nil, NewError(ErrorValidation, fmt.Errorf("workspace checkout for repository %q does not match retained branch %q", repository.ID, checkout.Branch))
	}
	status, err := r.git.Status(ctx, checkout.ResolvedPath)
	if err != nil {
		return nil, NewError(ErrorGit, fmt.Errorf("read checkout status for repository %q: %w", repository.ID, err))
	}
	status = withoutManagedChildEntries(status, checkout.ResolvedPath, childPaths)
	if len(status.Entries) == 0 {
		return nil, nil
	}
	if !force {
		return nil, NewError(ErrorDirtyWorkspace, fmt.Errorf("workspace checkout for repository %q is dirty (staged=%t modified=%t untracked=%t); use --force to remove this dirty worktree", repository.ID, status.Staged, status.Modified, status.Untracked))
	}
	overrides := make([]RemovalOverride, 0, 3)
	if status.Staged {
		overrides = append(overrides, RemovalOverride{RepositoryID: repository.ID, Reason: "staged changes"})
	}
	if status.Modified {
		overrides = append(overrides, RemovalOverride{RepositoryID: repository.ID, Reason: "modified files"})
	}
	if status.Untracked {
		overrides = append(overrides, RemovalOverride{RepositoryID: repository.ID, Reason: "untracked files"})
	}
	return overrides, nil
}

// Remove revalidates after acquiring the project lock, removes descendants
// first, and leaves state untouched on success for later checkout restoration.
func (r *WorkspaceRemover) Remove(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir string, force bool, progress func(transaction.Event)) (RemovalPlan, error) {
	value, err := r.PlanRemove(ctx, project, workspace, force)
	if err != nil {
		return RemovalPlan{}, err
	}
	if dataDir == "" {
		return RemovalPlan{}, NewError(ErrorValidation, errors.New("data directory is required"))
	}
	if r.locker == nil || r.writeRecovery == nil {
		return RemovalPlan{}, NewError(ErrorInternal, errors.New("workspace remover is not configured"))
	}
	handle, err := r.locker.ProjectLock(ctx, dataDir, project.ID, r.lockTimeout)
	if err != nil {
		return RemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("acquire project mutation lock: %w", err))
	}
	defer handle.Unlock()
	revalidated, err := r.PlanRemove(ctx, project, workspace, force)
	if err != nil {
		return RemovalPlan{}, err
	}
	if !reflect.DeepEqual(value, revalidated) {
		return RemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("workspace removal plan changed during locked revalidation"))
	}
	recoveryPath := removalRecoveryPath(dataDir, value)
	if _, err := os.Stat(recoveryPath); err == nil {
		return RemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("workspace %q has unresolved recovery metadata at %q", workspace.Name, recoveryPath))
	} else if !os.IsNotExist(err) {
		return RemovalPlan{}, NewError(ErrorInternal, fmt.Errorf("inspect removal recovery metadata: %w", err))
	}
	steps := r.removalSteps(project, value)
	result := (transaction.Runner{Progress: progress}).Run(ctx, steps)
	if result.Succeeded() {
		return value, nil
	}
	if result.RollbackFailure == nil {
		err := classifyTransactionError("remove workspace", result.Failure)
		if len(result.Completed) > 0 {
			err = withCleanRollback(err)
		}
		return RemovalPlan{}, err
	}
	record := store.RecoveryRecord{ProjectID: value.ProjectID, WorkspaceID: value.WorkspaceID, Operation: "remove", FailedStep: result.FailedStep, CompletedSteps: result.Completed, UnrevertedSteps: result.UnrevertedSteps, RollbackFailures: recoveryRollbackFailures(result.RollbackFailures)}
	if err := r.writeRecovery(recoveryPath, record); err != nil {
		return RemovalPlan{}, NewError(ErrorRollbackIncomplete, fmt.Errorf("workspace removal rollback is incomplete after %q: %w; also write recovery metadata: %v", result.FailedStep, result.RollbackFailure, err))
	}
	return RemovalPlan{}, NewError(ErrorRollbackIncomplete, fmt.Errorf("workspace removal rollback is incomplete after %q; recovery metadata: %q: %w", result.FailedStep, recoveryPath, result.RollbackFailure))
}

func (r *WorkspaceRemover) removalSteps(project domain.Project, value RemovalPlan) []transaction.Step {
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	steps := make([]transaction.Step, 0, len(value.Repositories))
	for _, item := range value.Repositories {
		repository := repositories[item.ID]
		item := item
		step := transaction.Step{Name: "remove_worktree:" + item.ID,
			Execute: func(ctx context.Context) error {
				if err := r.git.RemoveWorktree(ctx, repository.SourcePath, item.Path, item.ForceWorktree); err != nil {
					return NewError(ErrorGit, fmt.Errorf("remove worktree for repository %q at %q: %w", item.ID, item.Path, err))
				}
				return nil
			},
		}
		if !item.ForceWorktree {
			step.Rollback = func(ctx context.Context) error {
				if err := r.git.AddWorktree(ctx, repository.SourcePath, item.Path, item.Branch); err != nil {
					return NewError(ErrorGit, fmt.Errorf("restore removed worktree for repository %q at %q: %w", item.ID, item.Path, err))
				}
				return nil
			}
		}
		steps = append(steps, step)
	}
	return steps
}

func removalRecoveryPath(dataDir string, value RemovalPlan) string {
	return filepath.Join(dataDir, "projects", value.ProjectID, "recovery", value.WorkspaceID+".json")
}
