package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
)

// FetchRequest controls the explicit, non-transactional refresh of each
// checkout's configured remote-tracking ref. It never moves a local branch.
type FetchRequest struct {
	DryRun bool
	// OnComplete is called after each settled repository. A writer error stops
	// later network operations because their effects could no longer be reported.
	OnComplete func(FetchRepositoryResult) error
}

// FetchResult is the command-owned fetch v1 envelope.
type FetchResult struct {
	Version              int                     `json:"version"`
	Operation            string                  `json:"operation"`
	Status               AggregateStatus         `json:"status"`
	DryRun               bool                    `json:"dryRun"`
	ProjectID            string                  `json:"projectId"`
	Workspace            string                  `json:"workspace"`
	Partial              bool                    `json:"partial,omitempty"`
	MissingRepositoryIDs []string                `json:"missingRepositoryIds,omitempty"`
	Repositories         []FetchRepositoryResult `json:"repositories"`
	Failure              *AggregateFailure       `json:"failure,omitempty"`
}

type FetchRepositoryResult struct {
	ID                   string            `json:"id"`
	ParentID             string            `json:"parentId,omitempty"`
	Mount                string            `json:"mount"`
	Path                 string            `json:"path"`
	Branch               string            `json:"branch,omitempty"`
	Head                 string            `json:"head,omitempty"`
	Status               AggregateStatus   `json:"status"`
	Remote               string            `json:"remote,omitempty"`
	RemoteRef            string            `json:"remoteRef,omitempty"`
	PreviousRemoteCommit string            `json:"previousRemoteCommit,omitempty"`
	ActualRemoteCommit   string            `json:"actualRemoteCommit,omitempty"`
	Failure              *AggregateFailure `json:"failure,omitempty"`
}

type FetchService struct{ git gitadapter.Git }

func NewFetchService() *FetchService                       { return NewFetchServiceWith(gitadapter.NewAdapter("git")) }
func NewFetchServiceWith(git gitadapter.Git) *FetchService { return &FetchService{git: git} }

