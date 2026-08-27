package service

// M04 completes the already-active M03 transaction.  It deliberately does
// not recapture or re-plan: the strict journal and the immutable plan are the
// only authority after repository effects have begun.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/store"
)

const updateReconciliationVersion = 1

// UpdateReconciliation is the strict, URL-free public record for checkouts
// intentionally retained after their repository was removed from a manifest.
// It contains local identity evidence only; a clone URL or credential must
// never become reconciliation metadata.
type UpdateReconciliation struct {
	Version  int                         `json:"version"`
	Retained []UpdateReconciliationEntry `json:"retained"`
}

type UpdateReconciliationEntry struct {
	RepositoryID string `json:"repositoryId"`
	Path         string `json:"path"`
	CommonGitDir string `json:"commonGitDir"`
}

func EncodeUpdateReconciliation(facts []UpdateRetainedFact) ([]byte, error) {
	entries := make([]UpdateReconciliationEntry, 0, len(facts))
	for _, fact := range facts {
		entries = append(entries, UpdateReconciliationEntry{RepositoryID: fact.RepositoryID, Path: fact.Path, CommonGitDir: fact.CommonGitDir})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RepositoryID < entries[j].RepositoryID })
	value := UpdateReconciliation{Version: updateReconciliationVersion, Retained: entries}
	if _, err := decodeUpdateReconciliationValue(value); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func DecodeUpdateReconciliation(data []byte) ([]RetainedUnmanagedFact, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value UpdateReconciliation
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing reconciliation JSON")
	}
	return decodeUpdateReconciliationValue(value)
}

func decodeUpdateReconciliationValue(value UpdateReconciliation) ([]RetainedUnmanagedFact, error) {
	if value.Version != updateReconciliationVersion {
		return nil, errors.New("unsupported update reconciliation version")
	}
	facts := make([]RetainedUnmanagedFact, 0, len(value.Retained))
	previous := ""
	for _, entry := range value.Retained {
		if !safeUpdateJournalID(entry.RepositoryID) || entry.RepositoryID <= previous || !safeRetainedUpdateAuthorityPath(entry.Path) || !safeRetainedUpdateAuthorityPath(entry.CommonGitDir) || redactCredentialShapes(entry.Path) != entry.Path || redactCredentialShapes(entry.CommonGitDir) != entry.CommonGitDir {
			return nil, errors.New("invalid retained reconciliation entry")
		}
		facts = append(facts, RetainedUnmanagedFact{RepositoryID: entry.RepositoryID, Path: entry.Path, CommonGitDir: entry.CommonGitDir})
		previous = entry.RepositoryID
	}
	return facts, nil
}

type UpdatePublicationResult struct {
	Version      int                      `json:"version"`
	Operation    string                   `json:"operation"`
	Status       string                   `json:"status"`
	ProjectID    string                   `json:"projectId"`
	Workspace    string                   `json:"workspace"`
	OperationID  string                   `json:"operationId,omitempty"`
	Repositories []UpdateRepositoryResult `json:"repositories"`
	// Completed remains execution-only compatibility for internal callers; it
	// is never part of the public JSON contract.
	Completed []string `json:"-"`
}

type UpdateRepositoryResult struct {
	ID             string               `json:"id"`
	ParentID       string               `json:"parentId,omitempty"`
	Mount          string               `json:"mount"`
	Path           string               `json:"path"`
	Branch         string               `json:"branch,omitempty"`
	Classification UpdateClassification `json:"classification"`
	ActualHead     string               `json:"actualHead,omitempty"`
	Action         string               `json:"action"`
	Status         string               `json:"status"`
	RollbackStatus string               `json:"rollbackStatus,omitempty"`
}

