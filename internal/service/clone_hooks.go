package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/transaction"
)

// CloneLifecycleRequest keeps consent for portable code invocation scoped to a
// single clone command. Shared declarations are intentionally absent: they
// are distribution data, never clone execution authority.
type CloneLifecycleRequest struct {
	Plan        ClonePlan
	RunHooks    bool
	Environment []string
	Windows     bool
	Sink        io.Writer
	Progress    func(transaction.Event)
}

type CloneLifecycleResult struct {
	Core             CloneExecutionResult
	Hooks            []HookPlanEntry
	HooksApplicable  bool
	HooksCompleted   bool
	HooksSkipped     bool
	CompletedHookIDs []string
}

type CloneLifecycleDependencies struct {
	Executor *CloneExecutor
	Runner   hookLifecycleRunner
	Process  HookProcessAdapter
	Git      gitadapter.Git
	ReadFile func(string) ([]byte, error)
}

type CloneLifecycleCoordinator struct{ d CloneLifecycleDependencies }

func NewCloneLifecycleCoordinator() *CloneLifecycleCoordinator {
	return NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{})
}
func NewCloneLifecycleCoordinatorWith(d CloneLifecycleDependencies) *CloneLifecycleCoordinator {
	if d.Executor == nil {
		d.Executor = NewCloneExecutor()
	}
	if d.Runner == nil {
		d.Runner = NewHookRunner()
	}
	if d.Process == nil {
		d.Process = newHookProcessAdapter()
	}
	if d.Git == nil {
		d.Git = gitadapter.NewAdapter("git")
	}
	if d.ReadFile == nil {
		d.ReadFile = os.ReadFile
	}
	return &CloneLifecycleCoordinator{d: d}
}

func (c *CloneLifecycleCoordinator) Plan(ctx context.Context, plan ClonePlan) ([]HookPlanEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := plan.Validate(); err != nil {
		return nil, false, NewError(ErrorValidation, err)
	}
	manifest, err := config.LoadPortableManifest(plan.ManifestBytes())
	if err != nil {
		return nil, false, NewError(ErrorValidation, err)
	}
	var hooks []config.Hook
	if len(manifest.Hooks[config.HookEventPostClone]) != 0 {
		hooks, err = config.CanonicalHookEvent(config.HookEventPostClone, manifest.Hooks[config.HookEventPostClone], manifest.Project.BaseRepository)
	}
	if err != nil {
		return nil, false, NewError(ErrorValidation, err)
	}
	entries := cloneDryRunHookEntries(plan, manifest)
	if !reflect.DeepEqual(plan.Hooks, entries) {
		return nil, false, NewError(ErrorValidation, errors.New("clone plan hook projection does not match manifest authority"))
	}
	return cloneHookPlanEntries(entries), len(hooks) > 0, nil
}

