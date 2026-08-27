package service

// Push readiness deliberately contains no publishing operation. It observes
// the selected workspace and the one configured upstream ref per repository,
// then reports whether a manual publication would be safe to start.

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
)

type PushStatus string

const (
	PushStatusReady    PushStatus = "ready"
	PushStatusBlocked  PushStatus = "blocked"
	PushStatusFailed   PushStatus = "failed"
	PushStatusCanceled PushStatus = "canceled"
)

// PushFinding is an ordinary readiness condition. Its text is intentionally
// fixed: remote URLs, credentials, local manifest locations, and Git stderr
// never become command output.
type PushFinding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PushRepositoryResult struct {
	ID             string            `json:"id"`
	ParentID       string            `json:"parentId,omitempty"`
	Mount          string            `json:"mount"`
	Path           string            `json:"-"`
	Branch         string            `json:"branch,omitempty"`
	Head           string            `json:"head,omitempty"`
	ObservedCommit string            `json:"observedCommit,omitempty"`
	Status         PushStatus        `json:"status"`
	Findings       []PushFinding     `json:"findings,omitempty"`
	Failure        *AggregateFailure `json:"failure,omitempty"`
}

// PushResult is a command-owned v1 wire format. It has no remote mutation
// receipt because push readiness never publishes anything.
type PushResult struct {
	Version              int                    `json:"version"`
	Operation            string                 `json:"operation"`
	Status               PushStatus             `json:"status"`
	ProjectID            string                 `json:"projectId"`
	Workspace            string                 `json:"workspace"`
	Partial              bool                   `json:"partial,omitempty"`
	MissingRepositoryIDs []string               `json:"missingRepositoryIds,omitempty"`
	Repositories         []PushRepositoryResult `json:"repositories"`
	Failure              *AggregateFailure      `json:"failure,omitempty"`
}

type PushRequest struct {
	// OnComplete receives one final parent-first row. A failed writer prevents
	// later remote observations so callers never silently lose their output.
	OnComplete func(PushRepositoryResult) error
}

type PushService struct{ git gitadapter.Git }

func NewPushService() *PushService                       { return NewPushServiceWith(gitadapter.NewAdapter("git")) }
func NewPushServiceWith(git gitadapter.Git) *PushService { return &PushService{git: git} }

type pushPlanEntry struct {
	repository domain.Repository
	checkout   domain.Checkout
	entry      PushRepositoryResult
	upstream   gitadapter.Upstream
	remoteOK   bool
}