// ExecuteUpdate is the one public service orchestration. Dry-run callers keep
// using BuildUpdatePlan; a mutating invocation always executes and completes
// the same plan-bound journal rather than composing two independent actions.
func ExecuteUpdate(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir, override string) (UpdatePublicationResult, error) {
	snapshot, source, err := CollectUpdateSnapshot(ctx, project, workspace, dataDir, override, nil)
	if err != nil {
		return UpdatePublicationResult{}, err
	}
	plan, err := BuildUpdatePlan(snapshot, source)
	if err != nil {
		return UpdatePublicationResult{}, err
	}
	operationID, err := newUpdateOperationID()
	if err != nil {
		return UpdatePublicationResult{}, NewError(ErrorInternal, err)
	}
	request := UpdateExecutionRequest{DataDir: dataDir, ProjectID: project.ID, OperationID: operationID, Plan: plan}
	executor := NewUpdateExecutor()
	if _, err := executor.Execute(ctx, request); err != nil {
		return UpdatePublicationResult{}, err
	}
	return executor.CompleteUpdate(ctx, request)
}

func newUpdateOperationID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "update-" + hex.EncodeToString(bytes), nil
}

// RefuseActiveUpdateJournal is the mutating resolver guard. Read-only
// resolution deliberately does not call it, so status and doctor can still
// report an interrupted operation instead of becoming blind to it.
func RefuseActiveUpdateJournal(dataDir, projectID string) error {
	if !filepath.IsAbs(dataDir) || !safeUpdateJournalID(projectID) {
		return NewError(ErrorValidation, errors.New("invalid update journal scope"))
	}
	directory := filepath.Join(dataDir, "projects", projectID, "update")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return NewError(ErrorConflict, fmt.Errorf("inspect update journal directory: %w", err))
	}
	for _, entry := range entries {
		if !entry.IsDir() || !safeUpdateOperationID(entry.Name()) {
			return NewError(ErrorConflict, errors.New("unsafe update operation authority is present"))
		}
		journalPath := filepath.Join(directory, entry.Name(), "journal.json")
		snapshot, snapshotErr := secureCloneFileSnapshot(journalPath)
		if snapshotErr != nil || !snapshot.exists {
			continue
		}
		journal, decodeErr := decodeStrictUpdateJournal(snapshot.data)
		if decodeErr != nil || journal.ProjectID != projectID || journal.OperationID != entry.Name() {
			return NewError(ErrorConflict, errors.New("unsafe update journal is present"))
		}
		if journal.RollbackState == "active" || journal.RollbackState == "rolling-back" || journal.RollbackState == "incomplete" || journal.RollbackState == "cleaning" {
			return NewError(ErrorConflict, errors.New("an update journal is active; recover it before mutating the project"))
		}
	}
	return nil
}

