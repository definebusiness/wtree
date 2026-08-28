package service

// The update executor is intentionally an internal operation boundary.  M03
// records and reverses repository effects, but does not publish a new project
// configuration, workspace state, or registry generation; that visibility
// boundary belongs to M04.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/transaction"
)

const UpdateJournalVersion = 1

var errNoActiveUpdateJournal = errors.New("no active update journal")

// UpdateJournal is strict private evidence for one update operation.  It is
// deliberately separate from every persisted public schema.
type UpdateJournal struct {
	Version       int                   `json:"version"`
	OperationID   string                `json:"operationId"`
	ProjectID     string                `json:"projectId"`
	PlanDigest    string                `json:"planDigest"`
	Generations   UpdatePlanGenerations `json:"generations"`
	Backups       []UpdateJournalBackup `json:"backups"`
	Progress      []UpdateJournalEffect `json:"progress"`
	Retained      []UpdateRetainedFact  `json:"retained,omitempty"`
	RollbackState string                `json:"rollbackState"`
	// TerminalOutcome is set only while terminal cleanup is in progress. It
	// records that every repository inverse has already reached a terminal
	// state; recovery must therefore resume private cleanup rather than repeat
	// a Git inverse after an interruption.
	TerminalOutcome string `json:"terminalOutcome,omitempty"`
	Failure         string `json:"failure,omitempty"`
}

// UpdateJournalBackup deliberately contains only safe metadata.  The exact
// source bytes live in a private opaque file beside the journal and never
// enter JSON, a recovery summary, an error, or a diagnostic.
type UpdateJournalBackup struct {
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode"`
	Length  int64  `json:"length"`
	SHA256  string `json:"sha256"`
}

type UpdateJournalEffect struct {
	Sequence   int    `json:"sequence"`
	Name       string `json:"name"`
	Repository string `json:"repository,omitempty"`
	Receipt    string `json:"receipt,omitempty"`
	State      string `json:"state"`
}

// UpdateRetainedFact is deterministic private M03 evidence for an omitted
// checkout. M04 is solely responsible for publishing reconciliation metadata.
type UpdateRetainedFact struct {
	RepositoryID    string `json:"repositoryId"`
	Path            string `json:"path"`
	CommonGitDir    string `json:"commonGitDir"`
	CandidateSHA256 string `json:"candidateSha256"`
}

func (journal UpdateJournal) Validate() error {
	if journal.Version != UpdateJournalVersion || !safeUpdateOperationID(journal.OperationID) || !safeUpdateJournalID(journal.ProjectID) || !validSHA256(journal.PlanDigest) {
		return errors.New("invalid update journal identity")
	}
	if journal.Generations.CurrentManifestSHA256 == "" || journal.Generations.CandidateManifestSHA256 == "" || journal.Generations.LocalConfigSHA256 == "" || journal.Generations.RegistrySHA256 == "" || journal.Generations.DefaultStateSHA256 == "" {
		return errors.New("update journal lacks captured generations")
	}
	for _, value := range []string{journal.Generations.CurrentManifestSHA256, journal.Generations.CandidateManifestSHA256, journal.Generations.LocalConfigSHA256, journal.Generations.RegistrySHA256, journal.Generations.DefaultStateSHA256} {
		if !validSHA256(value) {
			return errors.New("invalid update journal generation")
		}
	}
	if journal.Generations.ReconciliationSHA256 != "" && !validSHA256(journal.Generations.ReconciliationSHA256) {
		return errors.New("invalid update journal reconciliation generation")
	}
	if journal.RollbackState != "active" && journal.RollbackState != "rolling-back" && journal.RollbackState != "cleaning" && journal.RollbackState != "incomplete" {
		return errors.New("invalid update journal rollback state")
	}
	if journal.Failure != "" && boundedRedactedDiagnostic(journal.Failure) != journal.Failure {
		return errors.New("update journal failure is not redacted")
	}
	previousBackup := ""
	for _, backup := range journal.Backups {
		if !validUpdateBackupKind(backup.Kind) || backup.File != backup.Kind+".bin" || previousBackup >= backup.Kind {
			return errors.New("invalid update journal backup identity")
		}
		if backup.Existed {
			if backup.Length < 0 || backup.Mode&^uint32(0o7777) != 0 || !validSHA256(backup.SHA256) {
				return errors.New("invalid update journal backup metadata")
			}
		} else if backup.Mode != 0 || backup.Length != 0 || backup.SHA256 != "" {
			return errors.New("absent update journal backup has bytes")
		}
		previousBackup = backup.Kind
	}
	seenEffects := map[string]bool{}
	started := 0
	unreverted := 0
	rolledBack := 0
	for index, effect := range journal.Progress {
		if effect.Sequence != index+1 || !validUpdateEffectName(effect.Name) || seenEffects[effect.Name] || (effect.Repository != "" && !safeUpdateJournalID(effect.Repository)) || (effect.State != "started" && effect.State != "prepared" && effect.State != "completed" && effect.State != "rolled-back" && effect.State != "unreverted") || boundedRedactedDiagnostic(effect.Receipt) != effect.Receipt || (effect.State == "prepared" && effect.Receipt == "") {
			return errors.New("invalid update journal progress")
		}
		seenEffects[effect.Name] = true
		if effect.State == "started" {
			started++
			if index != len(journal.Progress)-1 {
				return errors.New("update journal has a non-final started effect")
			}
		}
		if effect.State == "prepared" && index != len(journal.Progress)-1 {
			return errors.New("update journal has a non-final prepared effect")
		}
		if effect.State == "unreverted" {
			unreverted++
		}
		if effect.State == "rolled-back" {
			rolledBack++
		}
	}
	if journal.RollbackState == "active" && (journal.Failure != "" || started > 1 || unreverted != 0 || rolledBack != 0) {
		return errors.New("active update journal has invalid transition state")
	}
	if journal.RollbackState == "rolling-back" && journal.Failure == "" {
		return errors.New("rolling-back update journal lacks failure evidence")
	}
	if journal.RollbackState == "incomplete" && (journal.Failure == "" || unreverted == 0) {
		return errors.New("incomplete update journal lacks unreverted evidence")
	}
	if journal.RollbackState == "cleaning" {
		if journal.TerminalOutcome != "success" && journal.TerminalOutcome != "rolled-back" {
			return errors.New("cleaning update journal lacks terminal outcome")
		}
		if started != 0 || unreverted != 0 {
			return errors.New("cleaning update journal has non-terminal effects")
		}
		if journal.TerminalOutcome == "success" && len(journal.Progress) != 0 && rolledBack != 0 {
			return errors.New("successful cleaning journal has rolled-back effects")
		}
		if journal.TerminalOutcome == "rolled-back" && len(journal.Progress) != rolledBack {
			return errors.New("rolled-back cleaning journal has completed effects")
		}
	} else if journal.RollbackState == "incomplete" && journal.TerminalOutcome != "" {
		if journal.TerminalOutcome != "success" && journal.TerminalOutcome != "rolled-back" {
			return errors.New("incomplete terminal cleanup journal lacks terminal outcome")
		}
		for _, effect := range journal.Progress {
			if effect.Name == "terminal-cleanup" {
				if effect.State != "unreverted" {
					return errors.New("incomplete terminal cleanup lacks pending cleanup evidence")
				}
				continue
			}
			if effect.State != "completed" && effect.State != "rolled-back" {
				return errors.New("incomplete terminal cleanup has non-terminal repository effect")
			}
		}
	} else if journal.TerminalOutcome != "" {
		return errors.New("non-cleaning update journal has terminal outcome")
	}
	previous := ""
	for _, fact := range journal.Retained {
		if !safeUpdateJournalID(fact.RepositoryID) || !filepath.IsAbs(fact.Path) || filepath.Clean(fact.Path) != fact.Path || !filepath.IsAbs(fact.CommonGitDir) || filepath.Clean(fact.CommonGitDir) != fact.CommonGitDir || !validSHA256(fact.CandidateSHA256) || fact.CandidateSHA256 != journal.Generations.CandidateManifestSHA256 || previous >= fact.RepositoryID {
			return errors.New("invalid retained update reconciliation fact")
		}
		previous = fact.RepositoryID
	}
	return nil
}

func validUpdateBackupKind(value string) bool {
	switch value {
	case "tracked-manifest", "local-config", "default-state", "registry", "reconciliation":
		return true
	default:
		return false
	}
}

func validUpdateEffectName(value string) bool {
	if value == "" || len(value) > 160 || boundedRedactedDiagnostic(value) != value {
		return false
	}
	for _, runeValue := range value {
		if !(runeValue >= 'a' && runeValue <= 'z' || runeValue >= '0' && runeValue <= '9' || runeValue == '-') {
			return false
		}
	}
	return true
}

func updatePlanDigest(plan UpdatePlan) (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	data, err := plan.JSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func UpdateJournalPath(dataDir, projectID, operationID string) (string, error) {
	if !safeUpdateJournalID(projectID) || !safeUpdateOperationID(operationID) {
		return "", errors.New("unsafe update journal identity")
	}
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return "", errors.New("update data directory must be absolute")
	}
	return filepath.Join(dataDir, "projects", projectID, "update", operationID, "journal.json"), nil
}

