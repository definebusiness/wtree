package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
)

// WorkspaceSelectionMode defines the only matching rules available to callers
// that intentionally select a workspace by user-supplied text.
type WorkspaceSelectionMode string

const (
	WorkspaceSelectionSubstring WorkspaceSelectionMode = "substring"
	WorkspaceSelectionExact     WorkspaceSelectionMode = "exact"
)

// WorkspaceSelectionPolicy defines whether a command can use a retained
// workspace whose recorded checkouts have all been removed. Checkout retains
// such state so it can restore it; explicit read-only navigation does not.
type WorkspaceSelectionPolicy string

const (
	WorkspaceSelectionEligible WorkspaceSelectionPolicy = "eligible"
	WorkspaceSelectionCheckout WorkspaceSelectionPolicy = "checkout"
)

// WorkspaceSelectionRequest contains the already-resolved project inventory
// boundary and command-specific matching policy. The selector never resolves
// project configuration, writes state, or discovers branch names.
type WorkspaceSelectionRequest struct {
	DataDir string
	Query   string
	Mode    WorkspaceSelectionMode
	Policy  WorkspaceSelectionPolicy
}

// WorkspaceSelectionCandidate is the stable, minimal identity exposed with a
// lookup failure. Candidates are always sorted by name and then ID.
type WorkspaceSelectionCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WorkspaceSelectionError supplies structured selection facts without asking
// callers to parse diagnostic text. Candidates is always a non-nil slice.
type WorkspaceSelectionError struct {
	Query      string
	Mode       WorkspaceSelectionMode
	Candidates []WorkspaceSelectionCandidate
	checkout   bool
}

// WorkspaceCheckoutSelection keeps the authority selected for checkout until
// reconciliation and the transaction's locked revalidation. Workspace is nil
// only for the explicit exact-branch path, where Precondition instead records
// that no exact workspace state existed at selection time.
type WorkspaceCheckoutSelection struct {
	Workspace    *domain.Workspace
	Precondition *WorkspaceCheckoutPrecondition
}

// WorkspaceCheckoutPrecondition is intentionally an internal service
// capability. A selected workspace pins the exact state-file generation that
// supplied its canonical name and retained mounts. A branch-only checkout pins
// the absence of an exact workspace selector so later state cannot retarget it.
type WorkspaceCheckoutPrecondition struct {
	dataDir       string
	projectID     string
	workspaceName string
	selected      *cloneFileSnapshot
	branchOnly    bool
}

func (p *WorkspaceCheckoutPrecondition) revalidate(project domain.Project) error {
	if p == nil {
		return nil
	}
	if p.projectID != project.ID {
		return NewError(ErrorConflict, errors.New("checkout selection project changed"))
	}
	if p.selected != nil {
		if err := revalidateCloneFileSnapshot(*p.selected); err != nil {
			return NewError(ErrorConflict, fmt.Errorf("selected workspace state changed: %w", err))
		}
		return nil
	}
	if p.branchOnly {
		_, found, err := FindWorkspace(project, p.dataDir, p.workspaceName)
		if err != nil {
			return err
		}
		if found {
			return NewError(ErrorConflict, fmt.Errorf("workspace selector %q appeared after exact branch checkout was selected", p.workspaceName))
		}
	}
	return nil
}

func (e *WorkspaceSelectionError) Error() string {
	if e == nil || len(e.Candidates) == 0 {
		if e != nil && e.checkout && e.Mode == WorkspaceSelectionSubstring {
			return fmt.Sprintf("workspace selector %q was not found; use checkout --exact <full-branch-name> for an existing local branch", e.Query)
		}
		return fmt.Sprintf("workspace selector %q was not found", e.Query)
	}
	candidates := make([]string, 0, len(e.Candidates))
	for _, candidate := range e.Candidates {
		candidates = append(candidates, fmt.Sprintf("%q (%q)", candidate.Name, candidate.ID))
	}
	return fmt.Sprintf("workspace selector %q is ambiguous among %s; narrow the query or use --exact", e.Query, strings.Join(candidates, ", "))
}

type workspaceRootDependencies struct {
	lstat      func(string) (fs.FileInfo, error)
	accessible func(string) error
}

// ValidateWorkspaceRoot confirms that a selected workspace has an accessible,
// usable logical root for scalar path output. It deliberately does not inspect
// the rest of the checkout health, which remains status and doctor responsibility.
func ValidateWorkspaceRoot(workspace domain.Workspace) error {
	return validateWorkspaceRootWithDependencies(workspace, workspaceRootDependencies{lstat: os.Lstat, accessible: workspaceRootAccessible})
}