// Fetch makes a single immutable parent-first plan, then revalidates the
// local authority immediately before every remote operation. Earlier selected
// tracking-ref updates intentionally remain visible after a later failure.
func (s *FetchService) Fetch(ctx context.Context, project domain.Project, workspace domain.Workspace, request FetchRequest) (FetchResult, error) {
	if s == nil || s.git == nil {
		return FetchResult{}, NewError(ErrorInternal, errors.New("fetch service is not configured"))
	}
	if err := project.Validate(); err != nil {
		return FetchResult{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	if err := workspace.Validate(project); err != nil {
		return FetchResult{}, NewError(ErrorValidation, fmt.Errorf("validate workspace: %w", err))
	}
	result, indexes := newFetchResult(project, workspace, request.DryRun)
	if err := ctx.Err(); err != nil {
		return cancelUnstartedFetch(result, err), err
	}
	plan, err := s.fetchPlan(ctx, project, workspace)
	if err != nil {
		if fetchObservationContextError(ctx, err) != nil {
			return cancelUnstartedFetch(result, err), err
		}
		return failFetchResult(result, nil, err), err
	}
	// Planning is complete before this point. Settle every entry exactly once at
	// its ParentFirst position, including failures already found by preflight.
	for position, planned := range plan {
		if planned.err != nil {
			index := indexes[planned.repository.ID]
			entry := result.Repositories[index]
			entry.Status, entry.Failure = AggregateStatusFailed, fetchFailure(planned.err)
			result.Repositories[index] = entry
			if callbackErr := notifyFetch(request.OnComplete, entry); callbackErr != nil {
				return stopFetchForOutput(result, indexes, fetchPlanItems(plan[position+1:]), callbackErr), callbackErr
			}
			continue
		}
		item := planned.item
		if err := ctx.Err(); err != nil {
			return cancelFetch(result, indexes, fetchPlanItems(plan[position:]), err, request.OnComplete)
		}
		// Revalidation makes the plan safe to use after another process changed
		// a checkout while an earlier repository was being fetched.
		current, checkErr := s.fetchRepository(ctx, item.repository, item.checkout)
		index := indexes[item.repository.ID]
		entry := result.Repositories[index]
		if checkErr == nil && !sameFetchAuthority(item, current) {
			checkErr = NewError(ErrorValidation, fmt.Errorf("fetch preflight %q: checkout authority changed after planning", item.repository.ID))
		}
		if checkErr != nil {
			if contextErr := fetchObservationContextError(ctx, checkErr); contextErr != nil {
				return cancelFetch(result, indexes, fetchPlanItems(plan[position:]), contextErr, request.OnComplete)
			}
			entry.Status, entry.Failure = AggregateStatusFailed, fetchFailure(checkErr)
			result.Repositories[index] = entry
			if callbackErr := notifyFetch(request.OnComplete, entry); callbackErr != nil {
				return stopFetchForOutput(result, indexes, fetchPlanItems(plan[position+1:]), callbackErr), callbackErr
			}
			continue
		}
		entry.Path, entry.Branch, entry.Head, entry.Remote, entry.RemoteRef = current.path, current.branch, current.head, current.upstream.Remote, current.upstream.Merge
		if request.DryRun {
			observation, observeErr := s.git.ObserveConfiguredRef(ctx, current.path, current.upstream.Remote, current.upstream.Merge)
			if contextErr := fetchObservationContextError(ctx, observeErr); contextErr != nil {
				return cancelFetch(result, indexes, fetchPlanItems(plan[position:]), contextErr, request.OnComplete)
			}
			if observeErr != nil {
				entry.Status, entry.Failure = AggregateStatusFailed, fetchFailure(observeErr)
			} else {
				entry.Status, entry.ActualRemoteCommit = AggregateStatusCompleted, observation.Commit
			}
		} else {
			receipt, fetchErr := s.git.FetchConfiguredRef(ctx, current.path, current.upstream.Remote, current.upstream.Merge)
			entry.PreviousRemoteCommit, entry.ActualRemoteCommit = receipt.PreviousRemoteCommit, receipt.ActualRemoteCommit
			if contextErr := fetchObservationContextError(ctx, fetchErr); contextErr != nil {
				entry.Status, entry.Failure = AggregateStatusCanceled, fetchFailure(contextErr)
				result.Repositories[index] = entry
				if callbackErr := notifyFetch(request.OnComplete, entry); callbackErr != nil {
					return stopFetchForOutput(result, indexes, fetchPlanItems(plan[position+1:]), callbackErr), callbackErr
				}
				return cancelFetch(result, indexes, fetchPlanItems(plan[position+1:]), contextErr, request.OnComplete)
			}
			if fetchErr != nil {
				entry.Status, entry.Failure = AggregateStatusFailed, fetchFailure(fetchErr)
			} else {
				entry.Status = AggregateStatusCompleted
			}
		}
		result.Repositories[index] = entry
		if callbackErr := notifyFetch(request.OnComplete, entry); callbackErr != nil {
			return stopFetchForOutput(result, indexes, fetchPlanItems(plan[position+1:]), callbackErr), callbackErr
		}
	}
	for _, entry := range result.Repositories {
		if entry.Status == AggregateStatusFailed {
			failure := *entry.Failure
			result.Status, result.Failure = AggregateStatusFailed, &failure
			return result, NewError(failure.Code, errors.New(failure.Message))
		}
	}
	result.Status = AggregateStatusCompleted
	return result, nil
}

func sameFetchAuthority(planned, current fetchRepository) bool {
	return planned.path == current.path && planned.branch == current.branch && planned.head == current.head && planned.upstream == current.upstream
}

type fetchRepository struct {
	repository         domain.Repository
	checkout           domain.Checkout
	path, branch, head string
	upstream           gitadapter.Upstream
}
type fetchPlanEntry struct {
	repository domain.Repository
	item       fetchRepository
	err        error
}
type fetchPreflightError struct {
	id    string
	cause error
}

func (e *fetchPreflightError) Error() string { return e.cause.Error() }
func (e *fetchPreflightError) Unwrap() error { return e.cause }

func newFetchResult(project domain.Project, workspace domain.Workspace, dryRun bool) (FetchResult, map[string]int) {
	result := FetchResult{Version: 1, Operation: "fetch", Status: AggregateStatusPlanned, DryRun: dryRun, ProjectID: project.ID, Workspace: workspace.Name, Partial: workspace.Partial, MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...)}
	checkouts := make(map[string]domain.Checkout, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	indexes := map[string]int{}
	for _, repository := range project.ParentFirst() {
		if checkout, ok := checkouts[repository.ID]; ok {
			indexes[repository.ID] = len(result.Repositories)
			result.Repositories = append(result.Repositories, FetchRepositoryResult{ID: repository.ID, ParentID: repository.ParentID, Mount: checkout.Mount, Path: checkout.ResolvedPath, Branch: checkout.Branch, Head: checkout.Head, Status: AggregateStatusPlanned})
		}
	}
	return result, indexes
}

func (s *FetchService) fetchPlan(ctx context.Context, project domain.Project, workspace domain.Workspace) ([]fetchPlanEntry, error) {
	checkouts := map[string]domain.Checkout{}
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	plan := make([]fetchPlanEntry, 0, len(checkouts))
	for _, repository := range project.ParentFirst() {
		checkout, ok := checkouts[repository.ID]
		if !ok {
			continue
		}
		item, err := s.fetchRepository(ctx, repository, checkout)
		if fetchObservationContextError(ctx, err) != nil {
			return plan, err
		}
		plan = append(plan, fetchPlanEntry{repository: repository, item: item, err: err})
	}
	return plan, nil
}
func fetchPlanItems(entries []fetchPlanEntry) []fetchRepository {
	result := make([]fetchRepository, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.item)
		if entry.item.repository.ID == "" {
			result[len(result)-1].repository = entry.repository
		}
	}
	return result
}

func (s *FetchService) fetchRepository(ctx context.Context, repository domain.Repository, checkout domain.Checkout) (fetchRepository, error) {
	path, err := canonicalExecDirectory(checkout.ResolvedPath)
	if err != nil {
		return fetchRepository{}, NewError(ErrorValidation, fmt.Errorf("fetch preflight %q: checkout path: %w", repository.ID, err))
	}
	common, err := s.git.CommonGitDir(ctx, path)
	if contextErr := fetchObservationContextError(ctx, err); contextErr != nil {
		return fetchRepository{}, contextErr
	}
	if err != nil {
		return fetchRepository{}, NewError(ErrorGit, fmt.Errorf("fetch preflight %q: read Git identity: %w", repository.ID, err))
	}
	if common != repository.CommonGitDir {
		return fetchRepository{}, NewError(ErrorValidation, fmt.Errorf("fetch preflight %q: Git identity does not match persisted repository", repository.ID))
	}
	top, err := s.git.TopLevel(ctx, path)
	if contextErr := fetchObservationContextError(ctx, err); contextErr != nil {
		return fetchRepository{}, contextErr
	}
	if err != nil {
		return fetchRepository{}, NewError(ErrorGit, fmt.Errorf("fetch preflight %q: read checkout root: %w", repository.ID, err))
	}
	if !sameCheckoutPath(top, path) {
		return fetchRepository{}, NewError(ErrorValidation, fmt.Errorf("fetch preflight %q: checkout root does not match persisted path", repository.ID))
	}
	branch, detached, err := s.git.CurrentBranch(ctx, path)
	if contextErr := fetchObservationContextError(ctx, err); contextErr != nil {
		return fetchRepository{}, contextErr
	}
	if err != nil {
		return fetchRepository{}, NewError(ErrorGit, fmt.Errorf("fetch preflight %q: read branch: %w", repository.ID, err))
	}
	if detached != checkout.Detached || (!detached && branch != checkout.Branch) {
		return fetchRepository{}, NewError(ErrorValidation, fmt.Errorf("fetch preflight %q: branch state does not match persisted checkout", repository.ID))
	}
	head, err := s.git.Head(ctx, path)
	if contextErr := fetchObservationContextError(ctx, err); contextErr != nil {
		return fetchRepository{}, contextErr
	}
	if err != nil {
		return fetchRepository{}, NewError(ErrorGit, fmt.Errorf("fetch preflight %q: read HEAD: %w", repository.ID, err))
	}
	if head != checkout.Head {
		return fetchRepository{}, NewError(ErrorValidation, fmt.Errorf("fetch preflight %q: HEAD does not match persisted checkout", repository.ID))
	}
	upstream, err := s.git.Upstream(ctx, path)
	if contextErr := fetchObservationContextError(ctx, err); contextErr != nil {
		return fetchRepository{}, contextErr
	}
	if err != nil {
		return fetchRepository{}, NewError(ErrorGit, fmt.Errorf("fetch preflight %q: read configured upstream: %w", repository.ID, err))
	}
	if detached || upstream.LocalBranch != branch || upstream.Remote == "" || upstream.Merge == "" {
		return fetchRepository{}, NewError(ErrorValidation, fmt.Errorf("fetch preflight %q: configured upstream does not match checkout", repository.ID))
	}
	return fetchRepository{repository: repository, checkout: checkout, path: path, branch: branch, head: head, upstream: upstream}, nil
}

func fetchObservationContextError(ctx context.Context, observation error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(observation, context.Canceled) || errors.Is(observation, context.DeadlineExceeded) {
		return observation
	}
	return nil
}
func fetchFailure(cause error) *AggregateFailure {
	kind := ErrorGit
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = ErrorInternal
	}
	var application *Error
	if errors.As(cause, &application) {
		kind = application.Kind
	}
	failure, err := NewAggregateFailure(kind, cause)
	if err != nil {
		return &AggregateFailure{Code: ErrorInternal, Message: "fetch failure"}
	}
	return &failure
}
func failFetchResult(result FetchResult, index *int, cause error) FetchResult {
	failure := fetchFailure(cause)
	result.Status, result.Failure = AggregateStatusFailed, failure
	if index != nil {
		entry := result.Repositories[*index]
		entry.Status, entry.Failure = AggregateStatusFailed, failure
		result.Repositories[*index] = entry
	}
	return result
}
func cancelFetch(result FetchResult, indexes map[string]int, pending []fetchRepository, cause error, callback func(FetchRepositoryResult) error) (FetchResult, error) {
	failure := fetchFailure(cause)
	result.Status, result.Failure = AggregateStatusFailed, failure
	for _, item := range pending {
		entry := result.Repositories[indexes[item.repository.ID]]
		if entry.Status == AggregateStatusPlanned {
			entry.Status, entry.Failure = AggregateStatusCanceled, failure
			result.Repositories[indexes[item.repository.ID]] = entry
			if err := notifyFetch(callback, entry); err != nil {
				return stopFetchForOutput(result, indexes, pending, err), err
			}
		}
	}
	return result, cause
}