func safeUpdateJournalID(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && filepath.Clean(value) == value && !strings.ContainsAny(value, "/\\\x00")
}
func safeUpdateOperationID(value string) bool {
	if !safeUpdateJournalID(value) || len(value) > 96 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// updateEffect is one executor-owned operation and its ownership-safe inverse.
// It is deliberately private: the production executor must derive every
// effect from the locked snapshot, rather than accepting caller-selected
// filesystem or Git work.
type updateEffect struct {
	Name       string
	Repository string
	// Prepare records an ownership receipt before an irreversible publish. It
	// is used only by the private added-repository effect; callers cannot
	// inject it through the production request.
	Prepare func(context.Context) (string, error)
	Execute func(context.Context) (string, error)
	// Cleanup is used only when Execute itself fails after creating private
	// operation-owned state but before the effect becomes completed.
	Cleanup  func(context.Context) error
	Rollback func(context.Context) error
}

type UpdateExecutionRequest struct {
	DataDir     string
	ProjectID   string
	OperationID string
	Plan        UpdatePlan
}

// updateExecutionTestSeams exists only for package tests. It cannot be
// supplied through the production request, which keeps execution authority
// bound to the private plan baseline and locked collector.
type updateExecutionTestSeams struct {
	effects    []updateEffect
	recapture  func(context.Context, UpdatePlan) (DriftSnapshot, error)
	revalidate func(context.Context) error
	progress   func(transaction.Event)
}

type UpdateExecutionResult struct {
	OperationID string
	Completed   []string
}

type UpdateExecutorDependencies struct {
	Locker           ProjectLocker
	Git              gitadapter.Git
	WriteJournal     func(string, []byte, os.FileMode, func() error) error
	Remove           func(string, func() error) error
	WriteRecoveryCAS func(string, store.RecoveryRecord, func() error) error
	Before           func(string) error
}

type UpdateExecutor struct{ dependencies UpdateExecutorDependencies }

func NewUpdateExecutor() *UpdateExecutor { return NewUpdateExecutorWith(UpdateExecutorDependencies{}) }
func NewUpdateExecutorWith(dependencies UpdateExecutorDependencies) *UpdateExecutor {
	if dependencies.Locker == nil {
		dependencies.Locker = lock.Manager{}
	}
	if dependencies.Git == nil {
		dependencies.Git = gitadapter.NewAdapter("git")
	}
	if dependencies.WriteJournal == nil {
		dependencies.WriteJournal = func(path string, data []byte, mode os.FileMode, compare func() error) error {
			return fsutil.WriteFileAtomicModeWithHook(path, data, mode, func(step string) error {
				if step == "before-rename" && compare != nil {
					return compare()
				}
				return nil
			})
		}
	}
	if dependencies.Remove == nil {
		dependencies.Remove = func(path string, compare func() error) error {
			if compare != nil {
				if err := compare(); err != nil {
					return err
				}
			}
			return os.Remove(path)
		}
	}
	if dependencies.WriteRecoveryCAS == nil {
		dependencies.WriteRecoveryCAS = store.WriteRecoveryCAS
	}
	return &UpdateExecutor{dependencies: dependencies}
}

func (executor *UpdateExecutor) Execute(ctx context.Context, request UpdateExecutionRequest) (UpdateExecutionResult, error) {
	return executor.execute(ctx, request, updateExecutionTestSeams{})
}

// Recover reopens one private M03 journal after an interrupted executor.  It
// deliberately accepts the original private plan rather than trying to infer
// authority from the filesystem: the journal digest must still bind to that
// plan, and every inverse is conditional on the generation recorded by the
// operation.  M04 owns recovery for its metadata publications; M03 can recover
// only fast-forwards and the tracked manifest they changed.
func (executor *UpdateExecutor) Recover(ctx context.Context, request UpdateExecutionRequest) error {
	if executor == nil || executor.dependencies.Locker == nil || executor.dependencies.WriteJournal == nil || executor.dependencies.Remove == nil || executor.dependencies.Git == nil {
		return NewError(ErrorInternal, errors.New("update executor is not configured"))
	}
	if err := request.Plan.Validate(); err != nil {
		return NewError(ErrorValidation, fmt.Errorf("validate update recovery plan: %w", err))
	}
	if err := validateUpdateExecutionRequest(request); err != nil {
		return NewError(ErrorValidation, err)
	}
	path, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
	if err != nil {
		return NewError(ErrorValidation, err)
	}
	handle, err := executor.dependencies.Locker.ProjectLock(ctx, request.DataDir, request.ProjectID, time.Second)
	if err != nil {
		return NewError(ErrorConflict, fmt.Errorf("acquire update recovery project lock: %w", err))
	}
	defer handle.Unlock()
	journal, err := openUpdateJournalForRecovery(path, request)
	if err != nil {
		if errors.Is(err, errNoActiveUpdateJournal) {
			return executor.recoverTerminalSummary(ctx, request, path)
		}
		return NewError(ErrorValidation, fmt.Errorf("open update recovery journal: %w", err))
	}
	if journal.RollbackState == "cleaning" || (journal.RollbackState == "incomplete" && journal.TerminalOutcome != "") {
		if err := executor.terminalCleanup(ctx, request, path, journal, journal.TerminalOutcome); err != nil {
			return err
		}
		return nil
	}
	if err := validateOwnedUpdateBackups(request, journal.Backups); err != nil {
		return executor.recoveryIncomplete(path, journal, "backup-verify", err)
	}
	journal.RollbackState, journal.Failure = "rolling-back", "recovery"
	if err := writeUpdateJournalAt(ctx, executor, path, journal, "recovery-start"); err != nil {
		return executor.recoveryIncomplete(path, journal, "recovery-start", err)
	}

	baseline := request.Plan.executionBaseline()
	for index := len(journal.Progress) - 1; index >= 0; index-- {
		effect := &journal.Progress[index]
		if effect.State == "rolled-back" {
			continue
		}
		if effect.State == "started" {
			return executor.recoveryIncomplete(path, journal, effect.Name, errors.New("interrupted update effect lacks an ownership receipt"))
		}
		if err := effectBoundary(ctx, executor, "recovery-"+effect.Name+"-before"); err != nil {
			return executor.recoveryIncomplete(path, journal, effect.Name, err)
		}
		if err := executor.recoverJournalEffectForRequest(ctx, request, baseline, *effect); err != nil {
			return executor.recoveryIncomplete(path, journal, effect.Name, err)
		}
		effect.State = "rolled-back"
		if err := writeUpdateJournalAt(ctx, executor, path, journal, "recovery-"+effect.Name+"-rolled-back"); err != nil {
			return executor.recoveryIncomplete(path, journal, effect.Name, err)
		}
		if err := effectBoundary(ctx, executor, "recovery-"+effect.Name+"-after"); err != nil {
			return executor.recoveryIncomplete(path, journal, effect.Name, err)
		}
	}
	if err := restoreTrackedManifestBackup(request, baseline.current.Path, request.Plan.CandidateManifestBytes(), journal.Backups); err != nil {
		return executor.recoveryIncomplete(path, journal, "tracked-manifest-restore", err)
	}
	return executor.terminalCleanup(ctx, request, path, journal, "rolled-back")
}

func openUpdateJournalForRecovery(path string, request UpdateExecutionRequest) (UpdateJournal, error) {
	snapshot, err := secureCloneFileSnapshot(path)
	if err != nil {
		return UpdateJournal{}, err
	}
	if !snapshot.exists {
		return UpdateJournal{}, errNoActiveUpdateJournal
	}
	if err := validateExistingUpdateJournalDirectory(filepath.Dir(path)); err != nil {
		return UpdateJournal{}, err
	}
	if snapshot.mode.Perm() != 0o600 {
		return UpdateJournal{}, errors.New("update recovery journal is absent or unsafe")
	}
	journal, err := decodeStrictUpdateJournal(snapshot.data)
	if err != nil {
		return UpdateJournal{}, err
	}
	digest, err := updatePlanDigest(request.Plan)
	if err != nil || journal.OperationID != request.OperationID || journal.ProjectID != request.ProjectID || journal.PlanDigest != digest || journal.Generations != request.Plan.Generations {
		return UpdateJournal{}, errors.New("update recovery journal does not bind to the request plan")
	}
	return journal, nil
}

func (executor *UpdateExecutor) recoverJournalEffect(ctx context.Context, baseline updateExecutionBaseline, effect UpdateJournalEffect) error {
	return executor.recoverJournalEffectForRequest(ctx, UpdateExecutionRequest{}, baseline, effect)
}

func (executor *UpdateExecutor) recoverJournalEffectForRequest(ctx context.Context, request UpdateExecutionRequest, baseline updateExecutionBaseline, effect UpdateJournalEffect) error {
	if strings.HasPrefix(effect.Name, "metadata-") {
		return executor.restorePublishedUpdateGeneration(ctx, request, baseline, effect)
	}
	if effect.Name == "repository-"+effect.Repository+"-add" {
		return executor.recoverPreparedAddition(ctx, request, baseline, effect)
	}
	const suffix = "-fast-forward"
	if !strings.HasPrefix(effect.Name, "repository-") || !strings.HasSuffix(effect.Name, suffix) || effect.Repository == "" || effect.Name != "repository-"+effect.Repository+suffix {
		return errors.New("update recovery effect is not an owned fast-forward")
	}
	receipt, err := decodeUpdateFastForwardReceipt(effect.Receipt)
	if err != nil {
		return err
	}
	for _, observation := range baseline.observations {
		if observation.RepositoryID != effect.Repository || observation.Path == "" {
			continue
		}
		if receipt.OperationID != request.OperationID || receipt.ProjectID != request.ProjectID || receipt.RepositoryID != effect.Repository || observation.Branch != receipt.Branch || observation.Head != receipt.OldCommit || observation.Upstream.Remote != receipt.Remote || observation.Upstream.Merge != receipt.RemoteRef {
			continue
		}
		current, err := executor.dependencies.Git.Head(ctx, observation.Path)
		if err != nil {
			return err
		}
		if current == receipt.NewCommit {
			if err := executor.dependencies.Git.RestoreFastForward(ctx, observation.Path, gitadapter.FastForwardReceipt{Branch: receipt.Branch, OldCommit: receipt.OldCommit, NewCommit: receipt.NewCommit}); err != nil {
				return err
			}
		} else if current != receipt.OldCommit {
			return errors.New("update recovery local branch changed concurrently")
		}
		return executor.dependencies.Git.RestoreConfiguredRef(ctx, observation.Path, gitadapter.ConfiguredRefFetch{Remote: receipt.Remote, RemoteRef: receipt.RemoteRef, PreviousRemoteCommit: receipt.PreviousRemoteCommit, ActualRemoteCommit: receipt.ActualRemoteCommit})
	}
	return errors.New("update recovery effect has no locked repository receipt")
}

// restorePublishedUpdateGeneration is the M04 crash-recovery inverse. The
// receipt is only a digest, never a path or URL: it proves the current public
// generation is still the one this operation published before an opaque exact
// backup may replace (or remove) it.
func (executor *UpdateExecutor) restorePublishedUpdateGeneration(ctx context.Context, request UpdateExecutionRequest, baseline updateExecutionBaseline, effect UpdateJournalEffect) error {
	kind := strings.TrimPrefix(effect.Name, "metadata-")
	if kind == effect.Name || !validUpdateBackupKind(kind) || !validSHA256(effect.Receipt) {
		return errors.New("invalid update metadata recovery receipt")
	}
	paths := map[string]string{
		"local-config":   baseline.project.ConfigPath,
		"default-state":  baseline.defaultState.Path,
		"registry":       filepath.Join(request.DataDir, "registry.json"),
		"reconciliation": filepath.Join(request.DataDir, "projects", request.ProjectID, "reconciliation.json"),
	}
	target := paths[kind]
	if target == "" {
		return errors.New("update metadata recovery target is absent")
	}
	var backup *UpdateJournalBackup
	journalPath, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
	if err != nil {
		return err
	}
	journal, err := openUpdateJournalForRecovery(journalPath, request)
	if err != nil {
		return err
	}
	for index := range journal.Backups {
		if journal.Backups[index].Kind == kind {
			backup = &journal.Backups[index]
			break
		}
	}
	if backup == nil {
		return errors.New("update metadata recovery backup is absent")
	}
	current, err := secureCloneFileSnapshot(target)
	if err != nil {
		return err
	}
	if backup.Existed {
		directory, err := updateBackupDirectory(request)
		if err != nil {
			return err
		}
		original, err := secureCloneFileSnapshot(filepath.Join(directory, backup.File))
		if err != nil || !cloneSnapshotHasExactBytes(original, original.data, 0o600) || int64(len(original.data)) != backup.Length || sha256String(original.data) != backup.SHA256 {
			return errors.New("update metadata recovery backup is unsafe")
		}
		if current.exists && bytes.Equal(current.data, original.data) {
			return nil
		}
		if !current.exists || sha256String(current.data) != effect.Receipt {
			return errors.New("update metadata generation changed concurrently")
		}
		return fsutil.WriteFileAtomicModeWithHook(target, original.data, os.FileMode(backup.Mode), func(step string) error {
			if step == "before-rename" {
				return revalidateCloneFileSnapshot(current)
			}
			return nil
		})
	}
	if !current.exists {
		return nil
	}
	if sha256String(current.data) != effect.Receipt {
		return errors.New("update metadata generation changed concurrently")
	}
	return executor.dependencies.Remove(target, func() error { return revalidateCloneFileSnapshot(current) })
}

func (executor *UpdateExecutor) recoveryIncomplete(path string, journal UpdateJournal, step string, cause error) error {
	marked := false
	for index := len(journal.Progress) - 1; index >= 0; index-- {
		if journal.Progress[index].State != "rolled-back" {
			journal.Progress[index].State = "unreverted"
			marked = true
			break
		}
	}
	if !marked {
		// Evidence verification itself is an operation boundary.  A malformed or
		// replaced blob can happen before any Git effect, so retain one synthetic
		// private progress receipt rather than silently leaving an apparently
		// active journal with no durable reason for the recovery refusal.
		journal.Progress = append(journal.Progress, UpdateJournalEffect{Sequence: len(journal.Progress) + 1, Name: "recovery-retained", State: "unreverted"})
	}
	journal.RollbackState = "incomplete"
	journal.Failure = boundedRedactedDiagnostic(fmt.Sprintf("recovery %s: %v", step, cause))
	_ = writeUpdateJournalAt(context.Background(), executor, path, journal, "recovery-incomplete")
	return NewError(ErrorRollbackIncomplete, fmt.Errorf("recover update %s: %w", step, cause))
}

// restoreTrackedManifestBackup accepts exactly two states after a guarded Git
// inverse: the original bytes are already present, or the operation-owned
// candidate bytes are still present and can be atomically replaced.  Any
// other generation is concurrent data and is deliberately retained.
func restoreTrackedManifestBackup(request UpdateExecutionRequest, target string, candidate []byte, backups []UpdateJournalBackup) error {
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("tracked manifest recovery target is unsafe")
	}
	var backup *UpdateJournalBackup
	for index := range backups {
		if backups[index].Kind == "tracked-manifest" {
			backup = &backups[index]
			break
		}
	}
	if backup == nil || !backup.Existed {
		return errors.New("tracked manifest recovery backup is absent")
	}
	directory, err := updateBackupDirectory(request)
	if err != nil {
		return err
	}
	blob, err := secureCloneFileSnapshot(filepath.Join(directory, backup.File))
	if err != nil || !blob.exists || blob.mode.Perm() != 0o600 || int64(len(blob.data)) != backup.Length || sha256String(blob.data) != backup.SHA256 {
		return errors.New("tracked manifest recovery backup is unsafe")
	}
	current, err := secureCloneFileSnapshot(target)
	if err != nil {
		return err
	}
	if current.exists && bytes.Equal(current.data, blob.data) {
		return nil
	}
	if !current.exists || !bytes.Equal(current.data, candidate) {
		return errors.New("tracked manifest recovery target changed concurrently")
	}
	return fsutil.WriteFileAtomicModeWithHook(target, blob.data, os.FileMode(backup.Mode), func(step string) error {
		if step != "before-rename" {
			return nil
		}
		return revalidateCloneFileSnapshot(current)
	})
}