// CompleteUpdate publishes the M04 generations only after M03 has retained a
// strict completed-effects journal. A clean publication failure restores
// exact backed-up bytes before asking M03 to reverse repository effects.
func (executor *UpdateExecutor) CompleteUpdate(ctx context.Context, request UpdateExecutionRequest) (UpdatePublicationResult, error) {
	if executor == nil || executor.dependencies.Git == nil {
		return UpdatePublicationResult{}, NewError(ErrorInternal, errors.New("update publisher is not configured"))
	}
	path, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
	if err != nil {
		return UpdatePublicationResult{}, NewError(ErrorValidation, err)
	}
	handle, err := executor.dependencies.Locker.ProjectLock(ctx, request.DataDir, request.ProjectID, time.Second)
	if err != nil {
		return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("acquire update publication project lock: %w", err))
	}
	released := false
	release := func() {
		if !released {
			handle.Unlock()
			released = true
		}
	}
	defer release()
	journal, err := openUpdateJournalForRecovery(path, request)
	if err != nil {
		return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("open update publication journal: %w", err))
	}
	if journal.RollbackState != "active" {
		return UpdatePublicationResult{}, NewError(ErrorConflict, errors.New("update journal is not ready for publication"))
	}
	for _, effect := range journal.Progress {
		if effect.State != "completed" {
			return UpdatePublicationResult{}, NewError(ErrorConflict, errors.New("repository effects are not complete"))
		}
	}
	// This pre-publication seam is intentionally before any public generation
	// changes. It lets the transaction prove the clean M03 recovery path without
	// conflating it with a failure after a tracked local configuration write.
	if err := effectBoundary(ctx, executor, "update-publication-preflight-before"); err != nil {
		return executor.rollbackPublishedUpdate(request, nil, err, release)
	}
	targets, err := executor.updatePublicationTargets(ctx, request, journal)
	if err != nil {
		return executor.rollbackPublishedUpdate(request, nil, err, release)
	}
	published := make([]publicationTarget, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return executor.rollbackPublishedUpdate(request, published, err, release)
		}
		if err := store.WriteRawCAS(target.path, target.data, func() error { return revalidateCloneFileSnapshot(target.before) }); err != nil {
			return executor.rollbackPublishedUpdate(request, published, NewError(ErrorConflict, fmt.Errorf("publish %s: %w", target.kind, err)), release)
		}
		published = append(published, target)
		journal.Progress = append(journal.Progress, UpdateJournalEffect{Sequence: len(journal.Progress) + 1, Name: "metadata-" + target.kind, State: "completed", Receipt: sha256String(target.data)})
		if err := writeUpdateJournalAt(ctx, executor, path, journal, "metadata-"+target.kind); err != nil {
			return executor.rollbackPublishedUpdate(request, published, err, release)
		}
	}
	if err := effectBoundary(ctx, executor, "publication-postcondition-before"); err != nil {
		return executor.rollbackPublishedUpdate(request, published, err, release)
	}
	if err := executor.verifyPublishedUpdate(ctx, request, targets); err != nil {
		return executor.rollbackPublishedUpdate(request, published, err, release)
	}
	if err := effectBoundary(ctx, executor, "publication-postcondition-after"); err != nil {
		return executor.rollbackPublishedUpdate(request, published, err, release)
	}
	if err := effectBoundary(ctx, executor, "publication-result-before"); err != nil {
		return executor.rollbackPublishedUpdate(request, published, err, release)
	}
	// A result is a publication postcondition, not best-effort presentation.
	// Build it while the journal can still roll back if a required observation
	// cannot be made.
	result, err := executor.updatePublicationResult(ctx, request, journal, targets)
	if err != nil {
		return executor.rollbackPublishedUpdate(request, published, err, release)
	}
	completed := make([]string, 0, len(journal.Progress))
	for _, effect := range journal.Progress {
		completed = append(completed, effect.Name)
	}
	if err := executor.terminalCleanup(ctx, request, path, journal, "success"); err != nil {
		return UpdatePublicationResult{}, err
	}
	result.Completed = completed
	return result, nil
}

type publicationTarget struct {
	kind, path string
	data       []byte
	before     cloneFileSnapshot
}

func (executor *UpdateExecutor) rollbackPublishedUpdate(request UpdateExecutionRequest, published []publicationTarget, cause error, release func()) (UpdatePublicationResult, error) {
	for i := len(published) - 1; i >= 0; i-- {
		target := published[i]
		current, err := secureCloneFileSnapshot(target.path)
		if err != nil || !current.exists || !bytes.Equal(current.data, target.data) {
			return UpdatePublicationResult{}, NewError(ErrorRollbackIncomplete, fmt.Errorf("update publication rollback retained %s: %w", target.kind, cause))
		}
		if target.before.exists {
			if err := store.WriteRawCAS(target.path, target.before.data, func() error { return revalidateCloneFileSnapshot(current) }); err != nil {
				return UpdatePublicationResult{}, NewError(ErrorRollbackIncomplete, fmt.Errorf("update publication rollback %s: %w", target.kind, err))
			}
		} else if err := executor.dependencies.Remove(target.path, func() error { return revalidateCloneFileSnapshot(current) }); err != nil {
			return UpdatePublicationResult{}, NewError(ErrorRollbackIncomplete, fmt.Errorf("update publication rollback %s: %w", target.kind, err))
		}
	}
	// M03 recovery obtains the same project lock. Release only after every
	// metadata generation has been restored (or durable incomplete evidence was
	// returned), so a second writer cannot observe a half-restored publication.
	release()
	if err := executor.Recover(context.Background(), request); err != nil {
		return UpdatePublicationResult{}, err
	}
	return UpdatePublicationResult{}, NewCleanRollbackError(cause)
}

