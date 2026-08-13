package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/marcel/wtree/internal/domain"
	gitadapter "github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/lock"
	"github.com/marcel/wtree/internal/store"
)

// DoctorFinding is a stable, machine-readable diagnosis. Severity is one of
// info, warning, or error; only findings explicitly marked fixable may be
// changed by DoctorFix.
type DoctorFinding struct {
	Code         string `json:"code"`
	Severity     string `json:"severity"`
	RepositoryID string `json:"repositoryId,omitempty"`
	Message      string `json:"message"`
	Fixable      bool   `json:"fixable,omitempty"`
}

type DoctorRepair struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type DoctorReport struct {
	ProjectID string          `json:"projectId"`
	Workspace string          `json:"workspace"`
	Findings  []DoctorFinding `json:"findings"`
	Repairs   []DoctorRepair  `json:"repairs,omitempty"`
	Fixed     bool            `json:"fixed,omitempty"`
	DryRun    bool            `json:"dryRun,omitempty"`
}

type DoctorFixRequest struct {
	DataDir string
	DryRun  bool
}

// DoctorService detects drift and performs only the M15 allowlisted repairs.
type DoctorService struct {
	git            gitadapter.Git
	locker         ProjectLocker
	writeWorkspace func(string, store.WorkspaceState) error
}

func NewDoctorService() *DoctorService {
	return NewDoctorServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteWorkspace)
}

func NewDoctorServiceWith(git gitadapter.Git, locker ProjectLocker, writeWorkspace func(string, store.WorkspaceState) error) *DoctorService {
	return &DoctorService{git: git, locker: locker, writeWorkspace: writeWorkspace}
}

