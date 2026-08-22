package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
)

// WorkspaceStatus is a read-only reconciliation of persisted workspace state
// with the checkout paths and Git facts currently present on disk.
type WorkspaceStatus struct {
	Workspace            string             `json:"workspace"`
	LogicalRoot          string             `json:"logicalRoot,omitempty"`
	BaseRepository       string             `json:"baseRepository,omitempty"`
	Partial              bool               `json:"partial,omitempty"`
	MissingRepositoryIDs []string           `json:"missingRepositoryIds,omitempty"`
	Repositories         []RepositoryStatus `json:"repositories"`
}

// RepositoryStatus intentionally keeps ordinary Git dirtiness separate from
// structural workspace drift. Status is one stable summary suitable for the
// human table while the boolean fields preserve the underlying facts in JSON.
type RepositoryStatus struct {
	ID                string `json:"id"`
	ParentID          string `json:"parentId,omitempty"`
	Branch            string `json:"branch,omitempty"`
	ExpectedBranch    string `json:"expectedBranch,omitempty"`
	Head              string `json:"head,omitempty"`
	Mount             string `json:"mount,omitempty"`
	Path              string `json:"path,omitempty"`
	ResolvedPath      string `json:"resolvedPath,omitempty"`
	Clean             bool   `json:"clean"`
	Staged            bool   `json:"staged,omitempty"`
	Modified          bool   `json:"modified,omitempty"`
	Untracked         bool   `json:"untracked,omitempty"`
	Missing           bool   `json:"missing,omitempty"`
	BranchMismatch    bool   `json:"branchMismatch,omitempty"`
	MountMismatch     bool   `json:"mountMismatch,omitempty"`
	Detached          bool   `json:"detached,omitempty"`
	UnknownRepository bool   `json:"unknownRepository,omitempty"`
	StaleState        bool   `json:"staleState,omitempty"`
	Ahead             int    `json:"ahead,omitempty"`
	Behind            int    `json:"behind,omitempty"`
	Upstream          bool   `json:"upstream,omitempty"`
	Status            string `json:"status"`
}

// StatusService owns no mutation operations. Its Git dependency is injected
// so reconciliation can be tested independently of command rendering.
type StatusService struct{ git gitadapter.Git }

func NewStatusService() *StatusService { return NewStatusServiceWith(gitadapter.NewAdapter("git")) }

func NewStatusServiceWith(git gitadapter.Git) *StatusService { return &StatusService{git: git} }

