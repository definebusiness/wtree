package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
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
	// path is private evidence retained while status projects local operation
	// findings. It deliberately does not change the doctor wire contract.
	path string
}

type DoctorRepair struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type DoctorReport struct {
	ProjectID      string             `json:"projectId"`
	Workspace      string             `json:"workspace"`
	LogicalRoot    string             `json:"logicalRoot,omitempty"`
	BaseRepository string             `json:"baseRepository,omitempty"`
	Repositories   []DoctorRepository `json:"repositories,omitempty"`
	Findings       []DoctorFinding    `json:"findings"`
	Repairs        []DoctorRepair     `json:"repairs,omitempty"`
	Fixed          bool               `json:"fixed,omitempty"`
	DryRun         bool               `json:"dryRun,omitempty"`
}

// DoctorRepository is the deterministic declared topology and observed
// presence for one checkout in a read-only doctor report.
type DoctorRepository struct {
	ID               string `json:"id"`
	ParentID         string `json:"parentId,omitempty"`
	Mount            string `json:"mount"`
	ResolvedPath     string `json:"resolvedPath"`
	Status           string `json:"status"`
	IdentityMismatch bool   `json:"identityMismatch,omitempty"`
	Missing          bool   `json:"missing,omitempty"`
	MountMismatch    bool   `json:"mountMismatch,omitempty"`
	BranchMismatch   bool   `json:"branchMismatch,omitempty"`
	HeadMismatch     bool   `json:"headMismatch,omitempty"`
}

type DoctorFixRequest struct {
	DataDir string
	DryRun  bool
}

// DoctorService detects drift and performs only the M15 allowlisted repairs.
type DoctorService struct {
	git               gitadapter.Git
	locker            ProjectLocker
	writeWorkspaceCAS func(string, store.WorkspaceState, func() error) error
	writeRawCAS       func(string, []byte, func() error) error
	writeRecoveryCAS  func(string, store.RecoveryRecord, func() error) error
}

func NewDoctorService() *DoctorService {
	return &DoctorService{git: gitadapter.NewAdapter("git"), locker: lock.Manager{}, writeWorkspaceCAS: store.WriteWorkspaceCAS, writeRawCAS: store.WriteRawCAS, writeRecoveryCAS: store.WriteRecoveryCAS}
}

func NewDoctorServiceWith(git gitadapter.Git, locker ProjectLocker, writeWorkspace func(string, store.WorkspaceState) error) *DoctorService {
	return &DoctorService{git: git, locker: locker, writeRawCAS: store.WriteRawCAS, writeRecoveryCAS: store.WriteRecoveryCAS, writeWorkspaceCAS: func(path string, state store.WorkspaceState, compare func() error) error {
		if compare != nil {
			if err := compare(); err != nil {
				return err
			}
		}
		return writeWorkspace(path, state)
	}}
}