func validateWorkspaceRootWithDependencies(workspace domain.Workspace, dependencies workspaceRootDependencies) error {
	info, err := dependencies.lstat(workspace.RootPath)
	if err != nil {
		return NewError(ErrorValidation, fmt.Errorf("inspect workspace root %q: %w", workspace.RootPath, err))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return NewError(ErrorValidation, fmt.Errorf("workspace root %q is a symlink", workspace.RootPath))
	}
	if !info.IsDir() {
		return NewError(ErrorValidation, fmt.Errorf("workspace root %q is not a directory", workspace.RootPath))
	}
	if err := dependencies.accessible(workspace.RootPath); err != nil {
		return NewError(ErrorValidation, fmt.Errorf("access workspace root %q: %w", workspace.RootPath, err))
	}
	return nil
}

func workspaceRootAccessible(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	_, err = directory.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

type workspaceSelectionDependencies struct {
	git   gitadapter.Git
	lstat func(string) (fs.FileInfo, error)
}

// WorkspaceSelector selects only validated persisted workspace state.
// Its observations are read-only and scoped to one Select invocation.
type WorkspaceSelector struct {
	dependencies workspaceSelectionDependencies
}

func NewWorkspaceSelector() *WorkspaceSelector {
	return NewWorkspaceSelectorWith(gitadapter.NewAdapter("git"))
}

// NewWorkspaceSelectorWith permits focused callers and tests to provide the
// existing local Git fact boundary. The selector only invokes ListWorktrees.
func NewWorkspaceSelectorWith(git gitadapter.Git) *WorkspaceSelector {
	return newWorkspaceSelectorWithDependencies(workspaceSelectionDependencies{git: git, lstat: os.Lstat})
}

func newWorkspaceSelectorWithDependencies(dependencies workspaceSelectionDependencies) *WorkspaceSelector {
	if dependencies.lstat == nil {
		dependencies.lstat = os.Lstat
	}
	return &WorkspaceSelector{dependencies: dependencies}
}

// Select resolves query to one canonical workspace. Exact mode compares full
// names and persisted IDs; substring mode compares only full names. Eligible
// policy excludes a workspace only after proving every recorded checkout is
// absent and no configured Git repository still registers any checkout path.
func (s *WorkspaceSelector) Select(ctx context.Context, project domain.Project, request WorkspaceSelectionRequest) (domain.Workspace, error) {
	if s == nil || s.dependencies.git == nil {
		return domain.Workspace{}, NewError(ErrorInternal, errors.New("workspace selector is not configured"))
	}
	if request.Query == "" {
		return domain.Workspace{}, NewError(ErrorInvalidArguments, errors.New("workspace selector is required"))
	}
	if request.Mode != WorkspaceSelectionSubstring && request.Mode != WorkspaceSelectionExact {
		return domain.Workspace{}, NewError(ErrorInternal, fmt.Errorf("unsupported workspace selection mode %q", request.Mode))
	}
	if request.Policy != WorkspaceSelectionEligible && request.Policy != WorkspaceSelectionCheckout {
		return domain.Workspace{}, NewError(ErrorInternal, fmt.Errorf("unsupported workspace selection policy %q", request.Policy))
	}

	workspaces, err := ListWorkspaces(project, request.DataDir)
	if err != nil {
		return domain.Workspace{}, err
	}
	return s.selectFromInventory(ctx, project, request, workspaces)
}

func (s *WorkspaceSelector) selectFromInventory(ctx context.Context, project domain.Project, request WorkspaceSelectionRequest, workspaces []domain.Workspace) (domain.Workspace, error) {
	if s == nil || s.dependencies.git == nil {
		return domain.Workspace{}, NewError(ErrorInternal, errors.New("workspace selector is not configured"))
	}
	if request.Query == "" {
		return domain.Workspace{}, NewError(ErrorInvalidArguments, errors.New("workspace selector is required"))
	}
	if request.Mode != WorkspaceSelectionSubstring && request.Mode != WorkspaceSelectionExact {
		return domain.Workspace{}, NewError(ErrorInternal, fmt.Errorf("unsupported workspace selection mode %q", request.Mode))
	}
	if request.Policy != WorkspaceSelectionEligible && request.Policy != WorkspaceSelectionCheckout {
		return domain.Workspace{}, NewError(ErrorInternal, fmt.Errorf("unsupported workspace selection policy %q", request.Policy))
	}
	candidates := make([]domain.Workspace, 0, len(workspaces))
	for _, workspace := range workspaces {
		if workspaceSelectionMatches(workspace, request.Query, request.Mode) {
			candidates = append(candidates, workspace)
		}
	}
	if request.Policy == WorkspaceSelectionEligible {
		cache := make(map[string][]gitadapter.Worktree)
		eligible := candidates[:0]
		for _, workspace := range candidates {
			removed, observeErr := s.workspaceRemoved(ctx, project, workspace, cache)
			if observeErr != nil {
				return domain.Workspace{}, observeErr
			}
			if !removed {
				eligible = append(eligible, workspace)
			}
		}
		candidates = eligible
	}

	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].Name == candidates[right].Name {
			return candidates[left].ID < candidates[right].ID
		}
		return candidates[left].Name < candidates[right].Name
	})
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	detail := &WorkspaceSelectionError{Query: request.Query, Mode: request.Mode, Candidates: selectionCandidates(candidates), checkout: request.Policy == WorkspaceSelectionCheckout}
	if len(candidates) == 0 {
		return domain.Workspace{}, NewError(ErrorWorkspaceNotFound, detail)
	}
	return domain.Workspace{}, NewError(ErrorConflict, detail)
}

