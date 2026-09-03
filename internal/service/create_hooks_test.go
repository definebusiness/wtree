package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestCreateDryRunHookJSONProjectionCopiesArguments(t *testing.T) {
	hooks := createLifecycleHookPlan(t)
	core := CreateResult{Plan: createLifecycleWorkspacePlan(t)}
	plain, err := json.Marshal(core.Plan)
	if err != nil {
		t.Fatal(err)
	}
	withoutHooks, err := json.Marshal(CreateLifecycleResult{Core: core})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, withoutHooks) {
		t.Fatalf("hook-free JSON changed\n got: %s\nwant: %s", withoutHooks, plain)
	}
	entries := hooks.Entries()
	result := CreateLifecycleResult{Core: core, DryRun: true, HooksApplicable: true, Hooks: cloneHookPlanEntries(entries)}
	entries[0].Arguments[0] = "changed-after-result"
	if got := result.Hooks[0].Arguments[0]; got != "--literal" {
		t.Fatalf("result aliases HookPlan arguments: %q", got)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["hooks"]; !ok || len(decoded["hooks"]) == 0 {
		t.Fatalf("dry hook projection missing hooks: %s", data)
	}
	if _, ok := decoded["hooksCompleted"]; ok {
		t.Fatalf("dry hook projection included status: %s", data)
	}
	for _, test := range []struct {
		name, wantCompleted, wantSkipped string
		result                           CreateLifecycleResult
	}{
		{name: "completed", wantCompleted: "true", wantSkipped: "false", result: CreateLifecycleResult{Core: core, HooksApplicable: true, HooksCompleted: true, CompletedHookIDs: []string{"setup"}}},
		{name: "skipped", wantCompleted: "false", wantSkipped: "true", result: CreateLifecycleResult{Core: core, HooksApplicable: true, HooksSkipped: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.result)
			if err != nil {
				t.Fatal(err)
			}
			var projection map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &projection); err != nil {
				t.Fatal(err)
			}
			if got := string(projection["hooksCompleted"]); got != test.wantCompleted || string(projection["hooksSkipped"]) != test.wantSkipped || bytes.Equal(projection["completedHookIds"], []byte("null")) {
				t.Fatalf("real projection = %s", encoded)
			}
			if _, exists := projection["hooks"]; exists {
				t.Fatalf("real projection included dry hooks: %s", encoded)
			}
		})
	}
}

func TestCreateHookLockedConfigChangeRejectsBeforeEffects(t *testing.T) {
	project, configPath, value := createLifecycleConfigFixture(t, true)
	coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{Process: &createHooksProcessFake{}})
	request := CreateLifecycleRequest{Project: project}
	before, err := coordinator.preflight(context.Background(), request, value, true)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(original, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	err = coordinator.requireLockedPreflight(context.Background(), request, value, before, true)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Kind != ErrorConflict {
		t.Fatalf("locked change error = %v, want conflict", err)
	}
}

func TestCreateNoHooksLockedValidationSkipsResolverAndRunner(t *testing.T) {
	project, _, value := createLifecycleConfigFixture(t, true)
	process := &createHooksProcessFake{}
	coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{Process: process})
	request := CreateLifecycleRequest{Project: project, NoHooks: true}
	before, err := coordinator.preflight(context.Background(), request, value, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.requireLockedPreflight(context.Background(), request, value, before, true); err != nil {
		t.Fatal(err)
	}
	if process.resolveCalls != 0 {
		t.Fatalf("no-hooks locked validation resolved executable %d times", process.resolveCalls)
	}
}

