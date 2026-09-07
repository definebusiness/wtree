package service

// Companion update owns one companion's local branch advancement. It is kept
// separate from update_* because configuration reconciliation has different
// rollback and membership semantics.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/store"
)

const companionUpdateVersion = 1

type CompanionUpdateRequest struct {
	Project      domain.Project
	DataDir      string
	RepositoryID string
	DryRun       bool
	Progress     func(CompanionUpdateEntry) error
}

type CompanionUpdatePlan struct {
	Version      int    `json:"version"`
	Operation    string `json:"operation"`
	ProjectID    string `json:"projectId"`
	RepositoryID string `json:"repositoryId"`
	Baseline     string `json:"baseline"`
	private      companionUpdatePrivate
}

type companionUpdatePrivate struct {
	dataDir                  string
	project                  domain.Project
	repository               domain.Repository
	remote, merge, remoteURL string
	baselineHead             string
	entries                  []companionUpdateState
	configBytes              []byte
	manifestPath             string
	manifestBytes            []byte
	stateFiles               map[string][]byte
}

type companionUpdateState struct {
	id, name, statePath string
	repositoryID        string
	state               store.WorkspaceState
	before              []byte
	checkout            store.CheckoutState
	valid, present      bool
}

type CompanionUpdateResult struct {
	Version      int                     `json:"version"`
	Operation    string                  `json:"operation"`
	Status       string                  `json:"status"`
	DryRun       bool                    `json:"dryRun"`
	ProjectID    string                  `json:"projectId"`
	RepositoryID string                  `json:"repositoryId"`
	Baseline     string                  `json:"baseline"`
	Entries      []CompanionUpdateEntry  `json:"entries"`
	Failure      *CompanionUpdateFailure `json:"failure,omitempty"`
}

type CompanionUpdateFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type CompanionUpdateEntry struct {
	Kind          string `json:"kind"`
	Workspace     string `json:"workspace,omitempty"`
	Branch        string `json:"branch,omitempty"`
	PreviousHEAD  string `json:"previousHead,omitempty"`
	ResultingHEAD string `json:"resultingHead,omitempty"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	Deferred      bool   `json:"deferred,omitempty"`
	cause         error
}

// companionUpdateResultWithFailure makes every settled failed result carry the
// same structured failure boundary as failures that originate in a concrete
// mutation step. Per-entry reasons are deliberately retained; the top-level
// category only supplies the stable process and JSON taxonomy.
func companionUpdateResultWithFailure(result CompanionUpdateResult) CompanionUpdateResult {
	if result.Status != "failed" || result.Failure != nil {
		return result
	}
	for _, entry := range result.Entries {
		if entry.Status != "failed" && entry.Status != "canceled" {
			continue
		}
		kind := companionUpdateEntryFailureKind(entry)
		if entry.Status == "canceled" {
			kind = ErrorInternal
		}
		message := fmt.Sprintf("companion %s %q: %s", entry.Kind, entry.Workspace, entry.Reason)
		result.Failure = &CompanionUpdateFailure{Code: string(kind), Message: message}
		return result
	}
	result.Failure = &CompanionUpdateFailure{Code: string(ErrorInternal), Message: "companion update failed without a settled entry"}
	return result
}

func companionUpdateEntryFailureKind(entry CompanionUpdateEntry) ErrorKind {
	switch entry.Reason {
	case "dirty", "dirty-baseline":
		return ErrorDirtyWorkspace
	case "fetch-failed":
		return ErrorGit
	case "rollback-incomplete":
		return ErrorRollbackIncomplete
	case "invalid-state":
		return ErrorValidation
	case "state-publication-failed", "":
		return ErrorInternal
	default:
		return ErrorConflict
	}
}

type CompanionUpdateService struct {
	git            gitadapter.Git
	locker         ProjectLocker
	writeRecovery  func(string, store.RecoveryRecord, func() error) error
	writeWorkspace func(string, store.WorkspaceState, func() error) error
}

func NewCompanionUpdateService() *CompanionUpdateService {
	return NewCompanionUpdateServiceWith(gitadapter.NewAdapter("git"), lock.Manager{})
}
func NewCompanionUpdateServiceWith(git gitadapter.Git, locker ProjectLocker) *CompanionUpdateService {
	if git == nil {
		git = gitadapter.NewAdapter("git")
	}
	if locker == nil {
		locker = lock.Manager{}
	}
	return &CompanionUpdateService{git: git, locker: locker, writeRecovery: store.WriteRecoveryCAS, writeWorkspace: store.WriteWorkspaceCAS}
}

func NewCompanionUpdateServiceWithDependencies(git gitadapter.Git, locker ProjectLocker, writeWorkspace func(string, store.WorkspaceState, func() error) error, writeRecovery func(string, store.RecoveryRecord, func() error) error) *CompanionUpdateService {
	service := NewCompanionUpdateServiceWithRecovery(git, locker, writeRecovery)
	if writeWorkspace != nil {
		service.writeWorkspace = writeWorkspace
	}
	return service
}

// NewCompanionUpdateServiceWithRecovery exposes the recovery publication
// boundary for failure-injection tests without widening the command API.
func NewCompanionUpdateServiceWithRecovery(git gitadapter.Git, locker ProjectLocker, writeRecovery func(string, store.RecoveryRecord, func() error) error) *CompanionUpdateService {
	service := NewCompanionUpdateServiceWith(git, locker)
	if writeRecovery != nil {
		service.writeRecovery = writeRecovery
	}
	return service
}

func (s *CompanionUpdateService) Plan(ctx context.Context, request CompanionUpdateRequest) (CompanionUpdatePlan, error) {
	if s == nil {
		s = NewCompanionUpdateService()
	}
	if err := ctx.Err(); err != nil {
		return CompanionUpdatePlan{}, err
	}
	if request.DataDir == "" || request.RepositoryID == "" {
		return CompanionUpdatePlan{}, NewError(ErrorValidation, errors.New("data directory and companion repository are required"))
	}
	if err := request.Project.Validate(); err != nil {
		return CompanionUpdatePlan{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	dataDir, err := filepath.Abs(request.DataDir)
	if err != nil {
		return CompanionUpdatePlan{}, NewError(ErrorValidation, err)
	}
	repository, ok := companionUpdateRepository(request.Project, request.RepositoryID)
	if !ok {
		return CompanionUpdatePlan{}, NewError(ErrorValidation, fmt.Errorf("unknown repository %q", request.RepositoryID))
	}
	if !repository.Companion {
		return CompanionUpdatePlan{}, NewError(ErrorValidation, fmt.Errorf("repository %q is not a companion", request.RepositoryID))
	}
	if repository.DefaultBranch == "" {
		return CompanionUpdatePlan{}, NewError(ErrorValidation, errors.New("companion has no configured baseline"))
	}
	common, err := s.git.CommonGitDir(ctx, repository.SourcePath)
	if err != nil {
		return CompanionUpdatePlan{}, NewError(ErrorGit, fmt.Errorf("observe companion identity: %w", err))
	}
	if common != repository.CommonGitDir {
		return CompanionUpdatePlan{}, NewError(ErrorConflict, errors.New("companion Git identity changed"))
	}
	head, err := s.git.ResolveRef(ctx, repository.SourcePath, "refs/heads/"+repository.DefaultBranch)
	if err != nil {
		return CompanionUpdatePlan{}, NewError(ErrorGit, fmt.Errorf("resolve companion baseline: %w", err))
	}
	configBytes, manifestPath, manifestBytes, err := companionUpdateSourceGeneration(request.Project)
	if err != nil {
		return CompanionUpdatePlan{}, err
	}
	remote, merge, remoteURL, err := companionUpdateUpstreamBytes(configBytes, manifestBytes, request.RepositoryID)
	if err != nil {
		return CompanionUpdatePlan{}, err
	}
	observedURL, err := s.git.ConfiguredRemoteURL(ctx, repository.SourcePath, remote)
	if err != nil || observedURL != remoteURL {
		return CompanionUpdatePlan{}, NewError(ErrorConflict, errors.New("configured companion remote URL disagrees with manifest"))
	}
	stateFiles, err := companionUpdateStateGeneration(dataDir, request.Project.ID)
	if err != nil {
		return CompanionUpdatePlan{}, NewError(ErrorConflict, fmt.Errorf("inspect workspace state generation: %w", err))
	}
	entries := companionUpdateInventory(dataDir, request.Project, request.RepositoryID, stateFiles)
	projectCopy := request.Project
	projectCopy.Repositories = append([]domain.Repository(nil), request.Project.Repositories...)
	return CompanionUpdatePlan{Version: companionUpdateVersion, Operation: "companion-update", ProjectID: request.Project.ID, RepositoryID: request.RepositoryID, Baseline: repository.DefaultBranch, private: companionUpdatePrivate{dataDir: dataDir, project: projectCopy, repository: repository, remote: remote, merge: merge, remoteURL: remoteURL, baselineHead: head, entries: entries, configBytes: configBytes, manifestPath: manifestPath, manifestBytes: manifestBytes, stateFiles: stateFiles}}, nil
}

func companionUpdateSourceGeneration(project domain.Project) ([]byte, string, []byte, error) {
	configBytes, err := companionUpdateRegularFile(project.ConfigPath)
	if err != nil {
		return nil, "", nil, NewError(ErrorConflict, fmt.Errorf("read local configuration: %w", err))
	}
	local, err := config.LoadProject(configBytes)
	if err != nil {
		return nil, "", nil, NewError(ErrorConflict, fmt.Errorf("decode local configuration: %w", err))
	}
	manifestPath := filepath.Join(filepath.Dir(project.ConfigPath), filepath.FromSlash(local.Manifest.Path))
	manifestBytes, err := companionUpdateRegularFile(manifestPath)
	if err != nil {
		return nil, "", nil, NewError(ErrorConflict, fmt.Errorf("read portable manifest: %w", err))
	}
	return configBytes, manifestPath, manifestBytes, nil
}

func companionUpdateRegularFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("configuration authority is not a regular file")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return nil, errors.New("configuration authority changed while reading")
	}
	return data, nil
}

func companionUpdateUpstreamBytes(localData, manifestData []byte, id string) (string, string, string, error) {
	local, err := config.LoadProject(localData)
	if err != nil {
		return "", "", "", NewError(ErrorConflict, fmt.Errorf("decode local configuration: %w", err))
	}
	if local.Manifest.Path == "" {
		return "", "", "", NewError(ErrorConflict, errors.New("configured companion upstream authority is unavailable"))
	}
	manifest, err := config.LoadPortableManifest(manifestData)
	if err != nil {
		return "", "", "", NewError(ErrorConflict, fmt.Errorf("decode portable manifest: %w", err))
	}
	repo, ok := manifest.Repositories[id]
	if !ok || !repo.Companion || repo.Upstream.Remote == "" || repo.Upstream.Merge == "" {
		return "", "", "", NewError(ErrorConflict, errors.New("configured companion upstream authority is unavailable"))
	}
	if repo.Clone.URL == "" {
		return "", "", "", NewError(ErrorConflict, errors.New("configured companion fetch URL is unavailable"))
	}
	return repo.Upstream.Remote, repo.Upstream.Merge, repo.Clone.URL, nil
}

func companionUpdateStateGeneration(dataDir, projectID string) (map[string][]byte, error) {
	dir := WorkspaceStateDirectory(dataDir, projectID)
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(files))
	for _, file := range files {
		path := filepath.Join(dir, file.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		value := []byte(info.Mode().String() + "\x00")
		if info.Mode().IsRegular() {
			data, readErr := companionUpdateRegularFile(path)
			if readErr != nil {
				value = append(value, []byte("read-error")...)
			} else {
				value = append(value, data...)
			}
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				value = append(value, []byte("link-error")...)
			} else {
				value = append(value, []byte(target)...)
			}
		}
		result[file.Name()] = value
	}
	return result, nil
}

func sameCompanionGeneration(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for name, leftValue := range left {
		if rightValue, ok := right[name]; !ok || !bytes.Equal(leftValue, rightValue) {
			return false
		}
	}
	return true
}

func companionUpdateInventory(dataDir string, project domain.Project, repositoryID string, generations map[string][]byte) []companionUpdateState {
	dir := WorkspaceStateDirectory(dataDir, project.ID)
	names := make([]string, 0, len(generations))
	for name := range generations {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]companionUpdateState, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		generation := generations[name]
		separator := bytes.IndexByte(generation, 0)
		if separator < 1 || generation[0] != '-' {
			// State records are local regular files. Never follow a symlink or
			// accept a directory/special node as mutation authority; retain it as
			// a deterministic invalid entry so it cannot conceal later work.
			result = append(result, companionUpdateState{id: name, name: name, statePath: path, valid: false})
			continue
		}
		data := append([]byte(nil), generation[separator+1:]...)
		state, decodeErr := store.DecodeWorkspace(data)
		entry := companionUpdateState{statePath: path, before: append([]byte(nil), data...), valid: decodeErr == nil}
		if entry.valid {
			workspace, validateErr := workspaceFromState(state)
			if validateErr == nil {
				validateErr = workspace.Validate(project)
			}
			if validateErr != nil {
				entry.valid = false
			} else {
				entry.state = state
				entry.repositoryID = repositoryID
				entry.id = state.ID
				entry.name = state.Name
				checkout, found := state.Repositories[repositoryID]
				if !found {
					continue
				}
				entry.checkout = checkout
				// Presence is an immutable planning fact. A valid workspace that
				// was already absent is outside this operation; one removed after
				// planning must remain a visible `missing` result rather than be
				// silently reclassified as removed after the fetch boundary.
				_, presentErr := os.Stat(checkout.ResolvedPath)
				// Only a definite not-exist result proves an already removed
				// workspace. Permission and other observation failures must remain
				// visible to the later fail-closed workspace check.
				entry.present = !os.IsNotExist(presentErr)
			}
		}
		if !entry.valid {
			entry.id = name
			entry.name = name
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].name, result[j].name
		if a == "default" {
			return b != "default"
		}
		if b == "default" {
			return false
		}
		return a < b
	})
	return result
}

func (s *CompanionUpdateService) Execute(ctx context.Context, request CompanionUpdateRequest) (CompanionUpdateResult, error) {
	plan, err := s.Plan(ctx, request)
	if err != nil {
		return CompanionUpdateResult{}, err
	}
	return s.ExecutePlan(ctx, request, plan)
}
func (s *CompanionUpdateService) ExecutePlan(ctx context.Context, request CompanionUpdateRequest, plan CompanionUpdatePlan) (result CompanionUpdateResult, err error) {
	defer func() {
		result = companionUpdateResultWithFailure(result)
	}()
	if s == nil {
		s = NewCompanionUpdateService()
	}
	if err := plan.Validate(); err != nil {
		return CompanionUpdateResult{}, err
	}
	if request.Project.ID != plan.ProjectID || request.RepositoryID != plan.RepositoryID {
		return CompanionUpdateResult{}, NewError(ErrorValidation, errors.New("companion update request does not match plan"))
	}
	result = CompanionUpdateResult{Version: companionUpdateVersion, Operation: "companion-update", DryRun: request.DryRun, ProjectID: plan.ProjectID, RepositoryID: plan.RepositoryID, Baseline: plan.Baseline, Entries: []CompanionUpdateEntry{}}
	if request.DryRun {
		if blocked, err := hasCompanionUpdateProjectRecovery(plan.private.dataDir, plan.ProjectID, plan.private.entries); err != nil {
			return CompanionUpdateResult{}, NewError(ErrorConflict, fmt.Errorf("inspect project recovery: %w", err))
		} else if blocked {
			return CompanionUpdateResult{}, NewError(ErrorConflict, errors.New("project recovery is present"))
		}
		return s.dryRun(ctx, plan, result), nil
	}
	handle, err := acquireProjectMutationAuthority(ctx, s.locker, plan.private.dataDir, plan.ProjectID, time.Second)
	if err != nil {
		return CompanionUpdateResult{}, err
	}
	defer handle.Unlock()
	if blocked, err := hasCompanionUpdateProjectRecovery(plan.private.dataDir, plan.ProjectID, plan.private.entries); err != nil || blocked {
		if err != nil {
			return CompanionUpdateResult{}, NewError(ErrorConflict, err)
		}
		return CompanionUpdateResult{}, NewError(ErrorConflict, errors.New("project recovery is present"))
	}
	if err := s.revalidatePlan(ctx, plan); err != nil {
		return CompanionUpdateResult{}, err
	}
	fetch, fetchErr := s.git.FetchConfiguredRef(ctx, plan.private.repository.SourcePath, plan.private.remote, plan.private.merge)
	// FetchConfiguredRef deliberately returns an ownership receipt even when a
	// process or cancellation error follows a tracking-ref mutation. Validate
	// that receipt before classifying the failure. Companion update never rolls
	// the tracking ref back: it is an authenticated fetch fact, not one of this
	// command's local branch/worktree/state transactions.
	if receiptErr := validateCompanionFetchReceipt(plan, fetch); receiptErr != nil {
		fetchErr = errors.Join(fetchErr, receiptErr)
	}
	if fetchErr != nil {
		result.Entries = append(result.Entries, CompanionUpdateEntry{Kind: "baseline", Branch: plan.Baseline, PreviousHEAD: plan.private.baselineHead, Action: "none", Status: "failed", Reason: "fetch-failed"})
		result.Status = "failed"
		result.Failure = &CompanionUpdateFailure{Code: string(ErrorGit), Message: fetchErr.Error()}
		return result, nil
	}
	_ = fetch
	return s.executeFetched(ctx, request, plan, result, fetch.ActualRemoteCommit)
}

func (p CompanionUpdatePlan) Validate() error {
	if p.Version != companionUpdateVersion || p.Operation != "companion-update" || p.ProjectID == "" || p.RepositoryID == "" || p.Baseline == "" || p.private.dataDir == "" || p.private.repository.ID != p.RepositoryID || p.private.baselineHead == "" || p.private.remote == "" || p.private.merge == "" || p.private.remoteURL == "" || len(p.private.configBytes) == 0 || p.private.manifestPath == "" || len(p.private.manifestBytes) == 0 || p.private.stateFiles == nil {
		return NewError(ErrorValidation, errors.New("companion update plan is incomplete"))
	}
	return nil
}

func (s *CompanionUpdateService) dryRun(ctx context.Context, plan CompanionUpdatePlan, result CompanionUpdateResult) CompanionUpdateResult {
	if ctx.Err() != nil {
		return companionCanceledDryRun(plan, result)
	}
	baseline := s.dryRunBaseline(ctx, plan)
	result.Entries = append(result.Entries, baseline)
	if ctx.Err() != nil {
		result.Entries[0].Status, result.Entries[0].Deferred, result.Entries[0].Reason = "canceled", false, ""
		result.Entries = append(result.Entries, companionCanceledDryRunEntries(plan.private.entries)...)
		result.Status = "failed"
		return result
	}
	for index, state := range plan.private.entries {
		if state.valid && !state.present {
			continue
		}
		if ctx.Err() != nil {
			result.Entries = append(result.Entries, companionCanceledDryRunEntries(plan.private.entries[index:])...)
			result.Status = "failed"
			return result
		}
		entry := s.dryRunWorkspace(ctx, plan, state)
		result.Entries = append(result.Entries, entry)
		if ctx.Err() != nil {
			// Invalid inventory records are already-settled local facts. A
			// concurrent cancellation must not conceal them as unobserved work.
			if state.valid {
				result.Entries[len(result.Entries)-1].Status, result.Entries[len(result.Entries)-1].Deferred, result.Entries[len(result.Entries)-1].Reason = "canceled", false, ""
			}
			result.Entries = append(result.Entries, companionCanceledDryRunEntries(plan.private.entries[index+1:])...)
			result.Status = "failed"
			return result
		}
	}
	result.Status = "planned"
	for _, entry := range result.Entries {
		if entry.Status == "failed" {
			result.Status = "failed"
		}
	}
	return result
}

// companionCanceledDryRun retains plan order without turning a cancellation
// observed at an observation boundary into a synthetic corruption or missing
// diagnosis. It deliberately performs no additional filesystem or Git reads.
func companionCanceledDryRun(plan CompanionUpdatePlan, result CompanionUpdateResult) CompanionUpdateResult {
	result.Entries = append(result.Entries, CompanionUpdateEntry{Kind: "baseline", Branch: plan.Baseline, PreviousHEAD: plan.private.baselineHead, Action: "none", Status: "canceled"})
	result.Entries = append(result.Entries, companionCanceledDryRunEntries(plan.private.entries)...)
	result.Status = "failed"
	return result
}

func companionCanceledDryRunEntries(states []companionUpdateState) []CompanionUpdateEntry {
	entries := make([]CompanionUpdateEntry, 0, len(states))
	for _, state := range states {
		if state.valid && !state.present {
			continue
		}
		if !state.valid {
			entries = append(entries, CompanionUpdateEntry{Kind: "workspace", Workspace: state.name, Action: "none", Status: "failed", Reason: "invalid-state"})
			continue
		}
		entry := CompanionUpdateEntry{Kind: "workspace", Workspace: state.name, Action: "none", Status: "canceled"}
		entry.Branch, entry.PreviousHEAD = state.checkout.Branch, state.checkout.Head
		entries = append(entries, entry)
	}
	return entries
}

func (s *CompanionUpdateService) dryRunBaseline(ctx context.Context, plan CompanionUpdatePlan) CompanionUpdateEntry {
	entry := CompanionUpdateEntry{Kind: "baseline", Branch: plan.Baseline, PreviousHEAD: plan.private.baselineHead, Action: "none", Status: "planned", Deferred: true}
	if ctx.Err() != nil {
		return entry
	}
	checkedOut, err := s.git.BranchCheckedOut(ctx, plan.private.repository.SourcePath, plan.Baseline)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "missing-baseline"
		return entry
	}
	if !checkedOut {
		return entry
	}
	owner, err := s.activeBaselineOwner(ctx, plan)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "missing-baseline"
		return entry
	}
	clean, err := s.git.IsClean(ctx, owner.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil || !clean {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "dirty-baseline"
		return entry
	}
	observed, err := s.git.Head(ctx, owner.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "missing-baseline"
		return entry
	}
	contains, err := s.git.IsAncestor(ctx, owner.checkout.ResolvedPath, owner.checkout.Head, observed)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil || !contains {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "diverged-baseline"
	}
	return entry
}

func (s *CompanionUpdateService) dryRunWorkspace(ctx context.Context, plan CompanionUpdatePlan, state companionUpdateState) CompanionUpdateEntry {
	entry := CompanionUpdateEntry{Kind: "workspace", Workspace: state.name, Branch: state.checkout.Branch, PreviousHEAD: state.checkout.Head, Action: "none", Status: "planned", Deferred: true}
	if !state.valid {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "invalid-state"
		return entry
	}
	if ctx.Err() != nil {
		return entry
	}
	_, statErr := os.Stat(state.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if statErr != nil {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "missing"
		return entry
	}
	common, err := s.git.CommonGitDir(ctx, state.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil || common != plan.private.repository.CommonGitDir {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "identity-mismatch"
		return entry
	}
	top, err := s.git.TopLevel(ctx, state.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil || !companionUpdateSamePath(top, state.checkout.ResolvedPath) {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "identity-mismatch"
		return entry
	}
	branch, detached, err := s.git.CurrentBranch(ctx, state.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil || detached {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "detached"
		return entry
	}
	if branch != state.checkout.Branch {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "branch-mismatch"
		return entry
	}
	clean, err := s.git.IsClean(ctx, state.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil || !clean {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "dirty"
		return entry
	}
	blocked, recoveryErr := hasWorkspaceRecovery(plan.private.dataDir, plan.ProjectID, state.id)
	if ctx.Err() != nil {
		return entry
	}
	if recoveryErr != nil || blocked {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "recovery-blocked"
		return entry
	}
	observed, err := s.git.Head(ctx, state.checkout.ResolvedPath)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "missing"
		return entry
	}
	contains, err := s.git.IsAncestor(ctx, state.checkout.ResolvedPath, state.checkout.Head, observed)
	if ctx.Err() != nil {
		return entry
	}
	if err != nil || !contains {
		entry.Status, entry.Deferred, entry.Reason = "failed", false, "rewritten"
	}
	return entry
}

func (s *CompanionUpdateService) revalidatePlan(ctx context.Context, plan CompanionUpdatePlan) error {
	configBytes, manifestPath, manifestBytes, sourceErr := companionUpdateSourceGeneration(plan.private.project)
	if sourceErr != nil || manifestPath != plan.private.manifestPath || !bytes.Equal(configBytes, plan.private.configBytes) || !bytes.Equal(manifestBytes, plan.private.manifestBytes) {
		return NewError(ErrorConflict, errors.New("companion configuration generation changed before execution"))
	}
	stateFiles, stateErr := companionUpdateStateGeneration(plan.private.dataDir, plan.ProjectID)
	if stateErr != nil || !sameCompanionGeneration(stateFiles, plan.private.stateFiles) {
		return NewError(ErrorConflict, errors.New("workspace state inventory changed before execution"))
	}
	common, err := s.git.CommonGitDir(ctx, plan.private.repository.SourcePath)
	if err != nil || common != plan.private.repository.CommonGitDir {
		return NewError(ErrorConflict, errors.New("companion Git identity changed before execution"))
	}
	head, err := s.git.ResolveRef(ctx, plan.private.repository.SourcePath, "refs/heads/"+plan.Baseline)
	if err != nil || head != plan.private.baselineHead {
		return NewError(ErrorConflict, errors.New("companion baseline generation changed before execution"))
	}
	remote, merge, remoteURL, err := companionUpdateUpstreamBytes(configBytes, manifestBytes, plan.RepositoryID)
	if err != nil || remote != plan.private.remote || merge != plan.private.merge || remoteURL != plan.private.remoteURL {
		return NewError(ErrorConflict, errors.New("configured companion upstream changed before execution"))
	}
	observedURL, urlErr := s.git.ConfiguredRemoteURL(ctx, plan.private.repository.SourcePath, plan.private.remote)
	if urlErr != nil || observedURL != plan.private.remoteURL {
		return NewError(ErrorConflict, errors.New("configured companion remote URL changed before execution"))
	}
	return nil
}

func (s *CompanionUpdateService) executeFetched(ctx context.Context, request CompanionUpdateRequest, plan CompanionUpdatePlan, result CompanionUpdateResult, target string) (CompanionUpdateResult, error) {
	baseline := CompanionUpdateEntry{Kind: "baseline", Branch: plan.Baseline, PreviousHEAD: plan.private.baselineHead, ResultingHEAD: target, Action: "fast-forward", Status: "completed"}
	checkedOut, err := s.git.BranchCheckedOut(ctx, plan.private.repository.SourcePath, plan.Baseline)
	if err != nil {
		return CompanionUpdateResult{}, NewError(ErrorGit, err)
	}
	var baselineOwner string
	if checkedOut {
		owner, ownerErr := s.activeBaselineOwner(ctx, plan)
		if ownerErr != nil {
			baseline.Status = "failed"
			baseline.Action = "none"
			baseline.Reason = "missing-baseline"
			result.Entries = append(result.Entries, baseline)
			result.Status = "failed"
			return result, nil
		}
		baselineOwner = owner.id
		if !companionUpdateStateCurrent(owner) {
			// No state write has been attempted. The previously valid record can
			// no longer prove ownership of the active baseline, so report the
			// baseline-authority vocabulary rather than publication failure.
			baseline.Status, baseline.Action, baseline.Reason = "failed", "none", "missing-baseline"
			result.Entries, result.Status = append(result.Entries, baseline), "failed"
			return result, nil
		}
		if blocked, recoveryErr := hasWorkspaceRecovery(plan.private.dataDir, plan.ProjectID, owner.id); recoveryErr != nil || blocked {
			baseline.Status, baseline.Action, baseline.Reason = "failed", "none", "missing-baseline"
			result.Entries, result.Status = append(result.Entries, baseline), "failed"
			return result, nil
		}
		clean, cleanErr := s.git.IsClean(ctx, owner.checkout.ResolvedPath)
		if cleanErr != nil || !clean {
			baseline.Status, baseline.Action, baseline.Reason = "failed", "none", "dirty-baseline"
			result.Entries, result.Status = append(result.Entries, baseline), "failed"
			return result, nil
		}
		top, topErr := s.git.TopLevel(ctx, owner.checkout.ResolvedPath)
		observed, headErr := s.git.Head(ctx, owner.checkout.ResolvedPath)
		contains, ancestryErr := s.git.IsAncestor(ctx, owner.checkout.ResolvedPath, owner.checkout.Head, observed)
		// A descendant observed after the locked plan is not an unchanged
		// baseline generation. Publishing target in that case could regress the
		// stored head while claiming success, so require the exact branch/HEAD
		// generation before either the unchanged state write or FastForward.
		if topErr != nil || !companionUpdateSamePath(top, owner.checkout.ResolvedPath) || headErr != nil || observed != plan.private.baselineHead || ancestryErr != nil || !contains {
			baseline.Status, baseline.Action, baseline.Reason = "failed", "none", "diverged-baseline"
			result.Entries, result.Status = append(result.Entries, baseline), "failed"
			return result, nil
		}
		var receipt gitadapter.FastForwardReceipt
		if plan.private.baselineHead != target {
			var forwardErr error
			receipt, forwardErr = s.git.FastForward(ctx, owner.checkout.ResolvedPath, plan.Baseline, plan.private.baselineHead, target)
			if forwardErr != nil {
				if uncertainReceipt, uncertain := s.companionForwardReceiptAfterError(plan, plan.Baseline, plan.private.baselineHead, target); uncertain {
					_, recoveryErr := s.recordCompanionRecovery(plan, "", "baseline-ref", uncertainReceipt, forwardErr)
					baseline.Action, baseline.ResultingHEAD, baseline.Status, baseline.Reason = "fast-forward", target, "failed", "rollback-incomplete"
					baseline.cause = errors.Join(forwardErr, recoveryErr)
					result.Failure = &CompanionUpdateFailure{Code: string(ErrorRollbackIncomplete), Message: baseline.cause.Error()}
					result.Entries, result.Status = append(result.Entries, baseline), "failed"
					return result, nil
				}
				baseline.Status = "failed"
				baseline.Action = "none"
				baseline.Reason = "diverged-baseline"
				result.Entries = append(result.Entries, baseline)
				result.Status = "failed"
				return result, nil
			}
		} else {
			baseline.Action = "unchanged"
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			if receipt.NewCommit != "" {
				if restoreErr := s.git.RestoreFastForward(context.WithoutCancel(ctx), owner.checkout.ResolvedPath, receipt); restoreErr != nil {
					_, recoveryErr := s.recordCompanionRecovery(plan, "", "baseline-ref", receipt, errors.Join(cancelErr, restoreErr))
					baseline.Status, baseline.Reason = "failed", "rollback-incomplete"
					result.Failure = &CompanionUpdateFailure{Code: string(ErrorRollbackIncomplete), Message: errors.Join(cancelErr, restoreErr, recoveryErr).Error()}
					result.Entries, result.Status = append(result.Entries, baseline), "failed"
					return result, nil
				}
				baseline.Action, baseline.ResultingHEAD = "none", receipt.OldCommit
			}
			baseline.Status = "canceled"
			result.Entries, result.Status = append(result.Entries, baseline), "failed"
			return result, nil
		}
		if publication, writeErr := s.publishCompanionHead(owner, target); writeErr != nil {
			stateRestoreErr := restoreCompanionPublishedState(owner, publication)
			var restoreErr error
			if receipt.NewCommit != "" {
				restoreErr = s.git.RestoreFastForward(context.WithoutCancel(ctx), owner.checkout.ResolvedPath, receipt)
			}
			baseline.Status = "failed"
			baseline.Reason = "state-publication-failed"
			if receipt.NewCommit != "" && restoreErr == nil && stateRestoreErr == nil {
				baseline.Action, baseline.ResultingHEAD = "none", receipt.OldCommit
			}
			if restoreErr != nil || stateRestoreErr != nil {
				var recoveryErr error
				if stateRestoreErr != nil {
					recoveryReceipt := receipt
					if restoreErr == nil {
						recoveryReceipt = gitadapter.FastForwardReceipt{}
					}
					recoveryErr = s.recordCompanionStatePublicationRecovery(plan, owner, "baseline-ref", recoveryReceipt, errors.Join(writeErr, stateRestoreErr, restoreErr))
				} else {
					_, recoveryErr = s.recordCompanionRecovery(plan, "", "baseline-ref", receipt, errors.Join(writeErr, restoreErr))
				}
				baseline.Reason = "rollback-incomplete"
				message := errors.Join(writeErr, stateRestoreErr, restoreErr).Error()
				if recoveryErr != nil {
					message = errors.Join(errors.Join(writeErr, stateRestoreErr, restoreErr), recoveryErr).Error()
				}
				result.Failure = &CompanionUpdateFailure{Code: string(ErrorRollbackIncomplete), Message: message}
			}
			result.Entries = append(result.Entries, baseline)
			result.Status = "failed"
			return result, nil
		}
	} else if plan.private.baselineHead != target {
		if receipt, err := s.git.FastForwardRef(ctx, plan.private.repository.SourcePath, plan.Baseline, plan.private.baselineHead, target); err != nil {
			baseline.Status = "failed"
			if receipt.NewCommit == target {
				// A ref-only adapter can return an ownership receipt with an
				// error after the CAS (for example an external worktree attached
				// the branch). Surface the possible movement; never continue into
				// workspace work or silently classify it as no effect.
				baseline.Action = "fast-forward"
				baseline.ResultingHEAD = target
				baseline.Reason = "rollback-incomplete"
				result.Entries = append(result.Entries, baseline)
				result.Status = "failed"
				_, recoveryErr := s.recordCompanionRecovery(plan, "", "baseline-ref", receipt, err)
				message := err.Error()
				if recoveryErr != nil {
					message = errors.Join(err, recoveryErr).Error()
				}
				result.Failure = &CompanionUpdateFailure{Code: string(ErrorRollbackIncomplete), Message: message}
				return result, nil
			} else {
				baseline.Action = "none"
			}
			baseline.Reason = "diverged-baseline"
			result.Entries = append(result.Entries, baseline)
			result.Status = "failed"
			return result, nil
		}
	} else {
		baseline.Action = "unchanged"
	}
	result.Entries = append(result.Entries, baseline)
	// The baseline is a settled visible result and must cross the same output
	// boundary before any independently mutable workspace is attempted. JSON
	// callers may omit Progress and render the completed envelope once.
	if request.Progress != nil {
		if err := request.Progress(baseline); err != nil {
			result.Status = "failed"
			result.Failure = &CompanionUpdateFailure{Code: string(ErrorInternal), Message: err.Error()}
			result.Entries = append(result.Entries, companionCanceledEntries(plan.private.entries, baselineOwner)...)
			return result, nil
		}
	}
	for index, state := range plan.private.entries {
		if state.id == baselineOwner {
			continue
		}
		// Removed valid workspaces are outside companion update. Invalid state
		// files remain visible, but a known valid record with no checkout must
		// never become a synthetic failed entry or trigger recreation.
		if state.valid && !state.present {
			continue
		}
		if ctx.Err() != nil {
			result.Status = "failed"
			result.Entries = append(result.Entries, companionCanceledEntries(plan.private.entries[index:], baselineOwner)...)
			break
		}
		entry := s.updateWorkspace(ctx, plan, state, target)
		result.Entries = append(result.Entries, entry)
		if entry.Reason == "rollback-incomplete" {
			result.Status = "failed"
			if entry.cause != nil {
				result.Failure = &CompanionUpdateFailure{Code: string(ErrorRollbackIncomplete), Message: entry.cause.Error()}
			}
			result.Entries = append(result.Entries, companionCanceledEntries(plan.private.entries[index+1:], baselineOwner)...)
			break
		}
		if request.Progress != nil {
			if err := request.Progress(entry); err != nil {
				result.Status = "failed"
				result.Failure = &CompanionUpdateFailure{Code: string(ErrorInternal), Message: err.Error()}
				result.Entries = append(result.Entries, companionCanceledEntries(plan.private.entries[index+1:], baselineOwner)...)
				break
			}
		}
		if entry.Status != "completed" {
			result.Status = "failed"
		}
		if ctx.Err() != nil {
			result.Status = "failed"
			result.Entries = append(result.Entries, companionCanceledEntries(plan.private.entries[index+1:], baselineOwner)...)
			break
		}
	}
	if result.Status == "" {
		result.Status = "completed"
	}
	return result, nil
}

func companionCanceledEntries(states []companionUpdateState, baselineOwner string) []CompanionUpdateEntry {
	entries := make([]CompanionUpdateEntry, 0, len(states))
	for _, state := range states {
		if state.id == baselineOwner {
			continue
		}
		if !state.valid {
			entries = append(entries, CompanionUpdateEntry{Kind: "workspace", Workspace: state.name, Action: "none", Status: "failed", Reason: "invalid-state"})
			continue
		}
		if !state.present {
			continue
		}
		entries = append(entries, CompanionUpdateEntry{Kind: "workspace", Workspace: state.name, Branch: state.checkout.Branch, PreviousHEAD: state.checkout.Head, Action: "none", Status: "canceled"})
	}
	return entries
}

func (s *CompanionUpdateService) recordCompanionRecovery(plan CompanionUpdatePlan, workspaceID, failedStep string, receipt gitadapter.FastForwardReceipt, cause error) (bool, error) {
	if s.writeRecovery == nil {
		return false, errors.New("companion recovery writer is unavailable")
	}
	observed, observeErr := s.git.ResolveRef(context.WithoutCancel(context.Background()), plan.private.repository.SourcePath, "refs/heads/"+receipt.Branch)
	owned := observeErr == nil && observed == receipt.NewCommit
	if workspaceID == "" {
		workspaceID = "companion-update-" + plan.RepositoryID
	}
	path := filepath.Join(plan.private.dataDir, "projects", plan.ProjectID, "recovery", workspaceID+".json")
	completed := []string{failedStep + ":" + receipt.Branch}
	unreverted := []string{}
	failure := cause.Error()
	if owned {
		unreverted = append(unreverted, failedStep+":"+receipt.Branch+"="+receipt.NewCommit)
	} else {
		// A foreign or unobservable ref is never ours to name as an owned
		// residual. The attached transition may nevertheless have materialized
		// the fetched tree and index before ownership was lost, so retain that
		// actionable possibility for recovery without claiming the ref.
		unreverted = append(unreverted, failedStep+":worktree-index=possible@"+receipt.NewCommit)
		failure = errors.Join(cause, fmt.Errorf("ref ownership lost; observed %q while expected %q", observed, receipt.NewCommit), observeErr).Error()
	}
	record := store.RecoveryRecord{
		Version: plan.Version, ProjectID: plan.ProjectID, WorkspaceID: workspaceID,
		Operation: "companion-update", FailedStep: failedStep,
		CompletedSteps: completed, UnrevertedSteps: unreverted,
		RollbackFailures: []store.RollbackFailure{{Step: "inspect-" + failedStep, Error: failure}},
	}
	err := s.writeRecovery(path, record, func() error {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect recovery generation: %w", err)
		}
		return errors.New("recovery generation already exists")
	})
	return true, err
}

// recordCompanionStatePublicationRecovery records a split-transaction
// possibility without overwriting a state generation or ref we can no longer
// prove we own.  The state path is named only after its exact attempted bytes
// failed CAS restoration; the ref observation retains the existing
// worktree/index-residue vocabulary.
func (s *CompanionUpdateService) recordCompanionStatePublicationRecovery(plan CompanionUpdatePlan, state companionUpdateState, failedStep string, receipt gitadapter.FastForwardReceipt, cause error) error {
	if s.writeRecovery == nil {
		return errors.New("companion recovery writer is unavailable")
	}
	workspaceID := state.id
	if workspaceID == "" {
		workspaceID = "companion-update-" + plan.RepositoryID
	}
	unreverted := []string{"state:" + state.id}
	failures := []store.RollbackFailure{{Step: "restore-state", Error: cause.Error()}}
	if receipt.Branch != "" && receipt.NewCommit != "" {
		observed, observeErr := s.git.ResolveRef(context.WithoutCancel(context.Background()), plan.private.repository.SourcePath, "refs/heads/"+receipt.Branch)
		if observeErr == nil && observed == receipt.NewCommit {
			unreverted = append(unreverted, failedStep+":"+receipt.Branch+"="+receipt.NewCommit)
		} else {
			unreverted = append(unreverted, failedStep+":worktree-index=possible@"+receipt.NewCommit)
			failures = append(failures, store.RollbackFailure{Step: "inspect-" + failedStep, Error: errors.Join(fmt.Errorf("ref ownership lost; observed %q while expected %q", observed, receipt.NewCommit), observeErr).Error()})
		}
	}
	path := filepath.Join(plan.private.dataDir, "projects", plan.ProjectID, "recovery", workspaceID+".json")
	return s.writeRecovery(path, store.RecoveryRecord{Version: plan.Version, ProjectID: plan.ProjectID, WorkspaceID: workspaceID, Operation: "companion-update", FailedStep: failedStep, CompletedSteps: []string{failedStep}, UnrevertedSteps: unreverted, RollbackFailures: failures}, func() error {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect recovery generation: %w", err)
		}
		return errors.New("recovery generation already exists")
	})
}

func (s *CompanionUpdateService) activeBaselineOwner(ctx context.Context, plan CompanionUpdatePlan) (companionUpdateState, error) {
	var owners []companionUpdateState
	for _, state := range plan.private.entries {
		if err := ctx.Err(); err != nil {
			return companionUpdateState{}, err
		}
		if !state.valid {
			continue
		}
		if state.checkout.Branch != plan.Baseline || state.checkout.Detached {
			continue
		}
		common, err := s.git.CommonGitDir(ctx, state.checkout.ResolvedPath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return companionUpdateState{}, ctxErr
		}
		if err == nil && common == plan.private.repository.CommonGitDir {
			branch, detached, err := s.git.CurrentBranch(ctx, state.checkout.ResolvedPath)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return companionUpdateState{}, ctxErr
			}
			if err == nil && !detached && branch == plan.Baseline {
				owners = append(owners, state)
			}
		}
	}
	if len(owners) != 1 {
		return companionUpdateState{}, errors.New("active baseline lacks exactly one valid workspace authority")
	}
	return owners[0], nil
}

func (s *CompanionUpdateService) updateWorkspace(ctx context.Context, plan CompanionUpdatePlan, state companionUpdateState, baseline string) CompanionUpdateEntry {
	entry := CompanionUpdateEntry{Kind: "workspace", Workspace: state.name, Branch: state.checkout.Branch, PreviousHEAD: state.checkout.Head, Action: "none", Status: "failed"}
	if !state.valid {
		entry.Reason = "invalid-state"
		return entry
	}
	if !companionUpdateStateCurrent(state) {
		entry.Reason = "stale-generation"
		return entry
	}
	if _, err := os.Stat(state.checkout.ResolvedPath); err != nil {
		entry.Reason = "missing"
		return entry
	}
	common, err := s.git.CommonGitDir(ctx, state.checkout.ResolvedPath)
	if err != nil || common != plan.private.repository.CommonGitDir {
		entry.Reason = "identity-mismatch"
		return entry
	}
	top, err := s.git.TopLevel(ctx, state.checkout.ResolvedPath)
	if err != nil || !companionUpdateSamePath(top, state.checkout.ResolvedPath) {
		entry.Reason = "identity-mismatch"
		return entry
	}
	branch, detached, err := s.git.CurrentBranch(ctx, state.checkout.ResolvedPath)
	if err != nil || detached {
		entry.Reason = "detached"
		return entry
	}
	if branch != state.checkout.Branch {
		entry.Reason = "branch-mismatch"
		return entry
	}
	clean, err := s.git.IsClean(ctx, state.checkout.ResolvedPath)
	if err != nil || !clean {
		entry.Reason = "dirty"
		return entry
	}
	if blocked, recoveryErr := hasWorkspaceRecovery(plan.private.dataDir, plan.ProjectID, state.id); recoveryErr != nil || blocked {
		entry.Reason = "recovery-blocked"
		return entry
	}
	head, err := s.git.Head(ctx, state.checkout.ResolvedPath)
	if err != nil {
		entry.Reason = "missing"
		return entry
	}
	contains, err := s.git.IsAncestor(ctx, state.checkout.ResolvedPath, state.checkout.Head, head)
	if err != nil || !contains {
		entry.Reason = "rewritten"
		return entry
	}
	behind := false
	if head != baseline {
		behind, err = s.git.IsAncestor(ctx, state.checkout.ResolvedPath, head, baseline)
	}
	if err != nil {
		entry.Reason = "diverged"
		return entry
	}
	containsBaseline, err := s.git.IsAncestor(ctx, state.checkout.ResolvedPath, baseline, head)
	if err != nil {
		entry.Reason = "diverged"
		return entry
	}
	if !behind && !containsBaseline {
		entry.Reason = "diverged"
		return entry
	}
	entry.ResultingHEAD = head
	if behind {
		receipt, err := s.git.FastForward(ctx, state.checkout.ResolvedPath, branch, head, baseline)
		if err != nil {
			if uncertainReceipt, uncertain := s.companionForwardReceiptAfterError(plan, branch, head, baseline); uncertain {
				stage := "workspace-" + state.id + "-ref"
				_, recoveryErr := s.recordCompanionRecovery(plan, state.id, stage, uncertainReceipt, err)
				entry.Action, entry.ResultingHEAD, entry.Reason = "fast-forward", baseline, "rollback-incomplete"
				entry.cause = errors.Join(err, recoveryErr)
				return entry
			}
			entry.Reason = "stale-generation"
			return entry
		}
		entry.Action = "fast-forward"
		entry.ResultingHEAD = baseline
		if cancelErr := ctx.Err(); cancelErr != nil {
			restoreErr := s.git.RestoreFastForward(context.WithoutCancel(ctx), state.checkout.ResolvedPath, receipt)
			if restoreErr != nil {
				stage := "workspace-" + state.id + "-ref"
				_, recoveryErr := s.recordCompanionRecovery(plan, state.id, stage, receipt, errors.Join(cancelErr, restoreErr))
				entry.Reason = "rollback-incomplete"
				entry.cause = errors.Join(cancelErr, restoreErr, recoveryErr)
				return entry
			}
			entry.Action, entry.ResultingHEAD, entry.Status = "none", receipt.OldCommit, "canceled"
			return entry
		}
		if publication, err := s.publishCompanionHead(state, baseline); err != nil {
			entry.Reason = "state-publication-failed"
			stateRestoreErr := restoreCompanionPublishedState(state, publication)
			if restoreErr := s.git.RestoreFastForward(context.WithoutCancel(ctx), state.checkout.ResolvedPath, receipt); restoreErr != nil {
				stage := "workspace-" + state.id + "-ref"
				var recoveryErr error
				if stateRestoreErr != nil {
					recoveryErr = s.recordCompanionStatePublicationRecovery(plan, state, stage, receipt, errors.Join(err, stateRestoreErr, restoreErr))
				} else {
					_, recoveryErr = s.recordCompanionRecovery(plan, state.id, stage, receipt, errors.Join(err, restoreErr))
				}
				entry.Reason = "rollback-incomplete"
				entry.cause = errors.Join(err, stateRestoreErr, restoreErr, recoveryErr)
			} else if stateRestoreErr != nil {
				stage := "workspace-" + state.id + "-state"
				recoveryErr := s.recordCompanionStatePublicationRecovery(plan, state, stage, gitadapter.FastForwardReceipt{}, errors.Join(err, stateRestoreErr))
				entry.Reason = "rollback-incomplete"
				entry.cause = errors.Join(err, stateRestoreErr, recoveryErr)
			} else {
				entry.Action, entry.ResultingHEAD = "none", receipt.OldCommit
			}
			return entry
		}
	} else {
		entry.Action = "unchanged"
		if ctx.Err() != nil {
			entry.Status = "canceled"
			return entry
		}
		if publication, err := s.publishCompanionHead(state, head); err != nil {
			entry.Reason = "state-publication-failed"
			if stateRestoreErr := restoreCompanionPublishedState(state, publication); stateRestoreErr != nil {
				stage := "workspace-" + state.id + "-state"
				recoveryErr := s.recordCompanionStatePublicationRecovery(plan, state, stage, gitadapter.FastForwardReceipt{}, errors.Join(err, stateRestoreErr))
				entry.Reason = "rollback-incomplete"
				entry.cause = errors.Join(err, stateRestoreErr, recoveryErr)
			}
			return entry
		}
	}
	entry.Status = "completed"
	return entry
}

// companionForwardReceiptAfterError accounts for the existing attached
// FastForward API's intentionally receipt-less transition failures. Only the
// exact old ref proves that cleanup completed with no possible worktree/index
// residue. A new, foreign, or unobservable ref is fail-closed recovery: never
// overwrite it, but stop later work and leave an actionable record.
func (s *CompanionUpdateService) companionForwardReceiptAfterError(plan CompanionUpdatePlan, branch, oldCommit, newCommit string) (gitadapter.FastForwardReceipt, bool) {
	actual, err := s.git.ResolveRef(context.WithoutCancel(context.Background()), plan.private.repository.SourcePath, "refs/heads/"+branch)
	if err == nil && actual == oldCommit {
		return gitadapter.FastForwardReceipt{}, false
	}
	return gitadapter.FastForwardReceipt{Branch: branch, OldCommit: oldCommit, NewCommit: newCommit}, true
}

func validateCompanionFetchReceipt(plan CompanionUpdatePlan, receipt gitadapter.ConfiguredRefFetch) error {
	if receipt.Remote != plan.private.remote || receipt.RemoteRef != plan.private.merge || !aggregateObjectID(receipt.ActualRemoteCommit) || (receipt.PreviousRemoteCommit != "" && !aggregateObjectID(receipt.PreviousRemoteCommit)) {
		return errors.New("configured fetch receipt does not match companion authority")
	}
	return nil
}

// companionUpdateStateCurrent is deliberately a last local observation before
// any branch/worktree transition. The publication CAS remains the final
// authority, but this avoids moving a checkout at all when its persisted
// generation was already replaced after the project-wide revalidation.
func companionUpdateStateCurrent(state companionUpdateState) bool {
	current, err := os.ReadFile(state.statePath)
	return err == nil && bytes.Equal(current, state.before)
}

type companionStatePublication struct {
	attempted []byte
	installed bool
	uncertain bool
}

func (s *CompanionUpdateService) publishCompanionHead(state companionUpdateState, head string) (companionStatePublication, error) {
	next := state.state
	next.Repositories = make(map[string]store.CheckoutState, len(state.state.Repositories))
	for id, value := range state.state.Repositories {
		next.Repositories[id] = value
	}
	checkout := next.Repositories[state.repositoryID]
	checkout.Head = head
	next.Repositories[state.repositoryID] = checkout
	if s.writeWorkspace == nil {
		return companionStatePublication{}, errors.New("companion workspace writer is unavailable")
	}
	attempted, encodeErr := store.WorkspaceBytes(next)
	if encodeErr != nil {
		return companionStatePublication{}, encodeErr
	}
	err := s.writeWorkspace(state.statePath, next, func() error {
		current, err := os.ReadFile(state.statePath)
		if err != nil || !bytes.Equal(current, state.before) {
			return errors.New("workspace state generation changed")
		}
		return nil
	})
	if err == nil {
		return companionStatePublication{attempted: attempted}, nil
	}
	// An atomic writer can report a directory-sync failure after replacement.
	// Exact bytes are the operation's ownership receipt; never treat that case
	// as an untouched state file merely because the writer returned an error.
	current, readErr := os.ReadFile(state.statePath)
	installed := readErr == nil && bytes.Equal(current, attempted)
	// A post-replacement error without the exact attempted bytes is not an
	// untouched state file: another writer may have replaced it, or observation
	// may be unavailable. Preserve it and fail closed. For non-post-replacement
	// errors, the exact prior generation is the only clean no-effect receipt.
	uncertain := !installed && (fsutil.ReplacementCompleted(err) || readErr != nil || !bytes.Equal(current, state.before))
	return companionStatePublication{attempted: attempted, installed: installed, uncertain: uncertain}, err
}

func restoreCompanionPublishedState(state companionUpdateState, publication companionStatePublication) error {
	if publication.uncertain {
		return errors.New("workspace state publication outcome is foreign or unobservable")
	}
	if !publication.installed {
		return nil
	}
	err := store.WriteRawCAS(state.statePath, state.before, func() error {
		current, readErr := os.ReadFile(state.statePath)
		if readErr != nil || !bytes.Equal(current, publication.attempted) {
			return errors.New("published workspace state generation is no longer owned")
		}
		return nil
	})
	current, readErr := os.ReadFile(state.statePath)
	if readErr == nil && bytes.Equal(current, state.before) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restore owned workspace state: %w", err)
	}
	return errors.New("workspace state restore did not retain the exact prior generation")
}

func companionUpdateSamePath(left, right string) bool {
	canonicalLeft, leftErr := pathutil.CanonicalPotentialPath(left)
	canonicalRight, rightErr := pathutil.CanonicalPotentialPath(right)
	return leftErr == nil && rightErr == nil && pathutil.CaseFoldedPathEqual(canonicalLeft, canonicalRight)
}

func hasWorkspaceRecovery(dataDir, projectID, workspaceID string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dataDir, "projects", projectID, "recovery", workspaceID+".json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// hasCompanionUpdateProjectRecovery reserves the command-wide pre-result
// failure for recovery that cannot be tied to one independently inventoried
// workspace. A known workspace's recovery is instead a local fact: it is
// rendered as recovery-blocked and does not conceal later safe workspaces.
func hasCompanionUpdateProjectRecovery(dataDir, projectID string, states []companionUpdateState) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "projects", projectID, "recovery"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	workspaceIDs := make(map[string]struct{}, len(states))
	for _, state := range states {
		if state.valid {
			workspaceIDs[state.id] = struct{}{}
		}
	}
	for _, entry := range entries {
		if _, err := os.Lstat(filepath.Join(dataDir, "projects", projectID, "recovery", entry.Name())); err != nil {
			return false, fmt.Errorf("inspect recovery node %q: %w", entry.Name(), err)
		}
		workspaceID := strings.TrimSuffix(entry.Name(), ".json")
		if _, known := workspaceIDs[workspaceID]; !known {
			return true, nil
		}
	}
	return false, nil
}

func companionUpdateRepository(project domain.Project, id string) (domain.Repository, bool) {
	for _, r := range project.Repositories {
		if r.ID == id {
			return r, true
		}
	}
	return domain.Repository{}, false
}
