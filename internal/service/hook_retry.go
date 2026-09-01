package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
)

const HookRetryResultVersion = 1

type HookRetryRequest struct {
	Project     domain.Project
	Workspace   domain.Workspace
	DataDir     string
	Environment []string
	Windows     bool
	Sink        io.Writer
}
type HookRetryResult struct {
	Version          int      `json:"version"`
	Operation        string   `json:"operation"`
	Status           string   `json:"status"`
	Workspace        string   `json:"workspace"`
	Event            string   `json:"event"`
	Source           string   `json:"source"`
	ResumedAt        int      `json:"resumedAt"`
	CompletedHookIDs []string `json:"completedHookIds"`
}
type HookRetryPlanRequest struct {
	Project     domain.Project
	Workspace   domain.Workspace
	Record      store.HookRunRecord
	DataDir     string
	Environment []string
	Windows     bool
}
type hookRetryPlanBuilder interface {
	Rebuild(context.Context, HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error)
}
type hookRetryRunner interface {
	Resume(context.Context, HookResumeRequest) (HookRunResult, error)
}
type HookRetryService struct {
	inventory *HookRunInventoryService
	builder   hookRetryPlanBuilder
	runner    hookRetryRunner
}

func newAuthoritativeHookRunInventory() *HookRunInventoryService {
	return NewHookRunInventoryServiceWith(hookRetryDefaultBuilder{process: newHookProcessAdapter(), git: gitadapter.NewAdapter("git")})
}

func NewHookRetryService() *HookRetryService {
	builder := hookRetryDefaultBuilder{process: newHookProcessAdapter(), git: gitadapter.NewAdapter("git")}
	return NewHookRetryServiceWith(NewHookRunInventoryServiceWith(builder), builder, NewHookRunner())
}
func NewHookRetryServiceWith(inventory *HookRunInventoryService, builder hookRetryPlanBuilder, runner hookRetryRunner) *HookRetryService {
	return &HookRetryService{inventory: inventory, builder: builder, runner: runner}
}
func (s *HookRetryService) Retry(ctx context.Context, q HookRetryRequest) (HookRetryResult, error) {
	if s == nil || s.inventory == nil || s.builder == nil || s.runner == nil {
		return HookRetryResult{}, NewError(ErrorInternal, errors.New("hook retry is not configured"))
	}
	inventory, err := s.inventory.Inspect(ctx, HookRunInventoryRequest{Project: q.Project, Workspace: q.Workspace, DataDir: q.DataDir, Environment: q.Environment, Windows: q.Windows})
	if err != nil {
		return HookRetryResult{}, err
	}
	if inventory.Classification != HookRunResumable || inventory.record == nil {
		return HookRetryResult{}, hookRetryInventoryError(inventory.Classification)
	}
	record := *inventory.record
	run, err := s.runner.Resume(ctx, HookResumeRequest{DataDir: q.DataDir, ProjectID: q.Project.ID, WorkspaceID: q.Workspace.ID, Event: record.Event, Environment: append([]string(nil), q.Environment...), Windows: q.Windows, Sink: q.Sink, Prepare: func(ctx context.Context, current store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
		return s.builder.Rebuild(ctx, HookRetryPlanRequest{Project: q.Project, Workspace: q.Workspace, Record: current, DataDir: q.DataDir, Environment: append([]string(nil), q.Environment...), Windows: q.Windows})
	}})
	if err != nil {
		return HookRetryResult{}, err
	}
	if !validHookRetryResult(record, run) {
		return HookRetryResult{}, NewError(ErrorConflict, errors.New("hooks retry: hook run result is invalid; inspect with wtree doctor"))
	}
	if run.Failure != nil {
		if run.Failure.Kind == HookFailureLock {
			return HookRetryResult{}, NewError(ErrorConflict, errors.New("hooks retry: hook run is already active; wait for it to finish"))
		}
		if run.Failure.Kind == HookFailureGeneration {
			return HookRetryResult{}, NewError(ErrorConflict, errors.New("hooks retry: hook run is stale; a fresh run is required"))
		}
		if run.Failure.Kind == HookFailureRecord {
			return HookRetryResult{}, NewError(ErrorConflict, errors.New("hooks retry: hook run record is invalid; inspect with wtree doctor"))
		}
		return HookRetryResult{}, NewError(ErrorSetupIncomplete, &SetupIncompleteError{Details: SetupIncompleteDetails{Operation: "retry", CoreStatus: "completed", SetupStatus: "incomplete", Event: record.Event, FailureKind: run.Failure.Kind, CompletedHookIDs: append([]string(nil), run.CompletedIDs...), RetryCommand: "wtree hooks retry " + q.Workspace.Name}})
	}
	status := "completed"
	if record.State == "finalizing" {
		status = "finalized"
	}
	return HookRetryResult{Version: HookRetryResultVersion, Operation: "hooks-retry", Status: status, Workspace: q.Workspace.Name, Event: record.Event, Source: record.Source, ResumedAt: record.NextIndex, CompletedHookIDs: append([]string(nil), run.CompletedIDs...)}, nil
}

