package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/transaction"
)

type CreateLifecycleRequest struct {
	Project     domain.Project
	Workspace   WorkspacePlanRequest
	NoHooks     bool
	Reconcile   bool
	Environment []string
	Windows     bool
	Sink        io.Writer
	Progress    func(transaction.Event)
}
type CreateLifecycleResult struct {
	Core             CreateResult    `json:"-"`
	Hooks            []HookPlanEntry `json:"-"`
	DryRun           bool            `json:"-"`
	HooksApplicable  bool            `json:"-"`
	HooksCompleted   bool            `json:"-"`
	HooksSkipped     bool            `json:"-"`
	CompletedHookIDs []string        `json:"-"`
}
type hookLifecycleRunner interface {
	Run(context.Context, HookRunRequest) (HookRunResult, error)
}
type CreateLifecycleDependencies struct {
	Planner   *WorkspacePlanner
	Creator   *WorkspaceCreator
	Runner    hookLifecycleRunner
	Process   HookProcessAdapter
	ReadFile  func(string) ([]byte, error)
	Reconcile func(context.Context, string, domain.Project, func() error) error
}
type CreateLifecycleCoordinator struct{ d CreateLifecycleDependencies }

// createHookPreflight is the immutable hook authority captured before the
// create transaction starts. The generation is deliberately kept private: it
// is only used to prove that the definition which was preflighted is still the
// definition observed while the project lock is held.
type createHookPreflight struct {
	generation hookFileGeneration
	plan       HookPlan
	applicable bool
}

func NewCreateLifecycleCoordinator() *CreateLifecycleCoordinator {
	return NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{})
}
func NewCreateLifecycleCoordinatorWith(d CreateLifecycleDependencies) *CreateLifecycleCoordinator {
	if d.Planner == nil {
		d.Planner = NewWorkspacePlanner()
	}
	if d.Creator == nil {
		d.Creator = NewWorkspaceCreator()
	}
	if d.Process == nil {
		d.Process = newHookProcessAdapter()
	}
	if d.Runner == nil {
		d.Runner = NewHookRunnerWith(HookRunnerDependencies{Process: d.Process})
	}
	if d.ReadFile == nil {
		d.ReadFile = os.ReadFile
	}
	if d.Reconcile == nil {
		resolver := NewResolver()
		d.Reconcile = resolver.reconcileProjectWith
	}
	return &CreateLifecycleCoordinator{d}
}
func (r CreateLifecycleResult) MarshalJSON() ([]byte, error) {
	p := r.Core.Plan
	if !r.HooksApplicable {
		return json.Marshal(p)
	}
	if r.DryRun {
		return json.Marshal(struct {
			plan.WorkspacePlan
			Hooks []HookPlanEntry `json:"hooks"`
		}{p, cloneHookPlanEntries(r.Hooks)})
	}
	return json.Marshal(struct {
		plan.WorkspacePlan
		HooksCompleted bool     `json:"hooksCompleted"`
		HooksSkipped   bool     `json:"hooksSkipped"`
		Completed      []string `json:"completedHookIds"`
	}{p, r.HooksCompleted, r.HooksSkipped, cloneCreateHookIDs(r.CompletedHookIDs)})
}

func cloneHookPlanEntries(entries []HookPlanEntry) []HookPlanEntry {
	if entries == nil {
		return nil
	}
	cloned := make([]HookPlanEntry, len(entries))
	copy(cloned, entries)
	for i := range cloned {
		cloned[i].Arguments = append([]string(nil), cloned[i].Arguments...)
	}
	return cloned
}

func cloneCreateHookIDs(ids []string) []string {
	cloned := make([]string, len(ids))
	copy(cloned, ids)
	return cloned
}

func (c *CreateLifecycleCoordinator) Plan(ctx context.Context, q CreateLifecycleRequest) (CreateLifecycleResult, error) {
	result, _, err := c.planWithPreflight(ctx, q)
	return result, err
}

