package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
)

// ExecRequest describes a direct argv invocation across a workspace.
type ExecRequest struct {
	Program     string
	Args        []string
	Reverse     bool
	DryRun      bool
	Environment []string
	// OnComplete observes a settled process result. Returning an error stops
	// later invocations; JSON callers leave it nil.
	OnComplete func(ExecRepositoryResult) error
}

type ExecCommand struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

// ExecResult is the fixed command-owned exec v1 result envelope.
type ExecResult struct {
	Version              int                    `json:"version"`
	Operation            string                 `json:"operation"`
	Status               AggregateStatus        `json:"status"`
	DryRun               bool                   `json:"dryRun"`
	ProjectID            string                 `json:"projectId"`
	Workspace            string                 `json:"workspace"`
	Partial              bool                   `json:"partial,omitempty"`
	MissingRepositoryIDs []string               `json:"missingRepositoryIds,omitempty"`
	Command              ExecCommand            `json:"command"`
	ExecutionOrder       []string               `json:"executionOrder"`
	Repositories         []ExecRepositoryResult `json:"repositories"`
	Failure              *AggregateFailure      `json:"failure,omitempty"`
}

type ExecRepositoryResult struct {
	ID              string            `json:"id"`
	ParentID        string            `json:"parentId,omitempty"`
	Mount           string            `json:"mount"`
	Path            string            `json:"path"`
	Branch          string            `json:"branch,omitempty"`
	Head            string            `json:"head,omitempty"`
	Status          AggregateStatus   `json:"status"`
	Stdout          string            `json:"stdout,omitempty"`
	Stderr          string            `json:"stderr,omitempty"`
	StdoutTruncated bool              `json:"stdoutTruncated,omitempty"`
	StderrTruncated bool              `json:"stderrTruncated,omitempty"`
	ExitCode        *int              `json:"exitCode,omitempty"`
	Failure         *AggregateFailure `json:"failure,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	started         bool
}

// MarshalJSON keeps the public Go result convenient while making process
// applicability explicit on the wire. Empty output, false truncation flags,
// and exit status zero are facts for a started process, not absent values.
func (result ExecRepositoryResult) MarshalJSON() ([]byte, error) {
	type wireResult struct {
		ID              string            `json:"id"`
		ParentID        string            `json:"parentId,omitempty"`
		Mount           string            `json:"mount"`
		Path            string            `json:"path"`
		Branch          string            `json:"branch,omitempty"`
		Head            string            `json:"head,omitempty"`
		Status          AggregateStatus   `json:"status"`
		Stdout          *string           `json:"stdout,omitempty"`
		Stderr          *string           `json:"stderr,omitempty"`
		StdoutTruncated *bool             `json:"stdoutTruncated,omitempty"`
		StderrTruncated *bool             `json:"stderrTruncated,omitempty"`
		ExitCode        *int              `json:"exitCode,omitempty"`
		Failure         *AggregateFailure `json:"failure,omitempty"`
		Environment     map[string]string `json:"environment,omitempty"`
	}
	wire := wireResult{
		ID: result.ID, ParentID: result.ParentID, Mount: result.Mount, Path: result.Path,
		Branch: result.Branch, Head: result.Head, Status: result.Status,
		Failure: result.Failure, Environment: result.Environment,
	}
	if result.started {
		wire.Stdout, wire.Stderr = &result.Stdout, &result.Stderr
		wire.StdoutTruncated, wire.StderrTruncated = &result.StdoutTruncated, &result.StderrTruncated
		wire.ExitCode = result.ExitCode
		if wire.ExitCode == nil {
			exitCode := 0
			wire.ExitCode = &exitCode
		}
	}
	return json.Marshal(wire)
}

type ExecService struct{ git gitadapter.Git }

func NewExecService() *ExecService                       { return NewExecServiceWith(gitadapter.NewAdapter("git")) }
func NewExecServiceWith(git gitadapter.Git) *ExecService { return &ExecService{git: git} }

// Exec preflights every present checkout before starting any process. The
// result array is always parent-first; Reverse changes only invocation order.
func (s *ExecService) Exec(ctx context.Context, project domain.Project, workspace domain.Workspace, request ExecRequest) (ExecResult, error) {
	if s == nil || s.git == nil {
		return ExecResult{}, NewError(ErrorInternal, errors.New("exec service is not configured"))
	}
	if request.Program == "" {
		return ExecResult{}, NewError(ErrorInvalidArguments, errors.New("exec requires an executable"))
	}
	request.Args = append([]string{}, request.Args...)
	request.Environment = append([]string(nil), request.Environment...)
	if err := project.Validate(); err != nil {
		return ExecResult{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	if err := workspace.Validate(project); err != nil {
		return ExecResult{}, NewError(ErrorValidation, fmt.Errorf("validate workspace: %w", err))
	}
	result, indexes := newExecResult(project, workspace, request)
	if err := ctx.Err(); err != nil {
		return failExecResult(result, nil, err), err
	}
	facts, err := s.preflight(ctx, project, workspace)
	if err != nil {
		var preflight *execPreflightError
		if errors.As(err, &preflight) {
			index := indexes[preflight.id]
			return failExecResult(result, &index, err), err
		}
		return failExecResult(result, nil, err), err
	}
	if err := ctx.Err(); err != nil {
		return failExecResult(result, nil, err), err
	}
	for _, fact := range facts {
		index := indexes[fact.id]
		entry := fact.result(AggregateStatusPlanned)
		if request.DryRun {
			entry.Environment = execEnvironmentFacts(project.ID, workspace.Name, fact.id, fact.mount, fact.path, fact.branch, fact.head)
		}
		result.Repositories[index] = entry
	}
	if err := ctx.Err(); err != nil {
		return failExecResult(result, nil, err), err
	}
	if request.DryRun {
		return result, nil
	}
	execution := append([]execRepository(nil), facts...)
	if request.Reverse {
		reverseExecRepositories(execution)
	}
	for position, fact := range execution {
		if err := ctx.Err(); err != nil {
			canceled, callbackErr := cancelExecResult(result, indexes, execution[position:], err, request.OnComplete)
			if callbackErr != nil {
				return canceled, callbackErr
			}
			return canceled, err
		}
		direct, runErr := RunDirectProcess(ctx, DirectProcessRequest{Program: request.Program, Args: append([]string(nil), request.Args...), Directory: fact.path, Environment: execEnvironment(request.Environment, project.ID, workspace.Name, fact)})
		index := indexes[fact.id]
		entry := result.Repositories[index]
		entry.started = direct.started
		entry.Stdout, entry.Stderr, entry.StdoutTruncated, entry.StderrTruncated = direct.Stdout, direct.Stderr, direct.StdoutTruncated, direct.StderrTruncated
		if direct.started {
			exitCode := direct.ExitCode
			entry.ExitCode = &exitCode
		}
		if runErr != nil && (ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
			entry.Status, entry.Failure = AggregateStatusCanceled, execFailure(ErrorInternal, runErr)
			result.Repositories[index] = entry
			if callbackErr := notifyExec(request.OnComplete, entry); callbackErr != nil {
				return cancelAfterOutputFailure(result, indexes, execution[position+1:], callbackErr), callbackErr
			}
			canceled, callbackErr := cancelExecResult(result, indexes, execution[position+1:], runErr, request.OnComplete)
			if callbackErr != nil {
				return canceled, callbackErr
			}
			return canceled, runErr
		}
		if runErr != nil {
			entry.Status, entry.Failure = AggregateStatusFailed, execFailure(execErrorKind(runErr), runErr)
		} else if direct.ExitCode != 0 {
			entry.Status, entry.Failure = AggregateStatusFailed, execFailure(ErrorConflict, fmt.Errorf("program exited with status %d", direct.ExitCode))
		} else {
			entry.Status = AggregateStatusCompleted
		}
		result.Repositories[index] = entry
		if callbackErr := notifyExec(request.OnComplete, entry); callbackErr != nil {
			return cancelAfterOutputFailure(result, indexes, execution[position+1:], callbackErr), callbackErr
		}
	}
	for _, fact := range execution {
		entry := result.Repositories[indexes[fact.id]]
		if entry.Status == AggregateStatusFailed {
			failure := *entry.Failure
			result.Status, result.Failure = AggregateStatusFailed, &failure
			return result, NewError(failure.Code, errors.New(failure.Message))
		}
	}
	result.Status = AggregateStatusCompleted
	return result, nil
}

func newExecResult(project domain.Project, workspace domain.Workspace, request ExecRequest) (ExecResult, map[string]int) {
	checkouts := map[string]domain.Checkout{}
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	result := ExecResult{
		Version: 1, Operation: "exec", Status: AggregateStatusPlanned,
		DryRun: request.DryRun, ProjectID: project.ID, Workspace: workspace.Name,
		Partial:              workspace.Partial,
		MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...),
		Command:              ExecCommand{Program: request.Program, Args: append([]string{}, request.Args...)},
		ExecutionOrder:       make([]string, 0, len(workspace.Checkouts)),
		Repositories:         make([]ExecRepositoryResult, 0, len(workspace.Checkouts)),
	}
	indexes := map[string]int{}
	for _, repository := range project.ParentFirst() {
		checkout, present := checkouts[repository.ID]
		if !present {
			continue
		}
		indexes[repository.ID] = len(result.Repositories)
		result.Repositories = append(result.Repositories, ExecRepositoryResult{ID: repository.ID, ParentID: repository.ParentID, Mount: checkout.Mount, Path: checkout.ResolvedPath, Branch: checkout.Branch, Head: checkout.Head, Status: AggregateStatusPlanned})
		result.ExecutionOrder = append(result.ExecutionOrder, repository.ID)
	}
	if request.Reverse {
		reverseExecStrings(result.ExecutionOrder)
	}
	return result, indexes
}

func failExecResult(result ExecResult, index *int, cause error) ExecResult {
	failure := execFailure(execErrorKind(cause), cause)
	result.Status, result.Failure = AggregateStatusFailed, failure
	if index != nil {
		entry := result.Repositories[*index]
		entry.Status, entry.Failure = AggregateStatusFailed, failure
		result.Repositories[*index] = entry
	}
	return result
}

func cancelExecResult(result ExecResult, indexes map[string]int, pending []execRepository, cause error, callback func(ExecRepositoryResult) error) (ExecResult, error) {
	failure := execFailure(ErrorInternal, cause)
	result.Status, result.Failure = AggregateStatusFailed, failure
	for _, fact := range pending {
		entry := result.Repositories[indexes[fact.id]]
		entry.Status, entry.Failure = AggregateStatusCanceled, failure
		result.Repositories[indexes[fact.id]] = entry
		if callbackErr := notifyExec(callback, entry); callbackErr != nil {
			return cancelAfterOutputFailure(result, indexes, pending, callbackErr), callbackErr
		}
	}
	return result, nil
}

// cancelAfterOutputFailure marks entries that were never started without
// calling the failed output sink again. The original writer error remains the
// caller-visible cause, rather than being mistaken for a child failure.
func cancelAfterOutputFailure(result ExecResult, indexes map[string]int, pending []execRepository, cause error) ExecResult {
	failure := execFailure(ErrorInternal, cause)
	result.Status, result.Failure = AggregateStatusFailed, failure
	for _, fact := range pending {
		entry := result.Repositories[indexes[fact.id]]
		if entry.Status == AggregateStatusPlanned {
			entry.Status, entry.Failure = AggregateStatusCanceled, failure
			result.Repositories[indexes[fact.id]] = entry
		}
	}
	return result
}

func notifyExec(callback func(ExecRepositoryResult) error, entry ExecRepositoryResult) error {
	if callback != nil {
		return callback(cloneExecRepositoryResult(entry))
	}
	return nil
}

func cloneExecRepositoryResult(entry ExecRepositoryResult) ExecRepositoryResult {
	cloned := entry
	if entry.ExitCode != nil {
		exitCode := *entry.ExitCode
		cloned.ExitCode = &exitCode
	}
	if entry.Failure != nil {
		failure := *entry.Failure
		cloned.Failure = &failure
	}
	if entry.Environment != nil {
		cloned.Environment = make(map[string]string, len(entry.Environment))
		for key, value := range entry.Environment {
			cloned.Environment[key] = value
		}
	}
	return cloned
}
func reverseExecRepositories(repositories []execRepository) {
	for left, right := 0, len(repositories)-1; left < right; left, right = left+1, right-1 {
		repositories[left], repositories[right] = repositories[right], repositories[left]
	}
}

func reverseExecStrings(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func execFailure(kind ErrorKind, cause error) *AggregateFailure {
	failure, err := NewAggregateFailure(kind, cause)
	if err != nil {
		return &AggregateFailure{Code: ErrorInternal, Message: "exec failure"}
	}
	return &failure
}
func execErrorKind(cause error) ErrorKind {
	var application *Error
	if errors.As(cause, &application) {
		return application.Kind
	}
	var gitError *gitadapter.Error
	if errors.As(cause, &gitError) {
		return ErrorGit
	}
	return ErrorInternal
}

type execPreflightError struct {
	id    string
	cause error
}

func (e *execPreflightError) Error() string { return e.cause.Error() }
func (e *execPreflightError) Unwrap() error { return e.cause }

type execRepository struct {
	id, parentID, mount, path, branch, head string
	detached                                bool
}

func (repository execRepository) result(status AggregateStatus) ExecRepositoryResult {
	return ExecRepositoryResult{ID: repository.id, ParentID: repository.parentID, Mount: repository.mount, Path: repository.path, Branch: repository.branch, Head: repository.head, Status: status}
}

func (s *ExecService) preflight(ctx context.Context, project domain.Project, workspace domain.Workspace) ([]execRepository, error) {
	checkouts := map[string]domain.Checkout{}
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	result := make([]execRepository, 0, len(checkouts))
	for _, repository := range project.ParentFirst() {
		checkout, present := checkouts[repository.ID]
		if !present {
			continue
		}
		fact, err := s.preflightRepository(ctx, repository, checkout)
		if err != nil {
			return nil, &execPreflightError{id: repository.ID, cause: err}
		}
		result = append(result, fact)
	}
	return result, nil
}

func (s *ExecService) preflightRepository(ctx context.Context, repository domain.Repository, checkout domain.Checkout) (execRepository, error) {
	path, err := canonicalExecDirectory(checkout.ResolvedPath)
	if err != nil {
		return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: checkout path: %w", repository.ID, err))
	}
	common, err := s.git.CommonGitDir(ctx, path)
	if contextErr := execObservationContextError(ctx, err); contextErr != nil {
		return execRepository{}, contextErr
	}
	if err != nil {
		return execRepository{}, NewError(ErrorGit, fmt.Errorf("exec preflight %q: read Git identity: %w", repository.ID, err))
	}
	if common != repository.CommonGitDir {
		return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: Git identity does not match persisted repository", repository.ID))
	}
	top, err := s.git.TopLevel(ctx, path)
	if contextErr := execObservationContextError(ctx, err); contextErr != nil {
		return execRepository{}, contextErr
	}
	if err != nil {
		return execRepository{}, NewError(ErrorGit, fmt.Errorf("exec preflight %q: read checkout root: %w", repository.ID, err))
	}
	if !sameCheckoutPath(top, path) {
		return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: checkout root does not match persisted path", repository.ID))
	}
	branch, detached, err := s.git.CurrentBranch(ctx, path)
	if contextErr := execObservationContextError(ctx, err); contextErr != nil {
		return execRepository{}, contextErr
	}
	if err != nil {
		return execRepository{}, NewError(ErrorGit, fmt.Errorf("exec preflight %q: read branch: %w", repository.ID, err))
	}
	if detached != checkout.Detached || (!detached && branch != checkout.Branch) {
		return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: branch state does not match persisted checkout", repository.ID))
	}
	head, err := s.git.Head(ctx, path)
	if contextErr := execObservationContextError(ctx, err); contextErr != nil {
		return execRepository{}, contextErr
	}
	if err != nil {
		return execRepository{}, NewError(ErrorGit, fmt.Errorf("exec preflight %q: read HEAD: %w", repository.ID, err))
	}
	if head != checkout.Head {
		return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: HEAD does not match persisted checkout", repository.ID))
	}
	return execRepository{id: repository.ID, parentID: repository.ParentID, mount: checkout.Mount, path: path, branch: checkout.Branch, head: head, detached: checkout.Detached}, nil
}

// execObservationContextError gives cancellation precedence at each blocking
// Git observation. An adapter may return a wrapped context error before its
// caller observes cancellation, so preserve that error instead of recasting it
// as a Git failure.
func execObservationContextError(ctx context.Context, observation error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(observation, context.Canceled) || errors.Is(observation, context.DeadlineExceeded) {
		return observation
	}
	return nil
}

func canonicalExecDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("persisted checkout path must be a clean absolute directory")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("persisted checkout path is not a directory")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical {
		return "", errors.New("canonical checkout path is invalid")
	}
	return canonical, nil
}

func execEnvironment(inherited []string, projectID, workspace string, repository execRepository) []string {
	environment := make([]string, 0, len(inherited)+7)
	for _, value := range inherited {
		key, _, found := strings.Cut(value, "=")
		if found && strings.HasPrefix(key, "WTREE_") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "WTREE_PROJECT_ID="+projectID, "WTREE_WORKSPACE="+workspace, "WTREE_REPOSITORY_ID="+repository.id, "WTREE_MOUNT="+repository.mount, "WTREE_PATH="+repository.path, "WTREE_BRANCH="+repository.branch, "WTREE_COMMIT="+repository.head)
}

func execEnvironmentFacts(projectID, workspace, repositoryID, mount, path, branch, head string) map[string]string {
	return map[string]string{"WTREE_PROJECT_ID": projectID, "WTREE_WORKSPACE": workspace, "WTREE_REPOSITORY_ID": repositoryID, "WTREE_MOUNT": mount, "WTREE_PATH": path, "WTREE_BRANCH": branch, "WTREE_COMMIT": head}
}
