package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestRenderCleanRollbackDiagnosticUsesStderrOnlyForHumanOutput(t *testing.T) {
	err := service.NewError(service.ErrorGit, service.NewCleanRollbackError(errors.New("add worktree failed")))
	var human, json bytes.Buffer
	if renderErr := renderCleanRollbackDiagnostic(&human, false, err); renderErr != nil {
		t.Fatal(renderErr)
	}
	if got, want := human.String(), "Rollback complete.\n"; got != want {
		t.Fatalf("human diagnostic = %q, want %q", got, want)
	}
	if renderErr := renderCleanRollbackDiagnostic(&json, true, err); renderErr != nil {
		t.Fatal(renderErr)
	}
	if json.Len() != 0 {
		t.Fatalf("JSON diagnostic = %q, want empty", json.String())
	}
}

func TestRenderCreateFailureDiagnosticReportsExactInternalEvidence(t *testing.T) {
	result := service.CreateResult{
		RetainedIgnoreFiles: []service.IgnoreFileUpdate{{Path: "/worktrees/retained/.gitignore"}},
		RemovedIgnoreFiles:  []service.IgnoreFileUpdate{{Path: "/worktrees/removed/.gitignore"}},
		UnverifiedMounts: []service.UnverifiedMount{{
			ParentPath: "/worktrees/parent", ChildPath: "/worktrees/parent/backend", Mount: "backend",
		}},
	}
	var human, json bytes.Buffer
	if err := renderCreateFailureDiagnostic(&human, false, result); err != nil {
		t.Fatal(err)
	}
	want := "Retained changed .gitignore files:\n" +
		"  /worktrees/retained/.gitignore\n" +
		"Removed .gitignore files with clean rollback:\n" +
		"  /worktrees/removed/.gitignore\n" +
		"Unverified mounts; child worktrees were not added:\n" +
		"  /worktrees/parent -> /worktrees/parent/backend (backend)\n"
	if human.String() != want {
		t.Fatalf("human diagnostic = %q, want %q", human.String(), want)
	}
	if err := renderCreateFailureDiagnostic(&json, true, result); err != nil {
		t.Fatal(err)
	}
	if json.Len() != 0 {
		t.Fatalf("JSON diagnostic = %q, want empty", json.String())
	}
}

func TestRenderCreateFailureDiagnosticReportsOnlyRemainingUnverifiedChild(t *testing.T) {
	result := service.CreateResult{UnverifiedMounts: []service.UnverifiedMount{{
		ParentPath: "/worktrees/parent", ChildPath: "/worktrees/parent/beta", Mount: "beta",
	}}}
	var output bytes.Buffer
	if err := renderCreateFailureDiagnostic(&output, false, result); err != nil {
		t.Fatal(err)
	}
	want := "Unverified mounts; child worktrees were not added:\n  /worktrees/parent -> /worktrees/parent/beta (beta)\n"
	if output.String() != want {
		t.Fatalf("human diagnostic = %q, want %q", output.String(), want)
	}
}

func TestRenderWorkspacePlanAlignsEveryColumn(t *testing.T) {
	value := plan.WorkspacePlan{
		Operation:     plan.Create,
		WorkspaceName: "feature/customer-search",
		RootPath:      "/worktrees/feature-customer-search",
		Repositories: []plan.RepositoryPlan{
			{ID: "root", Base: "4516c867", Branch: "feature/customer-search", Mount: ".", Path: "/worktrees/feature-customer-search"},
			{ID: "backend", ParentID: "root", Base: "8b7c9ba0", Branch: "feature/customer-search", Mount: "backend", Path: "/worktrees/feature-customer-search/backend"},
		},
	}
	var output bytes.Buffer
	if err := renderWorkspacePlan(&output, value); err != nil {
		t.Fatal(err)
	}
	want := "Operation: create\n" +
		"Workspace: feature/customer-search\n" +
		"Target: /worktrees/feature-customer-search\n\n" +
		"REPOSITORY  BASE      BRANCH                   MOUNT    PATH\n" +
		"root        4516c867  feature/customer-search  .        /worktrees/feature-customer-search\n" +
		"backend     8b7c9ba0  feature/customer-search  backend  /worktrees/feature-customer-search/backend\n\n" +
		"Automatic ignore protection (execution will ensure):\n" +
		"  " + filepath.Join(value.RootPath, ".gitignore") + "\n" +
		"    /backend/\n\n" +
		"No changes made. Dry run performs no mutation.\n"
	if output.String() != want {
		t.Fatalf("renderWorkspacePlan() = %q, want %q", output.String(), want)
	}
}