// Push checks the committed portable authority and the selected workspace.
// It uses ls-remote only for the explicit configured branch and never fetches,
// creates a ref, writes state, locks, or contacts a symbolic remote HEAD.
func (s *PushService) Push(ctx context.Context, project domain.Project, workspace domain.Workspace, request PushRequest) (PushResult, error) {
	if s == nil || s.git == nil {
		return PushResult{}, NewError(ErrorInternal, errors.New("push service is not configured"))
	}
	if err := project.Validate(); err != nil {
		return PushResult{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	if err := workspace.Validate(project); err != nil {
		return PushResult{}, NewError(ErrorValidation, fmt.Errorf("validate workspace: %w", err))
	}
	result := newPushResult(project, workspace)
	if err := ctx.Err(); err != nil {
		return cancelUnstartedPush(result, err, request.OnComplete)
	}
	manifest, manifestErr := s.pushManifest(ctx, project, workspace)
	if manifestErr != nil {
		if contextErr := contextObservationError(ctx, manifestErr); contextErr != nil {
			return cancelUnstartedPush(result, contextErr, request.OnComplete)
		}
		return failUnstartedPush(result, manifestErr), manifestErr
	}
	plan, canceled, planErr := s.pushPlan(ctx, project, workspace, manifest, result)
	if canceled != nil {
		return cancelUnstartedPush(result, canceled, request.OnComplete)
	}
	if planErr != nil {
		return failUnstartedPush(result, planErr), planErr
	}

	for index := range plan {
		if err := ctx.Err(); err != nil {
			return cancelPush(result, index, err, request.OnComplete)
		}
		planned := plan[index]
		entry := planned.entry
		if planned.remoteOK {
			advertised, err := s.git.AdvertisedCommit(ctx, planned.upstream.FetchURL, planned.upstream.Merge)
			if contextObservationError(ctx, err) != nil {
				return cancelPush(result, index, contextObservationError(ctx, err), request.OnComplete)
			}
			if err != nil {
				if errors.Is(err, gitadapter.ErrRemoteRefNotFound) {
					entry.Status = PushStatusBlocked
					entry.Findings = append(entry.Findings, pushFinding("unpublished-head"))
				} else {
					entry.Status = PushStatusFailed
					entry.Failure = pushOperationalFailure(err)
				}
			} else {
				entry.ObservedCommit = advertised
				if advertised != entry.Head {
					entry.Status = PushStatusBlocked
					entry.Findings = append(entry.Findings, pushFinding("unpublished-head"))
				}
			}
		}
		result.Repositories[index] = entry
		if err := notifyPush(request.OnComplete, entry); err != nil {
			if cancellation := contextObservationError(ctx, err); cancellation != nil {
				return cancelPushAfterCallback(result, index, cancellation, request.OnComplete)
			}
			return stopPushForOutput(result, index+1, err), err
		}
	}
	return finishPush(result)
}

func (s *PushService) pushManifest(ctx context.Context, project domain.Project, workspace domain.Workspace) (config.PortableManifest, error) {
	configurationBytes, err := os.ReadFile(project.ConfigPath)
	if err != nil {
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("read validated local configuration"))
	}
	configuration, err := config.LoadProject(configurationBytes)
	if err != nil {
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("decode validated local configuration"))
	}
	if configuration.Project.ID != project.ID || configuration.Project.BaseRepository != project.BaseRepository {
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("local configuration does not match selected project"))
	}
	var base domain.Checkout
	for _, checkout := range workspace.Checkouts {
		if checkout.RepositoryID == project.BaseRepository {
			base = checkout
			break
		}
	}
	if base.RepositoryID == "" {
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("selected workspace does not contain the base checkout"))
	}
	var baseRepository domain.Repository
	for _, repository := range project.Repositories {
		if repository.ID == project.BaseRepository {
			baseRepository = repository
			break
		}
	}
	path, err := canonicalExecDirectory(base.ResolvedPath)
	if err != nil {
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("base checkout path is unavailable"))
	}
	common, err := s.git.CommonGitDir(ctx, path)
	if err != nil {
		if contextObservationError(ctx, err) != nil {
			return config.PortableManifest{}, contextObservationError(ctx, err)
		}
		return config.PortableManifest{}, NewError(ErrorGit, errors.New("read base checkout identity"))
	}
	if common != baseRepository.CommonGitDir {
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("base checkout identity does not match selected project"))
	}
	head, err := s.git.Head(ctx, path)
	if err != nil {
		if contextObservationError(ctx, err) != nil {
			return config.PortableManifest{}, contextObservationError(ctx, err)
		}
		return config.PortableManifest{}, NewError(ErrorGit, errors.New("read base checkout HEAD"))
	}
	bytes, err := s.git.TrackedFile(ctx, path, head, configuration.Manifest.Path)
	if err != nil {
		if contextObservationError(ctx, err) != nil {
			return config.PortableManifest{}, contextObservationError(ctx, err)
		}
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("read committed portable manifest"))
	}
	manifest, err := config.LoadPortableManifest(bytes)
	if err != nil || manifest.Project.ID != project.ID || manifest.Project.BaseRepository != project.BaseRepository {
		return config.PortableManifest{}, NewError(ErrorValidation, errors.New("committed portable manifest does not match selected project"))
	}
	return manifest, nil
}