func (executor *UpdateExecutor) updatePublicationTargets(ctx context.Context, request UpdateExecutionRequest, journal UpdateJournal) ([]publicationTarget, error) {
	baseline := request.Plan.executionBaseline()
	candidate, err := config.LoadPortableManifest(request.Plan.CandidateManifestBytes())
	if err != nil {
		return nil, NewError(ErrorValidation, err)
	}
	local, err := config.LoadProject(baseline.localConfig)
	if err != nil {
		return nil, NewError(ErrorValidation, err)
	}
	state, err := store.DecodeWorkspace(baseline.defaultState.Bytes)
	if err != nil {
		return nil, NewError(ErrorValidation, err)
	}
	registry, err := store.DecodeRegistry(baseline.registry)
	if err != nil {
		return nil, NewError(ErrorValidation, err)
	}
	heads, err := executor.updateJournalHeads(ctx, journal, request)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	common := map[string]string{}
	for _, observation := range baseline.observations {
		paths[observation.RepositoryID], common[observation.RepositoryID] = observation.Path, observation.CommonGitDir
	}
	for _, repository := range candidateManifestParentFirst(candidate) {
		if paths[repository] == "" {
			return nil, NewError(ErrorConflict, fmt.Errorf("repository %q lacks a locked checkout path", repository))
		}
		if _, err := os.Lstat(paths[repository]); err != nil {
			return nil, NewError(ErrorConflict, fmt.Errorf("repository %q checkout disappeared: %w", repository, err))
		}
		head, err := executor.dependencies.Git.Head(ctx, paths[repository])
		if err != nil {
			return nil, NewError(ErrorGit, err)
		}
		expectedHead := heads[repository]
		if expectedHead == "" {
			expectedHead = publicationObservation(baseline.observations, repository).Head
		}
		if expectedHead == "" || head != expectedHead {
			return nil, NewError(ErrorConflict, fmt.Errorf("repository %q HEAD changed during publication", repository))
		}
		actualCommon, err := executor.dependencies.Git.CommonGitDir(ctx, paths[repository])
		if err != nil {
			return nil, NewError(ErrorGit, err)
		}
		observation := publicationObservation(baseline.observations, repository)
		if observation.CommonGitDir != "" && actualCommon != observation.CommonGitDir {
			return nil, NewError(ErrorConflict, fmt.Errorf("repository %q Git identity changed during publication", repository))
		}
		if observation.CommonGitDir == "" {
			if err := validateAddedPublicationIdentity(journal, request, repository, paths[repository], actualCommon); err != nil {
				return nil, err
			}
		}
		common[repository] = actualCommon
		relative, err := filepath.Rel(baseline.project.LogicalRoot, paths[repository])
		if err != nil {
			return nil, err
		}
		local.Repositories[repository] = config.Repository{Source: filepath.ToSlash(relative), Parent: candidate.Repositories[repository].Parent, DefaultMount: candidate.Repositories[repository].Mount, DefaultBranch: candidate.Repositories[repository].DefaultBranch}
		state.Repositories[repository] = store.CheckoutState{Branch: candidate.Repositories[repository].DefaultBranch, Mount: candidate.Repositories[repository].Mount, ResolvedPath: paths[repository], Head: head}
	}
	for id := range local.Repositories {
		if _, ok := candidate.Repositories[id]; !ok {
			delete(local.Repositories, id)
		}
	}
	for id := range state.Repositories {
		if _, ok := candidate.Repositories[id]; !ok {
			delete(state.Repositories, id)
		}
	}
	local.Project.Name = candidate.Project.Name
	local.Manifest.Source = request.Plan.Source.Value
	registryProject, ok := registry.Projects[request.ProjectID]
	if !ok {
		return nil, NewError(ErrorConflict, errors.New("project registry entry disappeared"))
	}
	registryProject.Name = candidate.Project.Name
	registryProject.RepositoryIDs = map[string]string{}
	for id := range candidate.Repositories {
		registryProject.RepositoryIDs[common[id]] = id
	}
	registry.Projects[request.ProjectID] = registryProject
	if err := executor.revalidateRetainedPublicationFacts(ctx, journal.Retained); err != nil {
		return nil, err
	}
	retained := append([]UpdateRetainedFact(nil), journal.Retained...)
	reconciliation, err := EncodeUpdateReconciliation(retained)
	if err != nil {
		return nil, err
	}
	localBytes, err := config.MarshalProject(local)
	if err != nil {
		return nil, err
	}
	stateBytes, err := store.WorkspaceBytes(state)
	if err != nil {
		return nil, err
	}
	registryBytes, err := store.RegistryBytes(registry)
	if err != nil {
		return nil, err
	}
	pathsByKind := map[string]string{"local-config": baseline.project.ConfigPath, "default-state": baseline.defaultState.Path, "registry": filepath.Join(request.DataDir, "registry.json"), "reconciliation": filepath.Join(request.DataDir, "projects", request.ProjectID, "reconciliation.json")}
	bytesByKind := map[string][]byte{"local-config": localBytes, "default-state": stateBytes, "registry": registryBytes, "reconciliation": reconciliation}
	kinds := []string{"local-config", "default-state", "registry", "reconciliation"}
	result := make([]publicationTarget, 0, len(kinds))
	for _, kind := range kinds {
		before, err := secureCloneFileSnapshot(pathsByKind[kind])
		if err != nil {
			return nil, err
		}
		result = append(result, publicationTarget{kind: kind, path: pathsByKind[kind], data: bytesByKind[kind], before: before})
	}
	return result, nil
}

