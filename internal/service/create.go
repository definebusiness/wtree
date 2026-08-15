package service

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/transaction"
)

// WorkspaceCreator executes a fully preflighted create plan. It owns the
// mapping between immutable plan actions and the Git effects that implement
// them; state publication remains in WorkspaceTransaction.
type WorkspaceCreator struct {
	git         gitadapter.Git
	planner     *WorkspacePlanner
	transaction *WorkspaceTransaction
}

func NewWorkspaceCreator() *WorkspaceCreator {
	git := gitadapter.NewAdapter("git")
	return NewWorkspaceCreatorWith(git, NewWorkspaceTransaction())
}

func NewWorkspaceCreatorWith(git gitadapter.Git, transaction *WorkspaceTransaction) *WorkspaceCreator {
	return &WorkspaceCreator{git: git, planner: NewWorkspacePlannerWithGit(git), transaction: transaction}
}

// Create plans before mutation, then revalidates under the project lock,
// executes parent-first Git effects, validates the resulting checkouts, and
// atomically commits workspace state.
func (c *WorkspaceCreator) Create(ctx context.Context, project domain.Project, request WorkspacePlanRequest, progress func(transaction.Event)) (plan.WorkspacePlan, error) {
	request.Operation = plan.Create
	return c.execute(ctx, project, request, progress)
}

// Checkout executes an existing-branch workspace plan. It shares the same
// transaction and result-validation boundary as create, but its immutable plan
// contains only add-worktree effects.
func (c *WorkspaceCreator) Checkout(ctx context.Context, project domain.Project, request WorkspacePlanRequest, progress func(transaction.Event)) (plan.WorkspacePlan, error) {
	request.Operation = plan.Checkout
	return c.execute(ctx, project, request, progress)
}

func (c *WorkspaceCreator) execute(ctx context.Context, project domain.Project, request WorkspacePlanRequest, progress func(transaction.Event)) (plan.WorkspacePlan, error) {
	if c == nil || c.git == nil || c.planner == nil || c.transaction == nil {
		return plan.WorkspacePlan{}, NewError(ErrorInternal, fmt.Errorf("workspace creator is not configured"))
	}
	value, err := c.planner.Plan(ctx, project, request)
	if err != nil {
		return plan.WorkspacePlan{}, err
	}
	steps, err := c.steps(project, value)
	if err != nil {
		return plan.WorkspacePlan{}, err
	}
	_, err = c.transaction.Execute(ctx, WorkspaceTransactionRequest{
		Plan:     value,
		DataDir:  request.DataDir,
		Steps:    steps,
		Progress: progress,
		Revalidate: func(ctx context.Context) error {
			revalidated, err := c.planner.Plan(ctx, project, request)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(value, revalidated) {
				return NewError(ErrorConflict, fmt.Errorf("workspace plan changed during locked revalidation"))
			}
			return nil
		},
		ValidateResult: func(ctx context.Context) error {
			return c.validateResult(ctx, project, value)
		},
	})
	if err != nil {
		return plan.WorkspacePlan{}, err
	}
	return value, nil
}

func (c *WorkspaceCreator) steps(project domain.Project, value plan.WorkspacePlan) ([]transaction.Step, error) {
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	planned := make(map[string]plan.RepositoryPlan, len(value.Repositories))
	for _, repository := range value.Repositories {
		planned[repository.ID] = repository
	}
	steps := make([]transaction.Step, 0, len(value.Steps))
	for _, step := range value.Steps {
		repository, found := repositories[step.RepositoryID]
		if !found {
			return nil, NewError(ErrorValidation, fmt.Errorf("create plan names unknown repository %q", step.RepositoryID))
		}
		item := planned[step.RepositoryID]
		switch step.Action {
		case plan.CreateBranch:
			steps = append(steps, transaction.Step{
				Name: string(step.Action) + ":" + repository.ID,
				Execute: func(ctx context.Context) error {
					if err := c.git.CreateBranch(ctx, repository.SourcePath, item.Branch, item.Base); err != nil {
						return NewError(ErrorGit, fmt.Errorf("create branch %q for repository %q: %w", item.Branch, repository.ID, err))
					}
					return nil
				},
				Rollback: func(ctx context.Context) error {
					if err := c.git.DeleteBranch(ctx, repository.SourcePath, item.Branch, true); err != nil {
						return NewError(ErrorGit, fmt.Errorf("delete created branch %q for repository %q: %w", item.Branch, repository.ID, err))
					}
					return nil
				},
			})
		case plan.AddWorktree:
			steps = append(steps, transaction.Step{
				Name: string(step.Action) + ":" + repository.ID,
				Execute: func(ctx context.Context) error {
					if err := c.git.AddWorktree(ctx, repository.SourcePath, item.Path, item.Branch); err != nil {
						return NewError(ErrorGit, fmt.Errorf("add worktree for repository %q at %q: %w", repository.ID, item.Path, err))
					}
					return nil
				},
				Rollback: func(ctx context.Context) error {
					if err := c.git.RemoveWorktree(ctx, repository.SourcePath, item.Path, true); err != nil {
						return NewError(ErrorGit, fmt.Errorf("remove created worktree for repository %q at %q: %w", repository.ID, item.Path, err))
					}
					return nil
				},
			})
		default:
			return nil, NewError(ErrorValidation, fmt.Errorf("create plan has unsupported action %q", step.Action))
		}
	}
	return steps, nil
}

func (c *WorkspaceCreator) validateResult(ctx context.Context, project domain.Project, value plan.WorkspacePlan) error {
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	for _, item := range value.Repositories {
		repository := repositories[item.ID]
		identity, err := c.git.CommonGitDir(ctx, item.Path)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("verify created repository %q identity: %w", item.ID, err))
		}
		if identity != repository.CommonGitDir {
			return NewError(ErrorValidation, fmt.Errorf("created repository %q identity %q does not match configured identity %q", item.ID, identity, repository.CommonGitDir))
		}
		branch, detached, err := c.git.CurrentBranch(ctx, item.Path)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("verify created repository %q branch: %w", item.ID, err))
		}
		if detached || branch != item.Branch {
			return NewError(ErrorValidation, fmt.Errorf("created repository %q is on branch %q, want %q", item.ID, branch, item.Branch))
		}
		head, err := c.git.Head(ctx, item.Path)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("verify created repository %q HEAD: %w", item.ID, err))
		}
		if head != item.Base {
			return NewError(ErrorValidation, fmt.Errorf("created repository %q HEAD %q does not match planned base %q", item.ID, head, item.Base))
		}
		worktrees, err := c.git.ListWorktrees(ctx, repository.SourcePath)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("verify created repository %q worktree: %w", item.ID, err))
		}
		found := false
		for _, worktree := range worktrees {
			if sameFilesystemPath(worktree.Path, item.Path) && worktree.Branch == item.Branch {
				found = true
				break
			}
		}
		if !found {
			return NewError(ErrorValidation, fmt.Errorf("created repository %q worktree is not registered at %q", item.ID, item.Path))
		}
	}
	return nil
}

func sameFilesystemPath(left, right string) bool {
	canonicalLeft, leftErr := filepath.EvalSymlinks(left)
	canonicalRight, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil && rightErr == nil {
		return filepath.Clean(canonicalLeft) == filepath.Clean(canonicalRight)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