func TestHookCreateReadsActualPublishedStateAndProjectsFailureDetails(t *testing.T) {
	project, configPath, value := createLifecycleConfigFixture(t, true)
	dataDir := t.TempDir()
	statePath := WorkspaceStatePath(dataDir, project.ID, value.WorkspaceID)
	reads := []string{}
	coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{ReadFile: func(path string) ([]byte, error) {
		reads = append(reads, path)
		switch path {
		case configPath:
			return []byte("config-generation"), nil
		case statePath:
			return []byte("mutated-published-state"), nil
		default:
			return nil, os.ErrNotExist
		}
	}})
	snapshot, err := coordinator.generationVerifier(CreateLifecycleRequest{Project: project, Workspace: WorkspacePlanRequest{DataDir: dataDir}}, CreateResult{Plan: value})(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reads, []string{configPath, statePath}) || string(snapshot.WorkspaceStateBytes) != "mutated-published-state" {
		t.Fatalf("runner generation reads = %#v snapshot=%q", reads, snapshot.WorkspaceStateBytes)
	}
	exit := 17
	details := createSetupIncompleteDetails(value, HookRunResult{CompletedIDs: []string{"first"}, Failure: &HookRunFailure{Kind: HookFailureNonZero, HookID: "second", RepositoryID: "root", ExitCode: &exit, Timeout: true}}, nil)
	if details.HookID != "second" || details.Repository != "root" || details.ExitCode == nil || *details.ExitCode != 17 || details.Timeout || !reflect.DeepEqual(details.CompletedHookIDs, []string{"first"}) {
		t.Fatalf("setup details = %#v", details)
	}
	if got := createSetupIncompleteDetails(value, HookRunResult{}, errors.New("private path /secret")); got.FailureKind != HookFailureRecord || got.HookID != "" || got.CompletedHookIDs == nil {
		t.Fatalf("unexpected runner error details = %#v", got)
	}
}

func TestCreateHookFailureDetailsProjectEveryStableRunnerKind(t *testing.T) {
	value := createLifecycleWorkspacePlan(t)
	kinds := []HookFailureKind{HookFailureNonZero, HookFailureMissing, HookFailureLaunch, HookFailureTimeout, HookFailureCanceled, HookFailureOutput, HookFailureGeneration, HookFailureRecord, HookFailureLock}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			exit := 31
			run := HookRunResult{CompletedIDs: []string{"first"}, Failure: &HookRunFailure{Kind: kind, HookID: "second", RepositoryID: "root", ExitCode: &exit, Timeout: kind == HookFailureTimeout}}
			details := createSetupIncompleteDetails(value, run, nil)
			if details.FailureKind != kind || details.HookID != "second" || details.Repository != "root" || details.CompletedHookIDs == nil || !reflect.DeepEqual(details.CompletedHookIDs, []string{"first"}) || (kind == HookFailureTimeout) != details.Timeout {
				t.Fatalf("details = %#v", details)
			}
			if kind == HookFailureNonZero && (details.ExitCode == nil || *details.ExitCode != exit) {
				t.Fatalf("nonzero exit omitted: %#v", details)
			}
			if kind != HookFailureNonZero && details.ExitCode != nil {
				t.Fatalf("non-nonzero exit leaked: %#v", details)
			}
		})
	}
}