// Doctor is strictly read-only. It observes source identities, persisted
// checkout metadata, discovered worktrees, and unresolved recovery records.
func (d *DoctorService) Doctor(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir string) (DoctorReport, error) {
	if d == nil || d.git == nil {
		return DoctorReport{}, NewError(ErrorInternal, errors.New("doctor is not configured"))
	}
	if err := project.Validate(); err != nil {
		return DoctorReport{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	report := DoctorReport{ProjectID: project.ID, Workspace: workspace.Name}
	registry, registryErr := readRegistry(filepath.Join(dataDir, "registry.json"))
	if registryErr != nil {
		report.Findings = append(report.Findings, DoctorFinding{Code: "invalid-registry", Severity: "error", Message: "project registry cannot be read"})
	} else if registered, exists := registry.Projects[project.ID]; !exists || registered.ConfigPath != project.ConfigPath || !sameRepositoryIDs(registered.RepositoryIDs, repositoryIDs(project)) {
		report.Findings = append(report.Findings, DoctorFinding{Code: "stale-registry", Severity: "warning", Message: "project registry does not match verified project configuration"})
	}
	for _, repository := range project.ParentFirst() {
		identity, err := d.git.CommonGitDir(ctx, repository.SourcePath)
		if err != nil || identity != repository.CommonGitDir {
			report.Findings = append(report.Findings, DoctorFinding{Code: "source-identity-mismatch", Severity: "error", RepositoryID: repository.ID, Message: "configured source checkout is unavailable or has a different Git identity"})
		}
	}
	observed, duplicates, unknown, err := d.discover(ctx, project, workspace.RootPath)
	if err != nil {
		return DoctorReport{}, err
	}
	for _, path := range unknown {
		report.Findings = append(report.Findings, DoctorFinding{Code: "unknown-repository", Severity: "error", Message: "unknown nested Git checkout at " + path})
	}
	for _, id := range duplicates {
		report.Findings = append(report.Findings, DoctorFinding{Code: "duplicate-checkout", Severity: "error", RepositoryID: id, Message: "configured repository appears more than once"})
	}
	checkouts := workspaceCheckoutMap(workspace)
	pathRepair := false
	pruneNeeded := false
	for _, repository := range project.ParentFirst() {
		checkout, persisted := checkouts[repository.ID]
		actual, found := observed[repository.ID]
		if !persisted {
			report.Findings = append(report.Findings, DoctorFinding{Code: "stale-state", Severity: "error", RepositoryID: repository.ID, Message: "workspace state omits configured repository"})
			continue
		}
		if !found {
			report.Findings = append(report.Findings, DoctorFinding{Code: "missing-checkout", Severity: "warning", RepositoryID: repository.ID, Message: "persisted checkout is not present"})
			registered, registrationErr := d.missingWorktreeRegistration(ctx, repository.SourcePath, checkout.ResolvedPath)
			if registrationErr != nil {
				return DoctorReport{}, registrationErr
			}
			if registered {
				pruneNeeded = true
				report.Findings = append(report.Findings, DoctorFinding{Code: "stale-worktree-registration", Severity: "warning", RepositoryID: repository.ID, Message: "Git still registers a missing checkout", Fixable: true})
			}
			continue
		}
		expected, expectedErr := workspace.ResolveRepository(repository.ID)
		if expectedErr != nil || !sameCheckoutPath(expected, actual) || !sameCheckoutPath(checkout.ResolvedPath, actual) {
			pathRepair = true
			report.Findings = append(report.Findings, DoctorFinding{Code: "mount-mismatch", Severity: "warning", RepositoryID: repository.ID, Message: "Git identity matches at a different persisted path or mount", Fixable: true})
		}
		branch, detached, branchErr := d.git.CurrentBranch(ctx, actual)
		if branchErr != nil {
			return DoctorReport{}, NewError(ErrorGit, fmt.Errorf("read branch for %q: %w", repository.ID, branchErr))
		}
		if detached != checkout.Detached || (!detached && branch != checkout.Branch) {
			report.Findings = append(report.Findings, DoctorFinding{Code: "branch-mismatch", Severity: "warning", RepositoryID: repository.ID, Message: "actual Git branch or detached state differs from workspace state"})
		}
		head, headErr := d.git.Head(ctx, actual)
		if headErr != nil {
			return DoctorReport{}, NewError(ErrorGit, fmt.Errorf("read HEAD for %q: %w", repository.ID, headErr))
		}
		if head != checkout.Head {
			report.Findings = append(report.Findings, DoctorFinding{Code: "head-mismatch", Severity: "warning", RepositoryID: repository.ID, Message: "actual Git HEAD differs from workspace state"})
		}
	}
	if pathRepair {
		report.Repairs = append(report.Repairs, DoctorRepair{Code: "repair-mount-metadata", Message: "update verified checkout paths and mounts"})
	}
	if pruneNeeded {
		report.Repairs = append(report.Repairs, DoctorRepair{Code: "prune-worktree-metadata", Message: "prune Git registrations for missing checkouts"})
	}
	if hasRecovery(dataDir, project.ID, workspace.ID) {
		report.Findings = append(report.Findings, DoctorFinding{Code: "recovery-record", Severity: "error", Message: "an incomplete rollback recovery record requires manual action"})
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].RepositoryID == report.Findings[j].RepositoryID {
			return report.Findings[i].Code < report.Findings[j].Code
		}
		return report.Findings[i].RepositoryID < report.Findings[j].RepositoryID
	})
	return report, nil
}

// Fix applies only planned allowlisted repairs after holding the project lock.
func (d *DoctorService) Fix(ctx context.Context, project domain.Project, workspace domain.Workspace, request DoctorFixRequest) (DoctorReport, error) {
	report, err := d.Doctor(ctx, project, workspace, request.DataDir)
	if err != nil {
		return DoctorReport{}, err
	}
	report.DryRun = request.DryRun
	if request.DryRun || len(report.Repairs) == 0 {
		return report, nil
	}
	if d.locker == nil || d.writeWorkspace == nil {
		return DoctorReport{}, NewError(ErrorInternal, errors.New("doctor repair is not configured"))
	}
	handle, err := d.locker.ProjectLock(ctx, request.DataDir, project.ID, time.Second)
	if err != nil {
		return DoctorReport{}, NewError(ErrorConflict, fmt.Errorf("acquire project mutation lock: %w", err))
	}
	defer handle.Unlock()
	report, err = d.Doctor(ctx, project, workspace, request.DataDir)
	if err != nil {
		return DoctorReport{}, err
	}
	if hasRepair(report, "prune-worktree-metadata") {
		for _, repository := range project.Repositories {
			if err := d.git.WorktreePrune(ctx, repository.SourcePath); err != nil {
				return DoctorReport{}, NewError(ErrorGit, fmt.Errorf("prune worktree metadata for %q: %w", repository.ID, err))
			}
		}
		report.Fixed = true
	}
	if hasRepair(report, "repair-mount-metadata") {
		updated, err := d.repairedWorkspace(ctx, project, workspace)
		if err != nil {
			return DoctorReport{}, err
		}
		if err := d.writeWorkspace(WorkspaceStatePath(request.DataDir, project.ID, workspace.ID), doctorWorkspaceState(updated)); err != nil {
			return DoctorReport{}, NewError(ErrorInternal, fmt.Errorf("write repaired workspace state: %w", err))
		}
		report.Fixed = true
	}
	return report, nil
}