func TestCreateNoHooksFlagIsCreateOnlyAndConflictsWithDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	create := newWorkspacePlanCommand(&stdout, &stderr, new(string), plan.Create)
	if create.Flags().Lookup("no-hooks") == nil {
		t.Fatal("create is missing --no-hooks")
	}
	checkout := newWorkspacePlanCommand(&stdout, &stderr, new(string), plan.Checkout)
	if checkout.Flags().Lookup("no-hooks") != nil {
		t.Fatal("checkout unexpectedly accepts --no-hooks")
	}
	if err := Execute([]string{"create", "feature", "--no-hooks", "--dry-run"}, &stdout, &stderr); err == nil || ExitCode(err) != 2 {
		t.Fatalf("create --no-hooks --dry-run = %v, want invalid arguments", err)
	}
}

func TestCreateHookLifecycleFactoryRendersDryAndSetupFailureSeparately(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	value := plan.WorkspacePlan{Version: plan.Version, Operation: plan.Create, ProjectID: "project", WorkspaceName: "feature", WorkspaceID: "feature", RootPath: filepath.Join(t.TempDir(), "workspace"), LogicalRoot: filepath.Join(t.TempDir(), "logical"), BaseRepository: "root", Repositories: []plan.RepositoryPlan{{ID: "root", Base: "0123456789abcdef0123456789abcdef01234567", Branch: "feature", Mount: ".", Path: filepath.Join(t.TempDir(), "workspace")}}}
	value.LogicalRoot = value.RootPath
	value.Steps = []plan.Step{{Action: plan.CreateBranch, RepositoryID: "root", Inverse: plan.DeleteBranch}, {Action: plan.AddWorktree, RepositoryID: "root", Inverse: plan.RemoveWorktree}}
	fake := &createLifecycleFake{planResult: service.CreateLifecycleResult{Core: service.CreateResult{Plan: value}, DryRun: true, HooksApplicable: true, Hooks: []service.HookPlanEntry{{ID: "setup", Repository: "root", WorkingDirectory: value.RootPath, ConfiguredExecutable: "setup", ResolvedExecutable: filepath.Join(value.RootPath, "setup"), Timeout: "1m0s"}}}}
	previous := newCreateLifecycle
	newCreateLifecycle = func() createLifecycle { return fake }
	t.Cleanup(func() { newCreateLifecycle = previous })
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", value.RootPath, "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	wantHookLine := "Hooks (local automatic post-create):\n  setup [root] " + value.RootPath + " <- setup => " + filepath.Join(value.RootPath, "setup") + " (1m0s) arguments=[]\n"
	if !bytes.Contains(stdout.Bytes(), []byte(wantHookLine)) || !bytes.HasSuffix(stdout.Bytes(), []byte("No changes made. Dry run performs no mutation.\n")) || fake.planCalls != 1 || fake.createCalls != 0 {
		t.Fatalf("dry lifecycle stdout=%q plan=%d create=%d", stdout.String(), fake.planCalls, fake.createCalls)
	}
	if fake.planRequest.Reconcile {
		t.Fatal("dry create requested reconciliation")
	}

	fake.createResult = service.CreateLifecycleResult{Core: service.CreateResult{Plan: value}, HooksApplicable: true, HooksCompleted: true, CompletedHookIDs: []string{"setup"}}
	stdout.Reset()
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", value.RootPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Hooks completed: setup\n")) || fake.createCalls != 1 {
		t.Fatalf("completed lifecycle stdout=%q calls=%d", stdout.String(), fake.createCalls)
	}
	if !fake.createRequest.Reconcile {
		t.Fatal("real create did not delegate reconciliation to the lifecycle coordinator")
	}

	fake.createResult = service.CreateLifecycleResult{Core: service.CreateResult{Plan: value}, HooksApplicable: true, HooksSkipped: true, CompletedHookIDs: []string{}}
	stdout.Reset()
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", value.RootPath, "--no-hooks"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Hooks intentionally skipped: post-create\n")) {
		t.Fatalf("skipped lifecycle stdout=%q", stdout.String())
	}
	stdout.Reset()
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", value.RootPath, "--no-hooks", "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var skipped map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &skipped); err != nil || string(skipped["hooksCompleted"]) != "false" || string(skipped["hooksSkipped"]) != "true" || string(skipped["completedHookIds"]) != "[]" || skipped["hooks"] != nil {
		t.Fatalf("skipped lifecycle JSON=%q err=%v", stdout.String(), err)
	}

	details := service.SetupIncompleteDetails{Operation: "create", CoreStatus: "completed", SetupStatus: "incomplete", Event: "post-create", FailureKind: service.HookFailureRecord, CompletedHookIDs: []string{}, RetryCommand: "wtree hooks retry feature"}
	fake.createResult = service.CreateLifecycleResult{Core: service.CreateResult{Plan: value}}
	fake.createErr = service.NewError(service.ErrorSetupIncomplete, &service.SetupIncompleteError{Details: details})
	stdout.Reset()
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", value.RootPath}, &stdout, &stderr); err == nil || !bytes.Contains(stdout.Bytes(), []byte("Created workspace: feature\n")) {
		t.Fatalf("human setup incomplete stdout=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", value.RootPath, "--json"}, &stdout, &stderr); err == nil || ExitCode(err) != 10 {
		t.Fatalf("JSON setup incomplete = %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope["success"] != false || bytes.Contains(stdout.Bytes(), []byte(`"version"`)) || !bytes.Contains(stdout.Bytes(), []byte(`"setup"`)) {
		t.Fatalf("JSON setup envelope=%q err=%v", stdout.String(), err)
	}
	writerErr := errors.New("injected JSON writer")
	partial := &createHookPartialWriter{err: writerErr}
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", value.RootPath, "--dry-run", "--json"}, partial, &stderr); !errors.Is(err, writerErr) || partial.calls != 1 || json.Valid(partial.Bytes()) {
		t.Fatalf("partial lifecycle JSON err=%v calls=%d bytes=%q", err, partial.calls, partial.Bytes())
	}
}

