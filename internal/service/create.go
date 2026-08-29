package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
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
	filesystem  workspaceFilesystem
}

// CreateResult keeps execution-only evidence out of the version-one workspace
// plan while allowing the human create renderer to report actual file updates.
type CreateResult struct {
	Plan                plan.WorkspacePlan
	LogicalRoot         string
	BaseRepository      string
	IgnoreUpdates       []IgnoreFileUpdate
	RetainedIgnoreFiles []IgnoreFileUpdate
	RemovedIgnoreFiles  []IgnoreFileUpdate
	UnverifiedMounts    []UnverifiedMount
}

type createdWorktreeReceipt struct {
	info      os.FileInfo
	commonGit string
	gitDir    string
	branch    string
	head      string
}

// UnverifiedMount identifies a child that was not added because its parent
// mount did not complete automatic-ignore verification.
type UnverifiedMount struct {
	ParentRepositoryID string
	ChildRepositoryID  string
	ParentPath         string
	ChildPath          string
	Mount              string
}

func NewWorkspaceCreator() *WorkspaceCreator {
	git := gitadapter.NewAdapter("git")
	return NewWorkspaceCreatorWith(git, NewWorkspaceTransaction())
}

func NewWorkspaceCreatorWith(git gitadapter.Git, transaction *WorkspaceTransaction) *WorkspaceCreator {
	return &WorkspaceCreator{git: git, planner: NewWorkspacePlannerWithGit(git), transaction: transaction, filesystem: newWorkspaceFilesystem()}
}

// Create plans before mutation, then revalidates under the project lock,
// executes parent-first Git effects, validates the resulting checkouts, and
// atomically commits workspace state.
func (c *WorkspaceCreator) Create(ctx context.Context, project domain.Project, request WorkspacePlanRequest, progress func(transaction.Event)) (plan.WorkspacePlan, error) {
	result, err := c.CreateWithResult(ctx, project, request, progress)
	return result.Plan, err
}

