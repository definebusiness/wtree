package service

import (
	"context"
	"errors"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HookFailureKind string

const (
	HookFailureNonZero    HookFailureKind = "non-zero-exit"
	HookFailureMissing    HookFailureKind = "missing-executable"
	HookFailureLaunch     HookFailureKind = "launch"
	HookFailureTimeout    HookFailureKind = "timeout"
	HookFailureCanceled   HookFailureKind = "canceled"
	HookFailureOutput     HookFailureKind = "output-writer"
	HookFailureGeneration HookFailureKind = "generation-changed"
	HookFailureRecord     HookFailureKind = "record"
	HookFailureLock       HookFailureKind = "lock"
)

type HookRunFailure struct {
	Kind                 HookFailureKind
	HookID, RepositoryID string
	ExitCode             *int
	Timeout              bool
}
type HookRunResult struct {
	Status       string
	CompletedIDs []string
	Failure      *HookRunFailure
}
type HookGenerationSnapshot struct{ SourceBytes, WorkspaceStateBytes []byte }
type HookGenerationVerifier func(context.Context) (HookGenerationSnapshot, error)
type HookRunRequest struct {
	DataDir              string
	Plan                 HookPlan
	InheritedEnvironment []string
	Windows              bool
	Sink                 io.Writer
	Revalidate           HookGenerationVerifier
	// NoMutationOnGenerationFailure is used only by retry. An existing durable
	// record is evidence for a prior incomplete run, not permission to rewrite
	// it when current authority no longer matches under the event lock.
	NoMutationOnGenerationFailure bool
}

// HookResumePreparer rebuilds the immutable authority for one existing run
// while its event lock is held. It receives a defensive record copy.
type HookResumePreparer func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error)
type HookResumeRequest struct {
	DataDir, ProjectID, WorkspaceID, Event string
	Prepare                                HookResumePreparer
	Environment                            []string
	Windows                                bool
	Sink                                   io.Writer
}
type hookRunLocker interface {
	HookRunLock(context.Context, string, string, string, string, time.Duration) (lock.Handle, error)
}
type HookRunnerDependencies struct {
	Locker  hookRunLocker
	Process HookProcessAdapter
	Clock   func() time.Time
	Read    func(string) (store.HookRunRecord, error)
	Write   func(string, store.HookRunRecord) error
	Remove  func(string) error
}
type HookRunner struct{ d HookRunnerDependencies }