// Doctor is strictly read-only. It observes source identities, persisted
// checkout metadata, discovered worktrees, and unresolved recovery records.
func (d *DoctorService) Doctor(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir string) (DoctorReport, error) {
	if d == nil || d.git == nil {
		return DoctorReport{}, NewError(ErrorInternal, errors.New("doctor is not configured"))
	}
	if err := ctx.Err(); err != nil {
		return DoctorReport{}, err
	}
	if err := project.Validate(); err != nil {
		return DoctorReport{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	report := DoctorReport{ProjectID: project.ID, Workspace: workspace.Name, LogicalRoot: workspace.RootPath, BaseRepository: project.BaseRepository}
	registry, registryErr := readRegistry(filepath.Join(dataDir, "registry.json"))
	if registryErr != nil {
		report.Findings = append(report.Findings, DoctorFinding{Code: "invalid-registry", Severity: "error", Message: "project registry cannot be read"})
	} else if registered, exists := registry.Projects[project.ID]; !exists || registered.ConfigPath != project.ConfigPath || !sameRepositoryIDs(registered.RepositoryIDs, repositoryIDs(project)) {
		report.Findings = append(report.Findings, DoctorFinding{Code: "stale-registry", Severity: "warning", Message: "project registry does not match verified project configuration"})
	}
	for _, repository := range project.ParentFirst() {
		identity, err := d.git.CommonGitDir(ctx, repository.SourcePath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DoctorReport{}, ctxErr
		}
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DoctorReport{}, ctxErr
		}
		if branchErr != nil {
			return DoctorReport{}, NewError(ErrorGit, fmt.Errorf("read branch for %q: %w", repository.ID, branchErr))
		}
		if detached != checkout.Detached || (!detached && branch != checkout.Branch) {
			report.Findings = append(report.Findings, DoctorFinding{Code: "branch-mismatch", Severity: "warning", RepositoryID: repository.ID, Message: "actual Git branch or detached state differs from workspace state"})
		}
		head, headErr := d.git.Head(ctx, actual)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DoctorReport{}, ctxErr
		}
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
	if err := ctx.Err(); err != nil {
		return DoctorReport{}, err
	}
	// Shared-drift collection is part of the report's authority. Returning an
	// error is safer than claiming a successful report after an unreadable or
	// changing journal/reconciliation generation; context cancellation keeps its
	// original identity for callers that need to stop promptly.
	snapshot, snapshotErr := collectLocalDriftSnapshot(ctx, d.git, project, dataDir)
	if snapshotErr != nil {
		if errors.Is(snapshotErr, context.Canceled) || errors.Is(snapshotErr, context.DeadlineExceeded) {
			return DoctorReport{}, snapshotErr
		}
		var unavailable doctorTrackedManifestUnavailable
		if errors.As(snapshotErr, &unavailable) {
			fallback, fallbackErr := doctorFallbackFindings(ctx, dataDir, project.ID)
			if fallbackErr != nil {
				if errors.Is(fallbackErr, context.Canceled) || errors.Is(fallbackErr, context.DeadlineExceeded) {
					return DoctorReport{}, fallbackErr
				}
				return DoctorReport{}, NewError(ErrorConflict, fmt.Errorf("collect doctor operation evidence: %w", fallbackErr))
			}
			report.Findings = append(report.Findings, fallback...)
		} else {
			return DoctorReport{}, NewError(ErrorConflict, fmt.Errorf("collect doctor drift snapshot: %w", snapshotErr))
		}
	} else {
		report.Findings = append(report.Findings, doctorDriftFindings(snapshot)...)
	}
	if workspace.Validate(project) == nil {
		report.Repositories = doctorRepositories(project, workspace, observed, report.Findings)
	}
	inventory, inventoryErr := newAuthoritativeHookRunInventory().Inspect(ctx, HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: dataDir, Environment: os.Environ(), Windows: runtime.GOOS == "windows"})
	if inventoryErr != nil {
		return DoctorReport{}, inventoryErr
	}
	switch inventory.Classification {
	case HookRunResumable:
		report.Findings = append(report.Findings, DoctorFinding{Code: "hook-setup-incomplete", Severity: "warning", Message: "hook setup is incomplete and can be resumed with wtree hooks retry " + workspace.Name})
	case HookRunStale:
		report.Findings = append(report.Findings, DoctorFinding{Code: "hook-run-stale", Severity: "warning", Message: "hook run no longer matches its source or workspace; a fresh run is required"})
	case HookRunInvalid:
		report.Findings = append(report.Findings, DoctorFinding{Code: "invalid-hook-run-record", Severity: "error", Message: "hook run record is invalid and requires manual inspection"})
	}
	repositoryOrder := map[string]int{"": 0}
	for index, repository := range project.ParentFirst() {
		repositoryOrder[repository.ID] = index + 1
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		left, right := repositoryOrder[report.Findings[i].RepositoryID], repositoryOrder[report.Findings[j].RepositoryID]
		if left == right {
			return report.Findings[i].Code < report.Findings[j].Code
		}
		return left < right
	})
	report.Findings = uniqueDoctorFindings(report.Findings)
	return report, nil
}

