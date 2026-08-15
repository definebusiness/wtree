package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/transaction"
)

func TestWorkspaceTransactionCommitsStateOnlyAfterValidatedSuccess(t *testing.T) {
	dataDir := t.TempDir()
	var calls []string
	coordinator := NewWorkspaceTransaction()
	request := transactionRequest(dataDir, []transaction.Step{
		step("create-root", &calls),
		step("add-root", &calls),
	})
	request.Revalidate = func(context.Context) error { calls = append(calls, "revalidate"); return nil }
	request.ValidateResult = func(context.Context) error { calls = append(calls, "validate-result"); return nil }

	result, err := coordinator.Execute(context.Background(), request)
	if err != nil || !result.Succeeded() {
		t.Fatalf("Execute = (%#v, %v), want success", result, err)
	}
	if got, want := calls, []string{"revalidate", "execute:create-root", "execute:add-root", "validate-result"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID))
	if err != nil {
		t.Fatalf("ReadWorkspace: %v", err)
	}
	if state.ID != request.Plan.WorkspaceID || state.Repositories["root"].ResolvedPath != "/workspace/root" {
		t.Fatalf("state = %#v", state)
	}
	if _, err := os.Stat(RecoveryRecordPath(dataDir, request.Plan)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery record err = %v, want absent", err)
	}
}

func TestWorkspaceStatePathsUseSharedStateDirectory(t *testing.T) {
	dataDir := t.TempDir()
	request := transactionRequest(dataDir, nil)
	if got, want := WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID), filepath.Join(dataDir, "state", request.Plan.ProjectID, request.Plan.WorkspaceID+".json"); got != want {
		t.Fatalf("WorkspaceStatePath = %q, want %q", got, want)
	}
	if got, want := WorkspaceStateDirectory(dataDir, request.Plan.ProjectID), filepath.Join(dataDir, "state", request.Plan.ProjectID); got != want {
		t.Fatalf("WorkspaceStateDirectory = %q, want %q", got, want)
	}
}

func TestWorkspaceTransactionRollbackFailureWritesRecoveryRecordAndReturnsExitNine(t *testing.T) {
	dataDir := t.TempDir()
	coordinator := NewWorkspaceTransaction()
	request := transactionRequest(dataDir, []transaction.Step{
		{Name: "create-root", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { return errors.New("cannot remove branch") }},
		{Name: "add-root", Execute: func(context.Context) error { return errors.New("injected add failure") }, Rollback: func(context.Context) error { return nil }},
	})

	result, err := coordinator.Execute(context.Background(), request)
	if result.RollbackFailure == nil {
		t.Fatalf("result = %#v, want rollback failure", result)
	}
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("error = %v, want %v", err, ErrorRollbackIncomplete)
	}
	record, readErr := store.ReadRecovery(RecoveryRecordPath(dataDir, request.Plan))
	if readErr != nil {
		t.Fatalf("ReadRecovery: %v", readErr)
	}
	if got, want := record.CompletedSteps, []string{"create-root"}; !reflect.DeepEqual(got, want) || record.FailedStep != "add-root" || record.ProjectID != request.Plan.ProjectID || record.Operation != string(plan.Create) {
		t.Fatalf("recovery = %#v", record)
	}
	if _, statErr := os.Stat(WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state stat = %v, want absent", statErr)
	}
}

func TestWorkspaceTransactionRecoveryDescribesOnlyUnrevertedMixedRollbackSteps(t *testing.T) {
	dataDir := t.TempDir()
	request := transactionRequest(dataDir, []transaction.Step{
		{Name: "create-root", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { return errors.New("branch remains") }},
		{Name: "add-root", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { return nil }},
		{Name: "create-child", Execute: func(context.Context) error { return errors.New("injected failure") }, Rollback: func(context.Context) error { return nil }},
	})

	_, err := NewWorkspaceTransaction().Execute(context.Background(), request)
	if err == nil {
		t.Fatal("Execute succeeded, want incomplete rollback")
	}
	record, err := store.ReadRecovery(RecoveryRecordPath(dataDir, request.Plan))
	if err != nil {
		t.Fatalf("ReadRecovery: %v", err)
	}
	if got, want := record.UnrevertedSteps, []string{"create-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unreverted steps = %v, want %v", got, want)
	}
	if got, want := record.RollbackFailures, []store.RollbackFailure{{Step: "create-root", Error: "branch remains"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback failures = %#v, want %#v", got, want)
	}
}

