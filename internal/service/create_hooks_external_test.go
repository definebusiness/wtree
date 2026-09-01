package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
)

func TestCreateDryRunHookTopologyProjectionDoesNotMutate(t *testing.T) {
	tests := []struct {
		name    string
		fixture func(*testing.T) (domain.Project, string)
		mounts  []service.MountOverride
	}{
		{name: "dot-root-defaults", fixture: func(t *testing.T) (domain.Project, string) {
			project, _, _, data := createFixture(t)
			return project, data
		}},
		{name: "nested-mount-override", fixture: func(t *testing.T) (domain.Project, string) {
			project, _, _, data := createFixture(t)
			return project, data
		}, mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}}},
		{name: "three-level-nested", fixture: func(t *testing.T) (domain.Project, string) {
			project, _, _, _, data := createThreeLevelFixture(t)
			return project, data
		}, mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}, {RepositoryID: "shared", Mount: "common"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, data := test.fixture(t)
			backendSource := repositorySource(project, "backend")
			executable := filepath.Join(backendSource, ".wtree-hooks", "setup")
			if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeCreateHooks(t, project, []config.Hook{
				{ID: "root-hook", Command: []string{"setup", "--literal"}},
				{ID: "source-hook", Repository: "backend", Command: []string{filepath.Join(".wtree-hooks", "setup"), "--fast"}, Timeout: 2 * time.Minute},
			})
			beforeConfig, err := os.ReadFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "workspace")
			resolver := &createHookResolveSpy{}
			coordinator := service.NewCreateLifecycleCoordinatorWith(service.CreateLifecycleDependencies{Process: resolver})
			result, err := coordinator.Plan(context.Background(), service.CreateLifecycleRequest{Project: project, Workspace: service.WorkspacePlanRequest{WorkspaceName: "feature/hooks", From: "HEAD", TargetPath: target, DataDir: data, Mounts: test.mounts}})
			if err != nil {
				t.Fatal(err)
			}
			if !result.DryRun || !result.HooksApplicable || len(result.Hooks) != 2 {
				t.Fatalf("dry hook plan = %#v", result)
			}
			root, source := result.Hooks[0], result.Hooks[1]
			if root.Repository != project.BaseRepository || root.Timeout != "1m0s" || !reflect.DeepEqual(root.Arguments, []string{"--literal"}) || root.WorkingDirectory != target || root.ConfiguredExecutable != "setup" || result.Core.Plan.Repositories[0].Branch != "feature/hooks" || len(result.Core.Plan.Repositories[0].Base) != 40 {
				t.Fatalf("default root projection = %#v", root)
			}
			backendTarget := repositoryPath(result.Core.Plan, "backend")
			if source.WorkingDirectory != backendTarget || source.Repository != "backend" || source.Timeout != "2m0s" || source.ResolvedExecutable != filepath.Join(backendSource, filepath.Join(".wtree-hooks", "setup")) || !reflect.DeepEqual(source.Arguments, []string{"--fast"}) {
				t.Fatalf("source-relative projection = %#v source=%q target=%q", source, backendSource, backendTarget)
			}
			if resolver.calls != 2 || resolver.directories[0] != target || resolver.directories[1] != backendSource {
				t.Fatalf("preflight resolve calls=%d directories=%#v", resolver.calls, resolver.directories)
			}
			afterConfig, err := os.ReadFile(project.ConfigPath)
			if err != nil || string(beforeConfig) != string(afterConfig) {
				t.Fatalf("dry plan changed config: %v", err)
			}
			if _, err := os.Stat(service.WorkspaceStatePath(data, project.ID, result.Core.Plan.WorkspaceID)); !os.IsNotExist(err) {
				t.Fatalf("dry plan wrote workspace state: %v", err)
			}
			if _, err := os.Stat(filepath.Join(data, "projects", project.ID, "hooks")); !os.IsNotExist(err) {
				t.Fatalf("dry plan created hook authority: %v", err)
			}
		})
	}
}