func (s *PushService) pushPlan(ctx context.Context, project domain.Project, workspace domain.Workspace, manifest config.PortableManifest, result PushResult) ([]pushPlanEntry, error, error) {
	checkouts := make(map[string]domain.Checkout, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	missing := make(map[string]bool, len(workspace.MissingRepositoryIDs))
	for _, id := range workspace.MissingRepositoryIDs {
		missing[id] = true
	}
	plan := make([]pushPlanEntry, 0, len(result.Repositories))
	for index, repository := range project.ParentFirst() {
		if err := ctx.Err(); err != nil {
			return plan, err, nil
		}
		entry := result.Repositories[index]
		checkout, found := checkouts[repository.ID]
		if !found || missing[repository.ID] {
			entry.Status = PushStatusBlocked
			entry.Findings = append(entry.Findings, pushFinding("missing-repository"))
			plan = append(plan, pushPlanEntry{repository: repository, entry: entry})
			continue
		}
		var upstream gitadapter.Upstream
		var remoteOK bool
		var cancellation error
		entry, upstream, remoteOK, cancellation = s.pushLocalEntry(ctx, repository, checkout, manifest, project, workspace)
		if cancellation != nil {
			return plan, cancellation, nil
		}
		if workspace.Partial {
			entry = pushBlocked(entry, "partial-workspace")
		}
		plan = append(plan, pushPlanEntry{repository: repository, checkout: checkout, entry: entry, upstream: upstream, remoteOK: remoteOK && pushMayObserveRemote(entry)})
	}
	return plan, nil, nil
}

// Local dirt and ahead/behind are readiness findings, not a reason to skip
// the independent exact-ref observation. Identity, attachment, and metadata
// failures are different: they do not establish authority for contacting a
// configured remote from that checkout.
func pushMayObserveRemote(entry PushRepositoryResult) bool {
	if entry.Status == PushStatusFailed || entry.Status == PushStatusCanceled {
		return false
	}
	for _, finding := range entry.Findings {
		switch finding.Code {
		case "missing-repository", "detached", "identity-mismatch", "metadata-commit-unavailable":
			return false
		}
	}
	return true
}

func (s *PushService) pushLocalEntry(ctx context.Context, repository domain.Repository, checkout domain.Checkout, manifest config.PortableManifest, project domain.Project, workspace domain.Workspace) (PushRepositoryResult, gitadapter.Upstream, bool, error) {
	entry := PushRepositoryResult{ID: repository.ID, ParentID: repository.ParentID, Mount: checkout.Mount, Path: checkout.ResolvedPath, Branch: checkout.Branch, Head: checkout.Head, Status: PushStatusReady}
	path, err := canonicalExecDirectory(checkout.ResolvedPath)
	if err != nil {
		return pushBlocked(entry, "missing-repository"), gitadapter.Upstream{}, false, nil
	}
	entry.Path = path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return pushBlocked(entry, "missing-repository"), gitadapter.Upstream{}, false, nil
	}
	common, err := s.git.CommonGitDir(ctx, path)
	if err != nil {
		return pushLocalObservationFailure(ctx, entry, err)
	}
	if common != repository.CommonGitDir {
		entry = pushBlocked(entry, "identity-mismatch")
	}
	top, err := s.git.TopLevel(ctx, path)
	if err != nil {
		return pushLocalObservationFailure(ctx, entry, err)
	}
	if !sameCheckoutPath(top, path) {
		entry = pushBlocked(entry, "identity-mismatch")
	}
	branch, detached, err := s.git.CurrentBranch(ctx, path)
	if err != nil {
		return pushLocalObservationFailure(ctx, entry, err)
	}
	entry.Branch = branch
	if detached {
		entry = pushBlocked(entry, "detached")
	} else if branch != checkout.Branch {
		entry = pushBlocked(entry, "identity-mismatch")
	}
	head, err := s.git.Head(ctx, path)
	if err != nil {
		return pushLocalObservationFailure(ctx, entry, err)
	}
	entry.Head = head
	// A workspace records the commit it was created from. The base repository
	// must be able to advance in order to commit project.wtree.yml itself, so
	// readiness requires the immutable identity roots to remain reachable, not
	// equality with that historical checkout generation.
	children := make([]string, 0)
	for _, child := range project.Repositories {
		if child.ParentID == repository.ID {
			if childPath, pathErr := workspace.ResolveRepository(child.ID); pathErr == nil {
				children = append(children, childPath)
			}
		}
	}
	status, err := s.git.Status(ctx, path)
	if err != nil {
		return pushLocalObservationFailure(ctx, entry, err)
	}
	if len(withoutManagedChildEntries(status, path, children).Entries) != 0 {
		entry = pushBlocked(entry, "dirty")
	}
	manifestRepository, found := manifest.Repositories[repository.ID]
	if !found || manifestRepository.Parent != repository.ParentID || manifestRepository.Mount != checkout.Mount {
		entry = pushBlocked(entry, "identity-mismatch")
	} else {
		requiredCommits := append([]string(nil), manifestRepository.Identity.InitialCommits...)
		// The persisted checkout commit is metadata too. It may be an ancestor
		// (for example after committing the tracked manifest), but it must still
		// be reachable from the current tip before that tip can be called ready.
		requiredCommits = append(requiredCommits, checkout.Head)
		contains, containsErr := s.git.ContainsCommits(ctx, path, requiredCommits)
		if containsErr != nil {
			return pushLocalObservationFailure(ctx, entry, containsErr)
		}
		if !contains {
			entry = pushBlocked(entry, "metadata-commit-unavailable")
		}
	}
	if detached {
		return entry, gitadapter.Upstream{}, false, nil
	}
	ahead, behind, configured, err := s.git.AheadBehind(ctx, path)
	if err != nil {
		return pushLocalObservationFailure(ctx, entry, err)
	}
	if !configured {
		return pushBlocked(entry, "missing-upstream"), gitadapter.Upstream{}, false, nil
	}
	if ahead > 0 && behind > 0 {
		entry = pushBlocked(entry, "diverged")
	} else if ahead > 0 {
		entry = pushBlocked(entry, "ahead")
	} else if behind > 0 {
		entry = pushBlocked(entry, "behind")
	}
	upstream, err := s.git.Upstream(ctx, path)
	if err != nil {
		if cancellation := contextObservationError(ctx, err); cancellation != nil {
			return entry, gitadapter.Upstream{}, false, cancellation
		}
		return pushBlocked(entry, "missing-upstream"), gitadapter.Upstream{}, false, nil
	}
	if !found || upstream.LocalBranch != checkout.Branch || upstream.Remote != manifestRepository.Upstream.Remote || upstream.Merge != manifestRepository.Upstream.Merge || upstream.Remote != manifestRepository.Clone.Remote || upstream.FetchURL != manifestRepository.Clone.URL {
		entry = pushBlocked(entry, "identity-mismatch")
	}
	// This immutable capture is the only authority used by the later remote
	// observation. Do not re-read local branch configuration after planning:
	// a concurrent config edit must not redirect ls-remote to another URL/ref.
	return entry, upstream, pushMayObserveRemote(entry), nil
}