func NewHookRunner() *HookRunner { return NewHookRunnerWith(HookRunnerDependencies{}) }
func NewHookRunnerWith(d HookRunnerDependencies) *HookRunner {
	if d.Locker == nil {
		d.Locker = lock.Manager{}
	}
	if d.Process == nil {
		d.Process = newHookProcessAdapter()
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Read == nil {
		d.Read = store.ReadHookRunRecord
	}
	if d.Write == nil {
		d.Write = store.WriteHookRunRecord
	}
	if d.Remove == nil {
		d.Remove = store.RemoveHookRunRecord
	}
	return &HookRunner{d}
}
func (r *HookRunner) Run(ctx context.Context, q HookRunRequest) (HookRunResult, error) {
	result := HookRunResult{Status: "incomplete", CompletedIDs: []string{}}
	if q.Revalidate == nil || q.Plan.Version != HookPlanVersion {
		return result, errors.New("invalid hook run request")
	}
	a := q.Plan.authority
	path, err := store.HookRunRecordPath(q.DataDir, a.projectID, a.workspaceID, a.event)
	if err != nil {
		return result, err
	}
	lockContext := ctx
	if q.Plan.Operation == "clone" && a.source == "portable" {
		// Core clone publication is already committed. A canceled caller must
		// still be able to acquire the short record lock and leave recoverable
		// authority behind; the original context remains authoritative for every
		// verifier and process operation below.
		lockContext = context.WithoutCancel(ctx)
	}
	h, err := r.d.Locker.HookRunLock(lockContext, q.DataDir, a.projectID, a.workspaceID, a.event, time.Second)
	if err != nil {
		result.Failure = &HookRunFailure{Kind: HookFailureLock}
		return result, nil
	}
	defer h.Unlock()
	record, err := r.d.Read(path)
	if os.IsNotExist(err) {
		if q.Plan.Operation != "clone" || a.source != "portable" {
			// Local create keeps its established pre-publication behavior: an
			// obsolete initial authority must not create a new record.
			failure, generationErr := q.generationFailure(ctx)
			if generationErr != nil {
				if q.NoMutationOnGenerationFailure {
					return result, generationErr
				}
				failure = hookGenerationFailureFromError(ctx, generationErr)
			}
			if failure != nil {
				result.Failure = failure
				return result, nil
			}
		}
		// Post-publication clone authority is itself a recoverable setup step.
		// Publish the immutable record before revalidation so an interrupted
		// state/Git/process observation leaves a truthful retry path rather than
		// an ordinary error after core publication.
		now := r.d.Clock().UTC()
		record = store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: a.projectID, WorkspaceID: a.workspaceID, WorkspaceName: a.workspaceName, Operation: q.Plan.Operation, Event: a.event, Source: a.source, SourceSHA256: q.Plan.SourceSHA256(), PlanSHA256: q.Plan.Digest(), WorkspaceStateSHA256: q.Plan.WorkspaceStateSHA256(), HookIDs: hookPlanIDs(a.entries), CompletedHookIDs: []string{}, State: "running", CreatedAt: now, UpdatedAt: now}
		if err = r.d.Write(path, record); err != nil {
			result.Failure = &HookRunFailure{Kind: HookFailureRecord}
			return result, nil
		}
	} else if err != nil {
		result.Failure = &HookRunFailure{Kind: HookFailureRecord}
		return result, nil
	} else if !matchesHookRunRecord(record, q.Plan) {
		result.Failure = &HookRunFailure{Kind: HookFailureGeneration}
		return result, nil
	}
	result.CompletedIDs = append(result.CompletedIDs, record.CompletedHookIDs...)
	if record.State == "finalizing" {
		failure, generationErr := q.generationFailure(ctx)
		if generationErr != nil {
			if q.NoMutationOnGenerationFailure {
				return result, generationErr
			}
			failure = hookGenerationFailureFromError(ctx, generationErr)
		}
		if failure != nil {
			result.Failure = failure
			return result, nil
		}
		if err := r.d.Remove(path); err != nil {
			// A post-delete directory-sync error means the record may have been
			// removed but its removal is not durable. Re-publish finalizing so a
			// later runner can only perform cleanup, never restart hooks.
			if restoreErr := r.d.Write(path, record); restoreErr != nil {
				result.Failure = &HookRunFailure{Kind: HookFailureRecord}
				return result, nil
			}
			result.Failure = &HookRunFailure{Kind: HookFailureRecord}
			return result, nil
		}
		return HookRunResult{Status: "completed", CompletedIDs: append([]string(nil), record.CompletedHookIDs...)}, nil
	}
	if q.NoMutationOnGenerationFailure {
		failure, generationErr := q.generationFailure(ctx)
		if generationErr != nil {
			return result, generationErr
		}
		if failure != nil {
			if record.NextIndex < len(a.entries) {
				failure.HookID, failure.RepositoryID = a.entries[record.NextIndex].ID, a.entries[record.NextIndex].Repository
			}
			result.Failure = failure
			return result, nil
		}
	}
	if record.State == "failed" {
		record.State, record.Failure, record.UpdatedAt = "running", nil, r.d.Clock().UTC()
		if err := r.d.Write(path, record); err != nil {
			result.Failure = &HookRunFailure{Kind: HookFailureRecord}
			return result, nil
		}
	}
	for i := record.NextIndex; i < len(a.entries); i++ {
		failure, generationErr := q.generationFailure(ctx)
		if generationErr != nil {
			if q.NoMutationOnGenerationFailure {
				return result, generationErr
			}
			failure = hookGenerationFailureFromError(ctx, generationErr)
		}
		if failure != nil {
			failure.HookID, failure.RepositoryID = a.entries[i].ID, a.entries[i].Repository
			if q.NoMutationOnGenerationFailure {
				result.Failure = failure
				return result, nil
			}
			failed, failErr := r.failRecord(path, record, result, failure)
			if failErr != nil {
				return failed, failErr
			}
			if hookContextSentinel(generationErr) {
				return failed, generationErr
			}
			return failed, nil
		}
		entry := a.entries[i]
		env, e := buildHookEnvironment(HookEnvironmentPolicy(a.source), q.Windows, q.InheritedEnvironment, q.Plan, i)
		if e != nil {
			if q.NoMutationOnGenerationFailure {
				result.Failure = entryFailure(a.entries[i], HookFailureGeneration, nil, false)
				return result, nil
			}
			return r.failRecord(path, record, result, entryFailure(a.entries[i], HookFailureGeneration, nil, false))
		}
		resolveDirectory := entry.TargetRepository
		if a.source == "local" && !filepath.IsAbs(entry.ConfiguredExecutable) && strings.ContainsAny(entry.ConfiguredExecutable, "/\\") {
			resolveDirectory = entry.SourceRepository
		}
		fact, e := r.d.Process.Resolve(ctx, HookExecutableRequest{Program: entry.ConfiguredExecutable, Directory: resolveDirectory, Environment: env})
		if e != nil || !fact.Available {
			if q.NoMutationOnGenerationFailure && hookContextSentinel(e) {
				return result, e
			}
			kind := HookFailureLaunch
			if e == nil {
				kind = HookFailureMissing
			}
			return r.failRecord(path, record, result, entryFailure(a.entries[i], kind, nil, false))
		}
		if a.source == "local" || (a.source == "portable" && !filepath.IsAbs(entry.ConfiguredExecutable) && strings.ContainsAny(entry.ConfiguredExecutable, "/\\")) {
			if trusted, trustedErr := trustedLocalSourceExecutable(entry.SourceRepository, entry.ConfiguredExecutable, fact.Resolved); trustedErr != nil {
				if q.NoMutationOnGenerationFailure {
					result.Failure = entryFailure(entry, HookFailureGeneration, nil, false)
					return result, nil
				}
				return r.failRecord(path, record, result, entryFailure(entry, HookFailureGeneration, nil, false))
			} else {
				fact.Resolved = trusted
			}
		}
		program := fact.Resolved
		if entry.Availability == "available" {
			if filepath.Clean(fact.Resolved) != entry.ResolvedExecutable {
				if q.NoMutationOnGenerationFailure {
					result.Failure = entryFailure(entry, HookFailureGeneration, nil, false)
					return result, nil
				}
				return r.failRecord(path, record, result, entryFailure(entry, HookFailureGeneration, nil, false))
			}
			program = entry.ResolvedExecutable
		}
		run, e := r.d.Process.Run(ctx, HookProcessRequest{Program: program, Arguments: entry.Arguments, Directory: entry.TargetRepository, Environment: env, Timeout: entry.Timeout, Event: a.event, HookID: entry.ID, Sink: q.Sink})
		if e != nil || run.ExitCode != 0 {
			kind, timeout := HookFailureLaunch, false
			if e == nil {
				kind = HookFailureNonZero
			} else if errors.Is(e, errHookProcessOutputWriter) {
				kind = HookFailureOutput
			} else if errors.Is(e, context.DeadlineExceeded) {
				kind, timeout = HookFailureTimeout, true
			} else if errors.Is(e, context.Canceled) {
				kind = HookFailureCanceled
			}
			var exit *int
			if kind == HookFailureNonZero {
				value := run.ExitCode
				exit = &value
			}
			return r.failRecord(path, record, result, entryFailure(entry, kind, exit, timeout))
		}
		record.CompletedHookIDs = append(record.CompletedHookIDs, entry.ID)
		record.NextIndex++
		record.UpdatedAt = r.d.Clock().UTC()
		// A running record may not claim every hook completed. The final success
		// is published by the finalizing record below, which is the durable
		// authority for cleanup-only retries.
		if record.NextIndex < len(a.entries) {
			if err := r.d.Write(path, record); err != nil {
				result.Failure = &HookRunFailure{Kind: HookFailureRecord}
				return result, nil
			}
		}
		result.CompletedIDs = append(result.CompletedIDs, entry.ID)
	}
	record.State = "finalizing"
	record.UpdatedAt = r.d.Clock().UTC()
	if err := r.d.Write(path, record); err != nil {
		result.Failure = &HookRunFailure{Kind: HookFailureRecord}
		return result, nil
	}
	if err := r.d.Remove(path); err != nil {
		if restoreErr := r.d.Write(path, record); restoreErr != nil {
			result.Failure = &HookRunFailure{Kind: HookFailureRecord}
			return result, nil
		}
		result.Failure = &HookRunFailure{Kind: HookFailureRecord}
		return result, nil
	}
	result.Status = "completed"
	return result, nil
}