func TestWorkspaceTransactionPreservesExistingRecoveryBeforeCleanFailure(t *testing.T) {
	dataDir := t.TempDir()
	ran := false
	request := transactionRequest(dataDir, []transaction.Step{{
		Name: "first-clean-failure",
		Execute: func(context.Context) error {
			ran = true
			return errors.New("clean failure")
		},
		Rollback: func(context.Context) error { return nil },
	}})
	recoveryPath := RecoveryRecordPath(dataDir, request.Plan)
	prior := []byte("{ \"version\": 1, \"projectId\": \"project-id\", \"workspaceId\": \"earlier\", \"operation\": \"create\", \"failedStep\": \"branch\", \"completedSteps\": [\"branch\"] }\n")
	if err := os.MkdirAll(filepath.Dir(recoveryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryPath, prior, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewWorkspaceTransaction().Execute(context.Background(), request)
	if err == nil {
		t.Fatal("Execute succeeded with unresolved recovery")
	}
	if ran {
		t.Fatal("effect ran despite unresolved recovery")
	}
	got, readErr := os.ReadFile(recoveryPath)
	if readErr != nil {
		t.Fatalf("ReadFile recovery: %v", readErr)
	}
	if !bytes.Equal(got, prior) {
		t.Fatalf("recovery bytes changed:\n got %q\nwant %q", got, prior)
	}
}

func TestWorkspaceTransactionCleanRollbackRemovesCurrentAttemptRecovery(t *testing.T) {
	dataDir := t.TempDir()
	request := transactionRequest(dataDir, nil)
	recoveryPath := RecoveryRecordPath(dataDir, request.Plan)
	request.Steps = []transaction.Step{
		{Name: "record-progress", Execute: func(context.Context) error {
			return store.WriteRecovery(recoveryPath, store.RecoveryRecord{ProjectID: request.Plan.ProjectID, WorkspaceID: request.Plan.WorkspaceID, Operation: "create", FailedStep: "interrupted"})
		}, Rollback: func(context.Context) error { return nil }},
		{Name: "clean-failure", Execute: func(context.Context) error { return errors.New("clean failure") }, Rollback: func(context.Context) error { return nil }},
	}

	if _, err := NewWorkspaceTransaction().Execute(context.Background(), request); err == nil {
		t.Fatal("Execute succeeded, want clean failure")
	}
	if _, err := os.Stat(recoveryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery stat = %v, want current attempt record removed", err)
	}
}

func TestWorkspaceTransactionStateCommitFailureRollsBackAndLeavesNoLogicalState(t *testing.T) {
	dataDir := t.TempDir()
	var calls []string
	coordinator := NewWorkspaceTransactionWith(
		lock.Manager{},
		func(path string, state store.WorkspaceState) error {
			if err := store.WriteWorkspace(path, state); err != nil {
				return err
			}
			return errors.New("injected state commit failure")
		},
		store.WriteRecovery,
		os.Remove,
	)
	request := transactionRequest(dataDir, []transaction.Step{step("create-root", &calls)})

	_, err := coordinator.Execute(context.Background(), request)
	if err == nil {
		t.Fatal("Execute succeeded, want state commit failure")
	}
	if got, want := calls, []string{"execute:create-root", "rollback:create-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if _, statErr := os.Stat(WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state stat = %v, want absent", statErr)
	}
	if _, statErr := os.Stat(RecoveryRecordPath(dataDir, request.Plan)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("recovery stat = %v, want absent", statErr)
	}
}

func TestWorkspaceTransactionStateCleanupFailureIsRecoveryIncomplete(t *testing.T) {
	dataDir := t.TempDir()
	coordinator := NewWorkspaceTransactionWith(
		lock.Manager{},
		func(path string, state store.WorkspaceState) error {
			if err := store.WriteWorkspace(path, state); err != nil {
				return err
			}
			return errors.New("injected post-publication failure")
		},
		store.WriteRecovery,
		func(path string) error {
			if path == WorkspaceStatePath(dataDir, "project-id", "workspace-id") {
				return errors.New("state cleanup denied")
			}
			return os.Remove(path)
		},
	)
	request := transactionRequest(dataDir, []transaction.Step{{Name: "create-root", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { return nil }}})

	result, err := coordinator.Execute(context.Background(), request)
	if result.RollbackFailure == nil {
		t.Fatalf("result = %#v, want cleanup failure", result)
	}
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("error = %v, want rollback incomplete", err)
	}
	if _, readErr := store.ReadRecovery(RecoveryRecordPath(dataDir, request.Plan)); readErr != nil {
		t.Fatalf("ReadRecovery: %v", readErr)
	}
}