func TestCreateHookRunnerObservesPublishedStateAfterProjectUnlock(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if _, err := NewInitializer().Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: repository.Path, ProjectPath: repository.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.ReadProjectFile(resolution.Project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(resolution.Project.ConfigPath, local); err != nil {
		t.Fatal(err)
	}
	runner := &createHooksRunnerFake{run: func(ctx context.Context, request HookRunRequest) (HookRunResult, error) {
		handle, err := lock.Manager{}.ProjectLock(ctx, data, resolution.Project.ID, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("project lock remained held at hook boundary: %v", err)
		}
		if err := handle.Unlock(); err != nil {
			t.Fatal(err)
		}
		state, err := store.ReadWorkspace(WorkspaceStatePath(data, resolution.Project.ID, request.Plan.authority.workspaceID))
		if err != nil || state.ID != request.Plan.authority.workspaceID {
			t.Fatalf("published state at hook boundary = %#v, %v", state, err)
		}
		snapshot, err := request.Revalidate(ctx)
		if err != nil || request.Plan.SourceSHA256() != digest(snapshot.SourceBytes) || request.Plan.WorkspaceStateSHA256() != digest(snapshot.WorkspaceStateBytes) {
			t.Fatalf("hook generations = %#v, %v", snapshot, err)
		}
		return HookRunResult{Status: "completed", CompletedIDs: []string{"setup"}}, nil
	}}
	process := &createHooksProcessFake{}
	coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{Runner: runner, Process: process})
	result, err := coordinator.Create(context.Background(), CreateLifecycleRequest{Project: resolution.Project, Workspace: WorkspacePlanRequest{WorkspaceName: "feature/hooks", TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.HooksCompleted || !reflect.DeepEqual(result.CompletedHookIDs, []string{"setup"}) || runner.calls != 1 || process.resolveCalls == 0 {
		t.Fatalf("create hook result=%#v runner=%d resolver=%d", result, runner.calls, process.resolveCalls)
	}
}

func TestCreateHookRejectsInconsistentRunnerCompletionProjection(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if _, err := NewInitializer().Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: repository.Path, ProjectPath: repository.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.ReadProjectFile(resolution.Project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	local.Hooks = config.HookEvents{"post-create": {
		{ID: "first", Command: []string{"first"}},
		{ID: "second", Command: []string{"second"}},
	}}
	if err := config.WriteProjectFile(resolution.Project.ConfigPath, local); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		run    HookRunResult
		ok     bool
		prefix []string
	}{
		{name: "incomplete-empty", run: HookRunResult{Status: "incomplete"}, prefix: []string{}},
		{name: "completed-subset", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first"}}, prefix: []string{"first"}},
		{name: "reordered", run: HookRunResult{Status: "completed", CompletedIDs: []string{"second", "first"}}, prefix: []string{}},
		{name: "unknown", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first", "unknown"}}, prefix: []string{"first"}},
		{name: "duplicate", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first", "first"}}, prefix: []string{"first"}},
		{name: "inconsistent-status", run: HookRunResult{Status: "incomplete", CompletedIDs: []string{"first", "second"}}, prefix: []string{"first", "second"}},
		{name: "exact-completed", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first", "second"}}, ok: true, prefix: []string{"first", "second"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &createHooksRunnerFake{run: func(context.Context, HookRunRequest) (HookRunResult, error) { return test.run, nil }}
			coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{Runner: runner, Process: &createHooksProcessFake{}})
			result, createErr := coordinator.Create(context.Background(), CreateLifecycleRequest{Project: resolution.Project, Workspace: WorkspacePlanRequest{WorkspaceName: "feature/" + test.name, TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}})
			if !reflect.DeepEqual(result.CompletedHookIDs, test.prefix) {
				t.Fatalf("completed projection=%#v, want %#v", result.CompletedHookIDs, test.prefix)
			}
			if test.ok {
				if createErr != nil || !result.HooksCompleted {
					t.Fatalf("valid runner result=%#v err=%v", result, createErr)
				}
				return
			}
			var application *Error
			if !errors.As(createErr, &application) || application.Kind != ErrorSetupIncomplete {
				t.Fatalf("inconsistent runner error=%v", createErr)
			}
			details, ok := SetupIncompleteFrom(createErr)
			if !ok || details.FailureKind != HookFailureRecord || !reflect.DeepEqual(details.CompletedHookIDs, test.prefix) {
				t.Fatalf("inconsistent runner details=%#v", details)
			}
		})
	}
}

func TestTrustedLocalSourceExecutableUsesProductionResolutionAndContainment(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	name, script := "setup", "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		name, script = "setup.cmd", "@exit /b 0\r\n"
	}
	outsideProgram := filepath.Join(outside, "outside"+filepath.Ext(name))
	if err := os.WriteFile(outsideProgram, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := newHookProcessAdapter()
	for _, test := range []struct {
		name        string
		program     string
		prepare     func(t *testing.T)
		wantErr     bool
		wantTrusted string
	}{
		{name: "lexical-parent-escape", program: filepath.Join("..", filepath.Base(outside), filepath.Base(outsideProgram)), wantErr: true},
		{name: "symlink-escape", program: filepath.Join("hooks", "escape"+filepath.Ext(name)), prepare: func(t *testing.T) {
			if err := os.MkdirAll(filepath.Join(source, "hooks"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outsideProgram, filepath.Join(source, "hooks", "escape"+filepath.Ext(name))); err != nil {
				t.Fatal(err)
			}
		}, wantErr: true},
		{name: "nested-regular", program: filepath.Join("hooks", "nested", name), prepare: func(t *testing.T) {
			path := filepath.Join(source, "hooks", "nested", name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}, wantTrusted: filepath.Join(source, "hooks", "nested", name)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.prepare != nil {
				test.prepare(t)
			}
			fact, err := adapter.Resolve(context.Background(), HookExecutableRequest{Program: test.program, Directory: source})
			if err != nil || !fact.Available {
				t.Fatalf("production resolve=%#v err=%v", fact, err)
			}
			trusted, trustedErr := trustedLocalSourceExecutable(source, test.program, fact.Resolved)
			if test.wantErr {
				if trustedErr == nil {
					t.Fatalf("trusted executable=%q, want containment error", trusted)
				}
				return
			}
			wantTrusted, evalErr := filepath.EvalSymlinks(test.wantTrusted)
			if evalErr != nil || trustedErr != nil || trusted != wantTrusted {
				t.Fatalf("trusted executable=%q err=%v want=%q eval=%v", trusted, trustedErr, wantTrusted, evalErr)
			}
		})
	}
}

func TestCreateHookReconcileCASRevalidatesCapturedAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, project domain.Project, executable string)
	}{
		{name: "local-generation", mutate: func(t *testing.T, project domain.Project, _ string) {
			before, err := os.ReadFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(project.ConfigPath, append(before, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "executable-availability", mutate: func(t *testing.T, _ domain.Project, executable string) {
			if err := os.Remove(executable); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := testutil.NewPushedGitRepository(t)
			repository.CommitFile("root.txt", "root\n", "root")
			data := t.TempDir()
			if _, err := NewInitializer().Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data}); err != nil {
				t.Fatal(err)
			}
			project, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: repository.Path, ProjectPath: repository.Path, DataDir: data})
			if err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(repository.Path, "hooks", "setup")
			if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			local, err := config.ReadProjectFile(project.Project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			local.Version = config.ProjectConfigVersion3
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{filepath.Join("hooks", "setup")}}}}
			if err := config.WriteProjectFile(project.Project.ConfigPath, local); err != nil {
				t.Fatal(err)
			}

			registryPath := filepath.Join(data, "registry.json")
			registry, err := store.ReadRegistry(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			registered := registry.Projects[project.Project.ID]
			registered.ConfigPath = filepath.Join(repository.Path, "stale.wtree.yml")
			registry.Projects[project.Project.ID] = registered
			if err := store.WriteRegistry(registryPath, registry); err != nil {
				t.Fatal(err)
			}
			beforeRegistry, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}

			resolver := NewResolver()
			write := resolver.writeRegistryCAS
			resolver.writeRegistryCAS = func(path string, value store.Registry, compare func() error) error {
				return write(path, value, func() error {
					test.mutate(t, project.Project, executable)
					return compare()
				})
			}
			runner := &createHooksRunnerFake{run: func(context.Context, HookRunRequest) (HookRunResult, error) {
				t.Fatal("runner started after reconciliation authority changed")
				return HookRunResult{}, nil
			}}
			coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{Process: newHookProcessAdapter(), Runner: runner, Reconcile: resolver.reconcileProjectWith})
			target := filepath.Join(t.TempDir(), "workspace")
			_, createErr := coordinator.Create(context.Background(), CreateLifecycleRequest{Project: project.Project, Reconcile: true, Workspace: WorkspacePlanRequest{WorkspaceName: "feature/reconcile", TargetPath: target, DataDir: data}})
			var application *Error
			if !errors.As(createErr, &application) || (application.Kind != ErrorConflict && application.Kind != ErrorValidation) {
				t.Fatalf("reconciliation authority error=%v", createErr)
			}
			afterRegistry, readErr := os.ReadFile(registryPath)
			if readErr != nil || !bytes.Equal(beforeRegistry, afterRegistry) {
				t.Fatalf("registry changed after rejected CAS: %v\n got=%q\nwant=%q", readErr, afterRegistry, beforeRegistry)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("reconciliation rejection created workspace: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(data, "projects", project.Project.ID, "hooks")); !os.IsNotExist(statErr) {
				t.Fatalf("reconciliation rejection created hook authority: %v", statErr)
			}
		})
	}
}

