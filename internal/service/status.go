package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

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
	Drift                []StatusDrift      `json:"drift,omitempty"`
}

// StatusDrift is additive local manifest/state/disk evidence. It is kept
// separate from RepositoryStatus.Status so the existing status vocabulary and
// exit behaviour remain compatible.
type StatusDrift struct {
	ID       string `json:"id,omitempty"`
	ParentID string `json:"parentId,omitempty"`
	Path     string `json:"path,omitempty"`
	Origin   string `json:"origin"`
	Check    string `json:"check"`
	Status   string `json:"status"`
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
	ExpectedIdentity  string `json:"expectedIdentity,omitempty"`
	ActualIdentity    string `json:"actualIdentity,omitempty"`
	ActualMount       string `json:"actualMount,omitempty"`
	ExpectedHead      string `json:"expectedHead,omitempty"`
	IdentityMismatch  bool   `json:"identityMismatch,omitempty"`
	HeadMismatch      bool   `json:"headMismatch,omitempty"`
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
	return s.status(ctx, project, workspace, "")
}

// StatusWithDataDir adds the compatible local-drift projection. The data
// directory is explicit so the historical Status API remains usable by
// callers that only need checkout/Git facts.
func (s *StatusService) StatusWithDataDir(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir string) (WorkspaceStatus, error) {
	return s.status(ctx, project, workspace, dataDir)
}

func (s *StatusService) status(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir string) (WorkspaceStatus, error) {
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
		status, err := s.repositoryStatus(ctx, repository, checkout, checkoutPath, workspace.RootPath, duplicate[repository.ID], childPaths)
		if err != nil {
			return WorkspaceStatus{}, err
		}
		value.Repositories = append(value.Repositories, status)
	}
	if dataDir != "" {
		snapshot, err := collectLocalDriftSnapshot(ctx, s.git, project, dataDir)
		if err != nil {
			var unavailable doctorTrackedManifestUnavailable
			// A locally initialised project may not yet have committed its
			// portable manifest. Preserve the long-standing checkout-only
			// status in that state; there is no authoritative generation from
			// which to claim manifest drift.
			if errors.As(err, &unavailable) {
				fallback, fallbackErr := doctorFallbackFindings(ctx, dataDir, project.ID)
				if fallbackErr != nil {
					return WorkspaceStatus{}, NewError(ErrorConflict, fmt.Errorf("collect local status fallback evidence: %w", fallbackErr))
				}
				applyStatusFallbackDrift(&value, project, fallback)
				return value, nil
			}
			var preflight *DriftPreflightError
			if errors.As(err, &preflight) {
				applyLocalStatusDrift(&value, project, workspace, preflight.Snapshot)
				return value, nil
			}
			if check := statusLocalAuthorityErrorCheck(err); check != "" {
				fallback, fallbackErr := doctorFallbackFindings(ctx, dataDir, project.ID)
				if fallbackErr != nil {
					return WorkspaceStatus{}, NewError(ErrorConflict, fmt.Errorf("collect local status fallback evidence: %w", fallbackErr))
				}
				applyStatusAuthorityDrift(&value, check)
				applyStatusFallbackDrift(&value, project, fallback)
				return value, nil
			}
			return WorkspaceStatus{}, NewError(ErrorConflict, fmt.Errorf("collect local status drift: %w", err))
		}
		applyLocalStatusDrift(&value, project, workspace, snapshot)
	}
	return value, nil
}

func statusLocalAuthorityErrorCheck(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "local project configuration"):
		return "local-config"
	case strings.Contains(message, "registry"):
		return "registry-generation"
	case strings.Contains(message, "default workspace state"), strings.Contains(message, "read default workspace"):
		return "default-state"
	case strings.Contains(message, "workspace-inventory"):
		return "persisted-workspace"
	}
	return ""
}

func applyStatusAuthorityDrift(value *WorkspaceStatus, check string) {
	for _, existing := range value.Drift {
		if existing.ID == "" && existing.Path == "" && existing.Check == check {
			return
		}
	}
	value.Drift = append(value.Drift, StatusDrift{Origin: "authority", Check: check, Status: "inconsistent"})
}