func TestCreateDryRunV2LifecycleRoutingPreservesExactOutput(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: repository.Path, ProjectPath: repository.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.NewWorkspacePlanner().Plan(context.Background(), resolution.Project, service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "feature/v2", TargetPath: target, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	var wantHuman bytes.Buffer
	if err := renderWorkspacePlan(&wantHuman, value); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{"create", "feature/v2", "--project", repository.Path, "--data-dir", data, "--path", target, "--dry-run"}
	if err := Execute(arguments, &stdout, &stderr); err != nil || stdout.String() != wantHuman.String() || stderr.Len() != 0 {
		t.Fatalf("v2 dry human=%q stderr=%q err=%v\nwant=%q", stdout.String(), stderr.String(), err, wantHuman.String())
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Execute(append(arguments, "--json"), &stdout, &stderr); err != nil || stdout.String() != string(encoded)+"\n" || stderr.Len() != 0 {
		t.Fatalf("v2 dry JSON=%q stderr=%q err=%v\nwant=%q", stdout.String(), stderr.String(), err, string(encoded)+"\n")
	}
}

func TestCreateV2LifecycleRoutingPreservesExactSuccessOutput(t *testing.T) {
	newFixture := func(t *testing.T) (string, string, string) {
		t.Helper()
		repository := testutil.NewPushedGitRepository(t)
		repository.CommitFile("root.txt", "root\n", "root")
		data := t.TempDir()
		if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data}); err != nil {
			t.Fatal(err)
		}
		return repository.Path, data, filepath.Join(t.TempDir(), "workspace")
	}
	projectPath, data, target := newFixture(t)
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath, ProjectPath: projectPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.NewWorkspacePlanner().Plan(context.Background(), resolution.Project, service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "feature/v2-success", TargetPath: target, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	var wantHuman bytes.Buffer
	if err := renderCreateSuccess(&wantHuman, value, nil); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"create", "feature/v2-success", "--project", projectPath, "--data-dir", data, "--path", target}, &stdout, &stderr); err != nil || stdout.String() != wantHuman.String() || stderr.Len() != 0 {
		t.Fatalf("v2 success human=%q stderr=%q err=%v want=%q", stdout.String(), stderr.String(), err, wantHuman.String())
	}

	projectPath, data, target = newFixture(t)
	resolution, err = service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath, ProjectPath: projectPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	value, err = service.NewWorkspacePlanner().Plan(context.Background(), resolution.Project, service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "feature/v2-json", TargetPath: target, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Execute([]string{"create", "feature/v2-json", "--project", projectPath, "--data-dir", data, "--path", target, "--json"}, &stdout, &stderr); err != nil || stdout.String() != string(encoded)+"\n" || stderr.Len() != 0 {
		t.Fatalf("v2 success JSON=%q stderr=%q err=%v want=%q", stdout.String(), stderr.String(), err, string(encoded)+"\n")
	}
}