func validHookRetryResult(record store.HookRunRecord, run HookRunResult) bool {
	if record.State == "finalizing" {
		return run.Failure == nil && validHookRetrySuccess(record, run)
	}
	if run.Failure == nil {
		return validHookRetrySuccess(record, run)
	}
	if run.Status != "incomplete" || len(run.CompletedIDs) > len(record.HookIDs) {
		return false
	}
	if record.NextIndex < 0 || record.NextIndex > len(record.HookIDs) || len(record.CompletedHookIDs) < record.NextIndex || len(run.CompletedIDs) < record.NextIndex {
		return false
	}
	for index := 0; index < record.NextIndex; index++ {
		if record.CompletedHookIDs[index] != record.HookIDs[index] || run.CompletedIDs[index] != record.CompletedHookIDs[index] {
			return false
		}
	}
	for index, id := range run.CompletedIDs {
		if id != record.HookIDs[index] {
			return false
		}
	}
	return true
}

func validHookRetrySuccess(record store.HookRunRecord, run HookRunResult) bool {
	return run.Status == "completed" && strings.Join(run.CompletedIDs, "\x00") == strings.Join(record.HookIDs, "\x00")
}
func hookRetryInventoryError(classification HookRunClassification) error {
	switch classification {
	case HookRunAbsent:
		return NewError(ErrorConflict, errors.New("hooks retry: no incomplete hook run exists"))
	case HookRunStale:
		return NewError(ErrorConflict, errors.New("hooks retry: hook run is stale; a fresh run is required"))
	default:
		return NewError(ErrorConflict, errors.New("hooks retry: hook run record is invalid; inspect with wtree doctor"))
	}
}

type hookRetryDefaultBuilder struct {
	process HookProcessAdapter
	git     gitadapter.Git
}

func (b hookRetryDefaultBuilder) Rebuild(ctx context.Context, q HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
	if err := ctx.Err(); err != nil {
		return HookPlan{}, nil, err
	}
	if err := validateHookRetryWorkspaceFacts(ctx, b.git, q.Project, q.Workspace); err != nil {
		return HookPlan{}, nil, err
	}
	if q.Record.Source == "portable" {
		return b.rebuildPortable(ctx, q)
	}
	if q.Record.Source != "local" || q.Record.Operation != "create" || q.Record.Event != "post-create" {
		return HookPlan{}, nil, errors.New("unsupported hook retry source")
	}
	p := retryWorkspacePlan(q.Project, q.Workspace)
	coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{Process: b.process})
	preflight, err := coordinator.preflight(ctx, CreateLifecycleRequest{Project: q.Project, Workspace: WorkspacePlanRequest{DataDir: q.DataDir}, Environment: q.Environment, Windows: q.Windows}, p, false)
	if hookContextSentinel(err) {
		return HookPlan{}, nil, err
	}
	if err != nil || !preflight.applicable {
		return HookPlan{}, nil, errors.New("hook retry authority changed")
	}
	baseVerifier := coordinator.generationVerifier(CreateLifecycleRequest{Project: q.Project, Workspace: WorkspacePlanRequest{DataDir: q.DataDir}}, CreateResult{Plan: p})
	if err := validateHookRetryExecutableFacts(ctx, b.process, preflight.plan, q.Environment, q.Windows); err != nil {
		return HookPlan{}, nil, err
	}
	return preflight.plan, func(ctx context.Context) (HookGenerationSnapshot, error) {
		if err := validateHookRetryWorkspaceFacts(ctx, b.git, q.Project, q.Workspace); err != nil {
			return HookGenerationSnapshot{}, err
		}
		if err := validateHookRetryExecutableFacts(ctx, b.process, preflight.plan, q.Environment, q.Windows); err != nil {
			return HookGenerationSnapshot{}, err
		}
		return baseVerifier(ctx)
	}, nil
}

