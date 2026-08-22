package transaction

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRunnerRollsBackCompletedStepsInReverseOrder(t *testing.T) {
	var calls []string
	runner := Runner{Progress: func(event Event) { calls = append(calls, string(event.Kind)+":"+event.Step) }}

	result := runner.Run(context.Background(), []Step{
		{Name: "one", Execute: func(context.Context) error { calls = append(calls, "execute:one"); return nil }, Rollback: func(context.Context) error { calls = append(calls, "rollback:one"); return nil }},
		{Name: "two", Execute: func(context.Context) error { calls = append(calls, "execute:two"); return nil }, Rollback: func(context.Context) error { calls = append(calls, "rollback:two"); return nil }},
		{Name: "three", Execute: func(context.Context) error { return errors.New("injected failure") }, Rollback: func(context.Context) error { t.Fatal("failed step must not be rolled back"); return nil }},
	})

	if result.Failure == nil || result.RollbackFailure != nil {
		t.Fatalf("Run result = %#v, want execution failure and successful rollback", result)
	}
	if got, want := result.Completed, []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completed = %v, want %v", got, want)
	}
	if got, want := calls, []string{
		"execute_started:one", "execute:one", "execute_succeeded:one",
		"execute_started:two", "execute:two", "execute_succeeded:two",
		"execute_started:three", "execute_failed:three",
		"rollback_started:two", "rollback:two", "rollback_succeeded:two",
		"rollback_started:one", "rollback:one", "rollback_succeeded:one",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestRunnerRollsBackFailedExecutePartialEffectBeforeCompletedSteps(t *testing.T) {
	var calls []string
	failure := errors.New("execute failure")
	result := (Runner{Progress: func(event Event) { calls = append(calls, string(event.Kind)+":"+event.Step) }}).Run(context.Background(), []Step{
		{Name: "one", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { calls = append(calls, "undo:one"); return nil }},
		{Name: "two", Execute: func(context.Context) error { return failure }, RollbackFailedExecute: func(context.Context) error { calls = append(calls, "undo:two"); return nil }},
	})
	if result.FailedStep != "two" || !errors.Is(result.Failure, failure) || !result.FailedExecuteRolledBack || result.RollbackFailure != nil || len(result.UnrevertedSteps) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if got, want := calls, []string{"execute_started:one", "execute_succeeded:one", "execute_started:two", "execute_failed:two", "rollback_started:two", "undo:two", "rollback_succeeded:two", "rollback_started:one", "undo:one", "rollback_succeeded:one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestRunnerRecordsFailedExecuteCleanupFailure(t *testing.T) {
	failure, cleanupFailure, priorFailure := errors.New("execute"), errors.New("cleanup"), errors.New("prior")
	result := (Runner{}).Run(context.Background(), []Step{
		{Name: "one", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { return priorFailure }},
		{Name: "two", Execute: func(context.Context) error { return failure }, RollbackFailedExecute: func(context.Context) error { return cleanupFailure }},
	})
	if result.FailedStep != "two" || !errors.Is(result.Failure, failure) || result.FailedExecuteRolledBack || !errors.Is(result.RollbackFailure, cleanupFailure) || !errors.Is(result.RollbackFailure, priorFailure) {
		t.Fatalf("result = %#v", result)
	}
	if got, want := result.UnrevertedSteps, []string{"two", "one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unreverted = %v", got)
	}
	if got, want := result.RollbackFailures, []RollbackIssue{{Step: "two", Error: "cleanup"}, {Step: "one", Error: "prior"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures = %#v", got)
	}
}

func TestRunnerNilFailedExecuteCleanupPreservesLegacyResult(t *testing.T) {
	result := (Runner{}).Run(context.Background(), []Step{{Name: "failed", Execute: func(context.Context) error { return errors.New("failure") }}})
	if result.RollbackFailure != nil || result.FailedExecuteRolledBack || len(result.UnrevertedSteps) != 0 || result.FailedStep != "failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerInjectedFailureAtEveryStepBoundary(t *testing.T) {
	for failAt := 0; failAt < 3; failAt++ {
		t.Run(string(rune('1'+failAt)), func(t *testing.T) {
			var executed, rolledBack []string
			var events []EventKind
			steps := make([]Step, 0, 3)
			for _, name := range []string{"one", "two", "three"} {
				name := name
				steps = append(steps, Step{
					Name:     name,
					Execute:  func(context.Context) error { executed = append(executed, name); return nil },
					Rollback: func(context.Context) error { rolledBack = append(rolledBack, name); return nil },
				})
			}
			result := (Runner{Progress: func(event Event) { events = append(events, event.Kind) }, BeforeExecute: func(index int, _ Step) error {
				if index == failAt {
					return errors.New("injected boundary failure")
				}
				return nil
			}}).Run(context.Background(), steps)
			if result.Failure == nil || result.RollbackFailure != nil {
				t.Fatalf("result = %#v, want injected failure with complete rollback", result)
			}
			if got, want := executed, []string{"one", "two", "three"}[:failAt]; len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
				t.Fatalf("executed = %v, want %v", got, want)
			}
			wantRollback := make([]string, failAt)
			for index := 0; index < failAt; index++ {
				wantRollback[index] = []string{"one", "two", "three"}[failAt-1-index]
			}
			if len(rolledBack) != len(wantRollback) || (len(wantRollback) > 0 && !reflect.DeepEqual(rolledBack, wantRollback)) {
				t.Fatalf("rolled back = %v, want %v", rolledBack, wantRollback)
			}
			failedEvent := false
			for _, event := range events {
				failedEvent = failedEvent || event == ExecuteFailed
			}
			if !failedEvent {
				t.Fatalf("events = %v, want injected failure event before rollback", events)
			}
		})
	}
}

func TestRunnerReportsRollbackFailure(t *testing.T) {
	result := (Runner{}).Run(context.Background(), []Step{
		{Name: "one", Execute: func(context.Context) error { return nil }, Rollback: func(context.Context) error { return errors.New("undo failed") }},
		{Name: "two", Execute: func(context.Context) error { return errors.New("execute failed") }},
	})
	if result.RollbackFailure == nil || result.RollbackComplete() {
		t.Fatalf("result = %#v, want incomplete rollback", result)
	}
}