func TestCreateHookCoordinatorReconcilesStaleRegistryBeforeCreate(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if _, err := NewInitializer().Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: repository.Path, ProjectPath: repository.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.ReadProjectFile(resolution.Project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(resolution.Project.ConfigPath, local); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	registry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Projects[resolution.Project.ID]
	entry.ConfigPath = filepath.Join(repository.Path, "former-location.wtree.yml")
	registry.Projects[resolution.Project.ID] = entry
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	runner := &createHooksRunnerFake{run: func(context.Context, HookRunRequest) (HookRunResult, error) {
		return HookRunResult{Status: "completed", CompletedIDs: []string{"setup"}}, nil
	}}
	coordinator := NewCreateLifecycleCoordinatorWith(CreateLifecycleDependencies{Process: &createHooksProcessFake{}, Runner: runner})
	result, err := coordinator.Create(context.Background(), CreateLifecycleRequest{Project: resolution.Project, Reconcile: true, Workspace: WorkspacePlanRequest{WorkspaceName: "feature/relocated", TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}})
	if err != nil || !result.HooksCompleted || runner.calls != 1 {
		t.Fatalf("reconciled create result=%#v err=%v runner=%d", result, err, runner.calls)
	}
	updated, err := store.ReadRegistry(registryPath)
	if err != nil || updated.Projects[resolution.Project.ID].ConfigPath != resolution.Project.ConfigPath {
		t.Fatalf("registry was not reconciled before create: %#v err=%v", updated.Projects[resolution.Project.ID], err)
	}
}

type createHooksProcessFake struct{ resolveCalls int }

func (f *createHooksProcessFake) Resolve(context.Context, HookExecutableRequest) (HookExecutableFact, error) {
	f.resolveCalls++
	return HookExecutableFact{Resolved: filepath.Join(os.TempDir(), "setup"), Available: true}, nil
}

func (f *createHooksProcessFake) Run(context.Context, HookProcessRequest) (HookProcessResult, error) {
	return HookProcessResult{}, nil
}

type createHooksRunnerFake struct {
	calls int
	run   func(context.Context, HookRunRequest) (HookRunResult, error)
}

func (f *createHooksRunnerFake) Run(ctx context.Context, request HookRunRequest) (HookRunResult, error) {
	f.calls++
	return f.run(ctx, request)
}

func createLifecycleConfigFixture(t *testing.T, hook bool) (domain.Project, string, plan.WorkspacePlan) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(filepath.Join(root, "project.wtree.yml"))
	if hook {
		local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}, Timeout: time.Minute}}}
	}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	project := hookManagementProject(configPath, root)
	target := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	head := "0123456789abcdef0123456789abcdef01234567"
	value := plan.WorkspacePlan{Version: plan.Version, Operation: plan.Create, ProjectID: project.ID, WorkspaceName: "feature", WorkspaceID: "feature", RootPath: target, LogicalRoot: target, BaseRepository: "root", Repositories: []plan.RepositoryPlan{{ID: "root", Base: head, Branch: "feature", Mount: ".", Path: target}}, Steps: []plan.Step{{Action: plan.CreateBranch, RepositoryID: "root", Inverse: plan.DeleteBranch}, {Action: plan.AddWorktree, RepositoryID: "root", Inverse: plan.RemoveWorktree}}}
	return project, configPath, value
}