func (c *CreateLifecycleCoordinator) planWithPreflight(ctx context.Context, q CreateLifecycleRequest) (CreateLifecycleResult, createHookPreflight, error) {
	q.Workspace.Operation = plan.Create
	value, err := c.d.Planner.Plan(ctx, q.Project, q.Workspace)
	if err != nil {
		return CreateLifecycleResult{}, createHookPreflight{}, err
	}
	result := CreateLifecycleResult{Core: CreateResult{Plan: value}, DryRun: true}
	preflight, err := c.preflight(ctx, q, value, q.NoHooks)
	if err != nil {
		return result, createHookPreflight{}, err
	}
	result.HooksApplicable = preflight.applicable
	result.Hooks = preflight.plan.Entries()
	return result, preflight, nil
}
func (c *CreateLifecycleCoordinator) Create(ctx context.Context, q CreateLifecycleRequest) (CreateLifecycleResult, error) {
	q.Workspace.Operation = plan.Create
	pre, authority, err := c.planWithPreflight(ctx, q)
	if err != nil {
		return pre, err
	}
	if q.Reconcile {
		if err := c.d.Reconcile(ctx, q.Workspace.DataDir, q.Project, func() error {
			return c.requireLockedPreflight(ctx, q, pre.Core.Plan, authority, q.NoHooks)
		}); err != nil {
			return pre, err
		}
	}
	if q.NoHooks {
		pre.DryRun = false
		pre.HooksSkipped = pre.HooksApplicable
		pre.Hooks = nil
		pre.CompletedHookIDs = []string{}
		core, err := c.d.Creator.createWithResultRevalidate(ctx, q.Project, q.Workspace, q.Progress, func(ctx context.Context, locked plan.WorkspacePlan) error {
			return c.requireLockedPreflight(ctx, q, locked, authority, true)
		})
		pre.Core = core
		return pre, err
	}
	core, err := c.d.Creator.createWithResultRevalidate(ctx, q.Project, q.Workspace, q.Progress, func(ctx context.Context, p plan.WorkspacePlan) error {
		return c.requireLockedPreflight(ctx, q, p, authority, false)
	})
	result := CreateLifecycleResult{Core: core, HooksApplicable: pre.HooksApplicable, Hooks: pre.Hooks, CompletedHookIDs: []string{}}
	if err != nil {
		return result, err
	}
	if !result.HooksApplicable {
		return result, nil
	}
	run, e := c.d.Runner.Run(ctx, HookRunRequest{DataDir: q.Workspace.DataDir, Plan: authority.plan, InheritedEnvironment: append([]string{}, q.Environment...), Windows: q.Windows, Sink: q.Sink, Revalidate: c.generationVerifier(q, core)})
	result.CompletedHookIDs = orderedHookPrefix(authority.plan, run.CompletedIDs)
	if e != nil || run.Failure != nil {
		run.CompletedIDs = result.CompletedHookIDs
		details := createSetupIncompleteDetails(core.Plan, run, e)
		return result, NewError(ErrorSetupIncomplete, &SetupIncompleteError{Details: details})
	}
	if run.Status != "completed" || !reflect.DeepEqual(run.CompletedIDs, hookPlanIDs(authority.plan.authority.entries)) {
		run.CompletedIDs = result.CompletedHookIDs
		return result, NewError(ErrorSetupIncomplete, &SetupIncompleteError{Details: createSetupIncompleteDetails(core.Plan, run, errors.New("invalid hook runner result"))})
	}
	result.HooksCompleted = true
	return result, nil
}

func orderedHookPrefix(plan HookPlan, completed []string) []string {
	expected := hookPlanIDs(plan.authority.entries)
	prefix := make([]string, 0, len(completed))
	for index, id := range completed {
		if index >= len(expected) || id != expected[index] {
			break
		}
		prefix = append(prefix, id)
	}
	return prefix
}