func pushLocalObservationFailure(ctx context.Context, entry PushRepositoryResult, cause error) (PushRepositoryResult, gitadapter.Upstream, bool, error) {
	if cancellation := contextObservationError(ctx, cause); cancellation != nil {
		return entry, gitadapter.Upstream{}, false, cancellation
	}
	return pushFailed(entry, cause), gitadapter.Upstream{}, false, nil
}

func newPushResult(project domain.Project, workspace domain.Workspace) PushResult {
	result := PushResult{Version: 1, Operation: "push", Status: PushStatusReady, ProjectID: project.ID, Workspace: workspace.Name, Partial: workspace.Partial, MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...)}
	checkouts := make(map[string]domain.Checkout, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = checkout
	}
	for _, repository := range project.ParentFirst() {
		checkout := checkouts[repository.ID]
		result.Repositories = append(result.Repositories, PushRepositoryResult{ID: repository.ID, ParentID: repository.ParentID, Mount: checkout.Mount, Path: checkout.ResolvedPath, Branch: checkout.Branch, Head: checkout.Head, Status: PushStatusReady})
	}
	return result
}

var pushFindingMessages = map[string]string{
	"dirty":                       "checkout has uncommitted changes",
	"detached":                    "checkout HEAD is detached",
	"missing-upstream":            "checkout has no complete configured upstream",
	"ahead":                       "checkout is ahead of its configured upstream",
	"behind":                      "checkout is behind its configured upstream",
	"diverged":                    "checkout diverges from its configured upstream",
	"unpublished-head":            "local HEAD is not the exact advertised upstream tip",
	"identity-mismatch":           "checkout identity does not match committed metadata",
	"metadata-commit-unavailable": "a metadata commit is unavailable from the checkout HEAD",
	"missing-repository":          "required repository checkout is missing",
	"partial-workspace":           "workspace is partial",
}

func pushFinding(code string) PushFinding {
	message, found := pushFindingMessages[code]
	if !found {
		panic("invalid push finding code")
	}
	return PushFinding{Code: code, Message: message}
}

func pushBlocked(entry PushRepositoryResult, code string) PushRepositoryResult {
	if entry.Status != PushStatusFailed && entry.Status != PushStatusCanceled {
		entry.Status = PushStatusBlocked
	}
	for _, finding := range entry.Findings {
		if finding.Code == code {
			return entry
		}
	}
	entry.Findings = append(entry.Findings, pushFinding(code))
	return entry
}
func pushFailed(entry PushRepositoryResult, cause error) PushRepositoryResult {
	entry.Status, entry.Failure = PushStatusFailed, pushOperationalFailure(cause)
	return entry
}
func pushOperationalFailure(error) *AggregateFailure {
	return &AggregateFailure{Code: ErrorGit, Message: "Git observation failed"}
}