func createLifecycleHookPlan(t *testing.T) HookPlan {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "workspace")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	value, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "root", WorkspaceID: "workspace", WorkspaceName: "Workspace", SourceLogicalRoot: root, TargetLogicalRoot: root, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "setup", Repository: "root", SourceRepository: root, TargetRepository: target, Branch: "feature", Head: "0123456789abcdef0123456789abcdef01234567", ConfiguredExecutable: "setup", ResolvedExecutable: filepath.Join(root, "setup"), Availability: "available", Arguments: []string{"--literal"}, Timeout: time.Minute}}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func createLifecycleWorkspacePlan(t *testing.T) plan.WorkspacePlan {
	t.Helper()
	root := t.TempDir()
	head := "0123456789abcdef0123456789abcdef01234567"
	return plan.WorkspacePlan{Version: plan.Version, Operation: plan.Create, ProjectID: "project", WorkspaceName: "Workspace", WorkspaceID: "workspace", RootPath: root, LogicalRoot: root, BaseRepository: "root", Repositories: []plan.RepositoryPlan{{ID: "root", Base: head, Branch: "feature", Mount: ".", Path: root}}, Steps: []plan.Step{{Action: plan.CreateBranch, RepositoryID: "root", Inverse: plan.DeleteBranch}, {Action: plan.AddWorktree, RepositoryID: "root", Inverse: plan.RemoveWorktree}}}
}