// CreateWithResult executes create and returns its immutable public plan plus
// internal automatic-ignore evidence. The evidence is intentionally not part of
// plan.WorkspacePlan or its JSON representation.
func (c *WorkspaceCreator) CreateWithResult(ctx context.Context, project domain.Project, request WorkspacePlanRequest, progress func(transaction.Event)) (CreateResult, error) {
	request.Operation = plan.Create
	return c.executeCreate(ctx, project, request, progress)
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

func (c *WorkspaceCreator) executeCreate(ctx context.Context, project domain.Project, request WorkspacePlanRequest, progress func(transaction.Event)) (CreateResult, error) {
	if c == nil || c.git == nil || c.planner == nil || c.transaction == nil {
		return CreateResult{}, NewError(ErrorInternal, fmt.Errorf("workspace creator is not configured"))
	}
	value, err := c.planner.Plan(ctx, project, request)
	if err != nil {
		return CreateResult{}, err
	}
	steps, evidence, err := c.createSteps(project, value)
	if err != nil {
		return CreateResult{}, err
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
		return evidence.result(value), err
	}
	return evidence.result(value), nil
}

func (c *WorkspaceCreator) steps(project domain.Project, value plan.WorkspacePlan) ([]transaction.Step, error) {
	steps, _, err := c.createSteps(project, value)
	return steps, err
}

type createIgnoreEvidence struct {
	updatesByParent   map[string]IgnoreFileUpdate
	filesByParent     map[string]IgnoreFilePlan
	cleanWhenPlanned  map[string]bool
	removedParents    map[string]bool
	preservedBranches map[string]bool
	unverified        map[string]UnverifiedMount
	order             []string
}

func newCreateIgnoreEvidence() *createIgnoreEvidence {
	return &createIgnoreEvidence{
		updatesByParent:   make(map[string]IgnoreFileUpdate),
		filesByParent:     make(map[string]IgnoreFilePlan),
		cleanWhenPlanned:  make(map[string]bool),
		removedParents:    make(map[string]bool),
		preservedBranches: make(map[string]bool),
		unverified:        make(map[string]UnverifiedMount),
	}
}

func (e *createIgnoreEvidence) record(file IgnoreFilePlan) {
	if _, exists := e.updatesByParent[file.ParentRepositoryID]; !exists {
		e.order = append(e.order, file.ParentRepositoryID)
	}
	e.updatesByParent[file.ParentRepositoryID] = updateForIgnoreFile(file)
	e.filesByParent[file.ParentPath] = file
}

func (e *createIgnoreEvidence) expected(path string) (IgnoreFilePlan, bool) {
	file, found := e.filesByParent[path]
	return file, found
}

func (e *createIgnoreEvidence) recordCleanWhenPlanned(path string, status gitadapter.Status) {
	e.cleanWhenPlanned[path] = len(status.Entries) == 0
}

func (e *createIgnoreEvidence) addUnverified(requirement IgnoreRequirement, childPath string) {
	e.unverified[requirement.ChildRepositoryID] = UnverifiedMount{
		ParentRepositoryID: requirement.ParentRepositoryID,
		ChildRepositoryID:  requirement.ChildRepositoryID,
		ParentPath:         requirement.ParentPath,
		ChildPath:          childPath,
		Mount:              requirement.Mount,
	}
}

func (e *createIgnoreEvidence) markVerified(requirements []IgnoreRequirement) {
	for _, requirement := range requirements {
		delete(e.unverified, requirement.ChildRepositoryID)
	}
}

func (e *createIgnoreEvidence) markRemoved(path string) {
	file, found := e.filesByParent[path]
	if found && file.Changed {
		e.removedParents[file.ParentRepositoryID] = true
	}
}

func (e *createIgnoreEvidence) preserveBranch(repositoryID string) {
	e.preservedBranches[repositoryID] = true
}

func (e *createIgnoreEvidence) updates() []IgnoreFileUpdate {
	updates := make([]IgnoreFileUpdate, 0, len(e.order))
	for _, parentID := range e.order {
		updates = append(updates, e.updatesByParent[parentID])
	}
	return updates
}

func (e *createIgnoreEvidence) result(value plan.WorkspacePlan) CreateResult {
	result := CreateResult{Plan: value, LogicalRoot: value.LogicalRoot, BaseRepository: value.BaseRepository}
	for _, parentID := range e.order {
		update := e.updatesByParent[parentID]
		result.IgnoreUpdates = append(result.IgnoreUpdates, update)
		if e.removedParents[parentID] {
			result.RemovedIgnoreFiles = append(result.RemovedIgnoreFiles, update)
		} else {
			result.RetainedIgnoreFiles = append(result.RetainedIgnoreFiles, update)
		}
	}
	for _, item := range value.Repositories {
		if mount, found := e.unverified[item.ID]; found {
			result.UnverifiedMounts = append(result.UnverifiedMounts, mount)
		}
	}
	return result
}

func (c *WorkspaceCreator) createSteps(project domain.Project, value plan.WorkspacePlan) ([]transaction.Step, *createIgnoreEvidence, error) {
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	planned := make(map[string]plan.RepositoryPlan, len(value.Repositories))
	for _, repository := range value.Repositories {
		planned[repository.ID] = repository
	}
	requirements := map[string][]IgnoreRequirement{}
	if value.Operation == plan.Create {
		var err error
		requirements, err = createIgnoreRequirements(value)
		if err != nil {
			return nil, nil, NewError(ErrorValidation, err)
		}
	}
	evidence := newCreateIgnoreEvidence()
	grouping := newWorkspaceGrouping(value.RootPath, c.filesystem)
	for _, directChildren := range requirements {
		for _, requirement := range directChildren {
			evidence.addUnverified(requirement, planned[requirement.ChildRepositoryID].Path)
		}
	}
	steps := make([]transaction.Step, 0, len(value.Steps)+len(requirements))
	for _, step := range value.Steps {
		repository, found := repositories[step.RepositoryID]
		if !found {
			return nil, nil, NewError(ErrorValidation, fmt.Errorf("create plan names unknown repository %q", step.RepositoryID))
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
					if evidence.preservedBranches[repository.ID] {
						return NewError(ErrorGit, fmt.Errorf("preserve branch %q for repository %q because its created worktree could not be safely removed", item.Branch, repository.ID))
					}
					if err := c.git.DeleteBranch(ctx, repository.SourcePath, item.Branch, true); err != nil {
						return NewError(ErrorGit, fmt.Errorf("delete created branch %q for repository %q: %w", item.Branch, repository.ID, err))
					}
					return nil
				},
			})
		case plan.AddWorktree:
			parentPath := value.RootPath
			if item.ParentID != "" {
				parentPath = planned[item.ParentID].Path
			}
			if groupingStep, needed, err := grouping.step(item, parentPath); err != nil {
				return nil, nil, NewError(ErrorValidation, fmt.Errorf("prepare grouping for repository %q: %w", repository.ID, err))
			} else if needed {
				steps = append(steps, groupingStep)
			}
			worktreeAdded := false
			var worktreeReceipt *createdWorktreeReceipt
			rollbackWorktree := func(ctx context.Context) error {
				if !worktreeAdded {
					return nil
				}
				if worktreeReceipt == nil {
					return fmt.Errorf("preserve created worktree at %q because its ownership receipt was not captured", item.Path)
				}
				var err error
				if value.Operation == plan.Create {
					err = c.safeRemoveCreatedWorktree(ctx, repository.ID, repository.SourcePath, item.Path, worktreeReceipt, evidence)
				} else {
					_, err = c.removeOwnedCreatedWorktree(ctx, repository.SourcePath, item.Path, worktreeReceipt, nil)
				}
				if err != nil {
					return NewError(ErrorGit, fmt.Errorf("remove created worktree for repository %q at %q: %w", repository.ID, item.Path, err))
				}
				worktreeAdded = false
				return nil
			}
			steps = append(steps, transaction.Step{
				Name: string(step.Action) + ":" + repository.ID,
				Execute: func(ctx context.Context) error {
					if err := grouping.revalidate(item, parentPath); err != nil {
						return NewError(ErrorValidation, fmt.Errorf("revalidate grouping for repository %q: %w", repository.ID, err))
					}
					if err := c.git.AddWorktree(ctx, repository.SourcePath, item.Path, item.Branch); err != nil {
						return NewError(ErrorGit, fmt.Errorf("add worktree for repository %q at %q: %w", repository.ID, item.Path, err))
					}
					worktreeAdded = true
					captured, captureErr := c.captureWorktreeReceipt(context.WithoutCancel(ctx), item.Path, repository.CommonGitDir, item.Branch, item.Base)
					if captureErr != nil {
						return NewError(ErrorValidation, fmt.Errorf("capture created worktree %q ownership: %w", item.Path, captureErr))
					}
					worktreeReceipt = captured
					if err := grouping.recordWorktree(item.Path); err != nil {
						return NewError(ErrorValidation, err)
					}
					if err := grouping.releaseCreated(repository.ID); err != nil {
						return NewError(ErrorValidation, err)
					}
					return nil
				},
				Rollback:              rollbackWorktree,
				RollbackFailedExecute: rollbackWorktree,
			})
			if directChildren := requirements[repository.ID]; len(directChildren) != 0 {
				parentID, parentPath := repository.ID, item.Path
				steps = append(steps, transaction.Step{
					Name: "inspect_ignore_owner:" + parentID,
					Execute: func(ctx context.Context) error {
						_, err := c.git.StatusIncludingIgnored(ctx, parentPath)
						if err != nil {
							return NewError(ErrorGit, fmt.Errorf("inspect created parent worktree %q before automatic ignore planning: %w", parentID, err))
						}
						if _, err := c.git.StatusIncludingIgnored(ctx, parentPath); err != nil {
							return NewError(ErrorGit, fmt.Errorf("reinspect created parent worktree %q before automatic ignore planning: %w", parentID, err))
						}
						return nil
					},
					Rollback: func(context.Context) error { return nil },
				})
				steps = append(steps, transaction.Step{
					Name: "ensure_ignore:" + parentID,
					Execute: func(ctx context.Context) error {
						return c.ensureWorktreeIgnores(ctx, parentID, parentPath, directChildren, evidence)
					},
					// The parent worktree rollback owns removal of this automatic edit.
					Rollback: func(context.Context) error { return nil },
				})
			}
		default:
			return nil, nil, NewError(ErrorValidation, fmt.Errorf("create plan has unsupported action %q", step.Action))
		}
	}
	return steps, evidence, nil
}