func cloneDryRunHookEntries(plan ClonePlan, manifest config.PortableManifest) []HookPlanEntry {
	entries := make([]HookPlanEntry, 0, len(manifest.Hooks[config.HookEventPostClone])+len(manifest.SharedHooks[config.HookEventPostCreate]))
	appendEntry := func(source, event, policy string, hook config.Hook) {
		repository := hook.Repository
		if repository == "" {
			repository = manifest.Project.BaseRepository
		}
		repo := clonePlanRepository(plan, repository)
		timeout := hook.Timeout
		if timeout == 0 {
			timeout = config.HookDefaultTimeout
		}
		entries = append(entries, HookPlanEntry{Source: source, Event: event, ID: hook.ID, Repository: repository, WorkingDirectory: repo.Path, ConfiguredExecutable: hook.Command[0], Availability: "deferred", Arguments: append([]string(nil), hook.Command[1:]...), Timeout: timeout.String(), ExecutionPolicy: policy})
	}
	for _, hook := range manifest.Hooks[config.HookEventPostClone] {
		appendEntry("portable", config.HookEventPostClone, "requires-run-hooks", hook)
	}
	for _, hook := range manifest.SharedHooks[config.HookEventPostCreate] {
		appendEntry("shared", config.HookEventPostCreate, "inert", hook)
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
}

func clonePlanRepository(plan ClonePlan, id string) ClonePlanRepository {
	for _, repository := range plan.Repositories {
		if repository.ID == id {
			return repository
		}
	}
	return ClonePlanRepository{}
}

func (c *CloneLifecycleCoordinator) Clone(ctx context.Context, q CloneLifecycleRequest) (CloneLifecycleResult, error) {
	entries, applicable, err := c.Plan(ctx, q.Plan)
	if err != nil {
		return CloneLifecycleResult{}, err
	}
	plan := clonePlanCopy(q.Plan)
	plan.runHooks = q.RunHooks
	plan.hookEnvironment = append([]string(nil), q.Environment...)
	plan.hookWindows = q.Windows
	// The executor validates selected portable relative executables while all
	// repositories remain under its private staging root.
	core, err := c.d.Executor.Execute(ctx, plan, q.Progress)
	result := CloneLifecycleResult{Core: core, Hooks: entries, HooksApplicable: applicable, CompletedHookIDs: []string{}}
	if err != nil {
		return result, err
	}
	if !applicable || !q.RunHooks {
		result.HooksSkipped = applicable
		return result, nil
	}
	hookPlan, verifier, err := c.publishedPlan(ctx, plan, core, q.Environment, q.Windows)
	if err != nil {
		return result, NewError(ErrorSetupIncomplete, &SetupIncompleteError{Details: cloneSetupIncompleteDetails(core, HookRunResult{CompletedIDs: []string{}}, err), Cause: err})
	}
	run, runErr := c.d.Runner.Run(ctx, HookRunRequest{DataDir: plan.DataDir, Plan: hookPlan, InheritedEnvironment: append([]string(nil), q.Environment...), Windows: q.Windows, Sink: q.Sink, Revalidate: verifier})
	result.CompletedHookIDs = orderedHookPrefix(hookPlan, run.CompletedIDs)
	if !cloneHookRunCompleted(hookPlan, run, runErr) {
		// The lifecycle and public setup-incomplete details must expose the same
		// validated ordered prefix. A faulty runner is never allowed to surface
		// unknown, reordered, or duplicate IDs through JSON or diagnostics.
		run.CompletedIDs = append([]string(nil), result.CompletedHookIDs...)
		return result, NewError(ErrorSetupIncomplete, &SetupIncompleteError{Details: cloneSetupIncompleteDetails(core, run, runErr), Cause: runErr})
	}
	result.HooksCompleted = true
	return result, nil
}

func cloneHookRunCompleted(plan HookPlan, run HookRunResult, runErr error) bool {
	return runErr == nil && run.Failure == nil && run.Status == "completed" && reflect.DeepEqual(run.CompletedIDs, hookPlanIDs(plan.authority.entries))
}

func (c *CloneLifecycleCoordinator) publishedPlan(ctx context.Context, plan ClonePlan, core CloneExecutionResult, environment []string, windows bool) (HookPlan, HookGenerationVerifier, error) {
	entries, err := clonePublishedHookEntries(plan, core)
	if err != nil {
		return HookPlan{}, nil, err
	}
	statePath := core.StatePath
	hookPlan, err := newHookPlan(hookPlanInput{Operation: "clone", Source: "portable", Event: config.HookEventPostClone, Policy: "requires-run-hooks", ProjectID: plan.Project.ID, ProjectName: plan.Project.Name, BaseRepository: plan.BaseRepository, WorkspaceID: "default", WorkspaceName: "default", SourceLogicalRoot: core.LogicalRoot, TargetLogicalRoot: core.LogicalRoot, SourceBytes: plan.ManifestBytes(), WorkspaceStateBytes: core.workspaceStateBytes, Entries: entries})
	if err != nil {
		return HookPlan{}, nil, NewError(ErrorInternal, err)
	}
	return hookPlan, func(ctx context.Context) (HookGenerationSnapshot, error) {
		base, found := core.Repositories[plan.BaseRepository]
		if !found {
			return HookGenerationSnapshot{}, errors.New("base checkout unavailable")
		}
		head, headErr := c.d.Git.Head(ctx, base.ResolvedPath)
		if hookContextSentinel(headErr) {
			return HookGenerationSnapshot{}, headErr
		}
		if headErr != nil || head != base.Head {
			return HookGenerationSnapshot{}, errors.New("portable manifest authority changed")
		}
		current, trackedErr := c.d.Git.TrackedFile(ctx, base.ResolvedPath, head, "project.wtree.yml")
		if hookContextSentinel(trackedErr) {
			return HookGenerationSnapshot{}, trackedErr
		}
		if trackedErr != nil {
			return HookGenerationSnapshot{}, trackedErr
		}
		if err := c.validatePublishedPortableExecutables(ctx, hookPlan, environment, windows); err != nil {
			return HookGenerationSnapshot{}, err
		}
		state, stateErr := c.d.ReadFile(statePath)
		if stateErr != nil {
			return HookGenerationSnapshot{}, stateErr
		}
		return HookGenerationSnapshot{SourceBytes: current, WorkspaceStateBytes: state}, nil
	}, nil
}

// clonePublishedHookEntries deliberately leaves executable availability
// deferred. A clone has already committed before this point, so the runner
// must durably record the immutable manifest/state authority before any
// post-publication filesystem, Git, or process observation can fail. The
// runner's locked verifier performs those observations immediately before a
// launch and retry rebuilds the same deferred authority.
func clonePublishedHookEntries(plan ClonePlan, core CloneExecutionResult) ([]hookPlanInputEntry, error) {
	entries := make([]hookPlanInputEntry, 0, len(plan.Hooks))
	for _, hook := range plan.Hooks {
		if hook.Source != "portable" || hook.Event != config.HookEventPostClone {
			continue
		}
		checkout, found := core.Repositories[hook.Repository]
		if !found {
			return nil, NewError(ErrorConflict, errors.New("published hook repository is unavailable"))
		}
		timeout, err := time.ParseDuration(hook.Timeout)
		if err != nil || timeout <= 0 {
			return nil, NewError(ErrorValidation, errors.New("portable hook authority changed"))
		}
		entries = append(entries, hookPlanInputEntry{ID: hook.ID, Repository: hook.Repository, SourceRepository: checkout.ResolvedPath, TargetRepository: checkout.ResolvedPath, Branch: checkout.Branch, Head: checkout.Head, ConfiguredExecutable: hook.ConfiguredExecutable, Availability: "deferred", Arguments: append([]string(nil), hook.Arguments...), Timeout: timeout})
	}
	if len(entries) == 0 || len(core.workspaceStateBytes) == 0 {
		return nil, NewError(ErrorConflict, errors.New("published portable hook authority is unavailable"))
	}
	return entries, nil
}

// validatePublishedPortableExecutables is called by the runner while its
// dedicated event lock is held. It repeats checkout, tracked-content and
// physical executable facts so a post-publication replacement cannot turn a
// durable record into permission to launch different code.
func (c *CloneLifecycleCoordinator) validatePublishedPortableExecutables(ctx context.Context, plan HookPlan, inherited []string, windows bool) error {
	for index, entry := range plan.authority.entries {
		head, headErr := c.d.Git.Head(ctx, entry.TargetRepository)
		if hookContextSentinel(headErr) {
			return headErr
		}
		if headErr != nil || head != entry.Head {
			return errors.New("portable hook checkout authority changed")
		}
		env, envErr := buildHookEnvironment(HookEnvironmentPortable, windows, inherited, plan, index)
		if envErr != nil {
			return envErr
		}
		fact, resolveErr := c.d.Process.Resolve(ctx, HookExecutableRequest{Program: entry.ConfiguredExecutable, Directory: entry.TargetRepository, Environment: env})
		if hookContextSentinel(resolveErr) {
			return resolveErr
		}
		if resolveErr != nil || !fact.Available {
			return errors.New("portable hook executable is unavailable")
		}
		if strings.ContainsAny(entry.ConfiguredExecutable, "/\\") {
			relative := filepath.ToSlash(strings.ReplaceAll(entry.ConfiguredExecutable, `\`, "/"))
			if _, trackedErr := c.d.Git.TrackedFile(ctx, entry.TargetRepository, entry.Head, relative); trackedErr != nil {
				if hookContextSentinel(trackedErr) {
					return trackedErr
				}
				return errors.New("portable hook executable is not tracked")
			}
			trusted, trustedErr := trustedLocalSourceExecutable(entry.TargetRepository, entry.ConfiguredExecutable, fact.Resolved)
			if trustedErr != nil {
				return errors.New("portable hook executable escapes checkout")
			}
			fact.Resolved = trusted
		}
		if entry.Availability == "available" && filepath.Clean(fact.Resolved) != entry.ResolvedExecutable {
			return errors.New("portable hook executable changed")
		}
	}
	return nil
}

func portableHookEnvironmentForEntry(windows bool, inherited []string, plan ClonePlan, hook config.Hook, checkout store.CheckoutState) ([]string, error) {
	input := hookPlanInput{Operation: "clone", Source: "portable", Event: config.HookEventPostClone, Policy: "requires-run-hooks", ProjectID: plan.Project.ID, ProjectName: plan.Project.Name, BaseRepository: plan.BaseRepository, WorkspaceID: "default", WorkspaceName: "default", SourceLogicalRoot: plan.LogicalRoot, TargetLogicalRoot: plan.LogicalRoot, SourceBytes: []byte("x"), WorkspaceStateBytes: []byte("y"), Entries: []hookPlanInputEntry{{ID: hook.ID, Repository: hook.Repository, SourceRepository: checkout.ResolvedPath, TargetRepository: checkout.ResolvedPath, Branch: checkout.Branch, Head: checkout.Head, ConfiguredExecutable: hook.Command[0], ResolvedExecutable: checkout.ResolvedPath, Availability: "available", Timeout: hook.Timeout}}}
	p, err := newHookPlan(input)
	if err != nil {
		return nil, err
	}
	return buildHookEnvironment(HookEnvironmentPortable, windows, inherited, p, 0)
}

func cloneSetupIncompleteDetails(core CloneExecutionResult, run HookRunResult, runnerErr error) SetupIncompleteDetails {
	details := SetupIncompleteDetails{Operation: "clone", CoreStatus: "completed", SetupStatus: "incomplete", Event: config.HookEventPostClone, FailureKind: HookFailureRecord, CompletedHookIDs: append([]string(nil), run.CompletedIDs...), RetryCommand: "wtree hooks retry default"}
	if run.Failure != nil {
		details.FailureKind, details.HookID, details.Repository = run.Failure.Kind, run.Failure.HookID, run.Failure.RepositoryID
		if run.Failure.Kind == HookFailureNonZero {
			details.ExitCode = run.Failure.ExitCode
		}
		details.Timeout = run.Failure.Kind == HookFailureTimeout && run.Failure.Timeout
	}
	return details
}

// validateStagedCloneHooks executes before any public publication. It is
// called by CloneExecutor only for the explicit --run-hooks authorization.
func (executor *CloneExecutor) validateStagedCloneHooks(ctx context.Context, plan ClonePlan, paths map[string]string, heads map[string]string) error {
	if !plan.runHooks {
		return nil
	}
	manifest, err := config.LoadPortableManifest(plan.ManifestBytes())
	if err != nil {
		return err
	}
	hooks, err := config.CanonicalHookEvent(config.HookEventPostClone, manifest.Hooks[config.HookEventPostClone], manifest.Project.BaseRepository)
	if err != nil {
		return err
	}
	for _, hook := range hooks {
		path, found := paths[hook.Repository]
		if !found {
			return errors.New("staged hook repository is unavailable")
		}
		environment := plan.hookEnvironment
		if environment == nil {
			environment = os.Environ()
		}
		fact, resolveErr := hookResolveExecutable(ctx, HookExecutableRequest{Program: hook.Command[0], Directory: path, Environment: environment}, plan.hookWindows || runtime.GOOS == "windows")
		if resolveErr != nil || !fact.Available {
			return errors.New("portable hook executable is unavailable")
		}
		if strings.ContainsAny(hook.Command[0], "/\\") {
			relative := filepath.ToSlash(strings.ReplaceAll(hook.Command[0], `\`, "/"))
			if _, trackedErr := executor.dependencies.Git.TrackedFile(ctx, path, heads[hook.Repository], relative); trackedErr != nil {
				return errors.New("portable hook executable is not tracked")
			}
			if _, trustedErr := trustedLocalSourceExecutable(path, hook.Command[0], fact.Resolved); trustedErr != nil {
				return errors.New("portable hook executable escapes checkout")
			}
		}
	}
	return nil
}