func (executor *UpdateExecutor) executeForTest(ctx context.Context, request UpdateExecutionRequest, seams updateExecutionTestSeams) (UpdateExecutionResult, error) {
	return executor.execute(ctx, request, seams)
}

func (executor *UpdateExecutor) execute(ctx context.Context, request UpdateExecutionRequest, seams updateExecutionTestSeams) (UpdateExecutionResult, error) {
	if executor == nil || executor.dependencies.Locker == nil || executor.dependencies.WriteJournal == nil || executor.dependencies.Remove == nil {
		return UpdateExecutionResult{}, NewError(ErrorInternal, errors.New("update executor is not configured"))
	}
	if err := request.Plan.Validate(); err != nil {
		return UpdateExecutionResult{}, NewError(ErrorValidation, fmt.Errorf("validate update plan: %w", err))
	}
	if err := validateUpdateExecutionRequest(request); err != nil {
		return UpdateExecutionResult{}, NewError(ErrorValidation, err)
	}
	if seams.effects != nil {
		if err := validateUpdateTestEffects(seams.effects); err != nil {
			return UpdateExecutionResult{}, NewError(ErrorValidation, err)
		}
	}
	path, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
	if err != nil {
		return UpdateExecutionResult{}, NewError(ErrorValidation, err)
	}
	handle, err := executor.dependencies.Locker.ProjectLock(ctx, request.DataDir, request.ProjectID, time.Second)
	if err != nil {
		return UpdateExecutionResult{}, NewError(ErrorConflict, fmt.Errorf("acquire update project lock: %w", err))
	}
	defer handle.Unlock()
	if err := ctx.Err(); err != nil {
		return UpdateExecutionResult{}, err
	}
	if seams.revalidate != nil {
		if err := seams.revalidate(ctx); err != nil {
			return UpdateExecutionResult{}, classifyUpdateExecutionError("revalidate update plan", err)
		}
	}
	fresh, err := executor.recapture(ctx, request, seams.recapture)
	if err != nil {
		return UpdateExecutionResult{}, classifyUpdateExecutionError("recapture update plan", err)
	}
	if err := compareUpdateExecutionBaseline(request.Plan.executionBaseline(), fresh); err != nil {
		return UpdateExecutionResult{}, NewError(ErrorConflict, fmt.Errorf("locked update recapture differs from plan: %w", err))
	}
	effects := seams.effects
	production := effects == nil
	if effects == nil {
		effects, err = executor.productionEffects(ctx, request, fresh)
		if err != nil {
			return UpdateExecutionResult{}, classifyUpdateExecutionError("build update effects", err)
		}
	}
	if _, err := os.Lstat(path); err == nil {
		return UpdateExecutionResult{}, NewError(ErrorConflict, errors.New("update journal already exists"))
	} else if !os.IsNotExist(err) {
		return UpdateExecutionResult{}, NewError(ErrorInternal, fmt.Errorf("inspect update journal: %w", err))
	}
	journal, err := newUpdateJournal(request, fresh)
	if err != nil {
		return UpdateExecutionResult{}, NewError(ErrorValidation, fmt.Errorf("validate update journal authority: %w", err))
	}
	// M03 can change only the tracked base manifest, through the verified
	// fast-forward.  Back it up before the journal or any repository effect.
	// The helper also supports the four M04 metadata generations, but M03 must
	// not pretend to publish or mutate them.
	backupRequired := production && seams.recapture == nil
	if backupRequired {
		backups, err := prepareUpdateExecutionBackups(request, fresh)
		if err != nil {
			return UpdateExecutionResult{}, NewError(ErrorValidation, fmt.Errorf("prepare update backup: %w", err))
		}
		if err := writeUpdateBackups(request, backups); err != nil {
			return UpdateExecutionResult{}, NewError(ErrorInternal, fmt.Errorf("write update backup: %w", err))
		}
		journal.Backups = backupMetadata(backups)
	}
	if err := effectBoundary(ctx, executor, "journal-create-before"); err != nil {
		return UpdateExecutionResult{}, NewError(ErrorInternal, fmt.Errorf("write update journal: %w", err))
	}
	if err := writeNewUpdateJournal(executor, path, journal); err != nil {
		if backupRequired {
			_ = removeOwnedUpdateBackups(request, journal.Backups)
		}
		return UpdateExecutionResult{}, NewError(ErrorInternal, fmt.Errorf("write update journal: %w", err))
	}
	if err := effectBoundary(ctx, executor, "journal-create-after"); err != nil {
		return executor.fail(ctx, request, path, journal, nil, nil, "journal-create", err)
	}

	completed := make([]updateEffect, 0, len(effects))
	for index, effect := range effects {
		if err := boundary(ctx, executor, seams.progress, effect.Name+"-before"); err != nil {
			return executor.fail(ctx, request, path, journal, completed, nil, effect.Name, err)
		}
		journal.Progress = append(journal.Progress, UpdateJournalEffect{Sequence: index + 1, Name: effect.Name, Repository: effect.Repository, State: "started"})
		if err := writeUpdateJournalAt(ctx, executor, path, journal, effect.Name+"-started"); err != nil {
			return executor.fail(ctx, request, path, journal, completed, nil, effect.Name, err)
		}
		if effect.Prepare != nil {
			receipt, err := effect.Prepare(ctx)
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				return executor.fail(ctx, request, path, journal, completed, &effect, effect.Name, err)
			}
			journal.Progress[len(journal.Progress)-1].State, journal.Progress[len(journal.Progress)-1].Receipt = "prepared", boundedRedactedDiagnostic(receipt)
			if err := writeUpdateJournalAt(ctx, executor, path, journal, effect.Name+"-prepared"); err != nil {
				return executor.fail(ctx, request, path, journal, completed, &effect, effect.Name, err)
			}
		}
		receipt, err := effect.Execute(ctx)
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			return executor.fail(ctx, request, path, journal, completed, &effect, effect.Name, err)
		}
		journal.Progress[len(journal.Progress)-1].State, journal.Progress[len(journal.Progress)-1].Receipt = "completed", boundedRedactedDiagnostic(receipt)
		if err := writeUpdateJournalAt(ctx, executor, path, journal, effect.Name+"-completed"); err != nil {
			return executor.fail(ctx, request, path, journal, append(completed, effect), nil, effect.Name, err)
		}
		completed = append(completed, effect)
		if err := boundary(ctx, executor, seams.progress, effect.Name+"-after"); err != nil {
			return executor.fail(ctx, request, path, journal, completed, nil, effect.Name, err)
		}
	}
	if production {
		if err := effectBoundary(ctx, executor, "base-manifest-postcondition"); err != nil {
			return executor.fail(ctx, request, path, journal, completed, nil, "base-manifest-postcondition", err)
		}
		if err := executor.verifyExecutionBaseManifest(ctx, request.Plan, fresh); err != nil {
			return executor.fail(ctx, request, path, journal, completed, nil, "base-manifest-postcondition", err)
		}
	}
	if backupRequired {
		if err := validateOwnedUpdateBackups(request, journal.Backups); err != nil {
			return executor.fail(ctx, request, path, journal, completed, nil, "backup-verify", err)
		}
	}
	if production {
		// M03 has reached its deliberate handoff point: Git effects and opaque
		// backups remain bound by the strict active journal until M04 publishes
		// its coordinated metadata generations. A successful repository update
		// is therefore not terminal cleanup authority.
		if seams.progress != nil {
			for _, effect := range completed {
				seams.progress(transaction.Event{Kind: transaction.ExecuteSucceeded, Step: effect.Name})
			}
		}
		return UpdateExecutionResult{OperationID: request.OperationID, Completed: effectNames(completed)}, nil
	}
	if err := executor.terminalCleanup(ctx, request, path, journal, "success"); err != nil {
		return UpdateExecutionResult{}, err
	}
	if seams.progress != nil {
		for _, effect := range completed {
			seams.progress(transaction.Event{Kind: transaction.ExecuteSucceeded, Step: effect.Name})
		}
	}
	return UpdateExecutionResult{OperationID: request.OperationID, Completed: effectNames(completed)}, nil
}

// verifyExecutionBaseManifest closes the execution-time candidate binding: a
// selected-ref fast-forward is useful only when the actual base HEAD contains
// exactly the locked candidate manifest. It reads the tracked file at the
// observed HEAD and never falls back to a remote symbolic HEAD or working-tree
// bytes.
func (executor *UpdateExecutor) verifyExecutionBaseManifest(ctx context.Context, plan UpdatePlan, fresh DriftSnapshot) error {
	baseID := fresh.Project().BaseRepository
	var observation DriftRepositoryObservation
	for _, value := range fresh.Observations() {
		if value.RepositoryID == baseID {
			observation = value
			break
		}
	}
	if observation.Path == "" || observation.Head == "" {
		return errors.New("base repository execution facts are absent")
	}
	relative, err := filepath.Rel(observation.Path, fresh.CurrentManifestGeneration().Path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("tracked base manifest path escapes the base checkout")
	}
	actualHead, err := executor.dependencies.Git.Head(ctx, observation.Path)
	if err != nil {
		return fmt.Errorf("observe actual base HEAD: %w", err)
	}
	if actualHead == "" {
		return errors.New("actual base HEAD is absent")
	}
	tracked, err := executor.dependencies.Git.TrackedFile(ctx, observation.Path, actualHead, filepath.ToSlash(relative))
	if err != nil {
		return fmt.Errorf("read candidate manifest at actual base HEAD: %w", err)
	}
	if !bytes.Equal(tracked, plan.CandidateManifestBytes()) {
		return errors.New("actual base HEAD does not contain the locked candidate manifest")
	}
	return nil
}

func (executor *UpdateExecutor) fail(ctx context.Context, request UpdateExecutionRequest, path string, journal UpdateJournal, completed []updateEffect, failed *updateEffect, step string, cause error) (UpdateExecutionResult, error) {
	journal.RollbackState, journal.Failure = "rolling-back", boundedRedactedDiagnostic(cause.Error())
	_ = writeUpdateJournalAt(context.WithoutCancel(ctx), executor, path, journal, "rollback-start") // retained evidence is safer than replacing the cause.
	var rollback error
	if failed == nil && len(journal.Progress) != 0 && journal.Progress[len(journal.Progress)-1].State == "started" {
		// A failed transition from started to an effect receipt happens before
		// that effect is invoked. Record it as terminal before cleanup so a
		// later terminal-cleanup recovery never retries an unowned action.
		journal.Progress[len(journal.Progress)-1].State = "rolled-back"
	}
	if failed != nil && failed.Cleanup != nil {
		if err := failed.Cleanup(context.WithoutCancel(ctx)); err != nil {
			rollback = errors.Join(rollback, fmt.Errorf("cleanup failed %s: %w", failed.Name, err))
			if len(journal.Progress) != 0 {
				journal.Progress[len(journal.Progress)-1].State = "unreverted"
			}
		} else if len(journal.Progress) != 0 {
			journal.Progress[len(journal.Progress)-1].State = "rolled-back"
		}
	} else if failed != nil && len(journal.Progress) != 0 && journal.Progress[len(journal.Progress)-1].State == "started" {
		// Test-only effects that have no cleanup contract have not claimed an
		// ownership receipt. Their Execute error is therefore terminal before a
		// mutation; production effects with mutable staging always supply Cleanup.
		journal.Progress[len(journal.Progress)-1].State = "rolled-back"
	}
	for index := len(completed) - 1; index >= 0; index-- {
		effect := completed[index]
		if effect.Rollback == nil {
			rollback = errors.Join(rollback, fmt.Errorf("%s has no ownership-safe rollback", effect.Name))
			journal.Progress[index].State = "unreverted"
			continue
		}
		if err := effect.Rollback(context.WithoutCancel(ctx)); err != nil {
			rollback = errors.Join(rollback, fmt.Errorf("rollback %s: %w", effect.Name, err))
			journal.Progress[index].State = "unreverted"
			continue
		}
		journal.Progress[index].State = "rolled-back"
	}
	if rollback != nil {
		journal.RollbackState = "incomplete"
		journal.Failure = boundedRedactedDiagnostic(errors.Join(cause, rollback).Error())
		_ = writeUpdateJournalAt(context.WithoutCancel(ctx), executor, path, journal, "rollback-backup-incomplete")
		return UpdateExecutionResult{}, NewError(ErrorRollbackIncomplete, errors.Join(cause, rollback))
	}
	if len(journal.Backups) != 0 {
		if err := validateOwnedUpdateBackups(request, journal.Backups); err != nil {
			journal.RollbackState = "incomplete"
			journal.Failure = boundedRedactedDiagnostic(errors.Join(cause, err).Error())
			_ = writeUpdateJournalAt(context.WithoutCancel(ctx), executor, path, journal, "rollback-incomplete")
			return UpdateExecutionResult{}, NewError(ErrorRollbackIncomplete, errors.Join(cause, err))
		}
	}
	if err := executor.terminalCleanup(context.WithoutCancel(ctx), request, path, journal, "rolled-back"); err != nil {
		return UpdateExecutionResult{}, NewError(ErrorRollbackIncomplete, errors.Join(cause, err))
	}
	return UpdateExecutionResult{}, NewCleanRollbackError(classifyUpdateExecutionError(step, cause))
}