func TestCreateHookPreflightRejectsBeforeCoreEffects(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		prepare func(*testing.T, domain.Project)
	}{
		{name: "malformed-v3", command: []string{"setup"}, prepare: func(t *testing.T, project domain.Project) {
			if err := os.WriteFile(project.ConfigPath, []byte("version: 3\ninvalid: true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unavailable", command: []string{"missing"}},
		{name: "directory", command: []string{filepath.Join(".wtree-hooks", "directory")}},
		{name: "symlink-escape", command: []string{filepath.Join(".wtree-hooks", "escape")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, _, _, data := createFixture(t)
			if test.prepare == nil {
				writeCreateHooks(t, project, []config.Hook{{ID: "setup", Command: test.command}})
			} else {
				test.prepare(t, project)
			}
			process := &createHookRejectingResolver{}
			coordinator := service.NewCreateLifecycleCoordinatorWith(service.CreateLifecycleDependencies{Process: process})
			target := filepath.Join(t.TempDir(), "workspace")
			_, err := coordinator.Plan(context.Background(), service.CreateLifecycleRequest{Project: project, Workspace: service.WorkspacePlanRequest{WorkspaceName: "feature/reject", TargetPath: target, DataDir: data}})
			var application *service.Error
			if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorValidation {
				t.Fatalf("preflight error = %v", err)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("preflight created workspace: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(data, "projects", project.ID, "hooks")); !os.IsNotExist(statErr) {
				t.Fatalf("preflight created hook record/lock: %v", statErr)
			}
			if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, "feature-reject")); !os.IsNotExist(statErr) {
				t.Fatalf("preflight wrote state: %v", statErr)
			}
		})
	}
}

func TestCreateHookRunnerPersistsFirstSuccessAndStopsAtLaterFailure(t *testing.T) {
	project, _, _, data := createFixture(t)
	writeCreateHooks(t, project, []config.Hook{
		{ID: "first", Command: []string{"first"}},
		{ID: "second", Command: []string{"second"}},
	})
	process := &createHookSequenceProcess{failID: "second"}
	coordinator := service.NewCreateLifecycleCoordinatorWith(service.CreateLifecycleDependencies{Process: process})
	result, err := coordinator.Create(context.Background(), service.CreateLifecycleRequest{Project: project, Workspace: service.WorkspacePlanRequest{WorkspaceName: "feature/failure", TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}})
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorSetupIncomplete {
		t.Fatalf("create failure = %v", err)
	}
	details, ok := service.SetupIncompleteFrom(err)
	if !ok || details.HookID != "second" || details.Repository != "root" || details.FailureKind != service.HookFailureNonZero || details.ExitCode == nil || *details.ExitCode != 23 || !reflect.DeepEqual(details.CompletedHookIDs, []string{"first"}) {
		t.Fatalf("setup details = %#v resolver calls=%d directories=%#v", details, process.calls, process.directories)
	}
	if !reflect.DeepEqual(process.runs, []string{"first", "second"}) {
		t.Fatalf("hook run order = %#v", process.runs)
	}
	recordPath, err := store.HookRunRecordPath(data, project.ID, result.Core.Plan.WorkspaceID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.ReadHookRunRecord(recordPath)
	if err != nil || record.State != "failed" || record.NextIndex != 1 || !reflect.DeepEqual(record.CompletedHookIDs, []string{"first"}) || record.Failure == nil || record.Failure.HookID != "second" {
		t.Fatalf("durable failure record = %#v, %v", record, err)
	}
	if _, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, result.Core.Plan.WorkspaceID)); err != nil {
		t.Fatalf("core state rolled back after hook failure: %v", err)
	}
}

