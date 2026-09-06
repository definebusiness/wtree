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
	// NoCompanions selects every present ordinary repository. It is mutually
	// exclusive with RepositoryID and is resolved before checkout preflight.
	NoCompanions bool
	// RepositoryID selects one configured, present repository regardless of
	// role. An empty ID is the default all-present selection.
	RepositoryID string
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
	Companion       bool              `json:"companion,omitempty"`
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
		Companion       bool              `json:"companion,omitempty"`
	}
	wire := wireResult{
		ID: result.ID, ParentID: result.ParentID, Mount: result.Mount, Path: result.Path,
		Branch: result.Branch, Head: result.Head, Status: result.Status,
		Failure: result.Failure, Environment: result.Environment, Companion: result.Companion,
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
	// The unqualified command retains the pre-selection contract exactly: every
	// persisted checkout fact is authoritative. Scoped commands intentionally
	// establish their smaller authority below instead.
	if request.RepositoryID == "" && !request.NoCompanions {
		if err := workspace.Validate(project); err != nil {
			return ExecResult{}, NewError(ErrorValidation, fmt.Errorf("validate workspace: %w", err))
		}
	}
	selected, err := selectExecRepositories(project, workspace, request)
	if err != nil {
		return ExecResult{}, err
	}
	result, indexes := newExecResult(project, workspace, request, selected)
	scoped := request.RepositoryID != "" || request.NoCompanions
	if err := validateExecWorkspaceSelection(project, workspace, selected, scoped); err != nil {
		validation := NewError(ErrorValidation, fmt.Errorf("validate selected exec checkouts: %w", err))
		var selection *execSelectionError
		if errors.As(err, &selection) {
			index := indexes[selection.id]
			return failExecResult(result, &index, validation), validation
		}
		return failExecResult(result, nil, validation), validation
	}
	if err := ctx.Err(); err != nil {
		return failExecResult(result, nil, err), err
	}
	facts, err := s.preflight(ctx, project, workspace, selected)
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

func newExecResult(project domain.Project, workspace domain.Workspace, request ExecRequest, selected []domain.Repository) (ExecResult, map[string]int) {
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
		ExecutionOrder:       make([]string, 0, len(selected)),
		Repositories:         make([]ExecRepositoryResult, 0, len(selected)),
	}
	indexes := map[string]int{}
	for _, repository := range selected {
		checkout, present := checkouts[repository.ID]
		if !present {
			continue
		}
		indexes[repository.ID] = len(result.Repositories)
		result.Repositories = append(result.Repositories, ExecRepositoryResult{ID: repository.ID, ParentID: repository.ParentID, Mount: checkout.Mount, Path: checkout.ResolvedPath, Branch: checkout.Branch, Head: checkout.Head, Companion: repository.Companion, Status: AggregateStatusPlanned})
		result.ExecutionOrder = append(result.ExecutionOrder, repository.ID)
	}
	if request.Reverse {
		reverseExecStrings(result.ExecutionOrder)
	}
	return result, indexes
}

// selectExecRepositories establishes the complete subset authority before any
// Git observation. Unselected working trees are deliberately not inspected.
func selectExecRepositories(project domain.Project, workspace domain.Workspace, request ExecRequest) ([]domain.Repository, error) {
	if request.NoCompanions && request.RepositoryID != "" {
		return nil, NewError(ErrorInvalidArguments, errors.New("exec selectors are mutually exclusive"))
	}
	present := make(map[string]bool, len(workspace.Checkouts))
	counts := make(map[string]int, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		present[checkout.RepositoryID] = true
		counts[checkout.RepositoryID]++
	}
	ordered := project.ParentFirst()
	if request.RepositoryID != "" {
		for _, repository := range ordered {
			if repository.ID != request.RepositoryID {
				continue
			}
			if !present[repository.ID] {
				return nil, NewError(ErrorInvalidArguments, fmt.Errorf("exec repository %q is absent from workspace", repository.ID))
			}
			if counts[repository.ID] != 1 {
				return nil, NewError(ErrorValidation, fmt.Errorf("exec repository %q has duplicate checkout selection", repository.ID))
			}
			return []domain.Repository{repository}, nil
		}
		return nil, NewError(ErrorInvalidArguments, fmt.Errorf("exec repository %q is not configured", request.RepositoryID))
	}
	selected := make([]domain.Repository, 0, len(ordered))
	for _, repository := range ordered {
		if present[repository.ID] && (!request.NoCompanions || !repository.Companion) {
			if counts[repository.ID] != 1 {
				return nil, NewError(ErrorValidation, fmt.Errorf("exec repository %q has duplicate checkout selection", repository.ID))
			}
			selected = append(selected, repository)
		}
	}
	if request.NoCompanions && len(selected) == 0 {
		return nil, NewError(ErrorInvalidArguments, errors.New("exec --no-companions selected no present repositories"))
	}
	return selected, nil
}

// execSelectionError identifies the selected checkout whose persisted facts
// prevented preflight, so the result envelope can attribute the failure.
type execSelectionError struct {
	id    string
	cause error
}

func (e *execSelectionError) Error() string { return e.cause.Error() }
func (e *execSelectionError) Unwrap() error { return e.cause }