func newUpdateJournal(request UpdateExecutionRequest, fresh ...DriftSnapshot) (UpdateJournal, error) {
	digest, err := updatePlanDigest(request.Plan)
	if err != nil {
		return UpdateJournal{}, err
	}
	journal := UpdateJournal{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, PlanDigest: digest, Generations: request.Plan.Generations, RollbackState: "active", Progress: []UpdateJournalEffect{}}
	if len(fresh) != 0 {
		var retainedErr error
		journal.Retained, retainedErr = updateRetainedFacts(fresh[0], request.Plan.Generations.CandidateManifestSHA256)
		if retainedErr != nil {
			return UpdateJournal{}, retainedErr
		}
	}
	return journal, journal.Validate()
}

// updateBackupSource is deliberately private and holds the exact original
// bytes only while an executor is active.  It never becomes journal JSON.
type updateBackupSource struct {
	kind     string
	path     string
	snapshot cloneFileSnapshot
}

func prepareUpdateExecutionBackups(request UpdateExecutionRequest, fresh DriftSnapshot) ([]updateBackupSource, error) {
	// The journal is created before the first Git effect and consequently owns
	// the complete M04 rollback set.  Keeping every original generation in the
	// same opaque directory means the publication continuation never needs to
	// start a second transaction after repository effects have succeeded.
	baseline := request.Plan.executionBaseline()
	current := fresh.CurrentManifestGeneration()
	reconciliation := baseline.reconciliation
	if reconciliation.Path == "" {
		reconciliation.Path = filepath.Join(request.DataDir, "projects", request.ProjectID, "reconciliation.json")
	}
	return prepareUpdateBackupSources([]updateBackupSource{
		{kind: "default-state", path: baseline.defaultState.Path},
		{kind: "local-config", path: baseline.project.ConfigPath},
		{kind: "reconciliation", path: reconciliation.Path},
		{kind: "registry", path: filepath.Join(request.DataDir, "registry.json")},
		{kind: "tracked-manifest", path: current.Path},
	}, map[string][]byte{
		"default-state":    baseline.defaultState.Bytes,
		"local-config":     baseline.localConfig,
		"reconciliation":   baseline.reconciliation.Bytes,
		"registry":         baseline.registry,
		"tracked-manifest": current.Bytes,
	})
}

// prepareUpdateBackupSources is the M03 private backup contract shared by
// future M04 publication code.  Callers give authoritative captured bytes for
// each generation; a missing source is represented without creating a blob.
func prepareUpdateBackupSources(sources []updateBackupSource, expected map[string][]byte) ([]updateBackupSource, error) {
	if len(sources) == 0 {
		return nil, errors.New("update backup sources are required")
	}
	prepared := append([]updateBackupSource(nil), sources...)
	previous := ""
	for index := range prepared {
		source := &prepared[index]
		if !validUpdateBackupKind(source.kind) || source.kind <= previous || source.path == "" || !filepath.IsAbs(source.path) || filepath.Clean(source.path) != source.path {
			return nil, errors.New("unsafe update backup source")
		}
		previous = source.kind
		snapshot, err := secureCloneFileSnapshot(source.path)
		if err != nil {
			return nil, err
		}
		want, exists := expected[source.kind]
		if !exists {
			return nil, errors.New("update backup source lacks authoritative bytes")
		}
		if snapshot.exists != (len(want) != 0) || (snapshot.exists && !bytes.Equal(snapshot.data, want)) {
			return nil, NewError(ErrorConflict, fmt.Errorf("update backup source %q changed", source.kind))
		}
		if snapshot.exists {
			if err := validateUpdateBackupSource(source.kind, snapshot.data); err != nil {
				return nil, err
			}
		}
		source.snapshot = snapshot
	}
	return prepared, nil
}

func validateUpdateBackupSource(kind string, data []byte) error {
	// Scan the complete original generation before anything is persisted. This
	// is intentionally not the bounded diagnostic helper: truncation must never
	// turn a valid large generation into a false secret or hide a credential
	// that straddles a diagnostic-window boundary.
	if redactCredentialShapes(string(data)) != string(data) {
		return errors.New("update backup source contains a secret-shaped value")
	}
	switch kind {
	case "tracked-manifest":
		_, err := config.LoadPortableManifest(data)
		return err
	case "local-config":
		_, err := config.LoadProject(data)
		return err
	case "default-state":
		_, err := store.DecodeWorkspace(data)
		return err
	case "registry":
		_, err := store.DecodeRegistry(data)
		return err
	case "reconciliation":
		_, err := DecodeUpdateReconciliation(data)
		return err
	default:
		return errors.New("unknown update backup source")
	}
}

func backupMetadata(sources []updateBackupSource) []UpdateJournalBackup {
	backups := make([]UpdateJournalBackup, 0, len(sources))
	for _, source := range sources {
		item := UpdateJournalBackup{Kind: source.kind, File: source.kind + ".bin"}
		if source.snapshot.exists {
			item.Existed, item.Mode, item.Length, item.SHA256 = true, uint32(source.snapshot.mode.Perm()), int64(len(source.snapshot.data)), sha256String(source.snapshot.data)
		}
		backups = append(backups, item)
	}
	return backups
}

func updateBackupDirectory(request UpdateExecutionRequest) (string, error) {
	journal, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(journal), "backups"), nil
}

func writeUpdateBackups(request UpdateExecutionRequest, sources []updateBackupSource) error {
	directory, err := updateBackupDirectory(request)
	if err != nil {
		return err
	}
	if err := ensureUpdateJournalParent(directory); err != nil {
		return err
	}
	for _, source := range sources {
		path := filepath.Join(directory, source.kind+".bin")
		if !source.snapshot.exists {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return errors.New("absent update backup blob already exists")
			}
			continue
		}
		existing, err := secureCloneFileSnapshot(path)
		if err != nil {
			return err
		}
		if existing.exists {
			return errors.New("update backup blob already exists")
		}
		if err := fsutil.WriteFileAtomicModeWithHook(path, source.snapshot.data, 0o600, func(step string) error {
			if step != "before-rename" {
				return nil
			}
			if err := revalidateCloneFileSnapshot(source.snapshot); err != nil {
				return err
			}
			return revalidateCloneFileSnapshot(existing)
		}); err != nil {
			return err
		}
		if err := validateUpdateBackupBlob(path, backupMetadata([]updateBackupSource{source})[0]); err != nil {
			return err
		}
	}
	return nil
}

func validateUpdateBackupBlob(path string, backup UpdateJournalBackup) error {
	if !backup.Existed {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errors.New("absent update backup blob exists")
		}
		return nil
	}
	snapshot, err := secureCloneFileSnapshot(path)
	if err != nil || !snapshot.exists || snapshot.mode.Perm() != 0o600 || int64(len(snapshot.data)) != backup.Length || sha256String(snapshot.data) != backup.SHA256 {
		return errors.New("update backup blob does not match journal metadata")
	}
	return nil
}