func contextObservationError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func cancelUnstartedPush(result PushResult, cause error, callback func(PushRepositoryResult) error) (PushResult, error) {
	failure := &AggregateFailure{Code: ErrorInternal, Message: "push readiness canceled"}
	result.Status, result.Failure = PushStatusFailed, failure
	for index := range result.Repositories {
		result.Repositories[index].Status, result.Repositories[index].Failure = PushStatusCanceled, failure
		if err := notifyPush(callback, result.Repositories[index]); err != nil {
			return stopPushForOutput(result, index+1, err), err
		}
	}
	return result, cause
}
func failUnstartedPush(result PushResult, cause error) PushResult {
	failure := pushOperationalFailure(cause)
	if application := new(Error); errors.As(cause, &application) {
		failure.Code, failure.Message = application.Kind, "push readiness authority failed"
	}
	result.Status, result.Failure = PushStatusFailed, failure
	for index := range result.Repositories {
		result.Repositories[index].Status, result.Repositories[index].Failure = PushStatusFailed, failure
	}
	return result
}
func cancelPush(result PushResult, start int, cause error, callback func(PushRepositoryResult) error) (PushResult, error) {
	failure := &AggregateFailure{Code: ErrorInternal, Message: "push readiness canceled"}
	result.Status, result.Failure = PushStatusFailed, failure
	for index := 0; index < start; index++ {
		result.Repositories[index].Status, result.Repositories[index].Failure = PushStatusCanceled, failure
	}
	for index := start; index < len(result.Repositories); index++ {
		result.Repositories[index].Status, result.Repositories[index].Failure = PushStatusCanceled, failure
		if err := notifyPush(callback, result.Repositories[index]); err != nil {
			return stopPushForOutput(result, index+1, err), err
		}
	}
	return result, cause
}
func cancelPushAfterCallback(result PushResult, current int, cause error, callback func(PushRepositoryResult) error) (PushResult, error) {
	failure := &AggregateFailure{Code: ErrorInternal, Message: "push readiness canceled"}
	result.Status, result.Failure = PushStatusFailed, failure
	for index := 0; index <= current; index++ {
		result.Repositories[index].Status, result.Repositories[index].Failure = PushStatusCanceled, failure
	}
	for index := current + 1; index < len(result.Repositories); index++ {
		result.Repositories[index].Status, result.Repositories[index].Failure = PushStatusCanceled, failure
		if err := notifyPush(callback, result.Repositories[index]); err != nil && contextObservationError(context.Background(), err) == nil {
			return stopPushForOutput(result, index+1, err), err
		}
	}
	return result, cause
}
func stopPushForOutput(result PushResult, start int, cause error) PushResult {
	failure := &AggregateFailure{Code: ErrorInternal, Message: "push readiness output failed"}
	result.Status, result.Failure = PushStatusFailed, failure
	for index := start; index < len(result.Repositories); index++ {
		if result.Repositories[index].Status == PushStatusReady {
			result.Repositories[index].Status, result.Repositories[index].Failure = PushStatusCanceled, failure
		}
	}
	return result
}
func notifyPush(callback func(PushRepositoryResult) error, entry PushRepositoryResult) error {
	if callback == nil {
		return nil
	}
	copy := entry
	copy.Findings = append([]PushFinding(nil), entry.Findings...)
	if entry.Failure != nil {
		failure := *entry.Failure
		copy.Failure = &failure
	}
	return callback(copy)
}
func finishPush(result PushResult) (PushResult, error) {
	for _, entry := range result.Repositories {
		if entry.Status == PushStatusFailed {
			failure := *entry.Failure
			result.Status, result.Failure = PushStatusFailed, &failure
			return result, NewError(failure.Code, errors.New(failure.Message))
		}
	}
	for _, entry := range result.Repositories {
		if entry.Status == PushStatusBlocked {
			result.Status = PushStatusBlocked
			return result, NewError(ErrorConflict, errors.New("push readiness is blocked"))
		}
	}
	result.Status = PushStatusReady
	return result, nil
}