func TestWorkspaceTransactionStateFailureRestoresPriorBytes(t *testing.T) {
	dataDir := t.TempDir()
	request := transactionRequest(dataDir, []transaction.Step{{Name: "create-root", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { return nil }}})
	statePath := WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)
	prior := []byte("{\n  \"version\": 1,\n  \"id\": \"old\",\n  \"name\": \"old\",\n  \"path\": \"/old\",\n  \"repositories\": {}\n}\n")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator := NewWorkspaceTransactionWith(lock.Manager{}, func(path string, state store.WorkspaceState) error {
		if err := store.WriteWorkspace(path, state); err != nil {
			return err
		}
		return errors.New("injected post-publication state failure")
	}, store.WriteRecovery, os.Remove)

	if _, err := coordinator.Execute(context.Background(), request); err == nil {
		t.Fatal("Execute succeeded, want state failure")
	}
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile prior state: %v", err)
	}
	if !bytes.Equal(got, prior) {
		t.Fatalf("state bytes changed:\n got %q\nwant %q", got, prior)
	}
}

func TestWorkspaceTransactionResultValidationFailureRollsBackBeforeStateCommit(t *testing.T) {
	dataDir := t.TempDir()
	var calls []string
	request := transactionRequest(dataDir, []transaction.Step{step("create-root", &calls)})
	request.ValidateResult = func(context.Context) error {
		calls = append(calls, "validate-result")
		return errors.New("result identity mismatch")
	}

	_, err := NewWorkspaceTransaction().Execute(context.Background(), request)
	if err == nil {
		t.Fatal("Execute succeeded, want result validation failure")
	}
	if got, want := calls, []string{"execute:create-root", "validate-result", "rollback:create-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if _, statErr := os.Stat(WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state stat = %v, want absent", statErr)
	}
}

func TestWorkspaceTransactionHoldsLockThroughStateCommit(t *testing.T) {
	dataDir := t.TempDir()
	locker := &observingLocker{}
	coordinator := NewWorkspaceTransactionWith(locker, func(path string, value store.WorkspaceState) error {
		if !locker.locked {
			t.Fatal("state commit ran without project lock")
		}
		return store.WriteWorkspace(path, value)
	}, store.WriteRecovery, os.Remove)
	request := transactionRequest(dataDir, []transaction.Step{{Name: "create-root", Execute: func(context.Context) error {
		if !locker.locked {
			t.Fatal("execution ran without project lock")
		}
		return nil
	}, Rollback: func(context.Context) error { return nil }}})
	request.Revalidate = func(context.Context) error {
		if !locker.locked {
			t.Fatal("revalidation ran without project lock")
		}
		return nil
	}
	request.ValidateResult = func(context.Context) error {
		if !locker.locked {
			t.Fatal("result validation ran without project lock")
		}
		return nil
	}

	if _, err := coordinator.Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if locker.locked || locker.unlocks != 1 {
		t.Fatalf("lock state = locked:%t unlocks:%d, want released once", locker.locked, locker.unlocks)
	}
}

func TestWorkspaceTransactionRevalidationFailureHasNoEffectsOrState(t *testing.T) {
	dataDir := t.TempDir()
	request := transactionRequest(dataDir, []transaction.Step{{Name: "create-root", Execute: func(context.Context) error {
		t.Fatal("execution ran after revalidation failure")
		return nil
	}, Rollback: func(context.Context) error { return nil }}})
	request.Revalidate = func(context.Context) error { return errors.New("project drifted") }

	if _, err := NewWorkspaceTransaction().Execute(context.Background(), request); err == nil {
		t.Fatal("Execute succeeded, want revalidation failure")
	}
	if _, statErr := os.Stat(WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state stat = %v, want absent", statErr)
	}
}

func TestWorkspaceTransactionRequiresLockedRevalidationAndResultValidation(t *testing.T) {
	for _, missing := range []string{"revalidate", "result-validation"} {
		t.Run(missing, func(t *testing.T) {
			dataDir := t.TempDir()
			request := transactionRequest(dataDir, []transaction.Step{{Name: "create-root", Execute: func(context.Context) error {
				t.Fatal("effect ran without required boundary")
				return nil
			}, Rollback: func(context.Context) error { return nil }}})
			if missing == "revalidate" {
				request.Revalidate = nil
			} else {
				request.ValidateResult = nil
			}

			if _, err := NewWorkspaceTransaction().Execute(context.Background(), request); err == nil {
				t.Fatal("Execute succeeded without required boundary")
			}
			if _, err := os.Stat(WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("state stat = %v, want absent", err)
			}
		})
	}
}