// applyStatusFallbackDrift preserves legacy synchronized output when the
// initial portable manifest has not been committed, while never hiding local
// retained or unfinished-operation records that are independently
// authoritative. The manifest-unavailable diagnostic itself remains silent:
// it is not a repository drift classification without a tracked generation.
func applyStatusFallbackDrift(value *WorkspaceStatus, project domain.Project, findings []DoctorFinding) {
	parent := make(map[string]string, len(project.Repositories))
	for _, repository := range project.Repositories {
		parent[repository.ID] = repository.ParentID
	}
	for _, finding := range findings {
		switch finding.Code {
		case "retained-unmanaged-repository":
			value.Drift = append(value.Drift, StatusDrift{ID: finding.RepositoryID, ParentID: parent[finding.RepositoryID], Origin: "retained", Check: "retained-unmanaged", Status: "retained-unmanaged"})
		case "update-in-progress", "update-recovery-record":
			value.Drift = append(value.Drift, StatusDrift{Path: finding.path, Origin: "operation", Check: finding.Code, Status: "incomplete-operation"})
		}
	}
	sort.SliceStable(value.Drift, func(i, j int) bool {
		left, right := statusDriftOrder(project, value.Drift[i].ID), statusDriftOrder(project, value.Drift[j].ID)
		if left != right {
			return left < right
		}
		if value.Drift[i].Check != value.Drift[j].Check {
			return value.Drift[i].Check < value.Drift[j].Check
		}
		if value.Drift[i].Path != value.Drift[j].Path {
			return value.Drift[i].Path < value.Drift[j].Path
		}
		return value.Drift[i].Origin < value.Drift[j].Origin
	})
	value.Drift = uniqueStatusDrift(value.Drift)
}