// validateHookRetryWorkspaceFacts is deliberately shared by local and
// portable rebuilds. It validates the persisted checkout topology before a
// retry plan is accepted; verifier closures repeat the same facts under the
// event lock before each launch.
func validateHookRetryWorkspaceFacts(ctx context.Context, git gitadapter.Git, project domain.Project, workspace domain.Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if git == nil || workspace.Validate(project) != nil {
		return errors.New("hook retry workspace authority changed")
	}
	for _, repository := range project.ParentFirst() {
		path, pathErr := workspace.ResolveRepository(repository.ID)
		if pathErr != nil {
			return errors.New("hook retry workspace authority changed")
		}
		checkout, found := workspaceCheckoutMap(workspace)[repository.ID]
		if !found || !sameCheckoutPath(path, checkout.ResolvedPath) {
			return errors.New("hook retry workspace authority changed")
		}
		top, topErr := git.TopLevel(ctx, path)
		common, commonErr := git.CommonGitDir(ctx, path)
		branch, detached, branchErr := git.CurrentBranch(ctx, path)
		head, headErr := git.Head(ctx, path)
		if hookContextSentinel(topErr) {
			return topErr
		}
		if hookContextSentinel(commonErr) {
			return commonErr
		}
		if hookContextSentinel(branchErr) {
			return branchErr
		}
		if hookContextSentinel(headErr) {
			return headErr
		}
		if topErr != nil || commonErr != nil || branchErr != nil || headErr != nil || !sameCheckoutPath(top, path) || common != repository.CommonGitDir || detached != checkout.Detached || (!detached && branch != checkout.Branch) || head != checkout.Head {
			return errors.New("hook retry workspace authority changed")
		}
		if common, sourceErr := git.CommonGitDir(ctx, repository.SourcePath); hookContextSentinel(sourceErr) {
			return sourceErr
		} else if sourceErr != nil || common != repository.CommonGitDir {
			return errors.New("hook retry workspace authority changed")
		}
	}
	return nil
}