func TestWorkspaceTransactionContentionDoesNotRunEffects(t *testing.T) {
	dataDir := t.TempDir()
	coordinator := NewWorkspaceTransaction()
	coordinator.LockTimeout = 20 * time.Millisecond
	request := transactionRequest(dataDir, []transaction.Step{{Name: "create-root", Execute: func(context.Context) error { t.Fatal("effect ran despite lock contention"); return nil }, Rollback: func(context.Context) error { return nil }}})
	handle, err := coordinator.Locker.ProjectLock(context.Background(), dataDir, request.Plan.ProjectID, time.Second)
	if err != nil {
		t.Fatalf("acquire setup lock: %v", err)
	}
	defer handle.Unlock()

	if _, err := coordinator.Execute(context.Background(), request); err == nil {
		t.Fatal("Execute succeeded despite lock contention")
	}
}

func TestWorkspaceTransactionCancellationRollsBackAtNextSafeBoundary(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	request := transactionRequest(dataDir, []transaction.Step{
		{Name: "create-root", Execute: func(context.Context) error { calls = append(calls, "execute:create-root"); cancel(); return nil }, Rollback: func(context.Context) error { calls = append(calls, "rollback:create-root"); return nil }},
		{Name: "add-root", Execute: func(context.Context) error { t.Fatal("second step ran after cancellation"); return nil }, Rollback: func(context.Context) error { return nil }},
	})

	_, err := NewWorkspaceTransaction().Execute(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context canceled", err)
	}
	if got, want := calls, []string{"execute:create-root", "rollback:create-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestWorkspaceTransactionCancellationAfterFinalEffectRollsBackBeforeStateCommit(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	request := transactionRequest(dataDir, []transaction.Step{{
		Name: "create-root",
		Execute: func(context.Context) error {
			calls = append(calls, "execute:create-root")
			cancel()
			return nil
		},
		Rollback: func(context.Context) error { calls = append(calls, "rollback:create-root"); return nil },
	}})

	_, err := NewWorkspaceTransaction().Execute(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context canceled", err)
	}
	if got, want := calls, []string{"execute:create-root", "rollback:create-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if _, statErr := os.Stat(WorkspaceStatePath(dataDir, request.Plan.ProjectID, request.Plan.WorkspaceID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state stat = %v, want absent", statErr)
	}
}

func transactionRequest(dataDir string, steps []transaction.Step) WorkspaceTransactionRequest {
	return WorkspaceTransactionRequest{
		Plan: plan.WorkspacePlan{
			Version: 1, Operation: plan.Create, ProjectID: "project-id", WorkspaceName: "feature/login", WorkspaceID: "workspace-id", RootPath: "/workspace",
			Repositories: []plan.RepositoryPlan{{ID: "root", Base: "base", Branch: "feature/login", Mount: ".", Path: "/workspace/root"}},
			Steps:        []plan.Step{{Action: plan.CreateBranch, RepositoryID: "root", Inverse: plan.DeleteBranch}, {Action: plan.AddWorktree, RepositoryID: "root", Inverse: plan.RemoveWorktree}},
		},
		DataDir: dataDir,
		Steps:   steps,
		Revalidate: func(context.Context) error {
			return nil
		},
		ValidateResult: func(context.Context) error {
			return nil
		},
	}
}

func step(name string, calls *[]string) transaction.Step {
	return transaction.Step{Name: name, Execute: func(context.Context) error { *calls = append(*calls, "execute:"+name); return nil }, Rollback: func(context.Context) error { *calls = append(*calls, "rollback:"+name); return nil }}
}

type observingLocker struct {
	locked  bool
	unlocks int
}

func (l *observingLocker) ProjectLock(_ context.Context, _, _ string, _ time.Duration) (lock.Handle, error) {
	if l.locked {
		return nil, errors.New("already locked")
	}
	l.locked = true
	return observingHandle{locker: l}, nil
}

type observingHandle struct{ locker *observingLocker }

func (h observingHandle) Unlock() error {
	h.locker.locked = false
	h.locker.unlocks++
	return nil
}
