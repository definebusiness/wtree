package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/transaction"
)

// ProjectLocker is the small locking boundary required for workspace mutation.
type ProjectLocker interface {
	ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error)
}

// WorkspaceTransactionRequest supplies a validated plan and concrete effects.
// Revalidate runs only after the project lock is held; ValidateResult runs
// after effects and before workspace state becomes authoritative.
type WorkspaceTransactionRequest struct {
	Plan           plan.WorkspacePlan
	DataDir        string
	Steps          []transaction.Step
	Revalidate     func(context.Context) error
	ValidateResult func(context.Context) error
	Progress       func(transaction.Event)
	BeforeExecute  func(index int, step transaction.Step) error
}

// WorkspaceTransaction coordinates an operation's lock, reversible effects,
// result validation, and durable state boundary.
type WorkspaceTransaction struct {
	Locker         ProjectLocker
	LockTimeout    time.Duration
	writeWorkspace func(string, store.WorkspaceState) error
	writeRecovery  func(string, store.RecoveryRecord) error
	remove         func(string) error
	readFile       func(string) ([]byte, error)
	writeRaw       func(string, []byte) error
}

func NewWorkspaceTransaction() *WorkspaceTransaction {
	return NewWorkspaceTransactionWith(lock.Manager{}, store.WriteWorkspace, store.WriteRecovery, os.Remove)
}

func NewWorkspaceTransactionWith(locker ProjectLocker, writeWorkspace func(string, store.WorkspaceState) error, writeRecovery func(string, store.RecoveryRecord) error, remove func(string) error) *WorkspaceTransaction {
	return NewWorkspaceTransactionWithFiles(locker, writeWorkspace, writeRecovery, remove, os.ReadFile, store.WriteRawAtomic)
}

// NewWorkspaceTransactionWithFiles exposes the state snapshot/restore
// boundary for failure-injection tests and adapters.
func NewWorkspaceTransactionWithFiles(locker ProjectLocker, writeWorkspace func(string, store.WorkspaceState) error, writeRecovery func(string, store.RecoveryRecord) error, remove func(string) error, readFile func(string) ([]byte, error), writeRaw func(string, []byte) error) *WorkspaceTransaction {
	return &WorkspaceTransaction{
		Locker:         locker,
		LockTimeout:    time.Second,
		writeWorkspace: writeWorkspace,
		writeRecovery:  writeRecovery,
		remove:         remove,
		readFile:       readFile,
		writeRaw:       writeRaw,
	}
}

// WorkspaceStateDirectory is the established directory for authoritative
// workspace state. Readers and writers share this boundary so generated
// workspaces are visible to resolver and planner collision checks.
func WorkspaceStateDirectory(dataDir, projectID string) string {
	return filepath.Join(dataDir, "state", projectID)
}

// WorkspaceStatePath returns one workspace's authoritative state location.
func WorkspaceStatePath(dataDir, projectID, workspaceID string) string {
	return filepath.Join(WorkspaceStateDirectory(dataDir, projectID), workspaceID+".json")
}

// RecoveryRecordPath is the durable, actionable record left only when undoing
// a mutation cannot be completed.
func RecoveryRecordPath(dataDir string, value plan.WorkspacePlan) string {
	return filepath.Join(dataDir, "projects", value.ProjectID, "recovery", value.WorkspaceID+".json")
}

// Execute holds the project lock from revalidation to state commit. Any
// failure after an effect completes rolls every completed reversible effect
// back before returning.
func (w *WorkspaceTransaction) Execute(ctx context.Context, request WorkspaceTransactionRequest) (transaction.Result, error) {
	if err := request.Plan.Validate(); err != nil {
		return transaction.Result{}, NewError(ErrorValidation, fmt.Errorf("validate transaction plan: %w", err))
	}
	if err := validateTransactionRequest(request); err != nil {
		return transaction.Result{}, NewError(ErrorValidation, err)
	}
	if w == nil || w.Locker == nil || w.writeWorkspace == nil || w.writeRecovery == nil || w.remove == nil || w.readFile == nil || w.writeRaw == nil {
		return transaction.Result{}, NewError(ErrorInternal, errors.New("workspace transaction is not configured"))
	}
	timeout := w.LockTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	handle, err := w.Locker.ProjectLock(ctx, request.DataDir, request.Plan.ProjectID, timeout)
	if err != nil {
		return transaction.Result{}, NewError(ErrorConflict, fmt.Errorf("acquire project mutation lock: %w", err))
	}
	defer handle.Unlock()
	if request.Revalidate != nil {
		if err := request.Revalidate(ctx); err != nil {
			return transaction.Result{}, classifyTransactionError("revalidate plan", err)
		}
	}
	recoveryPath := RecoveryRecordPath(request.DataDir, request.Plan)
	_, hasRecovery, err := w.snapshotState(recoveryPath)
	if err != nil {
		return transaction.Result{}, NewError(ErrorInternal, fmt.Errorf("inspect recovery record: %w", err))
	}
	if hasRecovery {
		return transaction.Result{}, NewError(ErrorConflict, fmt.Errorf("workspace %q has an unresolved recovery record at %q; repair it before another mutation", request.Plan.WorkspaceName, recoveryPath))
	}
	statePath := WorkspaceStatePath(request.DataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)
	priorState, hadPrior, err := w.snapshotState(statePath)
	if err != nil {
		return transaction.Result{}, NewError(ErrorInternal, fmt.Errorf("snapshot workspace state: %w", err))
	}

	runner := transaction.Runner{Progress: request.Progress, BeforeExecute: request.BeforeExecute}
	result := runner.Run(ctx, request.Steps)
	if !result.Succeeded() {
		return w.finishFailure(request, result)
	}
	if err := ctx.Err(); err != nil {
		result = runner.Rollback(ctx, request.Steps, "canceled", err)
		return w.finishFailure(request, result)
	}
	if request.ValidateResult != nil {
		if err := request.ValidateResult(ctx); err != nil {
			result = runner.Rollback(ctx, request.Steps, "validate-result", err)
			return w.finishFailure(request, result)
		}
	}
	if err := ctx.Err(); err != nil {
		result = runner.Rollback(ctx, request.Steps, "canceled", err)
		return w.finishFailure(request, result)
	}
	state := workspaceState(request.Plan)
	if err := w.writeWorkspace(statePath, state); err != nil {
		cleanupErr := w.restoreFailedStateCommit(statePath, priorState, hadPrior, state)
		failure := errors.Join(fmt.Errorf("commit workspace state: %w", err), cleanupErr)
		result = runner.Rollback(ctx, request.Steps, "commit-state", failure)
		if cleanupErr != nil {
			result.RollbackFailure = errors.Join(result.RollbackFailure, cleanupErr)
			result.UnrevertedSteps = append(result.UnrevertedSteps, "commit-state")
			result.RollbackFailures = append(result.RollbackFailures, transaction.RollbackIssue{Step: "commit-state", Error: cleanupErr.Error()})
		}
		return w.finishFailure(request, result)
	}
	return result, nil
}