func (b hookRetryDefaultBuilder) rebuildPortable(ctx context.Context, q HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
	if err := ctx.Err(); err != nil {
		return HookPlan{}, nil, err
	}
	if b.git == nil || q.Record.Operation != "clone" || q.Record.Event != "post-clone" {
		return HookPlan{}, nil, errors.New("unsupported hook retry source")
	}
	localBytes, err := os.ReadFile(q.Project.ConfigPath)
	if err != nil {
		return HookPlan{}, nil, err
	}
	local, err := config.LoadProject(localBytes)
	if err != nil || local.Manifest.Path == "" || filepath.IsAbs(local.Manifest.Path) {
		return HookPlan{}, nil, errors.New("portable retry configuration changed")
	}
	basePath, err := q.Workspace.ResolveRepository(q.Project.BaseRepository)
	if err != nil {
		return HookPlan{}, nil, err
	}
	baseHead := ""
	for _, checkout := range q.Workspace.Checkouts {
		if checkout.RepositoryID == q.Project.BaseRepository {
			baseHead = checkout.Head
		}
	}
	if baseHead == "" {
		return HookPlan{}, nil, errors.New("portable retry base checkout changed")
	}
	manifestBytes, err := b.git.TrackedFile(ctx, basePath, baseHead, local.Manifest.Path)
	if err != nil {
		return HookPlan{}, nil, err
	}
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		return HookPlan{}, nil, err
	}
	hooks, err := config.CanonicalHookEvent(config.HookEventPostClone, manifest.Hooks[config.HookEventPostClone], manifest.Project.BaseRepository)
	if err != nil || len(hooks) == 0 {
		return HookPlan{}, nil, errors.New("portable retry hook authority changed")
	}
	statePath := WorkspaceStatePath(q.DataDir, q.Project.ID, q.Workspace.ID)
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		return HookPlan{}, nil, err
	}
	entries := make([]hookPlanInputEntry, 0, len(hooks))
	for _, hook := range hooks {
		path, pathErr := q.Workspace.ResolveRepository(hook.Repository)
		if pathErr != nil {
			return HookPlan{}, nil, pathErr
		}
		branch, head := "", ""
		for _, checkout := range q.Workspace.Checkouts {
			if checkout.RepositoryID == hook.Repository {
				branch, head = checkout.Branch, checkout.Head
			}
		}
		fact, resolveErr := b.process.Resolve(ctx, HookExecutableRequest{Program: hook.Command[0], Directory: path, Environment: q.Environment})
		if hookContextSentinel(resolveErr) {
			return HookPlan{}, nil, resolveErr
		}
		if resolveErr != nil || !fact.Available {
			return HookPlan{}, nil, errors.New("portable retry executable unavailable")
		}
		if !filepath.IsAbs(hook.Command[0]) && strings.ContainsAny(hook.Command[0], "/\\") {
			trusted, trustedErr := trustedLocalSourceExecutable(path, hook.Command[0], fact.Resolved)
			if trustedErr != nil {
				return HookPlan{}, nil, errors.New("portable retry executable escapes checkout")
			}
			fact.Resolved = trusted
		}
		entries = append(entries, hookPlanInputEntry{ID: hook.ID, Repository: hook.Repository, SourceRepository: path, TargetRepository: path, Branch: branch, Head: head, ConfiguredExecutable: hook.Command[0], ResolvedExecutable: fact.Resolved, Availability: "available", Arguments: append([]string(nil), hook.Command[1:]...), Timeout: hook.Timeout})
	}
	planValue, err := newHookPlan(hookPlanInput{Operation: "clone", Source: "portable", Event: "post-clone", Policy: "requires-run-hooks", ProjectID: q.Project.ID, ProjectName: q.Project.Name, BaseRepository: q.Project.BaseRepository, WorkspaceID: q.Workspace.ID, WorkspaceName: q.Workspace.Name, SourceLogicalRoot: q.Workspace.RootPath, TargetLogicalRoot: q.Workspace.RootPath, SourceBytes: manifestBytes, WorkspaceStateBytes: stateBytes, Entries: entries})
	if err != nil {
		return HookPlan{}, nil, err
	}
	// Rebuild occurs only for an existing durable record, so repeat the
	// tracked-file proof before accepting the rebuilt plan. The locked
	// verifier repeats it again immediately before a launch.
	if err := validatePortableRetryTrackedExecutableFacts(ctx, b.git, planValue); err != nil {
		return HookPlan{}, nil, err
	}
	if err := validateHookRetryExecutableFacts(ctx, b.process, planValue, q.Environment, q.Windows); err != nil {
		return HookPlan{}, nil, err
	}
	verifier := func(ctx context.Context) (HookGenerationSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return HookGenerationSnapshot{}, err
		}
		if err := validateHookRetryWorkspaceFacts(ctx, b.git, q.Project, q.Workspace); err != nil {
			return HookGenerationSnapshot{}, err
		}
		if err := validatePortableRetryTrackedExecutableFacts(ctx, b.git, planValue); err != nil {
			return HookGenerationSnapshot{}, err
		}
		if err := validateHookRetryExecutableFacts(ctx, b.process, planValue, q.Environment, q.Windows); err != nil {
			return HookGenerationSnapshot{}, err
		}
		currentHead, headErr := b.git.Head(ctx, basePath)
		if hookContextSentinel(headErr) {
			return HookGenerationSnapshot{}, headErr
		}
		if headErr != nil || currentHead != baseHead {
			return HookGenerationSnapshot{}, errors.New("portable retry base changed")
		}
		current, trackedErr := b.git.TrackedFile(ctx, basePath, baseHead, local.Manifest.Path)
		if trackedErr != nil {
			return HookGenerationSnapshot{}, trackedErr
		}
		state, stateErr := os.ReadFile(statePath)
		if stateErr != nil {
			return HookGenerationSnapshot{}, stateErr
		}
		return HookGenerationSnapshot{SourceBytes: current, WorkspaceStateBytes: state}, nil
	}
	return planValue, verifier, nil
}