func TestCreateNoHooksValidatesAndCommitsWithoutHookAuthority(t *testing.T) {
	project, _, _, data := createFixture(t)
	writeCreateHooks(t, project, []config.Hook{{ID: "setup", Command: []string{"would-not-resolve"}}})
	process := &createHookRejectingResolver{}
	runner := &createHookRunnerSpy{}
	coordinator := service.NewCreateLifecycleCoordinatorWith(service.CreateLifecycleDependencies{Process: process, Runner: runner})
	result, err := coordinator.Create(context.Background(), service.CreateLifecycleRequest{Project: project, NoHooks: true, Workspace: service.WorkspacePlanRequest{WorkspaceName: "feature/no-hooks", TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.HooksApplicable || !result.HooksSkipped || result.HooksCompleted || result.CompletedHookIDs == nil || process.calls != 0 || runner.calls != 0 {
		t.Fatalf("no-hooks result=%#v resolver=%d runner=%d", result, process.calls, runner.calls)
	}
	if _, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, result.Core.Plan.WorkspaceID)); err != nil {
		t.Fatalf("no-hooks did not commit core state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "projects", project.ID, "hooks")); !os.IsNotExist(err) {
		t.Fatalf("no-hooks created hook authority: %v", err)
	}
}

func TestCreateHookCoreFailureNeverReachesRunner(t *testing.T) {
	tests := []struct {
		name        string
		transaction func() *service.WorkspaceTransaction
	}{
		{name: "project-lock-before-effect", transaction: func() *service.WorkspaceTransaction {
			return service.NewWorkspaceTransactionWith(createHookFailLocker{}, store.WriteWorkspace, store.WriteRecovery, os.Remove)
		}},
		{name: "state-publication-after-effects", transaction: func() *service.WorkspaceTransaction {
			return service.NewWorkspaceTransactionWith(lock.Manager{}, func(string, store.WorkspaceState) error { return errors.New("injected state publication") }, store.WriteRecovery, os.Remove)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, _, _, data := createFixture(t)
			writeCreateHooks(t, project, []config.Hook{{ID: "setup", Command: []string{"setup"}}})
			runner, process := &createHookRunnerSpy{}, &createHookResolveSpy{}
			creator := service.NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), test.transaction())
			coordinator := service.NewCreateLifecycleCoordinatorWith(service.CreateLifecycleDependencies{Creator: creator, Runner: runner, Process: process})
			_, err := coordinator.Create(context.Background(), service.CreateLifecycleRequest{Project: project, Workspace: service.WorkspacePlanRequest{WorkspaceName: "feature/core-failure", TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}})
			if err == nil || runner.calls != 0 || process.calls == 0 {
				t.Fatalf("core failure=%v runner=%d preflight resolver=%d", err, runner.calls, process.calls)
			}
			if _, err := os.Stat(filepath.Join(data, "projects", project.ID, "hooks")); !os.IsNotExist(err) {
				t.Fatalf("core failure created hook authority: %v", err)
			}
		})
	}
}

func TestCreateHookCoordinatorCoreFailureBoundariesNeverRunHooks(t *testing.T) {
	for _, name := range []string{"effect-clean-rollback", "validate-result", "rollback-recovery", "canceled-before-publication"} {
		t.Run(name, func(t *testing.T) {
			project, _, _, data := createFixture(t)
			writeCreateHooks(t, project, []config.Hook{{ID: "setup", Command: []string{"setup"}}})
			target := filepath.Join(t.TempDir(), "workspace")
			ctx := context.Background()
			var creator *service.WorkspaceCreator
			switch name {
			case "effect-clean-rollback":
				git := &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 2}
				creator = service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction())
			case "validate-result":
				git := validationFailingCreateGit{Git: gitadapter.NewAdapter("git"), target: target}
				creator = service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction())
			case "rollback-recovery":
				git := rollbackFailingCreateGit{Git: gitadapter.NewAdapter("git")}
				creator = service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction())
			case "canceled-before-publication":
				canceled, cancel := context.WithCancel(context.Background())
				git := cancelAfterWorktreeGit{Git: gitadapter.NewAdapter("git"), cancel: cancel}
				creator, ctx = service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction()), canceled
				defer cancel()
			}
			runner, process := &createHookRunnerSpy{}, &createHookResolveSpy{}
			coordinator := service.NewCreateLifecycleCoordinatorWith(service.CreateLifecycleDependencies{Creator: creator, Runner: runner, Process: process})
			result, err := coordinator.Create(ctx, service.CreateLifecycleRequest{Project: project, Workspace: service.WorkspacePlanRequest{WorkspaceName: "feature/boundary", TargetPath: target, DataDir: data}})
			if err == nil || runner.calls != 0 || process.runCalls != 0 || process.calls == 0 {
				t.Fatalf("boundary error=%v runner=%d resolve=%d run=%d", err, runner.calls, process.calls, process.runCalls)
			}
			if _, statErr := os.Stat(filepath.Join(data, "projects", project.ID, "hooks")); !os.IsNotExist(statErr) {
				t.Fatalf("boundary created hook authority: %v", statErr)
			}
			if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, result.Core.Plan.WorkspaceID)); !os.IsNotExist(statErr) {
				t.Fatalf("boundary published state: %v", statErr)
			}
			switch name {
			case "effect-clean-rollback", "validate-result":
				if !service.HasCleanRollback(err) {
					t.Fatalf("boundary=%s error=%v, want clean rollback", name, err)
				}
			case "rollback-recovery":
				var application *service.Error
				if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
					t.Fatalf("rollback boundary error=%v", err)
				}
				if _, readErr := store.ReadRecovery(service.RecoveryRecordPath(data, result.Core.Plan)); readErr != nil {
					t.Fatalf("rollback recovery missing: %v", readErr)
				}
			case "canceled-before-publication":
				if !errors.Is(err, context.Canceled) || !service.HasCleanRollback(err) {
					t.Fatalf("cancellation boundary error=%v", err)
				}
			}
		})
	}
}