// SelectCheckout captures the selected state's file generation along with the
// canonical workspace. It deliberately uses the normal checkout policy, which
// includes retained removed workspaces. Exact callers may turn only a typed
// no-match into an exact local-branch checkout via SelectExactBranch.
func (s *WorkspaceSelector) SelectCheckout(ctx context.Context, project domain.Project, request WorkspaceSelectionRequest) (WorkspaceCheckoutSelection, error) {
	// Request validation precedes the checkout-specific inventory capture so an
	// invalid explicit selector remains deterministic even when persisted state
	// is damaged.
	if request.Query == "" {
		return WorkspaceCheckoutSelection{}, NewError(ErrorInvalidArguments, errors.New("workspace selector is required"))
	}
	if request.Mode != WorkspaceSelectionSubstring && request.Mode != WorkspaceSelectionExact {
		return WorkspaceCheckoutSelection{}, NewError(ErrorInternal, fmt.Errorf("unsupported workspace selection mode %q", request.Mode))
	}
	if request.Policy != WorkspaceSelectionCheckout {
		return WorkspaceCheckoutSelection{}, NewError(ErrorInternal, errors.New("checkout selection requires checkout policy"))
	}
	captured, err := captureWorkspaceInventory(project, request.DataDir)
	if err != nil {
		return WorkspaceCheckoutSelection{}, err
	}
	workspaces := make([]domain.Workspace, 0, len(captured))
	for _, entry := range captured {
		workspaces = append(workspaces, entry.workspace)
	}
	workspace, err := s.selectFromInventory(ctx, project, request, workspaces)
	if err != nil {
		return WorkspaceCheckoutSelection{}, err
	}
	var snapshot cloneFileSnapshot
	found := false
	for _, entry := range captured {
		if entry.workspace.ID == workspace.ID && entry.workspace.Name == workspace.Name {
			snapshot, found = entry.snapshot, true
			break
		}
	}
	if !found {
		return WorkspaceCheckoutSelection{}, NewError(ErrorConflict, fmt.Errorf("selected workspace %q disappeared from captured inventory", workspace.Name))
	}
	state, err := store.DecodeWorkspace(snapshot.data)
	if err != nil {
		return WorkspaceCheckoutSelection{}, NewError(ErrorConflict, fmt.Errorf("decode selected workspace state %q: %w", workspace.Name, err))
	}
	frozen, err := workspaceFromState(state)
	if err != nil {
		return WorkspaceCheckoutSelection{}, NewError(ErrorConflict, fmt.Errorf("decode selected workspace %q: %w", workspace.Name, err))
	}
	if err := frozen.Validate(project); err != nil {
		return WorkspaceCheckoutSelection{}, NewError(ErrorConflict, fmt.Errorf("validate selected workspace %q: %w", workspace.Name, err))
	}
	if frozen.ID != workspace.ID || frozen.Name != workspace.Name {
		return WorkspaceCheckoutSelection{}, NewError(ErrorConflict, fmt.Errorf("selected workspace %q changed while captured", workspace.Name))
	}
	return WorkspaceCheckoutSelection{Workspace: &frozen, Precondition: &WorkspaceCheckoutPrecondition{
		dataDir: request.DataDir, projectID: project.ID, workspaceName: frozen.Name, selected: &snapshot,
	}}, nil
}