func createIgnoreRequirements(value plan.WorkspacePlan) (map[string][]IgnoreRequirement, error) {
	planned := make(map[string]plan.RepositoryPlan, len(value.Repositories))
	for _, repository := range value.Repositories {
		planned[repository.ID] = repository
	}
	requirements := make(map[string][]IgnoreRequirement)
	for _, child := range value.Repositories {
		if child.ParentID == "" {
			continue
		}
		parent, found := planned[child.ParentID]
		if !found {
			return nil, fmt.Errorf("create plan repository %q has unknown parent %q", child.ID, child.ParentID)
		}
		requirements[parent.ID] = append(requirements[parent.ID], IgnoreRequirement{
			ParentRepositoryID: parent.ID,
			ChildRepositoryID:  child.ID,
			ParentPath:         parent.Path,
			Mount:              child.Mount,
		})
	}
	return requirements, nil
}

func (c *WorkspaceCreator) ensureWorktreeIgnores(ctx context.Context, parentID, parentPath string, requirements []IgnoreRequirement, evidence *createIgnoreEvidence) error {
	plan, err := NewIgnorePlanner(c.git).Plan(ctx, requirements)
	if err != nil {
		return fmt.Errorf("plan automatic ignore protection for parent repository %q: %w", parentID, err)
	}
	status, err := c.git.StatusIncludingIgnored(ctx, parentPath)
	if err != nil {
		return NewError(ErrorGit, fmt.Errorf("inspect parent repository %q after automatic ignore planning: %w", parentID, err))
	}
	evidence.recordCleanWhenPlanned(parentPath, status)
	result, err := NewIgnoreApplier().Apply(ctx, plan)
	for _, update := range result.Changed {
		for _, file := range plan.Files {
			if file.Path == update.Path {
				evidence.record(file)
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("apply automatic ignore protection for parent repository %q: %w", parentID, err)
	}
	for _, requirement := range requirements {
		current, inspectErr := c.git.InspectWorkingTreeIgnore(ctx, parentPath, requirement.Mount)
		if inspectErr != nil {
			return NewError(ErrorGit, fmt.Errorf("verify mount %q for repository %q: %w", requirement.Mount, requirement.ChildRepositoryID, inspectErr))
		}
		if !current.Qualifies(parentPath) {
			return NewError(ErrorValidation, fmt.Errorf("verify mount %q for repository %q is ignored by an in-checkout .gitignore", requirement.Mount, requirement.ChildRepositoryID))
		}
		evidence.markVerified([]IgnoreRequirement{requirement})
	}
	return nil
}

func (c *WorkspaceCreator) safeRemoveCreatedWorktree(ctx context.Context, repositoryID, sourcePath, worktreePath string, receipt *createdWorktreeReceipt, evidence *createIgnoreEvidence) error {
	status, err := c.git.StatusIncludingIgnored(ctx, worktreePath)
	if err != nil {
		return fmt.Errorf("inspect created worktree dirt: %w", err)
	}
	var expected *IgnoreFilePlan
	if len(status.Entries) != 0 {
		file, found := evidence.expected(worktreePath)
		if !found || !file.Changed || !evidence.cleanWhenPlanned[worktreePath] || !isExactAutomaticIgnoreDirt(status) {
			return fmt.Errorf("preserve created worktree with unrelated tracked, staged, or untracked changes at %q", worktreePath)
		}
		current, readErr := os.ReadFile(file.Path)
		if readErr != nil {
			return fmt.Errorf("preserve created worktree because automatic ignore target %q changed independently: %w", file.Path, readErr)
		}
		if !bytes.Equal(current, file.NewBytes) {
			return fmt.Errorf("preserve created worktree because automatic ignore target %q changed independently", file.Path)
		}
		expected = &file
	}
	publicPathRecreated, err := c.removeOwnedCreatedWorktree(ctx, sourcePath, worktreePath, receipt, expected)
	if err != nil {
		evidence.preserveBranch(repositoryID)
		return err
	}
	evidence.markRemoved(worktreePath)
	if publicPathRecreated {
		// The public path now belongs to a concurrent creator. The created
		// branch remains as the sole actionable recovery step, while this
		// transaction's already-isolated checkout has been safely deleted.
		evidence.preserveBranch(repositoryID)
	}
	return nil
}

// removeOwnedCreatedWorktree makes the whole checkout, rather than an
// individual .gitignore pathname, the cleanup ownership unit. The public name
// is atomically detached into a fresh private directory and revalidated there.
// Git is then asked to unregister a missing intermediate name, so Git never
// receives a path whose ignored contents it could recursively delete.
func (c *WorkspaceCreator) removeOwnedCreatedWorktree(ctx context.Context, sourcePath, worktreePath string, receipt *createdWorktreeReceipt, expected *IgnoreFilePlan) (publicPathRecreated bool, result error) {
	ownedInfo, err := os.Lstat(worktreePath)
	if err != nil {
		return false, fmt.Errorf("inspect created worktree ownership: %w", err)
	}
	if !ownedInfo.IsDir() || ownedInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("preserve created worktree because %q is no longer the planned directory", worktreePath)
	}
	if receipt == nil || !os.SameFile(receipt.info, ownedInfo) || c.worktreeReceiptMatches(ctx, worktreePath, receipt) != nil {
		return false, fmt.Errorf("preserve created worktree because %q no longer has the transaction ownership receipt", worktreePath)
	}
	if !primeFileIdentity(ownedInfo) {
		return false, fmt.Errorf("capture created worktree directory identity: %q", worktreePath)
	}
	quarantineRoot, err := os.MkdirTemp(filepath.Dir(worktreePath), ".wtree-worktree-rollback-*")
	if err != nil {
		return false, fmt.Errorf("allocate private worktree quarantine: %w", err)
	}
	ownedPath := filepath.Join(quarantineRoot, "owned")
	destroyPath := filepath.Join(quarantineRoot, "destroy")
	cleanupRoot := true
	defer func() {
		if cleanupRoot {
			if cleanupErr := os.Remove(quarantineRoot); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				result = errors.Join(result, fmt.Errorf("remove private worktree quarantine %q: %w", quarantineRoot, cleanupErr))
			}
		}
	}()
	if err := fsutil.RenameNoReplace(worktreePath, ownedPath); err != nil {
		return false, fmt.Errorf("capture created worktree ownership: %w", err)
	}
	movedInfo, err := os.Lstat(ownedPath)
	if err != nil || !os.SameFile(ownedInfo, movedInfo) {
		restoreErr := fsutil.RenameNoReplace(ownedPath, worktreePath)
		if restoreErr != nil {
			cleanupRoot = false
		}
		return false, errors.Join(fmt.Errorf("preserve created worktree because its directory changed at the ownership boundary"), restoreErr)
	}
	if err := c.git.WorktreeRepair(ctx, ownedPath); err != nil {
		return false, restoreCapturedWorktree(ctx, c.git, ownedPath, worktreePath, fmt.Errorf("repair captured worktree registration: %w", err), &cleanupRoot)
	}
	if err := c.validateWorktreeReceipt(ctx, ownedPath, receipt); err != nil {
		return false, restoreCapturedWorktree(ctx, c.git, ownedPath, worktreePath, fmt.Errorf("preserve created worktree because its ownership changed after quarantine: %w", err), &cleanupRoot)
	}
	status, err := c.git.StatusIncludingIgnored(ctx, ownedPath)
	if err != nil {
		return false, restoreCapturedWorktree(ctx, c.git, ownedPath, worktreePath, fmt.Errorf("revalidate captured worktree dirt: %w", err), &cleanupRoot)
	}
	if err := c.validateWorktreeReceipt(ctx, ownedPath, receipt); err != nil {
		return false, restoreCapturedWorktree(ctx, c.git, ownedPath, worktreePath, fmt.Errorf("preserve created worktree because its ownership changed during quarantine validation: %w", err), &cleanupRoot)
	}
	if !capturedWorktreeStillOwned(ownedPath, status, expected) {
		return false, restoreCapturedWorktree(ctx, c.git, ownedPath, worktreePath, fmt.Errorf("preserve created worktree because unrelated dirt appeared at the ownership boundary"), &cleanupRoot)
	}
	if err := fsutil.RenameNoReplace(ownedPath, destroyPath); err != nil {
		return false, restoreCapturedWorktree(ctx, c.git, ownedPath, worktreePath, fmt.Errorf("isolate owned worktree for destruction: %w", err), &cleanupRoot)
	}
	if _, err := os.Lstat(worktreePath); err == nil || !os.IsNotExist(err) {
		_ = c.git.WorktreeRepair(ctx, destroyPath)
		cleanupRoot = false
		return false, fmt.Errorf("preserve captured worktree at %q because the public path was concurrently recreated", destroyPath)
	}
	if err := c.git.RemoveWorktree(ctx, sourcePath, ownedPath, false); err != nil {
		repairErr := c.git.WorktreeRepair(ctx, destroyPath)
		cleanupRoot = false
		return false, errors.Join(fmt.Errorf("unregister captured worktree without deleting its owned tree: %w", err), repairErr)
	}
	if _, err := os.Lstat(worktreePath); err == nil || !os.IsNotExist(err) {
		if err := os.RemoveAll(destroyPath); err != nil {
			cleanupRoot = false
			return false, fmt.Errorf("remove isolated transaction-owned worktree %q after public-path recreation: %w", destroyPath, err)
		}
		return true, nil
	}
	if err := os.RemoveAll(destroyPath); err != nil {
		cleanupRoot = false
		return false, fmt.Errorf("remove transaction-owned worktree %q: %w", destroyPath, err)
	}
	return false, nil
}