type createHookResolveSpy struct {
	calls       int
	directories []string
	runCalls    int
}

func (s *createHookResolveSpy) Resolve(_ context.Context, request service.HookExecutableRequest) (service.HookExecutableFact, error) {
	s.calls++
	s.directories = append(s.directories, request.Directory)
	return service.HookExecutableFact{Resolved: filepath.Join(request.Directory, filepath.FromSlash(request.Program)), Available: true}, nil
}
func (s *createHookResolveSpy) Run(context.Context, service.HookProcessRequest) (service.HookProcessResult, error) {
	s.runCalls++
	return service.HookProcessResult{}, nil
}

type createHookRejectingResolver struct{ createHookResolveSpy }

func (s *createHookRejectingResolver) Resolve(context.Context, service.HookExecutableRequest) (service.HookExecutableFact, error) {
	s.calls++
	return service.HookExecutableFact{}, nil
}

type createHookSequenceProcess struct {
	createHookResolveSpy
	failID string
	runs   []string
}

type createHookRunnerSpy struct{ calls int }

func (s *createHookRunnerSpy) Run(context.Context, service.HookRunRequest) (service.HookRunResult, error) {
	s.calls++
	return service.HookRunResult{}, nil
}

type createHookFailLocker struct{}

func (createHookFailLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	return nil, errors.New("injected project lock")
}

func (s *createHookSequenceProcess) Resolve(ctx context.Context, request service.HookExecutableRequest) (service.HookExecutableFact, error) {
	return s.createHookResolveSpy.Resolve(ctx, request)
}

func (s *createHookSequenceProcess) Run(_ context.Context, request service.HookProcessRequest) (service.HookProcessResult, error) {
	s.runs = append(s.runs, request.HookID)
	if request.HookID == s.failID {
		return service.HookProcessResult{Started: true, ExitCode: 23}, nil
	}
	return service.HookProcessResult{Started: true}, nil
}

func writeCreateHooks(t *testing.T, project domain.Project, hooks []config.Hook) {
	t.Helper()
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	local.Hooks = config.HookEvents{"post-create": hooks}
	if err := config.WriteProjectFile(project.ConfigPath, local); err != nil {
		t.Fatal(err)
	}
}

func repositoryPath(value plan.WorkspacePlan, id string) string {
	for _, repository := range value.Repositories {
		if repository.ID == id {
			return repository.Path
		}
	}
	return ""
}
func repositorySource(project domain.Project, id string) string {
	for _, repository := range project.Repositories {
		if repository.ID == id {
			return repository.SourcePath
		}
	}
	return ""
}