func (d *DoctorService) missingWorktreeRegistration(ctx context.Context, source, path string) (bool, error) {
	worktrees, err := d.git.ListWorktrees(ctx, source)
	if err != nil {
		return false, NewError(ErrorGit, fmt.Errorf("list worktrees for %q: %w", source, err))
	}
	for _, worktree := range worktrees {
		if sameCheckoutPath(worktree.Path, path) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (d *DoctorService) discover(ctx context.Context, project domain.Project, root string) (map[string]string, []string, []string, error) {
	identities := map[string]string{}
	for _, repository := range project.Repositories {
		identities[repository.CommonGitDir] = repository.ID
	}
	found := map[string]string{}
	duplicates := []string{}
	unknown := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() || !hasGitMarker(path) {
			return nil
		}
		top, err := d.git.TopLevel(ctx, path)
		if err != nil || !sameCheckoutPath(top, path) {
			return nil
		}
		identity, err := d.git.CommonGitDir(ctx, path)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("read Git identity at %q: %w", path, err))
		}
		id, known := identities[identity]
		if !known {
			unknown = append(unknown, path)
			return nil
		}
		if _, exists := found[id]; exists {
			duplicates = append(duplicates, id)
			return nil
		}
		found[id] = path
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	sort.Strings(duplicates)
	sort.Strings(unknown)
	return found, duplicates, unknown, nil
}

func (d *DoctorService) repairedWorkspace(ctx context.Context, project domain.Project, workspace domain.Workspace) (domain.Workspace, error) {
	observed, duplicates, _, err := d.discover(ctx, project, workspace.RootPath)
	if err != nil {
		return domain.Workspace{}, err
	}
	if len(duplicates) != 0 {
		return domain.Workspace{}, NewError(ErrorValidation, errors.New("cannot repair ambiguous duplicate checkouts"))
	}
	updated := workspace
	paths := map[string]string{}
	for _, repository := range project.ParentFirst() {
		actual, ok := observed[repository.ID]
		if !ok {
			continue
		}
		paths[repository.ID] = actual
		for index := range updated.Checkouts {
			if updated.Checkouts[index].RepositoryID != repository.ID {
				continue
			}
			mount := "."
			if repository.ParentID != "" {
				relative, err := filepath.Rel(paths[repository.ParentID], actual)
				if err != nil || relative == "." || filepath.IsAbs(relative) {
					return domain.Workspace{}, NewError(ErrorValidation, fmt.Errorf("cannot derive repaired mount for %q", repository.ID))
				}
				mount = filepath.ToSlash(relative)
			}
			updated.Checkouts[index].Mount, updated.Checkouts[index].ResolvedPath = mount, actual
		}
	}
	if err := updated.Validate(project); err != nil {
		return domain.Workspace{}, NewError(ErrorValidation, fmt.Errorf("validate repaired workspace: %w", err))
	}
	return updated, nil
}

func workspaceCheckoutMap(workspace domain.Workspace) map[string]domain.Checkout {
	values := map[string]domain.Checkout{}
	for _, checkout := range workspace.Checkouts {
		values[checkout.RepositoryID] = checkout
	}
	return values
}
func hasRepair(report DoctorReport, code string) bool {
	for _, repair := range report.Repairs {
		if repair.Code == code {
			return true
		}
	}
	return false
}
func hasRecovery(dataDir, projectID, workspaceID string) bool {
	_, err := os.Stat(filepath.Join(dataDir, "projects", projectID, "recovery", workspaceID+".json"))
	return err == nil
}
func doctorWorkspaceState(workspace domain.Workspace) store.WorkspaceState {
	repositories := map[string]store.CheckoutState{}
	for _, checkout := range workspace.Checkouts {
		repositories[checkout.RepositoryID] = store.CheckoutState{Branch: checkout.Branch, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath, Head: checkout.Head, Detached: checkout.Detached}
	}
	return store.WorkspaceState{ID: workspace.ID, Name: workspace.Name, Path: workspace.RootPath, Partial: workspace.Partial, MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...), Repositories: repositories}
}