func (executor *UpdateExecutor) updateJournalHeads(ctx context.Context, journal UpdateJournal, request UpdateExecutionRequest) (map[string]string, error) {
	heads := map[string]string{}
	baseline := request.Plan.executionBaseline()
	for _, effect := range journal.Progress {
		if effect.Repository == "" {
			continue
		}
		if effect.State != "completed" || heads[effect.Repository] != "" {
			return nil, NewError(ErrorConflict, fmt.Errorf("repository %q has invalid publication receipt state", effect.Repository))
		}
		if receipt, err := decodeUpdateFastForwardReceipt(effect.Receipt); err == nil {
			if effect.Name != "repository-"+effect.Repository+"-fast-forward" {
				return nil, NewError(ErrorConflict, fmt.Errorf("repository %q fast-forward receipt has invalid effect authority", effect.Repository))
			}
			observation := publicationObservation(baseline.observations, effect.Repository)
			if err := executor.validateFastForwardPublicationReceipt(ctx, request, effect, observation, receipt); err != nil {
				return nil, err
			}
			heads[effect.Repository] = receipt.NewCommit
			continue
		}
		if receipt, err := decodeUpdateAddedReceipt(effect.Receipt); err == nil && effect.Name == "repository-"+effect.Repository+"-add" && receipt.OperationID == request.OperationID && receipt.ProjectID == request.ProjectID && receipt.RepositoryID == effect.Repository {
			heads[effect.Repository] = receipt.Head
			continue
		}
		return nil, NewError(ErrorConflict, fmt.Errorf("repository %q has an invalid publication receipt", effect.Repository))
	}
	return heads, nil
}

// validateFastForwardPublicationReceipt is the M04 use of M03's strict,
// opaque receipt grammar. Every receipt field is bound to the locked
// observation and the live checkout before any public metadata is built.
func (executor *UpdateExecutor) validateFastForwardPublicationReceipt(ctx context.Context, request UpdateExecutionRequest, effect UpdateJournalEffect, observation DriftRepositoryObservation, receipt updateFastForwardReceipt) error {
	if observation.RepositoryID != effect.Repository || observation.Path == "" || observation.CommonGitDir == "" || receipt.OperationID != request.OperationID || receipt.ProjectID != request.ProjectID || receipt.RepositoryID != effect.Repository || receipt.Branch != observation.Branch || receipt.OldCommit != observation.Head || receipt.Remote != observation.Upstream.Remote || receipt.RemoteRef != observation.Upstream.Merge || receipt.NewCommit != receipt.ActualRemoteCommit || (observation.AdvertisedKnown && receipt.ActualRemoteCommit != observation.AdvertisedCommit) {
		return NewError(ErrorConflict, fmt.Errorf("repository %q fast-forward receipt does not match locked authority", effect.Repository))
	}
	head, err := executor.dependencies.Git.Head(ctx, observation.Path)
	if err != nil || head != receipt.NewCommit {
		return NewError(ErrorConflict, fmt.Errorf("repository %q HEAD changed during publication", effect.Repository))
	}
	common, err := executor.dependencies.Git.CommonGitDir(ctx, observation.Path)
	if err != nil || common != observation.CommonGitDir {
		return NewError(ErrorConflict, fmt.Errorf("repository %q Git identity changed during publication", effect.Repository))
	}
	configured, err := executor.dependencies.Git.ObserveConfiguredRef(ctx, observation.Path, receipt.Remote, receipt.RemoteRef)
	if err != nil || configured.Remote != receipt.Remote || configured.RemoteRef != receipt.RemoteRef || configured.Commit != receipt.ActualRemoteCommit {
		return NewError(ErrorConflict, fmt.Errorf("repository %q configured ref changed during publication", effect.Repository))
	}
	return nil
}