func (c *WorkspaceCreator) captureWorktreeReceipt(ctx context.Context, path, expectedCommonGit, expectedBranch, expectedHead string) (*createdWorktreeReceipt, error) {
	info, err := c.filesystem.lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("created worktree is not a real directory")
	}
	common, err := c.git.CommonGitDir(ctx, path)
	if err != nil {
		return nil, err
	}
	if common != expectedCommonGit {
		return nil, fmt.Errorf("worktree common Git identity %q does not match planned %q", common, expectedCommonGit)
	}
	gitDir, err := c.git.GitDir(ctx, path)
	if err != nil {
		return nil, err
	}
	branch, detached, err := c.git.CurrentBranch(ctx, path)
	if err != nil {
		return nil, err
	}
	if detached {
		return nil, fmt.Errorf("created worktree is detached")
	}
	if branch != expectedBranch {
		return nil, fmt.Errorf("worktree branch %q does not match planned %q", branch, expectedBranch)
	}
	head, err := c.git.Head(ctx, path)
	if err != nil {
		return nil, err
	}
	if head != expectedHead {
		return nil, fmt.Errorf("worktree HEAD %q does not match planned %q", head, expectedHead)
	}
	return &createdWorktreeReceipt{info: info, commonGit: common, gitDir: gitDir, branch: branch, head: head}, nil
}