func doctorRepositories(project domain.Project, workspace domain.Workspace, observed map[string]string, findings []DoctorFinding) []DoctorRepository {
	checkouts := workspaceCheckoutMap(workspace)
	result := make([]DoctorRepository, 0, len(project.Repositories))
	for _, repository := range project.ParentFirst() {
		checkout, found := checkouts[repository.ID]
		if !found {
			continue
		}
		status := "known"
		if _, found := observed[repository.ID]; !found {
			status = "missing"
		}
		value := DoctorRepository{ID: repository.ID, ParentID: repository.ParentID, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath, Status: status}
		for _, finding := range findings {
			if finding.RepositoryID != repository.ID {
				continue
			}
			switch finding.Code {
			case "source-identity-mismatch":
				value.IdentityMismatch = true
			case "missing-checkout":
				value.Missing = true
			case "mount-mismatch":
				value.MountMismatch = true
			case "branch-mismatch":
				value.BranchMismatch = true
			case "head-mismatch":
				value.HeadMismatch = true
			}
		}
		result = append(result, value)
	}
	return result
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
	if d.locker == nil || d.writeWorkspaceCAS == nil || d.writeRawCAS == nil || d.writeRecoveryCAS == nil {
		return DoctorReport{}, NewError(ErrorInternal, errors.New("doctor repair is not configured"))
	}
	handle, err := acquireProjectMutationAuthority(ctx, d.locker, request.DataDir, project.ID, time.Second)
	if err != nil {
		return DoctorReport{}, err
	}
	defer handle.Unlock()
	statePath := WorkspaceStatePath(request.DataDir, project.ID, workspace.ID)
	stateSnapshot, err := secureCloneFileSnapshot(statePath)
	if err != nil {
		return DoctorReport{}, NewError(ErrorConflict, fmt.Errorf("capture doctor workspace state: %w", err))
	}
	if !stateSnapshot.exists {
		return DoctorReport{}, NewError(ErrorConflict, errors.New("doctor workspace state disappeared before repair"))
	}
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		return DoctorReport{}, NewError(ErrorConflict, fmt.Errorf("read doctor workspace state: %w", err))
	}
	currentWorkspace, err := workspaceFromState(state)
	if err != nil {
		return DoctorReport{}, NewError(ErrorConflict, fmt.Errorf("validate current doctor workspace state: %w", err))
	}
	if err := currentWorkspace.Validate(project); err != nil {
		return DoctorReport{}, NewError(ErrorConflict, fmt.Errorf("validate current doctor workspace state: %w", err))
	}
	report, err = d.Doctor(ctx, project, currentWorkspace, request.DataDir)
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
		updated, err := d.repairedWorkspace(ctx, project, currentWorkspace)
		if err != nil {
			return DoctorReport{}, err
		}
		updatedState := doctorWorkspaceState(updated)
		if err := d.writeWorkspaceCAS(statePath, updatedState, func() error { return revalidateCloneFileSnapshot(stateSnapshot) }); err != nil {
			if !fsutil.ReplacementCompleted(err) {
				return DoctorReport{}, NewError(ErrorInternal, fmt.Errorf("write repaired workspace state: %w", err))
			}
			attempted, encodeErr := store.WorkspaceBytes(updatedState)
			if encodeErr != nil {
				return DoctorReport{}, NewError(ErrorInternal, fmt.Errorf("encode repaired workspace state: %w", encodeErr))
			}
			return DoctorReport{}, finishReplacedPublicationFailure(stateSnapshot, attempted, err, request.DataDir, project.ID, workspace.ID, "doctor-fix", "commit-state", publicationRecoveryDependencies{writeRawCAS: d.writeRawCAS, writeRecoveryCAS: d.writeRecoveryCAS})
		}
		report.Fixed = true
	}
	return report, nil
}

func (d *DoctorService) missingWorktreeRegistration(ctx context.Context, source, path string) (bool, error) {
	worktrees, err := d.git.ListWorktrees(ctx, source)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil || !sameCheckoutPath(top, path) {
			return nil
		}
		identity, err := d.git.CommonGitDir(ctx, path)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
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
			owner := workspace.RootPath
			if repository.ParentID != "" {
				owner = paths[repository.ParentID]
			}
			relative, err := filepath.Rel(owner, actual)
			if err != nil || (repository.ParentID != "" && relative == ".") || filepath.IsAbs(relative) {
				return domain.Workspace{}, NewError(ErrorValidation, fmt.Errorf("cannot derive repaired mount for %q", repository.ID))
			}
			mount := filepath.ToSlash(relative)
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