func removeOwnedUpdateBackups(request UpdateExecutionRequest, backups []UpdateJournalBackup) error {
	directory, err := updateBackupDirectory(request)
	if err != nil {
		return err
	}
	if err := validateExistingUpdateJournalDirectory(directory); err != nil {
		return err
	}
	for _, backup := range backups {
		path := filepath.Join(directory, backup.File)
		if err := validateUpdateBackupBlob(path, backup); err != nil {
			return err
		}
		if backup.Existed {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(directory); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func validateOwnedUpdateBackups(request UpdateExecutionRequest, backups []UpdateJournalBackup) error {
	directory, err := updateBackupDirectory(request)
	if err != nil {
		return err
	}
	if err := validateExistingUpdateJournalDirectory(directory); err != nil {
		return err
	}
	for _, backup := range backups {
		if err := validateUpdateBackupBlob(filepath.Join(directory, backup.File), backup); err != nil {
			return err
		}
	}
	return nil
}

// updateTerminalRecoveryRecord is deliberately small and deterministic.  It
// is published before any terminal private evidence is removed, so a crash or
// an authority replacement cannot turn a recoverable operation into an
// evidence-free directory. The detailed journal and opaque blobs remain
// private; this public recovery record contains only a redacted pending step.
func updateTerminalRecoveryRecord(request UpdateExecutionRequest) (string, store.RecoveryRecord, error) {
	workspaceID := request.Plan.executionBaseline().workspace.ID
	if !safeUpdateJournalID(workspaceID) {
		return "", store.RecoveryRecord{}, errors.New("update default workspace identity is unsafe")
	}
	path := filepath.Join(request.DataDir, "projects", request.ProjectID, "recovery", workspaceID+".json")
	return path, store.RecoveryRecord{
		Version:         store.Version,
		ProjectID:       request.ProjectID,
		WorkspaceID:     workspaceID,
		Operation:       "update",
		FailedStep:      "terminal-cleanup",
		CompletedSteps:  []string{"repository-effects-terminal"},
		UnrevertedSteps: []string{"terminal-cleanup"},
	}, nil
}

type updateTerminalRecoveryState uint8

const (
	updateTerminalRecoveryAbsent updateTerminalRecoveryState = iota
	updateTerminalRecoveryOwned
	updateTerminalRecoveryChanged
)

type updateTerminalRecoveryInspection struct {
	state    updateTerminalRecoveryState
	snapshot cloneFileSnapshot
	cause    error
}

func inspectUpdateTerminalRecovery(request UpdateExecutionRequest) updateTerminalRecoveryInspection {
	path, value, err := updateTerminalRecoveryRecord(request)
	if err != nil {
		return updateTerminalRecoveryInspection{state: updateTerminalRecoveryChanged, cause: err}
	}
	want, err := store.RecoveryBytes(value)
	if err != nil {
		return updateTerminalRecoveryInspection{state: updateTerminalRecoveryChanged, cause: err}
	}
	snapshot, err := secureCloneFileSnapshot(path)
	if err != nil {
		return updateTerminalRecoveryInspection{state: updateTerminalRecoveryChanged, cause: err}
	}
	if !snapshot.exists {
		return updateTerminalRecoveryInspection{state: updateTerminalRecoveryAbsent, snapshot: snapshot}
	}
	if cloneSnapshotHasExactBytes(snapshot, want, 0o600) {
		return updateTerminalRecoveryInspection{state: updateTerminalRecoveryOwned, snapshot: snapshot}
	}
	return updateTerminalRecoveryInspection{state: updateTerminalRecoveryChanged, snapshot: snapshot, cause: errors.New("update terminal recovery record is not operation-owned")}
}

func (executor *UpdateExecutor) publishUpdateTerminalRecovery(ctx context.Context, request UpdateExecutionRequest) (cloneFileSnapshot, error) {
	path, value, err := updateTerminalRecoveryRecord(request)
	if err != nil {
		return cloneFileSnapshot{}, err
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-summary-publish-before"); err != nil {
		return cloneFileSnapshot{}, err
	}
	if err := publishExactRecoveryRecord(path, value, executor.dependencies.WriteRecoveryCAS); err != nil {
		return cloneFileSnapshot{}, err
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-summary-publish-after"); err != nil {
		return cloneFileSnapshot{}, err
	}
	inspection := inspectUpdateTerminalRecovery(request)
	if inspection.state != updateTerminalRecoveryOwned {
		return cloneFileSnapshot{}, errors.Join(inspection.cause, errors.New("published update terminal recovery record is not operation-owned"))
	}
	return inspection.snapshot, nil
}

func (executor *UpdateExecutor) removeUpdateTerminalRecovery(ctx context.Context, request UpdateExecutionRequest, expected cloneFileSnapshot) error {
	if !expected.exists {
		return errors.New("update terminal recovery removal lacks owned record")
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-summary-remove-before"); err != nil {
		return err
	}
	if err := executor.dependencies.Remove(expected.path, func() error { return revalidateCloneFileSnapshot(expected) }); err != nil {
		return err
	}
	// At this point both the operation authority and its owned recovery summary
	// are gone. A crash/failpoint after the unlink is a committed cleanup, not
	// an incomplete state that could truthfully retain recovery evidence.
	_ = effectBoundary(ctx, executor, "terminal-cleanup-summary-remove-after")
	return nil
}

// removeCleaningUpdateBackups continues from actual strict inventory. Missing
// blobs are expected after a crash between individual unlink operations, but a
// present blob must still be the exact operation-owned generation before it is
// removed. No active-journal validation is used in this terminal state.
func (executor *UpdateExecutor) removeCleaningUpdateBackups(ctx context.Context, request UpdateExecutionRequest, backups []UpdateJournalBackup) error {
	directory, err := updateBackupDirectory(request)
	if err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || validateExistingUpdateJournalDirectory(directory) != nil {
		return errors.New("update cleaning backup directory is unsafe")
	}
	for _, backup := range backups {
		path := filepath.Join(directory, backup.File)
		if err := effectBoundary(ctx, executor, "terminal-cleanup-backup-"+backup.Kind+"-remove-before"); err != nil {
			return err
		}
		snapshot, snapshotErr := secureCloneFileSnapshot(path)
		if snapshotErr != nil {
			return snapshotErr
		}
		if snapshot.exists {
			if !backup.Existed || snapshot.mode.Perm() != 0o600 || int64(len(snapshot.data)) != backup.Length || sha256String(snapshot.data) != backup.SHA256 {
				return errors.New("update cleaning backup blob is unsafe")
			}
			if err := executor.dependencies.Remove(path, func() error { return revalidateCloneFileSnapshot(snapshot) }); err != nil {
				return err
			}
		} else if !backup.Existed {
			// An absent source has no blob by contract. Keep the explicit branch so
			// a future backup kind cannot accidentally acquire deletion authority.
		}
		if err := effectBoundary(ctx, executor, "terminal-cleanup-backup-"+backup.Kind+"-remove-after"); err != nil {
			return err
		}
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-backup-directory-remove-before"); err != nil {
		return err
	}
	if err := os.Remove(directory); err != nil && !os.IsNotExist(err) {
		return err
	}
	return effectBoundary(ctx, executor, "terminal-cleanup-backup-directory-remove-after")
}

func (executor *UpdateExecutor) removeCleaningStaging(ctx context.Context, path string) error {
	staging := filepath.Join(filepath.Dir(path), "staging")
	if err := effectBoundary(ctx, executor, "terminal-cleanup-staging-remove-before"); err != nil {
		return err
	}
	info, err := os.Lstat(staging)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("update cleaning staging directory is unsafe")
		}
		if err := os.Remove(staging); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return effectBoundary(ctx, executor, "terminal-cleanup-staging-remove-after")
}

func terminalCleanupJournal(journal UpdateJournal, outcome string) (UpdateJournal, error) {
	if outcome != "success" && outcome != "rolled-back" {
		return UpdateJournal{}, errors.New("invalid update terminal cleanup outcome")
	}
	// A prior failed cleanup retains one synthetic unreverted cleanup receipt.
	// It is evidence of deletion work only, not a repository inverse, and is
	// removed when a fresh cleanup attempt resumes.
	if journal.RollbackState == "incomplete" && journal.TerminalOutcome != "" {
		progress := journal.Progress[:0]
		for _, effect := range journal.Progress {
			if effect.Name != "terminal-cleanup" {
				progress = append(progress, effect)
			}
		}
		journal.Progress = progress
	}
	journal.RollbackState, journal.TerminalOutcome, journal.Failure = "cleaning", outcome, ""
	return journal, journal.Validate()
}

func terminalCleanupIncomplete(journal UpdateJournal, cause error) UpdateJournal {
	journal.RollbackState = "incomplete"
	journal.Failure = boundedRedactedDiagnostic(fmt.Sprintf("terminal cleanup: %v", cause))
	journal.Progress = append(journal.Progress, UpdateJournalEffect{Sequence: len(journal.Progress) + 1, Name: "terminal-cleanup", State: "unreverted"})
	return journal
}

func (executor *UpdateExecutor) retainTerminalCleanupFailure(path string, journal UpdateJournal, cause error) error {
	retained := terminalCleanupIncomplete(journal, cause)
	if err := validateExistingUpdateJournalDirectory(filepath.Dir(path)); err == nil {
		if _, snapshotErr := secureCloneFileSnapshot(path); snapshotErr == nil {
			if writeErr := writeUpdateJournalAt(context.Background(), executor, path, retained, "terminal-cleanup-incomplete"); writeErr == nil {
				return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup: %w", cause))
			}
		}
		// The journal may have been removed immediately before a safe rmdir
		// failure. Reconstruct it only in the original private operation dir;
		// never create through a replaced/symlink authority.
		if writeErr := writeNewUpdateJournal(executor, path, retained); writeErr == nil {
			return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup: %w", cause))
		}
	}
	return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup retained recovery summary: %w", cause))
}

func (executor *UpdateExecutor) terminalCleanup(ctx context.Context, request UpdateExecutionRequest, path string, journal UpdateJournal, outcome string) error {
	cleaning, err := terminalCleanupJournal(journal, outcome)
	if err != nil {
		return err
	}
	operation, err := captureUpdateOperationAuthority(path)
	if err != nil {
		return executor.retainTerminalCleanupFailure(path, journal, err)
	}
	if journal.RollbackState != "cleaning" {
		if err := writeUpdateJournalAt(ctx, executor, path, cleaning, "terminal-cleanup-start"); err != nil {
			return executor.retainTerminalCleanupFailure(path, journal, err)
		}
	}
	inspection := inspectUpdateTerminalRecovery(request)
	var summary cloneFileSnapshot
	switch inspection.state {
	case updateTerminalRecoveryOwned:
		summary = inspection.snapshot
	case updateTerminalRecoveryAbsent:
		summary, err = executor.publishUpdateTerminalRecovery(ctx, request)
		if err != nil {
			return executor.retainTerminalCleanupFailure(path, cleaning, err)
		}
	case updateTerminalRecoveryChanged:
		return executor.retainTerminalCleanupFailure(path, cleaning, errors.Join(inspection.cause, errors.New("update terminal recovery summary is unowned")))
	}
	if err := executor.removeCleaningUpdateBackups(ctx, request, cleaning.Backups); err != nil {
		return executor.retainTerminalCleanupFailure(path, cleaning, err)
	}
	if err := executor.removeCleaningStaging(ctx, path); err != nil {
		return executor.retainTerminalCleanupFailure(path, cleaning, err)
	}
	if err := removeOwnedUpdateJournalAt(ctx, executor, path, "terminal-cleanup"); err != nil {
		return executor.retainTerminalCleanupFailure(path, cleaning, err)
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-operation-remove-before"); err != nil {
		return executor.retainTerminalCleanupFailure(path, cleaning, err)
	}
	if err := removeOwnedUpdateOperationAuthority(path, operation); err != nil {
		return executor.retainTerminalCleanupFailure(path, cleaning, err)
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-operation-remove-after"); err != nil {
		// Authority is already gone. The summary remains actionable and its
		// removal will be retried by a later recovery invocation.
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup: %w", err))
	}
	if err := executor.removeUpdateTerminalRecovery(ctx, request, summary); err != nil {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("remove update terminal recovery summary: %w", err))
	}
	return nil
}

// recoverTerminalSummary handles the narrow crash window after journal removal
// but before operation rmdir. There is no longer enough private inventory to
// unlink children safely, so it removes only an already-empty, identity-bound
// operation directory and otherwise leaves the exact recovery record as the
// actionable diagnosis. A retry converges once the concurrent obstruction is
// removed without ever deleting it on the operation's behalf.
func (executor *UpdateExecutor) recoverTerminalSummary(ctx context.Context, request UpdateExecutionRequest, journalPath string) error {
	inspection := inspectUpdateTerminalRecovery(request)
	operationPath := filepath.Dir(journalPath)
	_, operationErr := os.Lstat(operationPath)
	operationAbsent := os.IsNotExist(operationErr)
	if operationErr != nil && !operationAbsent {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("inspect update terminal operation authority: %w", operationErr))
	}
	switch inspection.state {
	case updateTerminalRecoveryChanged:
		return NewError(ErrorRollbackIncomplete, errors.Join(errors.New("update terminal recovery summary is changed or unsafe"), inspection.cause))
	case updateTerminalRecoveryAbsent:
		if operationAbsent {
			return nil
		}
		return NewError(ErrorRollbackIncomplete, errors.New("update terminal operation authority remains without an owned recovery summary"))
	case updateTerminalRecoveryOwned:
		if operationAbsent {
			if err := executor.removeUpdateTerminalRecovery(ctx, request, inspection.snapshot); err != nil {
				return NewError(ErrorRollbackIncomplete, fmt.Errorf("remove update terminal recovery summary: %w", err))
			}
			return nil
		}
	default:
		return NewError(ErrorRollbackIncomplete, errors.New("update terminal recovery summary state is invalid"))
	}
	summary := inspection.snapshot
	operation, err := captureUpdateOperationAuthority(journalPath)
	if err != nil {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup retained recovery summary: %w", err))
	}
	entries, err := os.ReadDir(filepath.Dir(journalPath))
	if err != nil {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("inspect update terminal operation authority: %w", err))
	}
	if len(entries) != 0 {
		return NewError(ErrorRollbackIncomplete, errors.New("update terminal cleanup remains pending; private operation authority is not empty"))
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-operation-remove-before"); err != nil {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup: %w", err))
	}
	if err := removeOwnedUpdateOperationAuthority(journalPath, operation); err != nil {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup retained recovery summary: %w", err))
	}
	if err := effectBoundary(ctx, executor, "terminal-cleanup-operation-remove-after"); err != nil {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("update terminal cleanup: %w", err))
	}
	if err := executor.removeUpdateTerminalRecovery(ctx, request, summary); err != nil {
		return NewError(ErrorRollbackIncomplete, fmt.Errorf("remove update terminal recovery summary: %w", err))
	}
	return nil
}

func updateRetainedFacts(snapshot DriftSnapshot, candidateDigest string) ([]UpdateRetainedFact, error) {
	observations := map[string]DriftRepositoryObservation{}
	for _, observation := range snapshot.Observations() {
		observations[observation.RepositoryID] = observation
	}
	retained := make([]UpdateRetainedFact, 0)
	for _, repository := range snapshot.Repositories() {
		if repository.Classification != UpdateClassificationRemovedRetained {
			continue
		}
		observation, exists := observations[repository.ID]
		if !exists || observation.Path == "" || observation.CommonGitDir == "" {
			return nil, fmt.Errorf("retained repository %q lacks exact ownership facts", repository.ID)
		}
		if !safeRetainedUpdateAuthorityPath(observation.Path) || !safeRetainedUpdateAuthorityPath(observation.CommonGitDir) {
			return nil, fmt.Errorf("retained repository %q has secret-shaped ownership facts", repository.ID)
		}
		retained = append(retained, UpdateRetainedFact{RepositoryID: repository.ID, Path: filepath.Clean(observation.Path), CommonGitDir: filepath.Clean(observation.CommonGitDir), CandidateSHA256: candidateDigest})
	}
	return retained, nil
}

func safeRetainedUpdateAuthorityPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && redactCredentialShapes(value) == value
}
func writeUpdateJournal(executor *UpdateExecutor, path string, journal UpdateJournal) error {
	expected, err := secureCloneFileSnapshot(path)
	if err != nil {
		return err
	}
	if !expected.exists {
		return errors.New("update journal disappeared before transition")
	}
	existing, err := decodeStrictUpdateJournal(expected.data)
	if err != nil || existing.Validate() != nil || existing.OperationID != journal.OperationID || existing.ProjectID != journal.ProjectID || existing.PlanDigest != journal.PlanDigest {
		return errors.New("update journal changed before transition")
	}
	return writeUpdateJournalExpected(executor, path, journal, expected)
}

func writeUpdateJournalAt(ctx context.Context, executor *UpdateExecutor, path string, journal UpdateJournal, transition string) error {
	if err := effectBoundary(ctx, executor, "journal-"+transition+"-before"); err != nil {
		return err
	}
	if err := writeUpdateJournal(executor, path, journal); err != nil {
		return err
	}
	return effectBoundary(ctx, executor, "journal-"+transition+"-after")
}