func (c *WorkspaceCreator) worktreeReceiptMatches(ctx context.Context, path string, receipt *createdWorktreeReceipt) error {
	common, err := c.git.CommonGitDir(ctx, path)
	if err != nil || common != receipt.commonGit {
		return fmt.Errorf("worktree common Git identity changed")
	}
	gitDir, err := c.git.GitDir(ctx, path)
	if err != nil || gitDir != receipt.gitDir {
		return fmt.Errorf("worktree Git directory identity changed")
	}
	branch, detached, err := c.git.CurrentBranch(ctx, path)
	if err != nil || detached || branch != receipt.branch {
		return fmt.Errorf("worktree branch changed")
	}
	head, err := c.git.Head(ctx, path)
	if err != nil || head != receipt.head {
		return fmt.Errorf("worktree HEAD changed")
	}
	return nil
}

func (c *WorkspaceCreator) validateWorktreeReceipt(ctx context.Context, path string, receipt *createdWorktreeReceipt) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect worktree directory identity: %w", err)
	}
	if receipt == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(receipt.info, info) {
		return fmt.Errorf("worktree directory identity changed")
	}
	return c.worktreeReceiptMatches(ctx, path, receipt)
}

func capturedWorktreeStillOwned(ownedPath string, status gitadapter.Status, expected *IgnoreFilePlan) bool {
	if expected == nil {
		return len(status.Entries) == 0
	}
	if !isExactAutomaticIgnoreDirt(status) {
		return false
	}
	relative, err := filepath.Rel(expected.ParentPath, expected.Path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	current, err := os.ReadFile(filepath.Join(ownedPath, relative))
	return err == nil && bytes.Equal(current, expected.NewBytes)
}

func restoreCapturedWorktree(ctx context.Context, git gitadapter.Git, capturedPath, targetPath string, cause error, cleanupRoot *bool) error {
	if err := fsutil.RenameNoReplace(capturedPath, targetPath); err != nil {
		*cleanupRoot = false
		return errors.Join(cause, fmt.Errorf("retain captured worktree at %q because %q was concurrently recreated: %w", capturedPath, targetPath, err))
	}
	repairErr := git.WorktreeRepair(ctx, targetPath)
	if repairErr != nil {
		*cleanupRoot = false
		return errors.Join(cause, fmt.Errorf("repair restored worktree registration: %w", repairErr))
	}
	return cause
}

func isExactAutomaticIgnoreDirt(status gitadapter.Status) bool {
	if status.Staged || len(status.Entries) == 0 {
		return false
	}
	for _, entry := range status.Entries {
		if filepath.Clean(filepath.FromSlash(entry.Path)) != ".gitignore" || entry.OriginalPath != "" || (entry.Index != ' ' && !entry.Untracked) {
			return false
		}
	}
	return true
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