// SelectExactBranch records the absence condition which authorizes the legacy
// exact local-branch path. It is never used for default matching.
func (s *WorkspaceSelector) SelectExactBranch(ctx context.Context, project domain.Project, dataDir, branch string) (WorkspaceCheckoutSelection, error) {
	if branch == "" {
		return WorkspaceCheckoutSelection{}, NewError(ErrorInvalidArguments, errors.New("workspace selector is required"))
	}
	captured, captureErr := captureWorkspaceInventory(project, dataDir)
	if captureErr != nil {
		return WorkspaceCheckoutSelection{}, captureErr
	}
	workspaces := make([]domain.Workspace, 0, len(captured))
	for _, entry := range captured {
		workspaces = append(workspaces, entry.workspace)
	}
	if _, err := s.selectFromInventory(ctx, project, WorkspaceSelectionRequest{DataDir: dataDir, Query: branch, Mode: WorkspaceSelectionExact, Policy: WorkspaceSelectionCheckout}, workspaces); err != nil {
		var application *Error
		if !errors.As(err, &application) || application.Kind != ErrorWorkspaceNotFound {
			return WorkspaceCheckoutSelection{}, err
		}
		return WorkspaceCheckoutSelection{Precondition: &WorkspaceCheckoutPrecondition{
			dataDir: dataDir, projectID: project.ID, workspaceName: branch, branchOnly: true,
		}}, nil
	}
	return WorkspaceCheckoutSelection{}, NewError(ErrorConflict, fmt.Errorf("workspace selector %q is not absent", branch))
}

func workspaceSelectionMatches(workspace domain.Workspace, query string, mode WorkspaceSelectionMode) bool {
	if mode == WorkspaceSelectionExact {
		return workspace.Name == query || workspace.ID == query
	}
	return strings.Contains(workspace.Name, query)
}

func selectionCandidates(workspaces []domain.Workspace) []WorkspaceSelectionCandidate {
	values := make([]WorkspaceSelectionCandidate, 0, len(workspaces))
	for _, workspace := range workspaces {
		values = append(values, WorkspaceSelectionCandidate{ID: workspace.ID, Name: workspace.Name})
	}
	return values
}

func (s *WorkspaceSelector) workspaceRemoved(ctx context.Context, project domain.Project, workspace domain.Workspace, cache map[string][]gitadapter.Worktree) (bool, error) {
	if len(workspace.Checkouts) == 0 {
		return false, NewError(ErrorValidation, fmt.Errorf("workspace %q has no recorded checkouts", workspace.Name))
	}
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	for _, checkout := range workspace.Checkouts {
		absent, err := s.definitelyAbsent(checkout.ResolvedPath)
		if err != nil {
			return false, NewError(ErrorInternal, fmt.Errorf("inspect workspace checkout %q: %w", checkout.ResolvedPath, err))
		}
		if !absent {
			return false, nil
		}
		repository, found := repositories[checkout.RepositoryID]
		if !found {
			return false, NewError(ErrorValidation, fmt.Errorf("workspace %q checkout has unknown repository %q", workspace.Name, checkout.RepositoryID))
		}
		worktrees, found := cache[repository.SourcePath]
		if !found {
			var listErr error
			worktrees, listErr = s.dependencies.git.ListWorktrees(ctx, repository.SourcePath)
			if listErr != nil {
				return false, NewError(ErrorGit, fmt.Errorf("list worktrees for repository %q: %w", repository.ID, listErr))
			}
			cache[repository.SourcePath] = worktrees
		}
		for _, worktree := range worktrees {
			if sameCheckoutPath(worktree.Path, checkout.ResolvedPath) {
				return false, nil
			}
		}
	}
	return true, nil
}

// definitelyAbsent follows no symlinks. A present node, including a dangling
// symlink, is damage rather than absence. A symlinked ancestor is likewise
// insufficient proof because it may resolve after the observation.
func (s *WorkspaceSelector) definitelyAbsent(path string) (bool, error) {
	if path == "" {
		return false, errors.New("checkout path is empty")
	}
	_, err := s.dependencies.lstat(path)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	missing := []string{}
	for current := filepath.Clean(path); ; {
		parent := filepath.Dir(current)
		if parent == current {
			return true, nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
		info, parentErr := s.dependencies.lstat(current)
		if parentErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				canonical, canonicalErr := filepath.EvalSymlinks(current)
				if canonicalErr != nil {
					return false, nil
				}
				candidate := canonical
				for index := len(missing) - 1; index >= 0; index-- {
					candidate = filepath.Join(candidate, missing[index])
				}
				return s.definitelyAbsent(candidate)
			}
			return true, nil
		}
		if !errors.Is(parentErr, fs.ErrNotExist) {
			return false, parentErr
		}
	}
}