// Resume never creates a record. It holds the authoritative event lock while
// rebuilding the plan, then delegates to the same record/process loop used by
// an initial run through a no-op child lock because ownership is already held.
func (r *HookRunner) Resume(ctx context.Context, q HookResumeRequest) (HookRunResult, error) {
	result := HookRunResult{Status: "incomplete", CompletedIDs: []string{}}
	if q.Prepare == nil {
		return result, errors.New("invalid hook resume request")
	}
	path, err := store.HookRunRecordPath(q.DataDir, q.ProjectID, q.WorkspaceID, q.Event)
	if err != nil {
		return result, err
	}
	h, err := r.d.Locker.HookRunLock(ctx, q.DataDir, q.ProjectID, q.WorkspaceID, q.Event, time.Second)
	if err != nil {
		result.Failure = &HookRunFailure{Kind: HookFailureLock}
		return result, nil
	}
	defer h.Unlock()
	record, err := r.d.Read(path)
	if err != nil {
		result.Failure = &HookRunFailure{Kind: HookFailureRecord}
		return result, nil
	}
	// Keep the same defensive, schema-valid clone both for the preparer and
	// the child runner. In particular, an empty completed prefix is a required
	// empty array on disk, not a nil slice.
	record = cloneHookResumeRecord(record)
	plan, verifier, err := q.Prepare(ctx, record)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, err
		}
		result.CompletedIDs = append(result.CompletedIDs, record.CompletedHookIDs...)
		result.Failure = &HookRunFailure{Kind: HookFailureGeneration}
		return result, nil
	}
	if verifier == nil {
		return result, errors.New("hook resume verifier is not configured")
	}
	if plan.Version != HookPlanVersion || !matchesHookRunRecord(record, plan) {
		result.CompletedIDs = append(result.CompletedIDs, record.CompletedHookIDs...)
		result.Failure = &HookRunFailure{Kind: HookFailureGeneration}
		return result, nil
	}
	child := *r
	child.d.Locker = hookResumeNoopLocker{}
	child.d.Read = func(string) (store.HookRunRecord, error) { return record, nil }
	return child.Run(ctx, HookRunRequest{DataDir: q.DataDir, Plan: plan, InheritedEnvironment: append([]string(nil), q.Environment...), Windows: q.Windows, Sink: q.Sink, Revalidate: verifier, NoMutationOnGenerationFailure: true})
}