// Status reconciles every configured repository. It deliberately reports
// absent or unexpected checkouts as drift rather than attempting repair.
func (s *StatusService) Status(ctx context.Context, project domain.Project, workspace domain.Workspace) (WorkspaceStatus, error) {
	if err := project.Validate(); err != nil {
		return WorkspaceStatus{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	checkouts := make(map[string]domain.Checkout, len(workspace.Checkouts))
	duplicate := make(map[string]bool)
	for _, checkout := range workspace.Checkouts {
		if _, exists := checkouts[checkout.RepositoryID]; exists {
			duplicate[checkout.RepositoryID] = true
		}
		checkouts[checkout.RepositoryID] = checkout
	}
	missing := make(map[string]bool, len(workspace.MissingRepositoryIDs))
	for _, id := range workspace.MissingRepositoryIDs {
		missing[id] = true
	}
	value := WorkspaceStatus{
		Workspace: workspace.Name, Partial: workspace.Partial,
		LogicalRoot: workspace.RootPath, BaseRepository: project.BaseRepository,
		MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...),
		Repositories:         make([]RepositoryStatus, 0, len(project.Repositories)),
	}
	sort.Strings(value.MissingRepositoryIDs)
	for _, repository := range project.ParentFirst() {
		checkout, found := checkouts[repository.ID]
		if !found {
			status := RepositoryStatus{ID: repository.ID, ParentID: repository.ParentID, Missing: missing[repository.ID], StaleState: !missing[repository.ID], Status: "stale-state"}
			if status.Missing {
				status.Status = "missing"
			}
			value.Repositories = append(value.Repositories, status)
			continue
		}
		checkoutPath, pathErr := workspace.ResolveRepository(repository.ID)
		if pathErr != nil {
			checkoutPath = ""
		}
		childPaths := make([]string, 0)
		for _, child := range project.Repositories {
			if child.ParentID != repository.ID {
				continue
			}
			if _, exists := checkouts[child.ID]; exists {
				if childPath, err := workspace.ResolveRepository(child.ID); err == nil {
					childPaths = append(childPaths, childPath)
				}
			}
		}
		status, err := s.repositoryStatus(ctx, repository, checkout, checkoutPath, duplicate[repository.ID], childPaths)
		if err != nil {
			return WorkspaceStatus{}, err
		}
		value.Repositories = append(value.Repositories, status)
	}
	return value, nil
}

func (s *StatusService) repositoryStatus(ctx context.Context, repository domain.Repository, checkout domain.Checkout, checkoutPath string, stale bool, managedChildPaths []string) (RepositoryStatus, error) {
	status := RepositoryStatus{ID: repository.ID, ParentID: repository.ParentID, ExpectedBranch: checkout.Branch, Mount: checkout.Mount, Path: checkoutPath, ResolvedPath: checkout.ResolvedPath, StaleState: stale}
	if checkout.ResolvedPath == "" || checkout.Head == "" || (checkout.Detached && checkout.Branch != "") || (!checkout.Detached && checkout.Branch == "") {
		status.StaleState = true
	}
	if status.Path == "" {
		status.Status = "stale-state"
		return status, nil
	}
	if _, err := os.Stat(status.Path); err != nil {
		if os.IsNotExist(err) {
			status.Missing, status.Status = true, "missing"
			return status, nil
		}
		return RepositoryStatus{}, NewError(ErrorInternal, fmt.Errorf("stat checkout %q: %w", status.Path, err))
	}
	commonGitDir, err := s.git.CommonGitDir(ctx, status.Path)
	if err != nil || commonGitDir != repository.CommonGitDir {
		status.UnknownRepository, status.Status = true, "unknown-repository"
		return status, nil
	}
	topLevel, err := s.git.TopLevel(ctx, status.Path)
	if err != nil {
		status.UnknownRepository, status.Status = true, "unknown-repository"
		return status, nil
	}
	if !sameCheckoutPath(topLevel, status.Path) {
		status.MountMismatch = true
	}
	branch, detached, err := s.git.CurrentBranch(ctx, status.Path)
	if err != nil {
		return RepositoryStatus{}, NewError(ErrorGit, fmt.Errorf("read branch for %q: %w", repository.ID, err))
	}
	status.Branch, status.Detached = branch, detached
	if detached != checkout.Detached || (!detached && branch != checkout.Branch) {
		if detached {
			status.Detached = true
		} else {
			status.BranchMismatch = true
		}
	}
	head, err := s.git.Head(ctx, status.Path)
	if err != nil {
		return RepositoryStatus{}, NewError(ErrorGit, fmt.Errorf("read HEAD for %q: %w", repository.ID, err))
	}
	status.Head = head
	gitStatus, err := s.git.Status(ctx, status.Path)
	if err != nil {
		return RepositoryStatus{}, NewError(ErrorGit, fmt.Errorf("read status for %q: %w", repository.ID, err))
	}
	gitStatus = withoutManagedChildEntries(gitStatus, status.Path, managedChildPaths)
	status.Clean = len(gitStatus.Entries) == 0
	status.Staged, status.Modified, status.Untracked = gitStatus.Staged, gitStatus.Modified, gitStatus.Untracked
	if !detached {
		ahead, behind, upstream, err := s.git.AheadBehind(ctx, status.Path)
		if err != nil {
			return RepositoryStatus{}, NewError(ErrorGit, fmt.Errorf("read upstream for %q: %w", repository.ID, err))
		}
		status.Ahead, status.Behind, status.Upstream = ahead, behind, upstream
	}
	status.Status = summarizedStatus(status)
	return status, nil
}

func withoutManagedChildEntries(status gitadapter.Status, checkoutPath string, children []string) gitadapter.Status {
	filtered := gitadapter.Status{Entries: make([]gitadapter.StatusEntry, 0, len(status.Entries))}
	for _, entry := range status.Entries {
		if entryIsOnlyWithinManagedChildren(entry, checkoutPath, children) {
			continue
		}
		filtered.Entries = append(filtered.Entries, entry)
		if entry.Untracked {
			filtered.Untracked = true
		} else {
			filtered.Staged = filtered.Staged || entry.Index != ' '
			filtered.Modified = filtered.Modified || entry.Worktree != ' '
		}
	}
	return filtered
}

func entryIsOnlyWithinManagedChildren(entry gitadapter.StatusEntry, checkoutPath string, children []string) bool {
	paths := []string{entry.Path}
	if entry.OriginalPath != "" {
		paths = append(paths, entry.OriginalPath)
	}
	for _, path := range paths {
		if !isManagedChildPath(path, checkoutPath, children) {
			return false
		}
	}
	return true
}

func isManagedChildPath(path, checkoutPath string, children []string) bool {
	path = filepath.ToSlash(path)
	for _, child := range children {
		relative, err := filepath.Rel(checkoutPath, child)
		if err != nil || relative == "." || relative == ".." {
			continue
		}
		relative = filepath.ToSlash(relative)
		if path == relative || len(path) > len(relative) && path[:len(relative)] == relative && path[len(relative)] == '/' {
			return true
		}
	}
	return false
}

func summarizedStatus(status RepositoryStatus) string {
	switch {
	case status.StaleState:
		return "stale-state"
	case status.Missing:
		return "missing"
	case status.UnknownRepository:
		return "unknown-repository"
	case status.MountMismatch:
		return "mount-mismatch"
	case status.Detached:
		return "detached"
	case status.BranchMismatch:
		return "branch-mismatch"
	case !status.Clean:
		return "modified"
	default:
		return "clean"
	}
}

func sameCheckoutPath(left, right string) bool {
	left, leftErr := filepath.EvalSymlinks(left)
	right, rightErr := filepath.EvalSymlinks(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