func publicationObservation(observations []DriftRepositoryObservation, repositoryID string) DriftRepositoryObservation {
	for _, observation := range observations {
		if observation.RepositoryID == repositoryID {
			return observation
		}
	}
	return DriftRepositoryObservation{}
}

func validateAddedPublicationIdentity(journal UpdateJournal, request UpdateExecutionRequest, repositoryID, root, commonGitDir string) error {
	for _, effect := range journal.Progress {
		if effect.Repository != repositoryID {
			continue
		}
		receipt, err := decodeUpdateAddedReceipt(effect.Receipt)
		if err != nil || effect.Name != "repository-"+repositoryID+"-add" || receipt.OperationID != request.OperationID || receipt.ProjectID != request.ProjectID || receipt.RepositoryID != repositoryID {
			return NewError(ErrorConflict, fmt.Errorf("repository %q addition receipt is invalid", repositoryID))
		}
		digest, digestErr := updateCommonGitDirDigest(root, commonGitDir)
		if digestErr != nil || digest != receipt.CommonGitDirSHA256 {
			return NewError(ErrorConflict, fmt.Errorf("repository %q Git identity changed during publication", repositoryID))
		}
		return nil
	}
	return NewError(ErrorConflict, fmt.Errorf("repository %q lacks publication identity authority", repositoryID))
}

func (executor *UpdateExecutor) revalidateRetainedPublicationFacts(ctx context.Context, retained []UpdateRetainedFact) error {
	for _, fact := range retained {
		info, err := os.Lstat(fact.Path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return NewError(ErrorConflict, fmt.Errorf("retained repository %q path changed during publication", fact.RepositoryID))
		}
		common, err := executor.dependencies.Git.CommonGitDir(ctx, fact.Path)
		if err != nil || common != fact.CommonGitDir {
			return NewError(ErrorConflict, fmt.Errorf("retained repository %q Git identity changed during publication", fact.RepositoryID))
		}
		top, err := executor.dependencies.Git.TopLevel(ctx, fact.Path)
		if err != nil || !sameCheckoutPath(top, fact.Path) {
			return NewError(ErrorConflict, fmt.Errorf("retained repository %q checkout changed during publication", fact.RepositoryID))
		}
	}
	return nil
}

