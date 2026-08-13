package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/marcel/wtree/internal/domain"
	gitadapter "github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/pathutil"
	"github.com/marcel/wtree/internal/plan"
	"github.com/marcel/wtree/internal/store"
)

// MountOverride applies one workspace-specific parent-relative mount.
type MountOverride struct {
	RepositoryID string
	Mount        string
}

// WorkspacePlanRequest contains only planning inputs. Planning never creates
// branches, worktrees, directories, or state files.
type WorkspacePlanRequest struct {
	Operation     plan.Operation
	WorkspaceName string
	From          string
	Mounts        []MountOverride
	TargetPath    string
	WorktreeRoot  string
	DataDir       string
}

// WorkspacePlanner creates fully preflighted, immutable plans.
type WorkspacePlanner struct{ git gitadapter.Git }

func NewWorkspacePlanner() *WorkspacePlanner {
	return NewWorkspacePlannerWithGit(gitadapter.NewAdapter("git"))
}

func NewWorkspacePlannerWithGit(git gitadapter.Git) *WorkspacePlanner {
	return &WorkspacePlanner{git: git}
}

func (p *WorkspacePlanner) Plan(ctx context.Context, project domain.Project, request WorkspacePlanRequest) (plan.WorkspacePlan, error) {
	if err := project.Validate(); err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	if request.Operation != plan.Create && request.Operation != plan.Checkout {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, fmt.Errorf("unsupported workspace operation %q", request.Operation))
	}
	mounts, err := mountMap(request.Mounts)
	if err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, err)
	}
	rootPath, err := plannedRoot(project.ID, request)
	if err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, err)
	}
	if request.TargetPath == "" {
		if err := pathutil.CheckPotentialWithin(request.WorktreeRoot, rootPath); err != nil {
			return plan.WorkspacePlan{}, NewError(ErrorValidation, fmt.Errorf("workspace target escapes configured worktree root: %w", err))
		}
	}
	if err := checkTargetPath(rootPath); err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorConflict, err)
	}
	if err := checkWorkspaceCollision(project.ID, request.DataDir, request.Operation, request.WorkspaceName, rootPath); err != nil {
		return plan.WorkspacePlan{}, err
	}
	paths, err := project.EffectivePaths(rootPath, mounts)
	if err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, fmt.Errorf("resolve workspace mounts: %w", err))
	}
	if err := p.preflightRepositories(ctx, project, request, paths); err != nil {
		return plan.WorkspacePlan{}, err
	}

	base := request.From
	if base == "" {
		base = "HEAD"
	}
	repositories := make([]plan.RepositoryPlan, 0, len(project.Repositories))
	for _, repository := range project.ParentFirst() {
		baseRef := base
		if request.Operation == plan.Checkout {
			baseRef = request.WorkspaceName
		}
		resolvedBase, err := p.resolveBase(ctx, repository, baseRef)
		if err != nil {
			return plan.WorkspacePlan{}, err
		}
		repositories = append(repositories, plan.RepositoryPlan{
			ID: repository.ID, ParentID: repository.ParentID, Base: resolvedBase,
			Branch: request.WorkspaceName, Mount: effectiveMount(repository, mounts), Path: paths[repository.ID],
		})
	}
	value := plan.WorkspacePlan{
		Version:       plan.Version,
		Operation:     request.Operation,
		ProjectID:     project.ID,
		WorkspaceName: request.WorkspaceName,
		WorkspaceID:   pathutil.StorageName(request.WorkspaceName),
		RootPath:      rootPath,
		Repositories:  repositories,
		Steps:         planSteps(request.Operation, repositories),
	}
	if err := value.Validate(); err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, fmt.Errorf("validate workspace plan: %w", err))
	}
	return value, nil
}

func (p *WorkspacePlanner) preflightRepositories(ctx context.Context, project domain.Project, request WorkspacePlanRequest, paths map[string]string) error {
	for _, repository := range project.ParentFirst() {
		validBranch, err := p.git.ValidBranchName(ctx, repository.SourcePath, request.WorkspaceName)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("validate branch %q for repository %q: %w", request.WorkspaceName, repository.ID, err))
		}
		if !validBranch {
			return NewError(ErrorValidation, fmt.Errorf("invalid branch name %q", request.WorkspaceName))
		}
		identity, err := p.git.CommonGitDir(ctx, repository.SourcePath)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("verify repository %q Git identity: %w", repository.ID, err))
		}
		if identity != repository.CommonGitDir {
			return NewError(ErrorValidation, fmt.Errorf("repository %q Git identity %q does not match configured identity %q", repository.ID, identity, repository.CommonGitDir))
		}
		switch request.Operation {
		case plan.Create:
			exists, err := p.git.BranchExists(ctx, repository.SourcePath, request.WorkspaceName)
			if err != nil {
				return NewError(ErrorGit, fmt.Errorf("check branch %q for repository %q: %w", request.WorkspaceName, repository.ID, err))
			}
			if exists {
				return NewError(ErrorConflict, fmt.Errorf("branch %q already exists for repository %q", request.WorkspaceName, repository.ID))
			}
		case plan.Checkout:
			exists, err := p.git.BranchExists(ctx, repository.SourcePath, request.WorkspaceName)
			if err != nil {
				return NewError(ErrorGit, fmt.Errorf("check branch %q for repository %q: %w", request.WorkspaceName, repository.ID, err))
			}
			if !exists {
				return NewError(ErrorValidation, fmt.Errorf("branch %q does not exist for repository %q", request.WorkspaceName, repository.ID))
			}
			checkedOut, err := p.git.BranchCheckedOut(ctx, repository.SourcePath, request.WorkspaceName)
			if err != nil {
				return NewError(ErrorGit, fmt.Errorf("check branch %q worktree use for repository %q: %w", request.WorkspaceName, repository.ID, err))
			}
			if checkedOut {
				return NewError(ErrorConflict, fmt.Errorf("branch %q is already checked out for repository %q", request.WorkspaceName, repository.ID))
			}
		}
		if err := checkSourceMountConflict(project, repository, request.Mounts); err != nil {
			return NewError(ErrorConflict, err)
		}
	}
	return nil
}