func (s *StatusService) repositoryStatus(ctx context.Context, repository domain.Repository, checkout domain.Checkout, checkoutPath, workspaceRoot string, stale bool, managedChildPaths []string) (RepositoryStatus, error) {
	status := RepositoryStatus{ID: repository.ID, ParentID: repository.ParentID, ExpectedBranch: checkout.Branch, Mount: checkout.Mount, Path: checkoutPath, ResolvedPath: checkout.ResolvedPath, ExpectedIdentity: repository.CommonGitDir, ExpectedHead: checkout.Head, StaleState: stale}
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
	status.ActualIdentity = commonGitDir
	if err != nil || commonGitDir != repository.CommonGitDir {
		status.UnknownRepository, status.IdentityMismatch, status.Status = true, true, "unknown-repository"
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
	if mount, mountErr := filepath.Rel(workspaceRoot, topLevel); mountErr == nil {
		status.ActualMount = filepath.ToSlash(mount)
		if status.ActualMount == "." {
			status.ActualMount = "."
		}
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
	status.HeadMismatch = head != checkout.Head
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

func applyLocalStatusDrift(value *WorkspaceStatus, project domain.Project, workspace domain.Workspace, snapshot DriftSnapshot) {
	byID := make(map[string]*RepositoryStatus, len(value.Repositories))
	parent := make(map[string]string, len(project.Repositories))
	for index := range value.Repositories {
		byID[value.Repositories[index].ID] = &value.Repositories[index]
	}
	for _, repository := range project.Repositories {
		parent[repository.ID] = repository.ParentID
	}
	canonical := make(map[string]bool)
	appendDrift := func(id, path, origin, check, status string) {
		key := id + "\x00" + path + "\x00" + check
		if canonical[key] {
			return
		}
		canonical[key] = true
		value.Drift = append(value.Drift, StatusDrift{ID: id, ParentID: parent[id], Path: path, Origin: origin, Check: check, Status: status})
	}
	// RepositoryStatus was observed from the workspace the user selected. The
	// snapshot's repository observations belong to its exact default workspace,
	// so workspace names, IDs, or path aliases must never bind them to another
	// checkout. A full structural match also prevents a changed default-state
	// generation from overwriting facts already observed for the selected one.
	selectedIsSnapshotDefault := workspace.ID == "default" && reflect.DeepEqual(workspace, snapshot.DefaultWorkspace())
	observations := make(map[string]DriftRepositoryObservation, len(snapshot.Observations()))
	for _, observation := range snapshot.Observations() {
		observations[observation.RepositoryID] = observation
		if !selectedIsSnapshotDefault {
			continue
		}
		status, found := byID[observation.RepositoryID]
		if !found {
			continue
		}
		if observation.CommonGitDir != "" {
			status.ActualIdentity = observation.CommonGitDir
		}
		if observation.IdentityKnown && !observation.IdentityMatches {
			status.IdentityMismatch = true
		}
	}
	for _, status := range value.Repositories {
		if status.Missing {
			appendDrift(status.ID, "", "manifest", "checkout", "declared-absent")
		}
		if status.IdentityMismatch {
			appendDrift(status.ID, "", "checkout", "identity", "mismatch")
		}
		if status.MountMismatch {
			appendDrift(status.ID, "", "checkout", "mount", "mismatch")
		}
		if status.Detached || status.BranchMismatch {
			appendDrift(status.ID, "", "checkout", "branch", "mismatch")
		}
		if status.HeadMismatch {
			appendDrift(status.ID, "", "checkout", "head", "mismatch")
		}
	}
	for _, difference := range snapshot.SetDifferences() {
		status := "state-or-disk-not-manifest"
		if difference.Check == "checkout" {
			status = "declared-absent"
		}
		appendDrift(difference.ID, "", difference.Origin, difference.Check, status)
	}
	for _, retained := range snapshot.RetainedUnmanaged() {
		appendDrift(retained.RepositoryID, retained.Path, "retained", "retained-unmanaged", "retained-unmanaged")
	}
	for _, failure := range snapshot.Failures() {
		if failure.RepositoryID == "project" {
			if statusProjectAuthorityFailure(failure.Check) {
				appendDrift("", "", "authority", failure.Check, "inconsistent")
			}
			continue
		}
		switch failure.Check {
		case "checkout":
			if selectedIsSnapshotDefault {
				appendDrift(failure.RepositoryID, "", "manifest", "checkout", "declared-absent")
			} else {
				appendDrift(failure.RepositoryID, observations[failure.RepositoryID].Path, "default-checkout", "checkout", "declared-absent")
			}
		case "identity":
			if !selectedIsSnapshotDefault {
				appendDrift(failure.RepositoryID, observations[failure.RepositoryID].Path, "default-checkout", "identity", "mismatch")
			}
		case "state-only", "disk-only":
			appendDrift(failure.RepositoryID, "", "observation", failure.Check, "state-or-disk-not-manifest")
		case "retained-unmanaged":
			appendDrift(failure.RepositoryID, "", "retained", "retained-unmanaged", "retained-unmanaged")
		}
	}
	for _, operation := range snapshot.Operations() {
		check := "update-recovery-record"
		if strings.Contains(filepath.ToSlash(operation.Path), "/update/") {
			check = "update-in-progress"
		}
		appendDrift("", operation.Path, "operation", check, "incomplete-operation")
	}
	sort.SliceStable(value.Drift, func(i, j int) bool {
		left, right := statusDriftOrder(project, value.Drift[i].ID), statusDriftOrder(project, value.Drift[j].ID)
		if left != right {
			return left < right
		}
		if value.Drift[i].Check != value.Drift[j].Check {
			return value.Drift[i].Check < value.Drift[j].Check
		}
		if value.Drift[i].Path != value.Drift[j].Path {
			return value.Drift[i].Path < value.Drift[j].Path
		}
		return value.Drift[i].Origin < value.Drift[j].Origin
	})
	value.Drift = uniqueStatusDrift(value.Drift)
}

func statusProjectAuthorityFailure(check string) bool {
	switch check {
	case "configuration-contract", "current-manifest-configuration", "current-manifest-repository-set", "current-manifest-project", "current-manifest-base", "tracked-manifest", "local-config", "registry-generation", "default-state", "workspace-state", "persisted-workspace", "unresolved-operation", "collection", "collection-completeness":
		return true
	}
	return false
}

func statusDriftOrder(project domain.Project, id string) int {
	for index, repository := range project.ParentFirst() {
		if repository.ID == id {
			return index
		}
	}
	return len(project.Repositories)
}

func uniqueStatusDrift(values []StatusDrift) []StatusDrift {
	result := values[:0]
	for _, value := range values {
		if len(result) != 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
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