func (c *CreateLifecycleCoordinator) generationVerifier(q CreateLifecycleRequest, core CreateResult) HookGenerationVerifier {
	return func(ctx context.Context) (HookGenerationSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return HookGenerationSnapshot{}, err
		}
		configBytes, err := c.d.ReadFile(q.Project.ConfigPath)
		if err != nil {
			return HookGenerationSnapshot{}, err
		}
		stateBytes, err := c.d.ReadFile(WorkspaceStatePath(q.Workspace.DataDir, q.Project.ID, core.Plan.WorkspaceID))
		if err != nil {
			return HookGenerationSnapshot{}, err
		}
		return HookGenerationSnapshot{SourceBytes: configBytes, WorkspaceStateBytes: stateBytes}, nil
	}
}

func createSetupIncompleteDetails(value plan.WorkspacePlan, run HookRunResult, runnerErr error) SetupIncompleteDetails {
	details := SetupIncompleteDetails{
		Operation:        "create",
		CoreStatus:       "completed",
		SetupStatus:      "incomplete",
		Event:            "post-create",
		FailureKind:      HookFailureRecord,
		CompletedHookIDs: append([]string{}, run.CompletedIDs...),
		RetryCommand:     "wtree hooks retry " + value.WorkspaceName,
	}
	if runnerErr != nil || run.Failure == nil {
		return details
	}
	details.FailureKind = run.Failure.Kind
	details.HookID = run.Failure.HookID
	details.Repository = run.Failure.RepositoryID
	if run.Failure.Kind == HookFailureNonZero {
		details.ExitCode = run.Failure.ExitCode
	}
	details.Timeout = run.Failure.Kind == HookFailureTimeout && run.Failure.Timeout
	return details
}
func (c *CreateLifecycleCoordinator) preflight(ctx context.Context, q CreateLifecycleRequest, p plan.WorkspacePlan, deferred bool) (createHookPreflight, error) {
	if err := ctx.Err(); err != nil {
		return createHookPreflight{}, err
	}
	generation, err := captureHookFileGeneration(q.Project.ConfigPath)
	if err != nil {
		return createHookPreflight{}, NewError(ErrorValidation, errors.New("local configuration is unavailable"))
	}
	local, err := config.LoadProject(generation.data)
	if err != nil {
		return createHookPreflight{}, NewError(ErrorValidation, err)
	}
	if local.Project.ID != q.Project.ID || local.Project.BaseRepository != q.Project.BaseRepository {
		return createHookPreflight{}, NewError(ErrorConflict, errors.New("local hook configuration project changed"))
	}
	if err := createHookConfigPlacementMatches(q.Project); err != nil {
		return createHookPreflight{}, err
	}
	if err := hookManagementTopologyMatches(q.Project, local); err != nil {
		return createHookPreflight{}, err
	}
	raw := local.Hooks[config.HookEventPostCreate]
	if len(raw) == 0 {
		return createHookPreflight{generation: generation}, nil
	}
	canonical, err := config.CanonicalHookEvent(config.HookEventPostCreate, raw, q.Project.BaseRepository)
	if err != nil {
		return createHookPreflight{}, NewError(ErrorValidation, err)
	}
	state, err := store.WorkspaceBytes(workspaceState(p))
	if err != nil {
		return createHookPreflight{}, err
	}
	source := map[string]string{}
	for _, repo := range q.Project.Repositories {
		source[repo.ID] = repo.SourcePath
	}
	entries := make([]hookPlanInputEntry, 0, len(canonical))
	for _, h := range canonical {
		target := ""
		branch, head := "", ""
		for _, r := range p.Repositories {
			if r.ID == h.Repository {
				target = r.Path
				branch = r.Branch
				head = r.Base
			}
		}
		if target == "" || source[h.Repository] == "" {
			return createHookPreflight{}, NewError(ErrorValidation, errors.New("hook repository missing"))
		}
		exe := h.Command[0]
		dir := target
		if !filepath.IsAbs(exe) && containsPathSeparator(exe) {
			dir = source[h.Repository]
		}
		fact := HookExecutableFact{}
		if !deferred {
			fact, err = c.d.Process.Resolve(ctx, HookExecutableRequest{Program: exe, Directory: dir, Environment: q.Environment})
			if hookContextSentinel(err) {
				return createHookPreflight{}, err
			}
			if err != nil || !fact.Available {
				return createHookPreflight{}, NewError(ErrorValidation, errors.New("hook executable unavailable"))
			}
			if trusted, trustedErr := trustedLocalSourceExecutable(source[h.Repository], exe, fact.Resolved); trustedErr != nil {
				return createHookPreflight{}, NewError(ErrorValidation, errors.New("hook executable escapes its source repository"))
			} else {
				fact.Resolved = trusted
			}
		}
		availability := "available"
		if deferred {
			availability = "deferred"
		}
		entries = append(entries, hookPlanInputEntry{ID: h.ID, Repository: h.Repository, SourceRepository: source[h.Repository], TargetRepository: target, Branch: branch, Head: head, ConfiguredExecutable: exe, ResolvedExecutable: fact.Resolved, Availability: availability, Arguments: append([]string{}, h.Command[1:]...), Timeout: h.Timeout})
	}
	hp, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: q.Project.ID, ProjectName: q.Project.Name, BaseRepository: q.Project.BaseRepository, WorkspaceID: p.WorkspaceID, WorkspaceName: p.WorkspaceName, SourceLogicalRoot: q.Project.LogicalRoot, TargetLogicalRoot: p.LogicalRoot, SourceBytes: generation.data, WorkspaceStateBytes: state, Entries: entries})
	if err != nil {
		return createHookPreflight{}, err
	}
	return createHookPreflight{generation: generation, plan: hp, applicable: true}, nil
}