func (w *WorkspaceTransaction) snapshotState(path string) ([]byte, bool, error) {
	data, err := w.readFile(path)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}

func (w *WorkspaceTransaction) restoreFailedStateCommit(path string, prior []byte, hadPrior bool, attempted store.WorkspaceState) error {
	if hadPrior {
		if err := w.writeRaw(path, prior); err != nil {
			return fmt.Errorf("restore prior workspace state: %w", err)
		}
		return nil
	}
	current, err := w.readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect failed state commit: %w", err)
	}
	expected, err := store.WorkspaceBytes(attempted)
	if err != nil {
		return fmt.Errorf("encode attempted workspace state: %w", err)
	}
	if !bytes.Equal(current, expected) {
		return errors.New("failed state commit changed workspace state unexpectedly; preserving it for recovery")
	}
	return removeIfExists(w.remove, path)
}

func validateTransactionRequest(request WorkspaceTransactionRequest) error {
	if request.DataDir == "" {
		return errors.New("transaction data directory is required")
	}
	if len(request.Steps) == 0 {
		return errors.New("transaction steps are required")
	}
	if request.Revalidate == nil {
		return errors.New("transaction revalidation is required")
	}
	if request.ValidateResult == nil {
		return errors.New("transaction result validation is required")
	}
	for _, step := range request.Steps {
		if step.Name == "" || step.Execute == nil {
			return errors.New("transaction steps require names and execute actions")
		}
	}
	return nil
}

func (w *WorkspaceTransaction) finishFailure(request WorkspaceTransactionRequest, result transaction.Result) (transaction.Result, error) {
	recoveryPath := RecoveryRecordPath(request.DataDir, request.Plan)
	if result.RollbackFailure == nil {
		// State cleanup belongs to the state-commit boundary: it either restores
		// a byte-identical prior file or removes only this attempt's published
		// bytes. Do not delete a pre-existing authoritative workspace here.
		cleanupErr := removeIfExists(w.remove, recoveryPath)
		if cleanupErr != nil {
			result.RollbackFailure = cleanupErr
		} else {
			err := classifyTransactionError("execute workspace transaction", result.Failure)
			if len(result.Completed) > 0 || result.FailedExecuteRolledBack {
				err = withCleanRollback(err)
			}
			return result, err
		}
	}
	record := store.RecoveryRecord{
		ProjectID: request.Plan.ProjectID, WorkspaceID: request.Plan.WorkspaceID,
		Operation: string(request.Plan.Operation), FailedStep: result.FailedStep, CompletedSteps: result.Completed,
		UnrevertedSteps: result.UnrevertedSteps, RollbackFailures: recoveryRollbackFailures(result.RollbackFailures),
	}
	recordErr := w.writeRecovery(recoveryPath, record)
	cause := fmt.Errorf("workspace rollback is incomplete after %q; recovery record %q: %w", result.FailedStep, recoveryPath, result.RollbackFailure)
	if recordErr != nil {
		cause = fmt.Errorf("%w; also write recovery record: %v", cause, recordErr)
	}
	return result, NewError(ErrorRollbackIncomplete, cause)
}

func withCleanRollback(err error) error {
	var application *Error
	if errors.As(err, &application) {
		return NewError(application.Kind, NewCleanRollbackError(application.Cause))
	}
	return NewCleanRollbackError(err)
}

func recoveryRollbackFailures(issues []transaction.RollbackIssue) []store.RollbackFailure {
	failures := make([]store.RollbackFailure, len(issues))
	for index, issue := range issues {
		failures[index] = store.RollbackFailure{Step: issue.Step, Error: issue.Error}
	}
	return failures
}

func workspaceState(value plan.WorkspacePlan) store.WorkspaceState {
	repositories := make(map[string]store.CheckoutState, len(value.Repositories))
	for _, repository := range value.Repositories {
		repositories[repository.ID] = store.CheckoutState{Branch: repository.Branch, Mount: repository.Mount, ResolvedPath: repository.Path, Head: repository.Base}
	}
	return store.WorkspaceState{ID: value.WorkspaceID, Name: value.WorkspaceName, Path: value.RootPath, Repositories: repositories}
}

func removeIfExists(remove func(string) error, path string) error {
	if err := remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %q: %w", path, err)
	}
	return nil
}

func classifyTransactionError(operation string, err error) error {
	var application *Error
	if errors.As(err, &application) {
		return err
	}
	return NewError(ErrorInternal, fmt.Errorf("%s: %w", operation, err))
}