func TestCreateHookLifecycleHumanWriterFailureHasNoFollowup(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	value := plan.WorkspacePlan{Version: plan.Version, Operation: plan.Create, ProjectID: "project", WorkspaceName: "feature", WorkspaceID: "feature", RootPath: target, LogicalRoot: target, BaseRepository: "root", Repositories: []plan.RepositoryPlan{{ID: "root", Base: "0123456789abcdef0123456789abcdef01234567", Branch: "feature", Mount: ".", Path: target}}, Steps: []plan.Step{{Action: plan.CreateBranch, RepositoryID: "root", Inverse: plan.DeleteBranch}, {Action: plan.AddWorktree, RepositoryID: "root", Inverse: plan.RemoveWorktree}}}
	previous := newCreateLifecycle
	fake := &createLifecycleFake{planResult: service.CreateLifecycleResult{Core: service.CreateResult{Plan: value}}, createResult: service.CreateLifecycleResult{Core: service.CreateResult{Plan: value}, HooksApplicable: true, HooksCompleted: true, CompletedHookIDs: []string{"setup"}}}
	newCreateLifecycle = func() createLifecycle { return fake }
	t.Cleanup(func() { newCreateLifecycle = previous })
	want := errors.New("human writer")
	writer := &createHookFailWriter{err: want}
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", target}, writer, io.Discard); !errors.Is(err, want) || writer.calls != 1 || fake.createCalls != 1 {
		t.Fatalf("completed writer err=%v calls=%d create=%d", err, writer.calls, fake.createCalls)
	}
	details := service.SetupIncompleteDetails{Operation: "create", CoreStatus: "completed", SetupStatus: "incomplete", Event: "post-create", FailureKind: service.HookFailureRecord, CompletedHookIDs: []string{}, RetryCommand: "wtree hooks retry feature"}
	fake.createErr = service.NewError(service.ErrorSetupIncomplete, &service.SetupIncompleteError{Details: details})
	writer.calls = 0
	if err := Execute([]string{"create", "feature", "--project", repository.Path, "--data-dir", data, "--path", target}, writer, io.Discard); !errors.Is(err, want) || writer.calls != 1 {
		t.Fatalf("setup writer err=%v calls=%d", err, writer.calls)
	}
}

func TestCreateHookDryRunHumanArgumentsAreUnambiguous(t *testing.T) {
	value := plan.WorkspacePlan{Operation: plan.Create, WorkspaceName: "feature", RootPath: "/workspace", LogicalRoot: "/workspace", Repositories: []plan.RepositoryPlan{{ID: "root", Base: "base", Branch: "feature", Mount: ".", Path: "/workspace"}}}
	result := service.CreateLifecycleResult{Core: service.CreateResult{Plan: value}, DryRun: true, HooksApplicable: true, Hooks: []service.HookPlanEntry{{ID: "quoted", Repository: "root", WorkingDirectory: "/workspace", ConfiguredExecutable: "setup", ResolvedExecutable: "/workspace/setup", Timeout: "1m0s", Arguments: []string{"space value", `quote"`, `slash\\`, "line\nbreak", ""}}, {ID: "empty", Repository: "root", WorkingDirectory: "/workspace", ConfiguredExecutable: "setup", ResolvedExecutable: "/workspace/setup", Timeout: "1m0s", Arguments: []string{}}}}
	var output bytes.Buffer
	if err := renderCreateLifecycleDryRun(&output, result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `arguments=["space value","quote\"","slash\\\\","line\nbreak",""]`) || !strings.Contains(output.String(), "arguments=[]") || strings.Contains(output.String(), "WTREE_") {
		t.Fatalf("ambiguous hook rendering: %q", output.String())
	}
}

type createLifecycleFake struct {
	planResult, createResult   service.CreateLifecycleResult
	createErr                  error
	planCalls, createCalls     int
	planRequest, createRequest service.CreateLifecycleRequest
}

func (f *createLifecycleFake) Plan(_ context.Context, request service.CreateLifecycleRequest) (service.CreateLifecycleResult, error) {
	f.planCalls++
	f.planRequest = request
	return f.planResult, nil
}
func (f *createLifecycleFake) Create(_ context.Context, request service.CreateLifecycleRequest) (service.CreateLifecycleResult, error) {
	f.createCalls++
	f.createRequest = request
	return f.createResult, f.createErr
}

type createHookPartialWriter struct {
	bytes.Buffer
	calls int
	err   error
}

type createHookFailWriter struct {
	calls int
	err   error
}

func (w *createHookFailWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, w.err
}

func (w *createHookPartialWriter) Write(value []byte) (int, error) {
	w.calls++
	if len(value) == 0 {
		return 0, w.err
	}
	limit := len(value) / 2
	if limit == 0 {
		limit = 1
	}
	_, _ = w.Buffer.Write(value[:limit])
	return limit, w.err
}