func decodeStrictUpdateJournal(data []byte) (UpdateJournal, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal UpdateJournal
	if err := decoder.Decode(&journal); err != nil {
		return UpdateJournal{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return UpdateJournal{}, errors.New("update journal contains trailing JSON")
	}
	return journal, journal.Validate()
}

func writeNewUpdateJournal(executor *UpdateExecutor, path string, journal UpdateJournal) error {
	expected, err := secureCloneFileSnapshot(path)
	if err != nil {
		return err
	}
	if expected.exists {
		return errors.New("update journal already exists")
	}
	return writeUpdateJournalExpected(executor, path, journal, expected)
}

func writeUpdateJournalExpected(executor *UpdateExecutor, path string, journal UpdateJournal, expected cloneFileSnapshot) error {
	if err := journal.Validate(); err != nil {
		return err
	}
	if err := ensureUpdateJournalParent(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return executor.dependencies.WriteJournal(path, data, 0o600, func() error { return revalidateCloneFileSnapshot(expected) })
}

// removeOwnedUpdateJournal treats journal removal as a compare-and-swap
// transition. A concurrent replacement (including a symlink or a byte change)
// is retained as recovery evidence rather than being unlinked by this
// operation.
func removeOwnedUpdateJournal(executor *UpdateExecutor, path string) error {
	expected, err := secureCloneFileSnapshot(path)
	if err != nil {
		return err
	}
	if !expected.exists {
		return errors.New("update journal disappeared before removal")
	}
	if _, err := decodeStrictUpdateJournal(expected.data); err != nil {
		return errors.New("update journal changed before removal")
	}
	return executor.dependencies.Remove(path, func() error { return revalidateCloneFileSnapshot(expected) })
}

func removeOwnedUpdateJournalAt(ctx context.Context, executor *UpdateExecutor, path, transition string) error {
	if err := effectBoundary(ctx, executor, "journal-"+transition+"-remove-before"); err != nil {
		return err
	}
	if err := removeOwnedUpdateJournal(executor, path); err != nil {
		return err
	}
	return effectBoundary(ctx, executor, "journal-"+transition+"-remove-after")
}
func ensureUpdateJournalParent(path string) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return errors.New("update journal directory must be absolute")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	// The configurable data directory can legitimately sit below a platform
	// alias (for example /var on macOS).  Validate the fixed operation suffix,
	// rather than rejecting that pre-existing authority path.
	current := path
	for count := 0; count != 4; count++ {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("unsafe update journal directory")
		}
		current = filepath.Dir(current)
	}
	return nil
}

// validateExistingUpdateJournalDirectory is the non-mutating counterpart used
// by reopen/cleanup paths.  A private blob must never be opened through an
// operation-directory alias created after the journal was written.
func validateExistingUpdateJournalDirectory(path string) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return errors.New("update journal directory must be absolute")
	}
	depth := 4 // operation, update, project, projects
	if filepath.Base(path) == "backups" {
		depth++ // the private blob directory is below the operation authority
	}
	for count, current := 0, path; count != depth; count, current = count+1, filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("unsafe update journal directory")
		}
	}
	return nil
}

// captureUpdateOperationAuthority records the operation directory identity
// before clean terminal cleanup. Later removal uses rmdir semantics and this
// identity check, so a concurrent child, symlink, replacement, or non-empty
// directory is retained as incomplete evidence rather than removed.
func captureUpdateOperationAuthority(journalPath string) (os.FileInfo, error) {
	operation := filepath.Dir(journalPath)
	if err := validateExistingUpdateJournalDirectory(operation); err != nil {
		return nil, err
	}
	info, err := os.Lstat(operation)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("update operation authority is unsafe")
	}
	return info, nil
}

func removeOwnedUpdateOperationAuthority(journalPath string, expected os.FileInfo) error {
	if expected == nil {
		return errors.New("update operation cleanup lacks ownership identity")
	}
	operation := filepath.Dir(journalPath)
	if err := validateExistingUpdateJournalDirectory(operation); err != nil {
		return err
	}
	current, err := os.Lstat(operation)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return errors.New("update operation authority changed before cleanup")
	}
	for _, name := range []string{"staging", "backups"} {
		child := filepath.Join(operation, name)
		info, childErr := os.Lstat(child)
		if os.IsNotExist(childErr) {
			continue
		}
		if childErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("update operation cleanup child is unsafe")
		}
		if err := os.Remove(child); err != nil {
			return fmt.Errorf("update operation cleanup child is not empty: %w", err)
		}
	}
	if err := os.Remove(operation); err != nil {
		return fmt.Errorf("update operation authority is not empty: %w", err)
	}
	return nil
}
func boundary(ctx context.Context, executor *UpdateExecutor, progress func(transaction.Event), step string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if executor.dependencies.Before != nil {
		if err := executor.dependencies.Before(step); err != nil {
			return err
		}
	}
	if progress != nil {
		progress(transaction.Event{Kind: transaction.ExecuteStarted, Step: step})
	}
	return nil
}

// effectBoundary is the fine-grained test/cancellation seam around every
// irreversible production boundary. Unlike the public request it cannot
// select an action; it can only stop the already-derived action before it
// touches Git or the filesystem.
func effectBoundary(ctx context.Context, executor *UpdateExecutor, step string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if executor.dependencies.Before != nil {
		return executor.dependencies.Before(step)
	}
	return nil
}

func effectNames(effects []updateEffect) []string {
	names := make([]string, len(effects))
	for i := range effects {
		names[i] = effects[i].Name
	}
	return names
}
func validateUpdateExecutionRequest(request UpdateExecutionRequest) error {
	if !safeUpdateJournalID(request.ProjectID) || !safeUpdateOperationID(request.OperationID) {
		return errors.New("update execution identity is required")
	}
	return nil
}

func validateUpdateTestEffects(effects []updateEffect) error {
	if len(effects) == 0 {
		return errors.New("test update effects are required")
	}
	seenNames := map[string]bool{}
	for _, effect := range effects {
		if effect.Name == "" || seenNames[effect.Name] || effect.Execute == nil || boundedRedactedDiagnostic(effect.Repository) != effect.Repository {
			return errors.New("invalid update execution effect")
		}
		seenNames[effect.Name] = true
	}
	return nil
}

// validateProductionUpdateActions closes the last caller-controlled action
// gap: the locked plan and the locked recapture must describe exactly the
// same ordered operation.  The executor only knows how to perform the four
// classifications represented by an UpdatePlan action; anything else is a
// pre-effect refusal.
func validateProductionUpdateActions(plan UpdatePlan, fresh DriftSnapshot) error {
	repositories := fresh.Repositories()
	actions := plan.Actions()
	if len(repositories) != len(actions) || len(repositories) != len(plan.Repositories()) {
		return errors.New("locked update action set changed")
	}
	for index, repository := range repositories {
		planned := plan.Repositories()[index]
		if repository.ID != planned.ID || repository.ParentID != planned.ParentID || repository.Mount != planned.Mount || repository.Classification != planned.Classification {
			return fmt.Errorf("locked repository action %q changed", repository.ID)
		}
		want := ""
		switch repository.Classification {
		case UpdateClassificationUnchanged:
			want = "unchanged"
		case UpdateClassificationFastForwardable:
			want = "fast-forward"
		case UpdateClassificationAdded:
			want = "add"
		case UpdateClassificationRemovedRetained:
			want = "retain-unmanaged"
		default:
			return fmt.Errorf("repository %q has unhandled update classification %q", repository.ID, repository.Classification)
		}
		if actions[index] != (UpdatePlanAction{Sequence: index + 1, Action: want, RepositoryID: repository.ID}) {
			return fmt.Errorf("locked repository action %q is not executable", repository.ID)
		}
	}
	return nil
}

func (executor *UpdateExecutor) productionEffects(ctx context.Context, request UpdateExecutionRequest, fresh DriftSnapshot) ([]updateEffect, error) {
	current, err := config.LoadPortableManifest(fresh.CurrentManifestBytes())
	if err != nil {
		return nil, err
	}
	candidate, err := config.LoadPortableManifest(request.Plan.CandidateManifestBytes())
	if err != nil {
		return nil, fmt.Errorf("decode locked candidate manifest: %w", err)
	}
	observations := map[string]DriftRepositoryObservation{}
	for _, value := range fresh.Observations() {
		observations[value.RepositoryID] = value
	}
	if err := validateProductionUpdateActions(request.Plan, fresh); err != nil {
		return nil, err
	}
	effects := make([]updateEffect, 0)
	for _, repository := range fresh.Repositories() {
		if repository.Classification == UpdateClassificationUnchanged || repository.Classification == UpdateClassificationRemovedRetained {
			continue
		}
		if repository.Classification == UpdateClassificationAdded {
			portable, exists := candidate.Repositories[repository.ID]
			observation := observations[repository.ID]
			if !exists || observation.Path == "" || !observation.TargetAbsent || !observation.IgnoreKnown || !observation.IgnoreVerified {
				return nil, fmt.Errorf("added repository %q lacks locked absent-mount or parent-ignore facts", repository.ID)
			}
			effect, err := executor.addedRepositoryEffectWithin(request, repository.ID, portable, observation, fresh.DefaultWorkspace().RootPath)
			if err != nil {
				return nil, err
			}
			effects = append(effects, effect)
			continue
		}
		if repository.Classification != UpdateClassificationFastForwardable {
			return nil, fmt.Errorf("repository %q cannot be executed with classification %q", repository.ID, repository.Classification)
		}
		portable, exists := current.Repositories[repository.ID]
		observation := observations[repository.ID]
		if !exists || observation.Path == "" || observation.Head == "" || observation.Branch != portable.DefaultBranch || !observation.UpstreamKnown || observation.Upstream.Remote != portable.Upstream.Remote || observation.Upstream.Merge != portable.Upstream.Merge {
			return nil, fmt.Errorf("repository %q lacks locked configured-ref facts", repository.ID)
		}
		path, branch, oldHead, remote, merge, repositoryID := observation.Path, observation.Branch, observation.Head, portable.Upstream.Remote, portable.Upstream.Merge, repository.ID
		var prepared updateFastForwardReceipt
		prepare := func(effectCtx context.Context) (string, error) {
			if err := effectBoundary(effectCtx, executor, "repository-"+repository.ID+"-observe"); err != nil {
				return "", err
			}
			observed, err := executor.dependencies.Git.ObserveConfiguredRef(effectCtx, path, remote, merge)
			if err != nil {
				return "", err
			}
			if err := effectBoundary(effectCtx, executor, "repository-"+repository.ID+"-observe-after"); err != nil {
				return "", err
			}
			if observed.Remote != remote || observed.RemoteRef != merge || observed.Commit == "" {
				return "", errors.New("configured ref observation does not match the locked upstream")
			}
			if err := effectBoundary(effectCtx, executor, "repository-"+repository.ID+"-fetch"); err != nil {
				return "", err
			}
			fetched, fetchErr := executor.dependencies.Git.FetchConfiguredRef(effectCtx, path, remote, merge)
			// Fetch has already mutated the selected tracking ref. Retain the
			// rollback-capable receipt before *any* boundary, cancellation, or
			// response validation can return control to generic cleanup, including
			// when the Git process reports an error after updating the ref.
			if fetched.Remote != remote || fetched.RemoteRef != merge || !aggregateObjectID(fetched.ActualRemoteCommit) || (fetched.PreviousRemoteCommit != "" && !aggregateObjectID(fetched.PreviousRemoteCommit)) {
				return "", errors.Join(fetchErr, errors.New("configured fetch returned no ownership-valid tracking generation"))
			}
			prepared = updateFastForwardReceipt{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, RepositoryID: repositoryID, Branch: branch, OldCommit: oldHead, NewCommit: fetched.ActualRemoteCommit, Remote: remote, RemoteRef: merge, PreviousRemoteCommit: fetched.PreviousRemoteCommit, ActualRemoteCommit: fetched.ActualRemoteCommit}
			if fetchErr != nil {
				return "", fetchErr
			}
			if err := effectBoundary(effectCtx, executor, "repository-"+repository.ID+"-fetch-after"); err != nil {
				return "", err
			}
			if fetched.Remote != remote || fetched.RemoteRef != merge || fetched.ActualRemoteCommit != observed.Commit {
				return "", errors.New("configured selected ref changed during fetch")
			}
			return encodeUpdateFastForwardReceipt(prepared)
		}
		execute := func(effectCtx context.Context) (string, error) {
			if !validUpdateFastForwardReceipt(prepared) {
				if _, err := prepare(effectCtx); err != nil {
					return "", err
				}
			}
			if err := effectBoundary(effectCtx, executor, "repository-"+repository.ID+"-fast-forward"); err != nil {
				return "", err
			}
			receipt, err := executor.dependencies.Git.FastForward(effectCtx, path, branch, oldHead, prepared.NewCommit)
			if err != nil {
				return "", err
			}
			if receipt.Branch != prepared.Branch || receipt.OldCommit != prepared.OldCommit || receipt.NewCommit != prepared.NewCommit {
				return "", errors.New("fast-forward receipt differs from the prepared generation")
			}
			if err := effectBoundary(effectCtx, executor, "repository-"+repository.ID+"-fast-forward-after"); err != nil {
				return "", err
			}
			return encodeUpdateFastForwardReceipt(prepared)
		}
		restore := func(rollbackCtx context.Context) error {
			if !validUpdateFastForwardReceipt(prepared) {
				return errors.New("configured-ref rollback receipt is absent")
			}
			current, err := executor.dependencies.Git.Head(rollbackCtx, path)
			if err != nil {
				return err
			}
			if current == prepared.NewCommit {
				if err := executor.dependencies.Git.RestoreFastForward(rollbackCtx, path, gitadapter.FastForwardReceipt{Branch: prepared.Branch, OldCommit: prepared.OldCommit, NewCommit: prepared.NewCommit}); err != nil {
					return err
				}
			} else if current != prepared.OldCommit {
				return errors.New("configured-ref rollback local branch changed concurrently")
			}
			return executor.dependencies.Git.RestoreConfiguredRef(rollbackCtx, path, gitadapter.ConfiguredRefFetch{Remote: prepared.Remote, RemoteRef: prepared.RemoteRef, PreviousRemoteCommit: prepared.PreviousRemoteCommit, ActualRemoteCommit: prepared.ActualRemoteCommit})
		}
		effects = append(effects, updateEffect{Name: "repository-" + repository.ID + "-fast-forward", Repository: repository.ID, Prepare: prepare, Execute: execute, Cleanup: restore, Rollback: restore})
	}
	return effects, nil
}