// validatePortableRetryTrackedExecutableFacts repeats the portable tracked
// file authority while the runner holds its event lock. The workspace-fact
// verifier establishes that each recorded checkout HEAD is still current;
// this check then binds every separator-bearing declaration to that exact
// committed generation before launch.
func validatePortableRetryTrackedExecutableFacts(ctx context.Context, git gitadapter.Git, value HookPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if git == nil {
		return errors.New("portable retry executable authority changed")
	}
	for _, entry := range value.authority.entries {
		if filepath.IsAbs(entry.ConfiguredExecutable) || !strings.ContainsAny(entry.ConfiguredExecutable, "/\\") {
			continue
		}
		relative := filepath.ToSlash(strings.ReplaceAll(entry.ConfiguredExecutable, `\`, "/"))
		if _, err := git.TrackedFile(ctx, entry.TargetRepository, entry.Head, relative); err != nil {
			if hookContextSentinel(err) {
				return err
			}
			return errors.New("portable retry executable is not tracked")
		}
	}
	return nil
}

func validateHookRetryExecutableFacts(ctx context.Context, process HookProcessAdapter, value HookPlan, inherited []string, windows bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if process == nil {
		return errors.New("hook retry executable authority changed")
	}
	for index, entry := range value.authority.entries {
		environment, err := buildHookEnvironment(HookEnvironmentPolicy(value.authority.source), windows, inherited, value, index)
		if err != nil {
			return errors.New("hook retry executable authority changed")
		}
		directory := entry.TargetRepository
		if value.authority.source == "local" && !filepath.IsAbs(entry.ConfiguredExecutable) && strings.ContainsAny(entry.ConfiguredExecutable, "/\\") {
			directory = entry.SourceRepository
		}
		fact, err := process.Resolve(ctx, HookExecutableRequest{Program: entry.ConfiguredExecutable, Directory: directory, Environment: environment})
		if hookContextSentinel(err) {
			return err
		}
		if err != nil || !fact.Available {
			return errors.New("hook retry executable authority changed")
		}
		if value.authority.source == "local" || (value.authority.source == "portable" && !filepath.IsAbs(entry.ConfiguredExecutable) && strings.ContainsAny(entry.ConfiguredExecutable, "/\\")) {
			if fact.Resolved, err = trustedLocalSourceExecutable(entry.SourceRepository, entry.ConfiguredExecutable, fact.Resolved); err != nil {
				return errors.New("hook retry executable authority changed")
			}
		}
		if entry.Availability == "available" && filepath.Clean(fact.Resolved) != entry.ResolvedExecutable {
			return errors.New("hook retry executable authority changed")
		}
	}
	return nil
}
func retryWorkspacePlan(project domain.Project, workspace domain.Workspace) plan.WorkspacePlan {
	repositories := make([]plan.RepositoryPlan, 0, len(project.Repositories))
	for _, repository := range project.ParentFirst() {
		for _, checkout := range workspace.Checkouts {
			if checkout.RepositoryID == repository.ID {
				repositories = append(repositories, plan.RepositoryPlan{ID: repository.ID, ParentID: repository.ParentID, Base: checkout.Head, Branch: checkout.Branch, Mount: checkout.Mount, Path: checkout.ResolvedPath})
				break
			}
		}
	}
	return plan.WorkspacePlan{Version: plan.Version, Operation: plan.Create, ProjectID: project.ID, WorkspaceName: workspace.Name, WorkspaceID: workspace.ID, RootPath: workspace.RootPath, LogicalRoot: workspace.RootPath, BaseRepository: project.BaseRepository, Repositories: repositories, Steps: planSteps(plan.Create, repositories)}
}
