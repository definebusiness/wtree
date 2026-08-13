// Package transaction executes reversible mutation steps without knowing their
// infrastructure details. Callers supply the effects and retain ownership of
// planning, locking, and durable state.
package transaction

import (
	"context"
	"errors"
	"fmt"
)

// EventKind describes one durable-boundary-free execution event.
type EventKind string

const (
	ExecuteStarted    EventKind = "execute_started"
	ExecuteSucceeded  EventKind = "execute_succeeded"
	ExecuteFailed     EventKind = "execute_failed"
	RollbackStarted   EventKind = "rollback_started"
	RollbackSucceeded EventKind = "rollback_succeeded"
	RollbackFailed    EventKind = "rollback_failed"
)

// Event is emitted synchronously as a step changes state. Renderers may use
// it for progress without coupling transaction execution to an output format.
type Event struct {
	Kind EventKind
	Step string
	Err  error
}

// Step is one mutation and its explicit inverse. A nil rollback deliberately
// models a non-reversible step and makes a failed transaction recoverable.
type Step struct {
	Name     string
	Execute  func(context.Context) error
	Rollback func(context.Context) error
}

// Result reports the original failure and any failure while undoing completed
// steps. Completed is execution order, suitable for a recovery record.
type Result struct {
	Completed        []string
	FailedStep       string
	Failure          error
	RollbackFailure  error
	UnrevertedSteps  []string
	RollbackFailures []RollbackIssue
}

// RollbackIssue identifies an effect that could not be undone and preserves
// the actionable adapter error without exposing an error interface in records.
type RollbackIssue struct {
	Step  string
	Error string
}

// Succeeded reports whether every step completed.
func (r Result) Succeeded() bool { return r.Failure == nil }

// RollbackComplete reports whether a failed execution left no completed
// reversible step behind.
func (r Result) RollbackComplete() bool { return r.Failure != nil && r.RollbackFailure == nil }

// Runner executes at safe step boundaries. A canceled context is observed
// before the next execution starts; completed effects are then rolled back
// with cancellation removed so cleanup itself is not abandoned.
type Runner struct {
	Progress      func(Event)
	BeforeExecute func(index int, step Step) error
}

// Rollback undoes completed steps after a caller-owned boundary (such as
// result validation or state commit) fails.
func (r Runner) Rollback(ctx context.Context, completed []Step, failedStep string, failure error) Result {
	result := Result{FailedStep: failedStep, Failure: failure, Completed: make([]string, 0, len(completed))}
	for _, step := range completed {
		result.Completed = append(result.Completed, step.Name)
	}
	return r.rollback(ctx, completed, result)
}

// Run executes steps in order and, on any failure (including cancellation or
// an injected BeforeExecute failure), rolls completed steps back in reverse.
func (r Runner) Run(ctx context.Context, steps []Step) Result {
	completed := make([]Step, 0, len(steps))
	result := Result{}
	for index, step := range steps {
		if err := validateStep(step); err != nil {
			result.FailedStep, result.Failure = step.Name, err
			return r.rollback(ctx, completed, result)
		}
		if err := ctx.Err(); err != nil {
			result.FailedStep, result.Failure = step.Name, err
			return r.rollback(ctx, completed, result)
		}
		if r.BeforeExecute != nil {
			if err := r.BeforeExecute(index, step); err != nil {
				r.emit(Event{Kind: ExecuteFailed, Step: step.Name, Err: err})
				result.FailedStep, result.Failure = step.Name, err
				return r.rollback(ctx, completed, result)
			}
		}
		r.emit(Event{Kind: ExecuteStarted, Step: step.Name})
		if err := step.Execute(ctx); err != nil {
			r.emit(Event{Kind: ExecuteFailed, Step: step.Name, Err: err})
			result.FailedStep, result.Failure = step.Name, err
			return r.rollback(ctx, completed, result)
		}
		r.emit(Event{Kind: ExecuteSucceeded, Step: step.Name})
		completed = append(completed, step)
		result.Completed = append(result.Completed, step.Name)
	}
	return result
}

func validateStep(step Step) error {
	if step.Name == "" {
		return errors.New("transaction step name is required")
	}
	if step.Execute == nil {
		return fmt.Errorf("transaction step %q has no execute action", step.Name)
	}
	return nil
}

func (r Runner) rollback(ctx context.Context, completed []Step, result Result) Result {
	cleanup := context.WithoutCancel(ctx)
	var rollbackErrors []error
	for index := len(completed) - 1; index >= 0; index-- {
		step := completed[index]
		r.emit(Event{Kind: RollbackStarted, Step: step.Name})
		if step.Rollback == nil {
			err := fmt.Errorf("transaction step %q is not reversible", step.Name)
			r.emit(Event{Kind: RollbackFailed, Step: step.Name, Err: err})
			rollbackErrors = append(rollbackErrors, err)
			result.UnrevertedSteps = append(result.UnrevertedSteps, step.Name)
			result.RollbackFailures = append(result.RollbackFailures, RollbackIssue{Step: step.Name, Error: err.Error()})
			continue
		}
		if err := step.Rollback(cleanup); err != nil {
			r.emit(Event{Kind: RollbackFailed, Step: step.Name, Err: err})
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback %q: %w", step.Name, err))
			result.UnrevertedSteps = append(result.UnrevertedSteps, step.Name)
			result.RollbackFailures = append(result.RollbackFailures, RollbackIssue{Step: step.Name, Error: err.Error()})
			continue
		}
		r.emit(Event{Kind: RollbackSucceeded, Step: step.Name})
	}
	result.RollbackFailure = errors.Join(rollbackErrors...)
	return result
}

func (r Runner) emit(event Event) {
	if r.Progress != nil {
		r.Progress(event)
	}
}