func (c *CreateLifecycleCoordinator) requireLockedPreflight(ctx context.Context, q CreateLifecycleRequest, p plan.WorkspacePlan, before createHookPreflight, deferred bool) error {
	if err := before.generation.verify(); err != nil {
		return NewError(ErrorConflict, errors.New("local hook configuration changed during locked revalidation"))
	}
	after, err := c.preflight(ctx, q, p, deferred)
	if err != nil {
		return err
	}
	if !sameHookFileGeneration(before.generation, after.generation) || before.applicable != after.applicable {
		return NewError(ErrorConflict, errors.New("local hook configuration changed during locked revalidation"))
	}
	if !before.applicable {
		return nil
	}
	if before.plan.SourceSHA256() != after.plan.SourceSHA256() || before.plan.Digest() != after.plan.Digest() || !reflect.DeepEqual(before.plan.Entries(), after.plan.Entries()) {
		return NewError(ErrorConflict, errors.New("local hook configuration changed during locked revalidation"))
	}
	return nil
}

func sameHookFileGeneration(left, right hookFileGeneration) bool {
	return left.path == right.path && left.info != nil && right.info != nil && os.SameFile(left.info, right.info) && left.info.Mode() == right.info.Mode() && bytes.Equal(left.data, right.data)
}

func createHookConfigPlacementMatches(project domain.Project) error {
	for _, repository := range project.Repositories {
		if repository.ID != project.BaseRepository {
			continue
		}
		base, baseErr := filepath.EvalSymlinks(repository.SourcePath)
		configPath, configErr := filepath.EvalSymlinks(project.ConfigPath)
		if baseErr == nil && configErr == nil && filepath.Clean(requestedConfigPath(base)) == filepath.Clean(configPath) {
			return nil
		}
		break
	}
	return NewError(ErrorConflict, errors.New("resolved project configuration placement changed"))
}
func containsPathSeparator(s string) bool { return strings.ContainsAny(s, "/\\") }

// trustedLocalSourceExecutable constrains only local relative declarations
// with an explicit separator. Bare PATH names and explicit absolute programs
// retain their existing resolver policy.
func trustedLocalSourceExecutable(source, configured, resolved string) (string, error) {
	if filepath.IsAbs(configured) || !containsPathSeparator(configured) {
		return resolved, nil
	}
	lexical := filepath.Clean(filepath.Join(source, filepath.FromSlash(strings.ReplaceAll(configured, `\`, "/"))))
	rel, err := filepath.Rel(filepath.Clean(source), lexical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("source-relative executable escapes repository")
	}
	trustedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	trustedExecutable, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	rel, err = filepath.Rel(trustedSource, trustedExecutable)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("source-relative executable escapes repository")
	}
	return trustedExecutable, nil
}