// cancelUnstartedFetch is used before a plan exists. A cancellation is not a
// failed preflight: every persisted present checkout is explicitly reported as
// canceled so callers cannot mistake an omitted row for an unobserved one.
func cancelUnstartedFetch(result FetchResult, cause error) FetchResult {
	failure := fetchFailure(cause)
	result.Status, result.Failure = AggregateStatusFailed, failure
	for index := range result.Repositories {
		entry := result.Repositories[index]
		entry.Status, entry.Failure = AggregateStatusCanceled, failure
		result.Repositories[index] = entry
	}
	return result
}
func stopFetchForOutput(result FetchResult, indexes map[string]int, pending []fetchRepository, cause error) FetchResult {
	failure := fetchFailure(cause)
	result.Status, result.Failure = AggregateStatusFailed, failure
	for _, item := range pending {
		index := indexes[item.repository.ID]
		entry := result.Repositories[index]
		if entry.Status == AggregateStatusPlanned {
			entry.Status, entry.Failure = AggregateStatusCanceled, failure
			result.Repositories[index] = entry
		}
	}
	return result
}
func notifyFetch(callback func(FetchRepositoryResult) error, entry FetchRepositoryResult) error {
	if callback == nil {
		return nil
	}
	copy := entry
	if entry.Failure != nil {
		failure := *entry.Failure
		copy.Failure = &failure
	}
	return callback(copy)
}