type hookResumeNoopLocker struct{}
type hookResumeNoopHandle struct{}

func (hookResumeNoopLocker) HookRunLock(context.Context, string, string, string, string, time.Duration) (lock.Handle, error) {
	return hookResumeNoopHandle{}, nil
}
func (hookResumeNoopHandle) Unlock() error { return nil }

func cloneHookResumeRecord(value store.HookRunRecord) store.HookRunRecord {
	value.HookIDs = append([]string{}, value.HookIDs...)
	// The strict record schema distinguishes an empty completed prefix from
	// null. Preserve the non-nil empty slice when a failed portable run is
	// retried before its first hook; otherwise Resume cannot durably transition
	// the record back to running.
	value.CompletedHookIDs = append([]string{}, value.CompletedHookIDs...)
	if value.Failure != nil {
		failure := *value.Failure
		if failure.ExitCode != nil {
			code := *failure.ExitCode
			failure.ExitCode = &code
		}
		value.Failure = &failure
	}
	return value
}

func matchesHookRunRecord(record store.HookRunRecord, plan HookPlan) bool {
	a := plan.authority
	return record.ProjectID == a.projectID &&
		record.WorkspaceID == a.workspaceID &&
		record.WorkspaceName == a.workspaceName &&
		record.Operation == plan.Operation &&
		record.Event == a.event &&
		record.Source == a.source &&
		record.SourceSHA256 == plan.SourceSHA256() &&
		record.PlanSHA256 == plan.Digest() &&
		record.WorkspaceStateSHA256 == plan.WorkspaceStateSHA256() &&
		strings.Join(record.HookIDs, "\x00") == strings.Join(hookPlanIDs(a.entries), "\x00")
}

func (q HookRunRequest) generationFailure(ctx context.Context) (*HookRunFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err := q.Revalidate(ctx)
	if err != nil {
		if hookContextSentinel(err) {
			return nil, err
		}
		return &HookRunFailure{Kind: HookFailureGeneration}, nil
	}
	if digest(snapshot.SourceBytes) != q.Plan.SourceSHA256() || digest(snapshot.WorkspaceStateBytes) != q.Plan.WorkspaceStateSHA256() {
		return &HookRunFailure{Kind: HookFailureGeneration}, nil
	}
	return nil, nil
}

func hookContextSentinel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// A non-retry runner keeps its historical durable failure classification. A
// retry's existing record must instead remain untouched and return the caller
// cancellation directly.
func hookGenerationFailureFromError(ctx context.Context, err error) *HookRunFailure {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &HookRunFailure{Kind: HookFailureTimeout, Timeout: true}
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return &HookRunFailure{Kind: HookFailureCanceled}
	}
	return &HookRunFailure{Kind: HookFailureGeneration}
}
func entryFailure(entry hookPlanInputEntry, kind HookFailureKind, exit *int, timeout bool) *HookRunFailure {
	return &HookRunFailure{Kind: kind, HookID: entry.ID, RepositoryID: entry.Repository, ExitCode: exit, Timeout: timeout}
}
func (r *HookRunner) failRecord(path string, record store.HookRunRecord, result HookRunResult, failure *HookRunFailure) (HookRunResult, error) {
	record.State = "failed"
	record.Failure = &store.HookRunFailure{Kind: string(failure.Kind), HookID: failure.HookID, RepositoryID: failure.RepositoryID, ExitCode: failure.ExitCode, Timeout: failure.Timeout}
	record.UpdatedAt = r.d.Clock().UTC()
	result.Failure = failure
	if err := r.d.Write(path, record); err != nil {
		result.Failure = &HookRunFailure{Kind: HookFailureRecord}
	}
	return result, nil
}
func hookPlanIDs(v []hookPlanInputEntry) []string {
	o := make([]string, len(v))
	for i := range v {
		o[i] = v[i].ID
	}
	return o
}