// updateFastForwardReceipt is opaque, strict, and private journal evidence for
// the configured-ref fetch and its subsequent local branch transition. It
// intentionally carries no path, URL, or diagnostic text; the locked baseline
// supplies the checkout authority during rollback/recovery.
type updateFastForwardReceipt struct {
	Version              int    `json:"version"`
	OperationID          string `json:"operationId"`
	ProjectID            string `json:"projectId"`
	RepositoryID         string `json:"repositoryId"`
	Branch               string `json:"branch"`
	OldCommit            string `json:"oldCommit"`
	NewCommit            string `json:"newCommit"`
	Remote               string `json:"remote"`
	RemoteRef            string `json:"remoteRef"`
	PreviousRemoteCommit string `json:"previousRemoteCommit,omitempty"`
	ActualRemoteCommit   string `json:"actualRemoteCommit"`
}

func validUpdateFastForwardReceipt(value updateFastForwardReceipt) bool {
	return value.Version == UpdateJournalVersion && safeUpdateOperationID(value.OperationID) && safeUpdateJournalID(value.ProjectID) && safeUpdateJournalID(value.RepositoryID) && value.Branch != "" && !strings.HasPrefix(value.Branch, "-") && aggregateObjectID(value.OldCommit) && aggregateObjectID(value.NewCommit) && value.NewCommit != value.OldCommit && value.Remote != "" && !strings.HasPrefix(value.Remote, "-") && strings.HasPrefix(value.RemoteRef, "refs/heads/") && aggregateObjectID(value.ActualRemoteCommit) && value.NewCommit == value.ActualRemoteCommit && (value.PreviousRemoteCommit == "" || aggregateObjectID(value.PreviousRemoteCommit)) && boundedRedactedDiagnostic(value.Remote) == value.Remote && boundedRedactedDiagnostic(value.RemoteRef) == value.RemoteRef
}

func encodeUpdateFastForwardReceipt(value updateFastForwardReceipt) (string, error) {
	if !validUpdateFastForwardReceipt(value) {
		return "", errors.New("invalid prepared update fast-forward receipt")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeUpdateFastForwardReceipt(encoded string) (updateFastForwardReceipt, error) {
	if encoded == "" || len(encoded) > 4096 || boundedRedactedDiagnostic(encoded) != encoded {
		return updateFastForwardReceipt{}, errors.New("invalid prepared update fast-forward receipt")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return updateFastForwardReceipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value updateFastForwardReceipt
	if err := decoder.Decode(&value); err != nil {
		return updateFastForwardReceipt{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validUpdateFastForwardReceipt(value) {
		return updateFastForwardReceipt{}, errors.New("invalid prepared update fast-forward receipt")
	}
	return value, nil
}

// updateAddedReceipt is an opaque private journal receipt. Paths and tree
// names are deliberately not persisted: a canonical digest binds the exact
// inventory while the locked baseline supplies the only authorized mount.
type updateAddedReceipt struct {
	Version            int    `json:"version"`
	OperationID        string `json:"operationId"`
	ProjectID          string `json:"projectId"`
	RepositoryID       string `json:"repositoryId"`
	Head               string `json:"head"`
	CommonGitDirSHA256 string `json:"commonGitDirSha256"`
	TreeSHA256         string `json:"treeSha256"`
	TreeEntries        int    `json:"treeEntries"`
}

func encodeUpdateAddedReceipt(request UpdateExecutionRequest, repositoryID, root, head, commonGitDir string, inventory cloneTreeInventory) (string, error) {
	digest, entries, err := updateTreeReceiptDigest(inventory)
	if err != nil {
		return "", err
	}
	commonDigest, err := updateCommonGitDirDigest(root, commonGitDir)
	if err != nil {
		return "", err
	}
	value := updateAddedReceipt{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, RepositoryID: repositoryID, Head: head, CommonGitDirSHA256: commonDigest, TreeSHA256: digest, TreeEntries: entries}
	if !validUpdateAddedReceipt(value) {
		return "", errors.New("invalid prepared update addition receipt")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeUpdateAddedReceipt(value string) (updateAddedReceipt, error) {
	if value == "" || len(value) > 4096 {
		return updateAddedReceipt{}, errors.New("invalid prepared update addition receipt")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return updateAddedReceipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt updateAddedReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return updateAddedReceipt{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return updateAddedReceipt{}, errors.New("prepared update addition receipt has trailing JSON")
	}
	if !validUpdateAddedReceipt(receipt) {
		return updateAddedReceipt{}, errors.New("invalid prepared update addition receipt")
	}
	return receipt, nil
}

func validUpdateAddedReceipt(value updateAddedReceipt) bool {
	return value.Version == UpdateJournalVersion && safeUpdateOperationID(value.OperationID) && safeUpdateJournalID(value.ProjectID) && safeUpdateJournalID(value.RepositoryID) && aggregateObjectID(value.Head) && validSHA256(value.CommonGitDirSHA256) && validSHA256(value.TreeSHA256) && value.TreeEntries > 0
}

func updateCommonGitDirDigest(root, commonGitDir string) (string, error) {
	if root == "" || commonGitDir == "" || !filepath.IsAbs(root) || !filepath.IsAbs(commonGitDir) {
		return "", errors.New("unsafe update Git identity")
	}
	relative, err := filepath.Rel(root, commonGitDir)
	if err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return sha256String([]byte(filepath.ToSlash(relative))), nil
	}
	canonicalRoot, rootErr := filepath.EvalSymlinks(root)
	canonicalCommon, commonErr := filepath.EvalSymlinks(commonGitDir)
	if rootErr != nil || commonErr != nil {
		return "", errors.New("unsafe update Git identity")
	}
	relative, err = filepath.Rel(canonicalRoot, canonicalCommon)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("update Git identity escapes repository")
	}
	return sha256String([]byte(filepath.ToSlash(relative))), nil
}

func updateTreeReceiptDigest(inventory cloneTreeInventory) (string, int, error) {
	if len(inventory.entries) == 0 || inventory.entries[0].path != "." {
		return "", 0, errors.New("update tree inventory is incomplete")
	}
	type entry struct {
		Path   string `json:"path"`
		Mode   uint32 `json:"mode"`
		Size   int64  `json:"size"`
		Mtime  int64  `json:"mtime,omitempty"`
		Digest string `json:"digest,omitempty"`
	}
	values := make([]entry, 0, len(inventory.entries))
	previous := ""
	for _, item := range inventory.entries {
		if item.path == "" || item.path <= previous || item.info == nil || (!item.mode.IsDir() && !item.mode.IsRegular() && item.mode&os.ModeSymlink == 0) {
			return "", 0, errors.New("update tree inventory is unsafe")
		}
		value := entry{Path: item.path, Mode: uint32(item.mode), Size: item.size, Digest: item.digest}
		if !item.mode.IsDir() {
			value.Mtime = item.mtime
		}
		values = append(values, value)
		previous = item.path
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", 0, err
	}
	return sha256String(data), len(values), nil
}

func validatePreparedAddedReceipt(encoded string, request UpdateExecutionRequest, repositoryID, root, head, commonGitDir string, inventory cloneTreeInventory) error {
	receipt, err := decodeUpdateAddedReceipt(encoded)
	if err != nil {
		return err
	}
	digest, entries, err := updateTreeReceiptDigest(inventory)
	if err != nil {
		return err
	}
	commonDigest, err := updateCommonGitDirDigest(root, commonGitDir)
	if err != nil {
		return err
	}
	if receipt.OperationID != request.OperationID || receipt.ProjectID != request.ProjectID || receipt.RepositoryID != repositoryID || receipt.Head != head || receipt.CommonGitDirSHA256 != commonDigest || receipt.TreeSHA256 != digest || receipt.TreeEntries != entries {
		return errors.New("prepared update addition ownership receipt changed")
	}
	return nil
}

func (executor *UpdateExecutor) recoverPreparedAddition(ctx context.Context, request UpdateExecutionRequest, baseline updateExecutionBaseline, effect UpdateJournalEffect) error {
	if request.DataDir == "" || !safeUpdateJournalID(effect.Repository) {
		return errors.New("prepared update addition recovery lacks authority")
	}
	var observation DriftRepositoryObservation
	for _, candidate := range baseline.observations {
		if candidate.RepositoryID == effect.Repository {
			observation = candidate
			break
		}
	}
	if observation.Path == "" || !observation.TargetAbsent || validateUpdateMountAuthority(baseline.workspace.RootPath, observation.Path) != nil {
		return errors.New("prepared update addition has no locked absent mount")
	}
	stage := filepath.Join(request.DataDir, "projects", request.ProjectID, "update", request.OperationID, "staging", effect.Repository)
	stageInfo, stageErr := os.Lstat(stage)
	mountInfo, mountErr := os.Lstat(observation.Path)
	stageExists, mountExists := stageErr == nil, mountErr == nil
	if (stageErr != nil && !os.IsNotExist(stageErr)) || (mountErr != nil && !os.IsNotExist(mountErr)) || (stageExists && mountExists) {
		return errors.New("prepared update addition location is ambiguous")
	}
	if !stageExists && !mountExists {
		return nil
	}
	root, info := stage, stageInfo
	if mountExists {
		root, info = observation.Path, mountInfo
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prepared update addition root is unsafe")
	}
	inventory, err := captureCloneTree(root)
	if err != nil {
		return err
	}
	head, err := executor.dependencies.Git.Head(ctx, root)
	if err != nil {
		return err
	}
	commonGitDir, err := executor.dependencies.Git.CommonGitDir(ctx, root)
	if err != nil {
		return err
	}
	if err := validatePreparedAddedReceipt(effect.Receipt, request, effect.Repository, root, head, commonGitDir, inventory); err != nil {
		return err
	}
	return removeOwnedUpdateTree(root, info, inventory)
}

// addedRepositoryEffect creates one repository entirely below the operation's
// private authority, proves the clone contract there, and only then publishes
// it to the captured absent mount. Its inverse refuses to remove a path whose
// directory or Git identity no longer matches the receipt it created.
func (executor *UpdateExecutor) addedRepositoryEffect(request UpdateExecutionRequest, id string, portable config.PortableRepository, observation DriftRepositoryObservation) (updateEffect, error) {
	return executor.addedRepositoryEffectWithin(request, id, portable, observation, filepath.Dir(observation.Path))
}

func (executor *UpdateExecutor) addedRepositoryEffectWithin(request UpdateExecutionRequest, id string, portable config.PortableRepository, observation DriftRepositoryObservation, workspaceRoot string) (updateEffect, error) {
	if observation.Path == "" || !filepath.IsAbs(observation.Path) || filepath.Clean(observation.Path) != observation.Path {
		return updateEffect{}, fmt.Errorf("added repository %q has unsafe mount", id)
	}
	if err := validateUpdateMountAuthority(workspaceRoot, observation.Path); err != nil {
		return updateEffect{}, fmt.Errorf("added repository %q mount authority: %w", id, err)
	}
	stageRoot := filepath.Join(request.DataDir, "projects", request.ProjectID, "update", request.OperationID, "staging")
	if !filepath.IsAbs(stageRoot) || filepath.Clean(stageRoot) != stageRoot {
		return updateEffect{}, errors.New("unsafe update staging authority")
	}
	stage := filepath.Join(stageRoot, id)
	var publishedInfo os.FileInfo
	var inventory cloneTreeInventory
	var stageInfo os.FileInfo
	var stageInventory cloneTreeInventory
	var published bool
	var commonGitDir, head string
	var err error
	cleanupStage := func() error {
		info, err := os.Lstat(stage)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil || stageInfo == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(stageInfo, info) || len(stageInventory.entries) == 0 {
			return errors.New("private update staging ownership changed")
		}
		if err := revalidateUpdateOwnedTree(stage, stageInventory); err != nil {
			return err
		}
		return removeOwnedUpdateTree(stage, stageInfo, stageInventory)
	}
	undoPublished := func(rollbackCtx context.Context) error {
		if publishedInfo == nil || commonGitDir == "" || head == "" || len(inventory.entries) == 0 {
			return errors.New("added repository has no owned publication receipt")
		}
		info, err := os.Lstat(observation.Path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(publishedInfo, info) {
			return errors.New("added repository mount ownership changed")
		}
		actualHead, err := executor.dependencies.Git.Head(rollbackCtx, observation.Path)
		if err != nil || actualHead != head {
			return errors.New("added repository HEAD ownership changed")
		}
		actualCommon, err := executor.dependencies.Git.CommonGitDir(rollbackCtx, observation.Path)
		if err != nil || actualCommon != commonGitDir {
			return errors.New("added repository Git identity changed")
		}
		if err := revalidateUpdateOwnedTree(observation.Path, inventory); err != nil {
			return fmt.Errorf("added repository tree ownership changed: %w", err)
		}
		return removeOwnedUpdateTree(observation.Path, publishedInfo, inventory)
	}
	var preparedReceipt string
	prepare := func(effectCtx context.Context) (string, error) {
		if err := ensurePrivateUpdateDirectory(stageRoot); err != nil {
			return "", err
		}
		if _, err := os.Lstat(stage); err == nil {
			return "", errors.New("private update staging path already exists")
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-clone"); err != nil {
			return "", err
		}
		if err := executor.dependencies.Git.Clone(effectCtx, portable.Clone.URL, stage, portable.Clone.Remote); err != nil {
			return "", err
		}
		stageInfo, err = os.Lstat(stage)
		if err != nil || !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("private update staging root is unsafe")
		}
		stageInventory, err = captureCloneTree(stage)
		if err != nil {
			return "", fmt.Errorf("capture private update staging inventory: %w", err)
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-clone-after"); err != nil {
			return "", err
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-fetch"); err != nil {
			return "", err
		}
		if err := executor.dependencies.Git.FetchTrackingBranch(effectCtx, stage, portable.Upstream.Remote, portable.Upstream.Merge); err != nil {
			return "", err
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-fetch-after"); err != nil {
			return "", err
		}
		stageInventory, err = captureCloneTree(stage)
		if err != nil {
			return "", fmt.Errorf("capture fetched update staging inventory: %w", err)
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-checkout"); err != nil {
			return "", err
		}
		head, err = executor.dependencies.Git.CheckoutTrackingBranch(effectCtx, stage, portable.DefaultBranch, portable.Upstream.Remote, portable.Upstream.Merge)
		if err != nil {
			return "", err
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-checkout-after"); err != nil {
			return "", err
		}
		stageInventory, err = captureCloneTree(stage)
		if err != nil {
			return "", fmt.Errorf("capture checked-out update staging inventory: %w", err)
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-verify"); err != nil {
			return "", err
		}
		if err := verifyAddedRepository(effectCtx, executor.dependencies.Git, stage, portable, head); err != nil {
			return "", err
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-verify-after"); err != nil {
			return "", err
		}
		inventory, err = captureCloneTree(stage)
		if err != nil {
			return "", fmt.Errorf("capture staged repository inventory: %w", err)
		}
		commonGitDir, err = executor.dependencies.Git.CommonGitDir(effectCtx, stage)
		if err != nil {
			return "", err
		}
		preparedReceipt, err = encodeUpdateAddedReceipt(request, id, stage, head, commonGitDir, inventory)
		if err != nil {
			return "", err
		}
		return preparedReceipt, nil
	}
	publish := func(effectCtx context.Context) (string, error) {
		if preparedReceipt == "" {
			if _, err := prepare(effectCtx); err != nil {
				return "", err
			}
		}
		if err := revalidateUpdateOwnedTree(stage, stageInventory); err != nil {
			return "", fmt.Errorf("prepared update staging changed: %w", err)
		}
		actualCommon, err := executor.dependencies.Git.CommonGitDir(effectCtx, stage)
		if err != nil {
			return "", err
		}
		if err := validatePreparedAddedReceipt(preparedReceipt, request, id, stage, head, actualCommon, inventory); err != nil {
			return "", err
		}
		if err := ensureAbsentUpdateMountWithin(workspaceRoot, observation.Path); err != nil {
			return "", err
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-publish"); err != nil {
			return "", err
		}
		if err := os.Rename(stage, observation.Path); err != nil {
			return "", err
		}
		published = true
		publishedInfo, err = os.Lstat(observation.Path)
		if err != nil || !publishedInfo.IsDir() || publishedInfo.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("published repository mount changed")
		}
		if err := translateCloneRootAfterRename(observation.Path, &inventory, publishedInfo); err != nil {
			return "", fmt.Errorf("published repository identity changed: %w", err)
		}
		commonGitDir, err = executor.dependencies.Git.CommonGitDir(effectCtx, observation.Path)
		if err != nil {
			return "", err
		}
		if err := validatePreparedAddedReceipt(preparedReceipt, request, id, observation.Path, head, commonGitDir, inventory); err != nil {
			return "", err
		}
		if err := effectBoundary(effectCtx, executor, "repository-"+id+"-publish-after"); err != nil {
			return "", err
		}
		return preparedReceipt, effectCtx.Err()
	}
	return updateEffect{Name: "repository-" + id + "-add", Repository: id, Prepare: prepare, Execute: publish, Cleanup: func(rollbackCtx context.Context) error {
		if published {
			return undoPublished(rollbackCtx)
		}
		return cleanupStage()
	}, Rollback: undoPublished}, nil
}

func ensurePrivateUpdateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	for current := filepath.Clean(path); current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe private update staging directory")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return errors.New("update staging directory is not private")
		}
		// Stop at the fixed operation authority. The configurable data directory
		// may itself be beneath a platform symlink such as macOS /var.
		if filepath.Base(current) == "update" {
			break
		}
	}
	return nil
}

func ensureAbsentUpdateMount(path string) error {
	return ensureAbsentUpdateMountWithin(filepath.Dir(path), path)
}

// validateUpdateMountAuthority ensures that an added checkout is contained by
// the logical workspace root before any filesystem operation. filepath.Rel is
// deliberately used instead of prefix matching so spaces, Unicode, and shared
// textual prefixes cannot escape the authority.
func validateUpdateMountAuthority(root, target string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("mount authority must use clean absolute paths")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("added repository mount escapes logical workspace root")
	}
	return nil
}

// ensureAbsentUpdateMountWithin rechecks the target and every ancestor that
// will be traversed by rename. This rejects symlink/type swaps at the last
// responsible moment and makes nested parent-first publication explicit.
func ensureAbsentUpdateMountWithin(root, target string) error {
	if err := validateUpdateMountAuthority(root, target); err != nil {
		return err
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		if err == nil {
			return errors.New("added repository target mount is occupied")
		}
		return fmt.Errorf("inspect added repository target mount: %w", err)
	}
	for current := filepath.Dir(target); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("added repository mount ancestor is unsafe")
		}
		if current == root {
			return nil
		}
	}
}

