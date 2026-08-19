package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
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
	var err error
	project, err = normalizeProjectMounts(project)
	if err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, err)
	}
	if request.Operation != plan.Create && request.Operation != plan.Checkout {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, fmt.Errorf("unsupported workspace operation %q", request.Operation))
	}
	mounts, err := mountMap(request.Mounts)
	if err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, err)
	}
	mounts, err = normalizeMountOverrides(project, mounts)
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
	if err := p.preflightRepositories(ctx, project, request, mounts, paths); err != nil {
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
		mount := effectiveMount(repository, mounts)
		repositories = append(repositories, plan.RepositoryPlan{
			ID: repository.ID, ParentID: repository.ParentID, Base: resolvedBase,
			Branch: request.WorkspaceName, Mount: mount, Path: paths[repository.ID],
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
	if _, err := WorkspacePlanIgnoreEnsures(value); err != nil {
		return plan.WorkspacePlan{}, NewError(ErrorValidation, fmt.Errorf("derive workspace ignore ensures: %w", err))
	}
	return value, nil
}

func (p *WorkspacePlanner) preflightRepositories(ctx context.Context, project domain.Project, request WorkspacePlanRequest, mounts map[string]string, paths map[string]string) error {
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
		if err := checkSourceMountConflict(project, repository, mounts); err != nil {
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

func normalizeMountOverrides(project domain.Project, mounts map[string]string) (map[string]string, error) {
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	normalized := make(map[string]string, len(mounts))
	for id, mount := range mounts {
		repository, exists := repositories[id]
		if !exists {
			return nil, fmt.Errorf("mount override has unknown repository %q", id)
		}
		value, err := pathutil.NormalizeMount(mount, repository.ParentID == "")
		if err != nil {
			return nil, fmt.Errorf("repository %q mount: %w", id, err)
		}
		normalized[id] = value
	}
	return normalized, nil
}

func normalizeProjectMounts(project domain.Project) (domain.Project, error) {
	normalized := project
	normalized.Repositories = append([]domain.Repository(nil), project.Repositories...)
	for index := range normalized.Repositories {
		repository := &normalized.Repositories[index]
		mount, err := pathutil.NormalizeMount(repository.DefaultMount, repository.ParentID == "")
		if err != nil {
			return domain.Project{}, fmt.Errorf("normalize repository %q default mount: %w", repository.ID, err)
		}
		repository.DefaultMount = mount
	}
	return normalized, nil
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

func checkSourceMountConflict(project domain.Project, repository domain.Repository, overrides map[string]string) error {
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
	if override, exists := overrides[repository.ID]; exists {
		mount = override
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

// IgnoreEnsure names the root .gitignore in one planned parent worktree and
// the literal direct-child rules that create execution must ensure there. It
// is derived from the public version-one repository entries; it is deliberately
// not a workspace-plan field or action.
type IgnoreEnsure struct {
	ParentRepositoryID string
	Path               string
	Rules              []string
}

// WorkspacePlanIgnoreEnsures projects normalized non-root repository entries
// into parent-file requirements. It performs no filesystem or Git inspection.
func WorkspacePlanIgnoreEnsures(value plan.WorkspacePlan) ([]IgnoreEnsure, error) {
	repositories := make(map[string]plan.RepositoryPlan, len(value.Repositories))
	for _, repository := range value.Repositories {
		if repository.ID == "" || repository.Path == "" {
			return nil, fmt.Errorf("workspace plan repository ID and path are required")
		}
		if _, exists := repositories[repository.ID]; exists {
			return nil, fmt.Errorf("workspace plan has duplicate repository %q", repository.ID)
		}
		repositories[repository.ID] = repository
	}

	type requirement struct {
		childID string
		rule    string
	}
	type group struct {
		parentID string
		path     string
		depth    int
		requires []requirement
	}
	groups := make(map[string]*group)
	for _, repository := range value.Repositories {
		if repository.ParentID == "" {
			continue
		}
		parent, found := repositories[repository.ParentID]
		if !found || parent.ID == repository.ID {
			return nil, fmt.Errorf("repository %q has unknown or invalid parent %q", repository.ID, repository.ParentID)
		}
		mount, err := pathutil.NormalizeMount(repository.Mount, false)
		if err != nil {
			return nil, fmt.Errorf("repository %q mount: %w", repository.ID, err)
		}
		if mount != repository.Mount {
			return nil, fmt.Errorf("repository %q mount %q is not normalized", repository.ID, repository.Mount)
		}
		rule, err := NestedDirectoryRule(mount)
		if err != nil {
			return nil, fmt.Errorf("repository %q mount: %w", repository.ID, err)
		}
		item := groups[parent.ID]
		if item == nil {
			item = &group{parentID: parent.ID, path: filepath.Join(parent.Path, ".gitignore")}
			groups[parent.ID] = item
		}
		item.requires = append(item.requires, requirement{childID: repository.ID, rule: rule})
	}

	for _, item := range groups {
		depth, err := workspaceRepositoryDepth(item.parentID, repositories)
		if err != nil {
			return nil, err
		}
		item.depth = depth
		sort.Slice(item.requires, func(left, right int) bool {
			return item.requires[left].childID < item.requires[right].childID
		})
	}
	ordered := make([]*group, 0, len(groups))
	for _, item := range groups {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].depth != ordered[right].depth {
			return ordered[left].depth < ordered[right].depth
		}
		return ordered[left].parentID < ordered[right].parentID
	})
	ensures := make([]IgnoreEnsure, 0, len(ordered))
	for _, item := range ordered {
		ensure := IgnoreEnsure{ParentRepositoryID: item.parentID, Path: item.path, Rules: make([]string, 0, len(item.requires))}
		for _, requirement := range item.requires {
			ensure.Rules = append(ensure.Rules, requirement.rule)
		}
		ensures = append(ensures, ensure)
	}
	return ensures, nil
}

func workspaceRepositoryDepth(id string, repositories map[string]plan.RepositoryPlan) (int, error) {
	depth, seen := 0, map[string]bool{}
	for id != "" {
		if seen[id] {
			return 0, fmt.Errorf("workspace plan repositories contain a cycle at %q", id)
		}
		seen[id] = true
		repository, found := repositories[id]
		if !found {
			return 0, fmt.Errorf("workspace plan has unknown repository %q", id)
		}
		if repository.ParentID == "" {
			return depth, nil
		}
		depth++
		id = repository.ParentID
	}
	return depth, nil
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