// validateExecWorkspaceSelection intentionally validates only the selected
// persisted checkout records. Its mount resolver receives selected checkout
// mounts and any present ancestors required to locate them; an absent ancestor
// resolves through its configured default. Unselected sibling or descendant
// corruption remains outside a scoped command's authority.
func validateExecWorkspaceSelection(project domain.Project, workspace domain.Workspace, selected []domain.Repository, scoped bool) error {
	if workspace.Version != domain.CurrentVersion || workspace.ID == "" || workspace.Name == "" || workspace.RootPath == "" {
		return errors.New("workspace identity is incomplete")
	}
	// A valid partial workspace may intentionally have no present checkouts for
	// the unchanged default command. Exact and ordinary-only selectors reject
	// an empty selection before reaching this validation boundary.
	if len(selected) == 0 {
		return nil
	}
	byID := make(map[string]domain.Checkout, len(workspace.Checkouts))
	counts := make(map[string]int, len(workspace.Checkouts))
	missingCounts := make(map[string]int, len(workspace.MissingRepositoryIDs))
	for _, checkout := range workspace.Checkouts {
		byID[checkout.RepositoryID] = checkout
		counts[checkout.RepositoryID]++
	}
	for _, id := range workspace.MissingRepositoryIDs {
		missingCounts[id]++
	}
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	scopeIDs := make(map[string]struct{}, len(selected))
	owners := make(map[string]string, len(selected))
	for _, repository := range selected {
		for current := repository; ; {
			scopeIDs[current.ID] = struct{}{}
			if _, exists := owners[current.ID]; !exists {
				owners[current.ID] = repository.ID
			}
			if current.ParentID == "" {
				break
			}
			current = repositories[current.ParentID]
		}
	}

	// EffectivePaths is authoritative only for selected checkouts and the
	// ancestors necessary to resolve their mounted locations. In particular, a
	// scoped command must not inherit sibling defaults or collision checks from
	// a checkout it will never preflight.
	scopedProject := project
	scopedProject.Repositories = make([]domain.Repository, 0, len(scopeIDs))
	for _, repository := range project.ParentFirst() {
		if _, included := scopeIDs[repository.ID]; included {
			scopedProject.Repositories = append(scopedProject.Repositories, repository)
		}
	}
	if _, included := scopeIDs[scopedProject.BaseRepository]; !included {
		for _, repository := range scopedProject.Repositories {
			if repository.ParentID == "" {
				scopedProject.BaseRepository = repository.ID
				break
			}
		}
	}
	mounts := make(map[string]string)
	for _, repository := range scopedProject.Repositories {
		checkout, present := byID[repository.ID]
		if !present {
			continue
		}
		if counts[repository.ID] != 1 || checkout.Mount == "" || checkout.ResolvedPath == "" {
			return &execSelectionError{id: owners[repository.ID], cause: fmt.Errorf("selected checkout ancestor %q is incomplete or duplicated", repository.ID)}
		}
		mounts[repository.ID] = checkout.Mount
	}
	for _, repository := range selected {
		checkout := byID[repository.ID]
		if counts[repository.ID] != 1 || missingCounts[repository.ID] != 0 || checkout.Head == "" || (scoped && (checkout.Detached || checkout.Branch == "")) {
			return &execSelectionError{id: repository.ID, cause: fmt.Errorf("selected checkout %q is incomplete or detached", repository.ID)}
		}
	}
	expectedPaths, err := scopedProject.EffectivePaths(workspace.RootPath, mounts)
	if err != nil {
		return fmt.Errorf("resolve selected checkout paths: %w", err)
	}
	for _, repository := range scopedProject.Repositories {
		checkout := byID[repository.ID]
		if counts[repository.ID] == 0 {
			continue
		}
		if checkout.ResolvedPath != expectedPaths[repository.ID] {
			return &execSelectionError{id: owners[repository.ID], cause: fmt.Errorf("selected checkout %q resolved path %q does not match expected path %q", repository.ID, checkout.ResolvedPath, expectedPaths[repository.ID])}
		}
	}
	return nil
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
	companion                               bool
}

func (repository execRepository) result(status AggregateStatus) ExecRepositoryResult {
	return ExecRepositoryResult{ID: repository.id, ParentID: repository.parentID, Mount: repository.mount, Path: repository.path, Branch: repository.branch, Head: repository.head, Companion: repository.companion, Status: status}
}

func (s *ExecService) preflight(ctx context.Context, project domain.Project, workspace domain.Workspace, selected []domain.Repository) ([]execRepository, error) {
	checkouts := map[string]domain.Checkout{}
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	result := make([]execRepository, 0, len(checkouts))
	for _, repository := range selected {
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
	if detached != checkout.Detached {
		return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: attachment state does not match persisted checkout", repository.ID))
	}
	if !detached && branch != checkout.Branch {
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
		if !repository.Companion {
			return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: HEAD does not match persisted checkout", repository.ID))
		}
		advanced, ancestorErr := gitIsAncestor(ctx, s.git, path, checkout.Head, head)
		if contextErr := execObservationContextError(ctx, ancestorErr); contextErr != nil {
			return execRepository{}, contextErr
		}
		if ancestorErr != nil {
			return execRepository{}, NewError(ErrorGit, fmt.Errorf("exec preflight %q: compare companion checkout history: %w", repository.ID, ancestorErr))
		}
		if !advanced {
			return execRepository{}, NewError(ErrorValidation, fmt.Errorf("exec preflight %q: companion HEAD is not descended from persisted checkout", repository.ID))
		}
	}
	return execRepository{id: repository.ID, parentID: repository.ParentID, mount: checkout.Mount, path: path, branch: checkout.Branch, head: head, detached: checkout.Detached, companion: repository.Companion}, nil
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