// revalidateUpdateOwnedTree permits only directory timestamp changes. Nested
// owned child publication necessarily changes a parent directory timestamp;
// every name, type, regular-file digest, symlink value, and object identity
// remains exact, so concurrent content or membership changes still refuse
// cleanup.
func revalidateUpdateOwnedTree(root string, expected cloneTreeInventory) error {
	actual, err := captureCloneTree(root)
	if err != nil {
		return err
	}
	if len(actual.entries) != len(expected.entries) {
		return errors.New("owned update tree membership changed")
	}
	for index := range expected.entries {
		want, got := expected.entries[index], actual.entries[index]
		if want.path != got.path || want.mode != got.mode || want.size != got.size || want.digest != got.digest || want.info == nil || got.info == nil || !os.SameFile(want.info, got.info) {
			return fmt.Errorf("owned update tree changed at %q", want.path)
		}
		if !want.mode.IsDir() && want.mtime != got.mtime {
			return fmt.Errorf("owned update file metadata changed at %q", want.path)
		}
	}
	return nil
}

func removeOwnedUpdateTree(path string, expected os.FileInfo, inventory cloneTreeInventory) error {
	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return errors.New("added repository mount ownership changed before cleanup")
	}
	if err := revalidateUpdateOwnedTree(path, inventory); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func verifyAddedRepository(ctx context.Context, git gitadapter.Git, path string, portable config.PortableRepository, expectedHead string) error {
	top, err := git.TopLevel(ctx, path)
	if err != nil || !sameUpdateDirectory(top, path) {
		return errors.New("added repository checkout root is unexpected")
	}
	branch, detached, err := git.CurrentBranch(ctx, path)
	if err != nil || detached || branch != portable.DefaultBranch {
		return errors.New("added repository branch is unexpected")
	}
	actualHead, err := git.Head(ctx, path)
	if err != nil || actualHead != expectedHead {
		return errors.New("added repository HEAD is unexpected")
	}
	upstream, err := git.Upstream(ctx, path)
	if err != nil || upstream.LocalBranch != portable.DefaultBranch || upstream.Remote != portable.Upstream.Remote || upstream.Merge != portable.Upstream.Merge || upstream.FetchURL != portable.Clone.URL {
		return errors.New("added repository upstream is unexpected")
	}
	contains, err := git.ContainsCommits(ctx, path, portable.Identity.InitialCommits)
	if err != nil || !contains {
		return errors.New("added repository identity roots are unexpected")
	}
	clean, err := git.IsClean(ctx, path)
	if err != nil || !clean {
		return errors.New("added repository clone is dirty")
	}
	hasSubmodules, err := git.HasSubmodules(ctx, path)
	if err != nil || hasSubmodules {
		return errors.New("added repository contains submodules")
	}
	return nil
}

// The configurable data and workspace roots may be reached through a platform
// alias such as macOS /var.  Git reports the physical spelling, while the
// operation authority retains the caller's lexical spelling; compare their
// resolved directories without permitting a different target.
func sameUpdateDirectory(left, right string) bool {
	canonicalLeft, leftErr := filepath.EvalSymlinks(left)
	canonicalRight, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(canonicalLeft) == filepath.Clean(canonicalRight)
}

func (executor *UpdateExecutor) recapture(ctx context.Context, request UpdateExecutionRequest, testRecapture func(context.Context, UpdatePlan) (DriftSnapshot, error)) (DriftSnapshot, error) {
	if testRecapture != nil {
		return testRecapture(ctx, request.Plan)
	}
	baseline := request.Plan.executionBaseline()
	collector := NewUpdateSnapshotCollector(baseline.project, baseline.workspace, baseline.dataDir, LoadedManifestSource{Kind: request.Plan.Source.Kind, Source: request.Plan.Source.Value, data: baseline.candidate})
	input, err := collector.CollectDriftSnapshot(ctx)
	if err != nil {
		return DriftSnapshot{}, err
	}
	return BuildDriftSnapshot(input)
}

func compareUpdateExecutionBaseline(expected updateExecutionBaseline, fresh DriftSnapshot) error {
	actual := newUpdateExecutionBaseline(fresh)
	if expected.dataDir != actual.dataDir || !reflect.DeepEqual(expected.project, actual.project) || !reflect.DeepEqual(expected.workspace, actual.workspace) || !reflect.DeepEqual(expected.current, actual.current) || !bytes.Equal(expected.localConfig, actual.localConfig) || !bytes.Equal(expected.registry, actual.registry) || !reflect.DeepEqual(expected.defaultState, actual.defaultState) || !reflect.DeepEqual(expected.reconciliation, actual.reconciliation) || !reflect.DeepEqual(expected.repositories, actual.repositories) || !bytes.Equal(expected.candidate, actual.candidate) {
		return errors.New("fixed project, generation, topology, or repository facts changed")
	}
	if len(expected.observations) != len(actual.observations) {
		return errors.New("repository observation set changed")
	}
	for index := range expected.observations {
		left, right := expected.observations[index], actual.observations[index]
		left.AdvertisedCommit, right.AdvertisedCommit = "", ""
		if !reflect.DeepEqual(left, right) {
			return fmt.Errorf("repository observation %q changed", expected.observations[index].RepositoryID)
		}
	}
	return nil
}
func classifyUpdateExecutionError(step string, err error) error {
	if application := (*Error)(nil); errors.As(err, &application) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewError(ErrorConflict, fmt.Errorf("%s: %w", step, err))
	}
	return NewError(ErrorInternal, fmt.Errorf("%s: %w", step, err))
}