func (executor *UpdateExecutor) updatePublicationResult(ctx context.Context, request UpdateExecutionRequest, journal UpdateJournal, targets []publicationTarget) (UpdatePublicationResult, error) {
	baseline := request.Plan.executionBaseline()
	candidate, err := config.LoadPortableManifest(request.Plan.CandidateManifestBytes())
	if err != nil {
		return UpdatePublicationResult{}, NewError(ErrorValidation, err)
	}
	paths := make(map[string]string, len(baseline.observations))
	branches := make(map[string]string, len(baseline.observations))
	for _, observation := range baseline.observations {
		paths[observation.RepositoryID], branches[observation.RepositoryID] = observation.Path, observation.Branch
	}
	for _, fact := range journal.Retained {
		paths[fact.RepositoryID] = fact.Path
	}
	var publishedState store.WorkspaceState
	var publishedRegistry store.Registry
	for _, target := range targets {
		switch target.kind {
		case "default-state":
			publishedState, err = store.DecodeWorkspace(target.data)
		case "registry":
			publishedRegistry, err = store.DecodeRegistry(target.data)
		}
		if err != nil {
			return UpdatePublicationResult{}, NewError(ErrorConflict, errors.New("published result authority is invalid"))
		}
	}
	registryProject, registryFound := publishedRegistry.Projects[request.ProjectID]
	if !registryFound {
		return UpdatePublicationResult{}, NewError(ErrorConflict, errors.New("published result registry authority is missing"))
	}
	actions := request.Plan.Actions()
	repositories := request.Plan.Repositories()
	if len(actions) != len(repositories) {
		return UpdatePublicationResult{}, NewError(ErrorConflict, errors.New("update result action authority is incomplete"))
	}
	values := make([]UpdateRepositoryResult, 0, len(request.Plan.Repositories()))
	for index, item := range repositories {
		action := actions[index]
		if action.RepositoryID != item.ID {
			return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("repository %q lacks immutable action authority", item.ID))
		}
		value := UpdateRepositoryResult{ID: item.ID, ParentID: item.ParentID, Mount: item.Mount, Path: paths[item.ID], Classification: item.Classification, Action: action.Action, Status: "completed"}
		if value.Path == "" {
			return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("repository %q lacks a verified result path", item.ID))
		}
		expectedHead := item.Head
		expectedCommon := ""
		if portable, ok := candidate.Repositories[item.ID]; ok {
			value.Branch = portable.DefaultBranch
			checkout, found := publishedState.Repositories[item.ID]
			if !found || !aggregateObjectID(checkout.Head) {
				return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("repository %q lacks published HEAD authority", item.ID))
			}
			expectedHead = checkout.Head
			for common, repositoryID := range registryProject.RepositoryIDs {
				if repositoryID == item.ID {
					if expectedCommon != "" {
						return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("repository %q has ambiguous published Git identity", item.ID))
					}
					expectedCommon = common
				}
			}
		} else {
			value.Branch = branches[item.ID]
			value.RollbackStatus = "retained-unmanaged"
			for _, fact := range journal.Retained {
				if fact.RepositoryID == item.ID {
					expectedCommon = fact.CommonGitDir
					break
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return UpdatePublicationResult{}, err
		}
		head, headErr := executor.dependencies.Git.Head(ctx, value.Path)
		if headErr != nil || !aggregateObjectID(expectedHead) || head != expectedHead {
			return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("repository %q lacks a verified actual commit", item.ID))
		}
		common, commonErr := executor.dependencies.Git.CommonGitDir(ctx, value.Path)
		if commonErr != nil || expectedCommon == "" || common != expectedCommon {
			return UpdatePublicationResult{}, NewError(ErrorConflict, fmt.Errorf("repository %q lacks a verified Git identity", item.ID))
		}
		if err := ctx.Err(); err != nil {
			return UpdatePublicationResult{}, err
		}
		value.ActualHead = expectedHead
		values = append(values, value)
	}
	return UpdatePublicationResult{Version: 1, Operation: "update", Status: "completed", ProjectID: baseline.project.ID, Workspace: baseline.workspace.Name, OperationID: request.OperationID, Repositories: values}, nil
}

func (executor *UpdateExecutor) verifyPublishedUpdate(ctx context.Context, request UpdateExecutionRequest, targets []publicationTarget) error {
	for _, target := range targets {
		current, err := os.ReadFile(target.path)
		if err != nil || !bytes.Equal(current, target.data) {
			return NewError(ErrorConflict, fmt.Errorf("published %s generation changed", target.kind))
		}
	}
	baseline := request.Plan.executionBaseline()
	project, err := NewResolver().ResolveReadOnly(ctx, ResolveRequest{Path: baseline.project.LogicalRoot, ProjectPath: baseline.project.ConfigPath, DataDir: request.DataDir})
	if err != nil {
		return NewError(ErrorValidation, fmt.Errorf("reload published project: %w", err))
	}
	if _, err := ListWorkspaces(project.Project, request.DataDir); err != nil {
		return err
	}
	return nil
}