func (p *WorkspacePlanner) resolveBase(ctx context.Context, repository domain.Repository, base string) (string, error) {
	if base == "HEAD" {
		value, err := p.git.Head(ctx, repository.SourcePath)
		if err != nil {
			return "", NewError(ErrorGit, fmt.Errorf("resolve HEAD for repository %q: %w", repository.ID, err))
		}
		return value, nil
	}
	value, err := p.git.ResolveRef(ctx, repository.SourcePath, base)
	if err != nil {
		return "", NewError(ErrorValidation, fmt.Errorf("resolve base %q for repository %q: %w", base, repository.ID, err))
	}
	return value, nil
}

func mountMap(overrides []MountOverride) (map[string]string, error) {
	mounts := make(map[string]string, len(overrides))
	for _, override := range overrides {
		if override.RepositoryID == "" || override.Mount == "" {
			return nil, fmt.Errorf("mount override requires repository ID and mount")
		}
		if _, exists := mounts[override.RepositoryID]; exists {
			return nil, fmt.Errorf("repository %q has multiple mount overrides", override.RepositoryID)
		}
		mounts[override.RepositoryID] = override.Mount
	}
	return mounts, nil
}

func plannedRoot(projectID string, request WorkspacePlanRequest) (string, error) {
	path := request.TargetPath
	if path == "" {
		if request.WorktreeRoot == "" {
			return "", fmt.Errorf("worktree root is required when --path is not supplied")
		}
		path = filepath.Join(request.WorktreeRoot, projectID, pathutil.StorageName(request.WorkspaceName))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make workspace path absolute: %w", err)
	}
	return filepath.Clean(abs), nil
}

func checkTargetPath(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace path %q is an existing symlink", path)
		}
		return fmt.Errorf("workspace path %q already exists", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect workspace path %q: %w", path, err)
	}
	parent := filepath.Dir(path)
	for {
		info, err := os.Stat(parent)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("workspace parent %q is not a directory", parent)
			}
			if info.Mode().Perm()&0o222 == 0 {
				return fmt.Errorf("workspace parent %q is not writable", parent)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect workspace parent %q: %w", parent, err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return fmt.Errorf("no existing workspace parent for %q", path)
		}
		parent = next
	}
}

func checkWorkspaceCollision(projectID, dataDir string, operation plan.Operation, name, rootPath string) error {
	if dataDir == "" {
		return nil
	}
	directory := WorkspaceStateDirectory(dataDir, projectID)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return NewError(ErrorValidation, fmt.Errorf("read workspace state: %w", err))
	}
	storageID := pathutil.StorageName(name)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		state, err := store.ReadWorkspace(filepath.Join(directory, entry.Name()))
		if err != nil {
			return NewError(ErrorValidation, fmt.Errorf("read workspace state %q: %w", entry.Name(), err))
		}
		if operation == plan.Checkout && (state.Name == name || state.ID == storageID) {
			continue
		}
		if state.Name == name || state.ID == storageID || filepath.Clean(state.Path) == rootPath {
			return NewError(ErrorConflict, fmt.Errorf("workspace %q is already registered", name))
		}
	}
	return nil
}

func checkSourceMountConflict(project domain.Project, repository domain.Repository, overrides []MountOverride) error {
	if repository.ParentID == "" {
		return nil
	}
	parents := make(map[string]domain.Repository, len(project.Repositories))
	for _, candidate := range project.Repositories {
		parents[candidate.ID] = candidate
	}
	parent := parents[repository.ParentID]
	// The source checkout is the best available pre-mutation view of parent
	// content. Its configured nested source is permitted; other existing
	// content at a planned mount would be obscured by an added worktree.
	mount := repository.DefaultMount
	for _, override := range overrides {
		if override.RepositoryID == repository.ID {
			mount = override.Mount
			break
		}
	}
	candidate := filepath.Join(parent.SourcePath, filepath.FromSlash(mount))
	info, err := os.Lstat(candidate)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect parent content at %q: %w", candidate, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("parent repository %q contains a symlink at nested mount %q", parent.ID, mount)
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err == nil && canonical == repository.SourcePath {
		return nil
	}
	return fmt.Errorf("parent repository %q contains content at nested mount %q", parent.ID, mount)
}

func effectiveMount(repository domain.Repository, mounts map[string]string) string {
	if mount, exists := mounts[repository.ID]; exists {
		return mount
	}
	return repository.DefaultMount
}

func planSteps(operation plan.Operation, repositories []plan.RepositoryPlan) []plan.Step {
	steps := make([]plan.Step, 0, len(repositories)*2)
	for _, repository := range repositories {
		if operation == plan.Create {
			steps = append(steps, plan.Step{Action: plan.CreateBranch, RepositoryID: repository.ID, Inverse: plan.DeleteBranch})
		}
		steps = append(steps, plan.Step{Action: plan.AddWorktree, RepositoryID: repository.ID, Inverse: plan.RemoveWorktree})
	}
	return steps
}
