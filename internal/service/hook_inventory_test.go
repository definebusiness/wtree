package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
)

func TestHookRunInventoryStrictlyClassifiesExactWorkspaceDirectory(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	configPath := filepath.Join(root, ".wtree.yml")
	project := hookManagementProject(configPath, root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	request := HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data}
	inventory := NewHookRunInventoryService()
	value, err := inventory.Inspect(context.Background(), request)
	if err != nil || value.Classification != HookRunAbsent || value.Setup == nil {
		t.Fatalf("absent inventory=%#v err=%v", value, err)
	}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: "root"}, CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	value, err = inventory.Inspect(context.Background(), request)
	if err != nil || value.Classification != HookRunResumable || len(value.Setup) != 1 || value.Setup[0].NextHookID != "setup" || value.record == nil {
		t.Fatalf("resumable inventory=%#v err=%v", value, err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "unexpected.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err = inventory.Inspect(context.Background(), request)
	if err != nil || value.Classification != HookRunInvalid {
		t.Fatalf("unsafe entry inventory=%#v err=%v", value, err)
	}
}

func TestHookRunInventoryRejectsIntermediateAncestorChange(t *testing.T) {
	for _, scenario := range []string{"missing", "replacement"} {
		t.Run(scenario, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
			workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
			request := HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data}
			path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: "root"}, CreatedAt: now, UpdatedAt: now}
			if err := store.WriteHookRunRecord(path, record); err != nil {
				t.Fatal(err)
			}
			hookRunInventoryStepHook = func(step string) error {
				if step != "after-open" {
					return nil
				}
				hookRunInventoryStepHook = nil
				if err := os.Rename(filepath.Join(data, "projects"), filepath.Join(data, "old-projects")); err != nil {
					if os.IsPermission(err) {
						t.Skipf("directory replacement fixture unavailable: %v", err)
					}
					return err
				}
				if scenario == "replacement" {
					return store.WriteHookRunRecord(path, record)
				}
				return nil
			}
			defer func() { hookRunInventoryStepHook = nil }()
			result, err := NewHookRunInventoryService().Inspect(context.Background(), request)
			if err != nil || result.Classification != HookRunInvalid {
				t.Fatalf("detached inventory=%#v %v", result, err)
			}
			if scenario == "replacement" {
				result, err = NewHookRunInventoryService().Inspect(context.Background(), request)
				if err != nil || result.Classification != HookRunResumable {
					t.Fatalf("fresh inventory=%#v %v", result, err)
				}
			}
		})
	}
}

func TestHookRunInventoryAcceptsOnlyBoundedRemovalEvidence(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	request := HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: "root"}, CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 2; index++ {
		name := ".post-create.json.remove-1-" + strconv.Itoa(index)
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte("evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := NewHookRunInventoryService().Inspect(context.Background(), request)
		want := HookRunResumable
		if index == 2 {
			want = HookRunInvalid
		}
		if err != nil || result.Classification != want {
			t.Fatalf("evidence count %d inventory=%#v %v, want %s", index, result, err, want)
		}
	}
}

func TestHookRunInventoryTreatsCompletedRecordRemovalAsAbsent(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: "root"}, CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveHookRunRecord(path); err != nil {
		t.Fatal(err)
	}
	result, err := NewHookRunInventoryService().Inspect(context.Background(), HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data})
	if err != nil || result.Classification != HookRunAbsent || result.Setup == nil {
		t.Fatalf("completed removal inventory=%#v %v", result, err)
	}
}

func TestHookRetryUsesSingleInventoryCandidateAndRendersBoundedResult(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"first", "second"}, CompletedHookIDs: []string{"first"}, NextIndex: 1, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "second", RepositoryID: "root"}, CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	builder := &hookRetryBuilderFake{}
	runner := &hookRetryRunnerFake{record: record, result: HookRunResult{Status: "completed", CompletedIDs: []string{"first", "second"}}}
	service := NewHookRetryServiceWith(NewHookRunInventoryService(), builder, runner)
	result, err := service.Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data})
	if err != nil || result.Version != 1 || result.Operation != "hooks-retry" || result.Status != "completed" || result.ResumedAt != 1 || strings.Join(result.CompletedHookIDs, ",") != "first,second" || runner.calls != 1 || builder.calls != 1 {
		t.Fatalf("Retry=%#v err=%v calls=%d", result, err, runner.calls)
	}
	if _, err := service.Retry(context.Background(), HookRetryRequest{Project: project, Workspace: domain.Workspace{ID: "missing", Name: "Missing"}, DataDir: data}); err == nil {
		t.Fatal("missing retry accepted")
	}
}

func TestHookRetryMapsLockedStaleAndSetupFailuresWithoutLeakingAuthority(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"first", "second"}, CompletedHookIDs: []string{"first"}, NextIndex: 1, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "second", RepositoryID: "root"}, CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		failure *HookRunFailure
		kind    ErrorKind
		message string
	}{
		{name: "locked", failure: &HookRunFailure{Kind: HookFailureLock}, kind: ErrorConflict, message: "hooks retry: hook run is already active; wait for it to finish"},
		{name: "stale", failure: &HookRunFailure{Kind: HookFailureGeneration}, kind: ErrorConflict, message: "hooks retry: hook run is stale; a fresh run is required"},
		{name: "record", failure: &HookRunFailure{Kind: HookFailureRecord}, kind: ErrorConflict, message: "hooks retry: hook run record is invalid; inspect with wtree doctor"},
		{name: "setup", failure: &HookRunFailure{Kind: HookFailureNonZero, HookID: "second", RepositoryID: "root"}, kind: ErrorSetupIncomplete, message: "setup_incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &hookRetryRunnerFake{record: record, result: HookRunResult{Status: "incomplete", CompletedIDs: []string{"first"}, Failure: test.failure}}
			_, err := NewHookRetryServiceWith(NewHookRunInventoryService(), &hookRetryBuilderFake{}, runner).Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data})
			if err == nil {
				t.Fatal("Retry succeeded")
			}
			var application *Error
			if !errors.As(err, &application) || application.Kind != test.kind || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Retry error = %v, want %s %q", err, test.kind, test.message)
			}
			if details, ok := SetupIncompleteFrom(err); test.kind == ErrorSetupIncomplete && (!ok || strings.Join(details.CompletedHookIDs, ",") != "first" || details.HookID != "") {
				t.Fatalf("setup details = %#v, ok=%v", details, ok)
			}
		})
	}
}

func TestHookRetryRejectsMalformedFailureResultWithoutReportingSetupIncomplete(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"first", "second"}, CompletedHookIDs: []string{"first"}, NextIndex: 1, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "second", RepositoryID: "root"}, CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	runner := &hookRetryRunnerFake{record: record, result: HookRunResult{Status: "incomplete", CompletedIDs: []string{"second"}, Failure: &HookRunFailure{Kind: HookFailureNonZero}}}
	_, err = NewHookRetryServiceWith(NewHookRunInventoryService(), &hookRetryBuilderFake{}, runner).Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorConflict || strings.Contains(err.Error(), "setup_incomplete") {
		t.Fatalf("malformed failure retry error=%v", err)
	}
}

func TestHookRetryRejectsAbsentInvalidAndInventoryStaleBeforeRunnerOrRecordMutation(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	request := HookRetryRequest{Project: project, Workspace: workspace, DataDir: data}
	for _, test := range []struct {
		name    string
		unknown bool
		message string
	}{
		{name: "absent", message: "hooks retry: no incomplete hook run exists"},
		{name: "unknown entry", unknown: true, message: "hooks retry: hook run record is invalid; inspect with wtree doctor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			caseData := t.TempDir()
			request.DataDir = caseData
			// Recreate the fixture against this case's data directory so cases
			// cannot inherit an inventory artifact from one another.
			if test.unknown {
				directory := filepath.Join(caseData, "projects", project.ID, "hooks", workspace.ID)
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "unknown.txt"), []byte("unsafe"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			builder, runner := &hookRetryBuilderFake{}, &hookRetryRunnerFake{}
			_, err := NewHookRetryServiceWith(NewHookRunInventoryService(), builder, runner).Retry(context.Background(), request)
			var application *Error
			if !errors.As(err, &application) || application.Kind != ErrorConflict || err.Error() != "conflict: "+test.message || builder.calls != 0 || runner.calls != 0 {
				t.Fatalf("Retry error=%v builder=%d runner=%d", err, builder.calls, runner.calls)
			}
		})
	}
}

func TestHookRetryRejectsInventoryStaleBeforeRunnerOrRecordMutation(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "clone", Event: "post-create", Source: "portable", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "running", CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	builder, runner := &hookRetryBuilderFake{}, &hookRetryRunnerFake{}
	_, err = NewHookRetryServiceWith(NewHookRunInventoryService(), builder, runner).Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorConflict || err.Error() != "conflict: hooks retry: hook run record is invalid; inspect with wtree doctor" || builder.calls != 0 || runner.calls != 0 {
		t.Fatalf("Retry error=%v builder=%d runner=%d", err, builder.calls, runner.calls)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("stale retry changed record: %v\nbefore=%s\nafter=%s", err, before, after)
	}
}

func TestHookRetryRejectsChangedPlanAuthorityBeforeRunnerOrRecordMutation(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace", RootPath: root}
	executable := hookInventoryTestExecutable(t)
	planValue, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: project.ID, ProjectName: project.Name, BaseRepository: project.BaseRepository, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, SourceLogicalRoot: root, TargetLogicalRoot: root, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "first", Repository: project.BaseRepository, SourceRepository: root, TargetRepository: root, Branch: "main", Head: "0123456789abcdef0123456789abcdef01234567", ConfiguredExecutable: executable, ResolvedExecutable: executable, Availability: "available", Timeout: time.Minute}, {ID: "second", Repository: project.BaseRepository, SourceRepository: root, TargetRepository: root, Branch: "main", Head: "0123456789abcdef0123456789abcdef01234567", ConfiguredExecutable: executable, ResolvedExecutable: executable, Availability: "available", Timeout: time.Minute}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: planValue.SourceSHA256(), PlanSHA256: planValue.Digest(), WorkspaceStateSHA256: planValue.WorkspaceStateSHA256(), HookIDs: []string{"first", "second"}, CompletedHookIDs: []string{"first"}, NextIndex: 1, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "second", RepositoryID: project.BaseRepository}, CreatedAt: now, UpdatedAt: now}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		plan HookPlan
	}{
		{name: "plan-digest", plan: hookRetryChangedPlan(t, project, workspace, []string{"first", "second"}, []string{"changed"})},
		{name: "ordered-hook-ids", plan: hookRetryChangedPlan(t, project, workspace, []string{"second", "first"}, nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := &hookRetryAuthorityBuilderFake{plan: test.plan, snapshot: HookGenerationSnapshot{SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state")}}
			runner := &hookRetryRunnerFake{}
			_, retryErr := NewHookRetryServiceWith(NewHookRunInventoryServiceWith(builder), builder, runner).Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data})
			var application *Error
			if !errors.As(retryErr, &application) || application.Kind != ErrorConflict || runner.calls != 0 {
				t.Fatalf("Retry error=%v runner=%d", retryErr, runner.calls)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("changed authority mutated record: %v\nbefore=%s\nafter=%s", readErr, before, after)
			}
		})
	}
}

func hookRetryChangedPlan(t *testing.T, project domain.Project, workspace domain.Workspace, ids, arguments []string) HookPlan {
	t.Helper()
	executable := hookInventoryTestExecutable(t)
	entries := make([]hookPlanInputEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, hookPlanInputEntry{ID: id, Repository: project.BaseRepository, SourceRepository: workspace.RootPath, TargetRepository: workspace.RootPath, Branch: "main", Head: "0123456789abcdef0123456789abcdef01234567", ConfiguredExecutable: executable, ResolvedExecutable: executable, Availability: "available", Arguments: append([]string(nil), arguments...), Timeout: time.Minute})
	}
	value, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: project.ID, ProjectName: project.Name, BaseRepository: project.BaseRepository, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, SourceLogicalRoot: workspace.RootPath, TargetLogicalRoot: workspace.RootPath, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func hookInventoryTestExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHookRunInventoryRejectsMultipleAndSymlinkedRecordsWithoutFollowingThem(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "running", CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	second, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-clone")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("not-a-record"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data}
	value, err := NewHookRunInventoryService().Inspect(context.Background(), request)
	if err != nil || value.Classification != HookRunInvalid {
		t.Fatalf("multiple=%#v err=%v", value, err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), second); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	value, err = NewHookRunInventoryService().Inspect(context.Background(), request)
	if err != nil || value.Classification != HookRunInvalid {
		t.Fatalf("symlink=%#v err=%v", value, err)
	}
}

func TestHookRunInventoryRejectsNonRegularLockEntries(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "running", CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), "post-create.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	value, err := NewHookRunInventoryService().Inspect(context.Background(), HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data})
	if err != nil || value.Classification != HookRunInvalid {
		t.Fatalf("nonregular lock inventory=%#v err=%v", value, err)
	}
}

func TestHookRunInventoryRejectsUnsafeDataAndOwnedParentsWithoutFollowingThem(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	request := HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data}
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Symlink(data, unsafe); err == nil {
		request.DataDir = unsafe
		value, inspectErr := NewHookRunInventoryService().Inspect(context.Background(), request)
		if inspectErr != nil || value.Classification != HookRunInvalid {
			t.Fatalf("symlink data inventory=%#v err=%v", value, inspectErr)
		}
	}
	request.DataDir = data
	parent := filepath.Join(data, "projects")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	value, inspectErr := NewHookRunInventoryService().Inspect(context.Background(), request)
	if inspectErr != nil || value.Classification != HookRunInvalid {
		t.Fatalf("nonprivate parent inventory=%#v err=%v", value, inspectErr)
	}
}

func TestHookRunInventoryMarksValidButWrongSourceEventPairingInvalid(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "clone", Event: "post-create", Source: "portable", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "running", CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	value, err := NewHookRunInventoryService().Inspect(context.Background(), HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data})
	if err != nil || value.Classification != HookRunInvalid || value.record != nil || len(value.Setup) != 0 {
		t.Fatalf("invalid inventory=%#v err=%v", value, err)
	}
}

func TestHookRunInventoryUsesInjectedRebuildAndVerifierForExactAuthority(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace", RootPath: root}
	executable := hookInventoryTestExecutable(t)
	planValue, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: project.ID, ProjectName: project.Name, BaseRepository: project.BaseRepository, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, SourceLogicalRoot: root, TargetLogicalRoot: root, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "setup", Repository: project.BaseRepository, SourceRepository: root, TargetRepository: root, Branch: "main", Head: "0123456789abcdef0123456789abcdef01234567", ConfiguredExecutable: executable, ResolvedExecutable: executable, Availability: "available", Arguments: []string{}, Timeout: time.Minute}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: planValue.SourceSHA256(), PlanSHA256: planValue.Digest(), WorkspaceStateSHA256: planValue.WorkspaceStateSHA256(), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "running", CreatedAt: now, UpdatedAt: now}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	builder := &hookRetryAuthorityBuilderFake{plan: planValue, snapshot: HookGenerationSnapshot{SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state")}}
	request := HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data}
	value, err := NewHookRunInventoryServiceWith(builder).Inspect(context.Background(), request)
	if err != nil || value.Classification != HookRunResumable || builder.calls != 1 {
		t.Fatalf("exact inventory=%#v err=%v calls=%d", value, err, builder.calls)
	}
	for _, mutate := range []struct {
		name  string
		apply func()
	}{
		{name: "source", apply: func() { builder.snapshot.SourceBytes = []byte("changed") }},
		{name: "workspace-state", apply: func() {
			builder.snapshot.SourceBytes = []byte("source")
			builder.snapshot.WorkspaceStateBytes = []byte("changed")
		}},
		{name: "plan-digest", apply: func() {
			builder.snapshot.WorkspaceStateBytes = []byte("state")
			builder.plan = mustHookRunnerPlanWithArguments(t, []string{"setup"}, []string{"changed"})
			builder.plan.authority.projectID, builder.plan.authority.workspaceID, builder.plan.authority.workspaceName = project.ID, workspace.ID, workspace.Name
		}},
		{name: "ordered-hook-ids", apply: func() {
			builder.plan = mustHookRunnerPlanEntries(t, "later", "setup")
			builder.plan.authority.projectID, builder.plan.authority.workspaceID, builder.plan.authority.workspaceName = project.ID, workspace.ID, workspace.Name
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			mutate.apply()
			value, err = NewHookRunInventoryServiceWith(builder).Inspect(context.Background(), request)
			if err != nil || value.Classification != HookRunStale || value.record != nil {
				t.Fatalf("changed inventory=%#v err=%v", value, err)
			}
		})
	}
}

func TestHookRunInventoryAndRetryPropagateCancellationWithoutMutation(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace", RootPath: root}
	planValue := hookRetryChangedPlan(t, project, workspace, []string{"setup"}, nil)
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: planValue.SourceSHA256(), PlanSHA256: planValue.Digest(), WorkspaceStateSHA256: planValue.WorkspaceStateSHA256(), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: project.BaseRepository}, CreatedAt: now, UpdatedAt: now}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		builder hookRetryPlanBuilder
		want    error
	}{
		{name: "rebuild canceled", builder: hookRetryBuilderFunc(func(context.Context, HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
			return HookPlan{}, nil, context.Canceled
		}), want: context.Canceled},
		{name: "verifier deadline", builder: hookRetryBuilderFunc(func(context.Context, HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
			return planValue, func(context.Context) (HookGenerationSnapshot, error) {
				return HookGenerationSnapshot{}, context.DeadlineExceeded
			}, nil
		}), want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := NewHookRunInventoryServiceWith(test.builder)
			value, inspectErr := inventory.Inspect(context.Background(), HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data})
			if !errors.Is(inspectErr, test.want) || value.Classification != "" {
				t.Fatalf("Inspect=%#v %v, want %v", value, inspectErr, test.want)
			}
			runner := &hookRetryRunnerFake{}
			_, retryErr := NewHookRetryServiceWith(inventory, test.builder, runner).Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data})
			if !errors.Is(retryErr, test.want) || runner.calls != 0 {
				t.Fatalf("Retry=%v runner=%d, want %v", retryErr, runner.calls, test.want)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("canceled inventory mutated record: %v\nbefore=%s\nafter=%s", readErr, before, after)
			}
		})
	}
}

func TestSameCheckoutPathPreservesPOSIXCaseDistinctness(t *testing.T) {
	if sameCheckoutPathForPlatform("/Workspace/API", "/workspace/api", false) {
		t.Fatal("case-distinct POSIX checkout paths were treated as equal")
	}
	if !sameCheckoutPathForPlatform(`C:\\Workspace\\API`, `c:\\workspace\\api`, true) {
		t.Fatal("case-only Windows checkout paths were not treated as aliases")
	}
}

func TestHookRetrySuccessResultRequiresExactCompletedRecord(t *testing.T) {
	record := store.HookRunRecord{HookIDs: []string{"first", "second"}, State: "failed"}
	for _, test := range []struct {
		name string
		run  HookRunResult
		ok   bool
	}{
		{name: "empty", run: HookRunResult{Status: "completed"}},
		{name: "subset", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first"}}},
		{name: "reordered", run: HookRunResult{Status: "completed", CompletedIDs: []string{"second", "first"}}},
		{name: "unknown", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first", "unknown"}}},
		{name: "duplicate", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first", "first"}}},
		{name: "exact", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first", "second"}}, ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validHookRetrySuccess(record, test.run); got != test.ok {
				t.Fatalf("validHookRetrySuccess=%t, want %t", got, test.ok)
			}
		})
	}
}

func TestHookRetryResultRequiresOrderedFailurePrefixAndFinalizingCleanup(t *testing.T) {
	record := store.HookRunRecord{HookIDs: []string{"first", "second"}, CompletedHookIDs: []string{"first"}, NextIndex: 1, State: "failed"}
	failure := &HookRunFailure{Kind: HookFailureNonZero}
	for _, test := range []struct {
		name string
		run  HookRunResult
		ok   bool
	}{
		{name: "short durable prefix", run: HookRunResult{Status: "incomplete", CompletedIDs: []string{}, Failure: failure}},
		{name: "ordered prefix", run: HookRunResult{Status: "incomplete", CompletedIDs: []string{"first"}, Failure: failure}, ok: true},
		{name: "failure full prefix", run: HookRunResult{Status: "incomplete", CompletedIDs: []string{"first", "second"}, Failure: failure}, ok: true},
		{name: "failure reordered", run: HookRunResult{Status: "incomplete", CompletedIDs: []string{"second"}, Failure: failure}},
		{name: "failure unknown", run: HookRunResult{Status: "incomplete", CompletedIDs: []string{"first", "unknown"}, Failure: failure}},
		{name: "failure duplicate", run: HookRunResult{Status: "incomplete", CompletedIDs: []string{"first", "first"}, Failure: failure}},
		{name: "failure wrong status", run: HookRunResult{Status: "completed", CompletedIDs: []string{"first"}, Failure: failure}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validHookRetryResult(record, test.run); got != test.ok {
				t.Fatalf("validHookRetryResult=%t, want %t", got, test.ok)
			}
		})
	}
	finalizing := record
	finalizing.State = "finalizing"
	if !validHookRetryResult(finalizing, HookRunResult{Status: "completed", CompletedIDs: []string{"first", "second"}}) {
		t.Fatal("valid finalizing cleanup result was rejected")
	}
	if validHookRetryResult(finalizing, HookRunResult{Status: "incomplete", CompletedIDs: []string{"first", "second"}, Failure: failure}) {
		t.Fatal("finalizing failure was accepted")
	}
}

func TestHookRetryPropagatesUnderLockVerifierSentinelsWithoutOutputOrMutation(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace", RootPath: root}
	planValue := hookRetryChangedPlan(t, project, workspace, []string{"setup"}, nil)
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: planValue.SourceSHA256(), PlanSHA256: planValue.Digest(), WorkspaceStateSHA256: planValue.WorkspaceStateSHA256(), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: project.BaseRepository}, CreatedAt: now, UpdatedAt: now}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		want error
	}{
		{name: "canceled", want: context.Canceled},
		{name: "deadline", want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.WriteHookRunRecord(path, record); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			verifications, runs := 0, 0
			builder := hookRetryBuilderFunc(func(context.Context, HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
				return planValue, func(context.Context) (HookGenerationSnapshot, error) {
					verifications++
					if verifications == 1 {
						return HookGenerationSnapshot{SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state")}, nil
					}
					return HookGenerationSnapshot{}, test.want
				}, nil
			})
			runner := NewHookRunnerWith(HookRunnerDependencies{Process: hookTestProcess{runCall: func(HookProcessRequest) { runs++ }}})
			var output bytes.Buffer
			_, retryErr := NewHookRetryServiceWith(NewHookRunInventoryServiceWith(builder), builder, runner).Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data, Sink: &output})
			if !errors.Is(retryErr, test.want) || verifications != 2 || runs != 0 || output.Len() != 0 {
				t.Fatalf("Retry=%v verifications=%d runs=%d output=%q", retryErr, verifications, runs, output.String())
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("under-lock sentinel mutated record: %v\nbefore=%s\nafter=%s", err, before, after)
			}
		})
	}
}

func TestHookRetryWorkspaceFactsRejectEveryPersistedCheckoutChange(t *testing.T) {
	source := hookPlanTestPath("retry-facts", "source")
	workspaceRoot := hookPlanTestPath("retry-facts", "workspace")
	sourceCommon := hookPlanTestPath("retry-facts", "source.git")
	workspaceCommon := hookPlanTestPath("retry-facts", "workspace.git")
	project := domain.Project{Version: domain.CurrentVersion, ID: "project", Name: "Project", ConfigPath: filepath.Join(source, ".wtree.yml"), LogicalRoot: source, BaseRepository: "root", Repositories: []domain.Repository{{ID: "root", SourcePath: source, CommonGitDir: sourceCommon, DefaultMount: ".", DefaultBranch: "main"}}}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "workspace", Name: "Workspace", RootPath: workspaceRoot, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: "0123456789abcdef0123456789abcdef01234567", Mount: ".", ResolvedPath: workspaceRoot}}}
	for _, test := range []struct {
		name   string
		mutate func(*hookRetryWorkspaceFactsGit, *domain.Workspace)
	}{
		{name: "persisted-path", mutate: func(_ *hookRetryWorkspaceFactsGit, value *domain.Workspace) {
			value.Checkouts[0].ResolvedPath = hookPlanTestPath("retry-facts", "other")
		}},
		{name: "top-level", mutate: func(git *hookRetryWorkspaceFactsGit, _ *domain.Workspace) {
			git.topLevel = hookPlanTestPath("retry-facts", "other")
		}},
		{name: "checkout-common-git-dir", mutate: func(git *hookRetryWorkspaceFactsGit, _ *domain.Workspace) {
			git.workspaceCommon = hookPlanTestPath("retry-facts", "other.git")
		}},
		{name: "registered-source-identity", mutate: func(git *hookRetryWorkspaceFactsGit, _ *domain.Workspace) {
			git.sourceCommon = hookPlanTestPath("retry-facts", "other.git")
		}},
		{name: "branch", mutate: func(git *hookRetryWorkspaceFactsGit, _ *domain.Workspace) { git.branch = "other" }},
		{name: "detached", mutate: func(git *hookRetryWorkspaceFactsGit, _ *domain.Workspace) { git.detached = true; git.branch = "" }},
		{name: "head", mutate: func(git *hookRetryWorkspaceFactsGit, _ *domain.Workspace) {
			git.head = "fedcba9876543210fedcba9876543210fedcba98"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := workspace
			value.Checkouts = append([]domain.Checkout(nil), workspace.Checkouts...)
			git := &hookRetryWorkspaceFactsGit{topLevel: workspaceRoot, sourcePath: source, workspaceCommon: workspaceCommon, sourceCommon: sourceCommon, branch: "main", head: workspace.Checkouts[0].Head}
			test.mutate(git, &value)
			if err := validateHookRetryWorkspaceFacts(context.Background(), git, project, value); err == nil {
				t.Fatal("changed persisted checkout fact was accepted")
			}
		})
	}
}

func TestHookRetryWorkspaceFactsRejectPartialWorkspace(t *testing.T) {
	source := hookPlanTestPath("retry-partial", "source")
	workspaceRoot := hookPlanTestPath("retry-partial", "workspace")
	sourceCommon := hookPlanTestPath("retry-partial", "source.git")
	workspaceCommon := hookPlanTestPath("retry-partial", "workspace.git")
	project := domain.Project{Version: domain.CurrentVersion, ID: "project", Name: "Project", ConfigPath: filepath.Join(source, ".wtree.yml"), LogicalRoot: source, BaseRepository: "root", Repositories: []domain.Repository{{ID: "root", SourcePath: source, CommonGitDir: sourceCommon, DefaultMount: ".", DefaultBranch: "main"}, {ID: "child", SourcePath: filepath.Join(source, "child"), ParentID: "root", CommonGitDir: hookPlanTestPath("retry-partial", "child.git"), DefaultMount: "child", DefaultBranch: "main"}}}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "workspace", Name: "Workspace", RootPath: workspaceRoot, Partial: true, MissingRepositoryIDs: []string{"child"}, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: "0123456789abcdef0123456789abcdef01234567", Mount: ".", ResolvedPath: workspaceRoot}}}
	git := &hookRetryWorkspaceFactsGit{topLevel: workspaceRoot, sourcePath: source, workspaceCommon: workspaceCommon, sourceCommon: sourceCommon, branch: "main", head: workspace.Checkouts[0].Head}
	if err := validateHookRetryWorkspaceFacts(context.Background(), git, project, workspace); err == nil {
		t.Fatal("partial workspace was accepted for retry")
	}
}

func TestHookRetryDefaultBuilderPropagatesAuthoritySentinels(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	project.Repositories[0].CommonGitDir = "/common.git"
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "workspace", Name: "Workspace", RootPath: root, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("a", 40), Mount: ".", ResolvedPath: root}}}
	record := store.HookRunRecord{Source: "local", Operation: "create", Event: "post-create"}
	for _, test := range []struct {
		name string
		fail string
		want error
	}{
		{name: "top-level canceled", fail: "TopLevel", want: context.Canceled},
		{name: "common-git-dir deadline", fail: "CommonGitDir", want: context.DeadlineExceeded},
		{name: "head canceled", fail: "Head", want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			git := &hookRetryWorkspaceFactsGit{topLevel: root, workspaceCommon: "/common.git", sourceCommon: "/common.git", branch: "main", head: workspace.Checkouts[0].Head, fail: test.fail, failErr: test.want}
			_, _, err := (hookRetryDefaultBuilder{git: git, process: hookTestProcess{}}).Rebuild(context.Background(), HookRetryPlanRequest{Project: project, Workspace: workspace, Record: record, DataDir: data})
			if !errors.Is(err, test.want) {
				t.Fatalf("Rebuild() = %v, want %v", err, test.want)
			}
		})
	}
	local := hookManagementLocal(root)
	local.Hooks = config.HookEvents{config.HookEventPostCreate: []config.Hook{{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(project.ConfigPath, local); err != nil {
		t.Fatal(err)
	}
	git := &hookRetryWorkspaceFactsGit{topLevel: root, workspaceCommon: "/common.git", sourceCommon: "/common.git", branch: "main", head: workspace.Checkouts[0].Head}
	if _, _, err := (hookRetryDefaultBuilder{git: git, process: hookTestProcess{resolveErr: context.DeadlineExceeded}}).Rebuild(context.Background(), HookRetryPlanRequest{Project: project, Workspace: workspace, Record: record, DataDir: data}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Resolve sentinel Rebuild() = %v", err)
	}
}

func TestHookRetryPortableExecutableTrackedFileSentinelsPropagateWithoutStaleOrMutation(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	executable := filepath.Join(root, "hooks", "setup")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	if err := config.WriteProjectFile(project.ConfigPath, hookManagementLocal(root)); err != nil {
		t.Fatal(err)
	}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "workspace", Name: "Workspace", RootPath: root, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("a", 40), Mount: ".", ResolvedPath: root}}}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: []config.Hook{{ID: "setup", Command: []string{"hooks/setup"}}}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	git := &hookRetryPortableGit{manifest: manifestBytes, root: root, head: workspace.Checkouts[0].Head, tracked: map[string]bool{"hooks/setup": true}}
	process := hookTestProcess{factSet: true, fact: HookExecutableFact{Resolved: executable, Available: true}}
	builder := hookRetryDefaultBuilder{git: git, process: process}
	request := HookRetryPlanRequest{Project: project, Workspace: workspace, Record: store.HookRunRecord{Source: "portable", Operation: "clone", Event: "post-clone"}, DataDir: data}
	planValue, _, err := builder.Rebuild(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	record := hookTestRecord(planValue, "failed", 0)
	record.CompletedHookIDs = []string{}
	record.Failure = &store.HookRunFailure{Kind: string(HookFailureMissing), HookID: "setup", RepositoryID: project.BaseRepository}
	path, err := store.HookRunRecordPath(data, project.ID, workspace.ID, "post-clone")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		want error
	}{
		{name: "canceled", want: context.Canceled},
		{name: "deadline", want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			git.trackedErr, git.trackedErrName = test.want, "hooks/setup"
			for _, operation := range []struct {
				name string
				run  func() error
			}{
				{name: "rebuild", run: func() error {
					_, _, rebuildErr := builder.Rebuild(context.Background(), request)
					return rebuildErr
				}},
				{name: "inventory", run: func() error {
					value, inspectErr := NewHookRunInventoryServiceWith(builder).Inspect(context.Background(), HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: data})
					if value.Classification != "" || len(value.Setup) != 0 {
						return errors.New("sentinel inventory was classified instead of interrupted")
					}
					return inspectErr
				}},
				{name: "retry", run: func() error {
					runner := &hookRetryRunnerFake{}
					var output bytes.Buffer
					_, retryErr := NewHookRetryServiceWith(NewHookRunInventoryServiceWith(builder), builder, runner).Retry(context.Background(), HookRetryRequest{Project: project, Workspace: workspace, DataDir: data, Sink: &output})
					if runner.calls != 0 || output.Len() != 0 {
						return errors.New("sentinel retry launched or rendered output")
					}
					return retryErr
				}},
			} {
				t.Run(operation.name, func(t *testing.T) {
					git.trackedCalls = nil
					if operationErr := operation.run(); !errors.Is(operationErr, test.want) {
						t.Fatalf("error = %v, want %v", operationErr, test.want)
					}
					if strings.Join(git.trackedCalls, ",") != "project.wtree.yml,hooks/setup" {
						t.Fatalf("TrackedFile calls = %v", git.trackedCalls)
					}
					after, readErr := os.ReadFile(path)
					if readErr != nil || !bytes.Equal(after, before) {
						t.Fatalf("sentinel mutated record: %v\nbefore=%s\nafter=%s", readErr, before, after)
					}
				})
			}
		})
	}
}

func TestHookRetryPortableRelativeExecutableRequiresTrackedPhysicalAuthorityBeforeAndUnderLock(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	executable := filepath.Join(root, "hooks", "nested", "setup")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	local := hookManagementLocal(root)
	if err := config.WriteProjectFile(project.ConfigPath, local); err != nil {
		t.Fatal(err)
	}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "workspace", Name: "Workspace", RootPath: root, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("a", 40), Mount: ".", ResolvedPath: root}}}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: []config.Hook{{ID: "setup", Command: []string{"hooks/nested/setup"}}}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	git := &hookRetryPortableGit{manifest: manifestBytes, root: root, head: workspace.Checkouts[0].Head, tracked: map[string]bool{"hooks/nested/setup": true}}
	process := hookTestProcess{factSet: true, fact: HookExecutableFact{Resolved: canonical, Available: true}}
	builder := hookRetryDefaultBuilder{git: git, process: process}
	record := store.HookRunRecord{Source: "portable", Operation: "clone", Event: "post-clone"}
	request := HookRetryPlanRequest{Project: project, Workspace: workspace, Record: record, DataDir: data}
	planValue, verifier, err := builder.Rebuild(context.Background(), request)
	if err != nil || verifier == nil || len(planValue.authority.entries) != 1 || planValue.authority.entries[0].ResolvedExecutable != canonical {
		t.Fatalf("tracked portable rebuild plan=%#v verifier=%t err=%v", planValue, verifier != nil, err)
	}
	if _, err := verifier(context.Background()); err != nil {
		t.Fatalf("tracked portable verifier: %v", err)
	}
	git.tracked["hooks/nested/setup"] = false
	if _, err := verifier(context.Background()); err == nil {
		t.Fatal("under-lock tracked-file removal was accepted")
	}
	if _, _, err := builder.Rebuild(context.Background(), request); err == nil {
		t.Fatal("untracked portable executable was accepted")
	}
	git.tracked["hooks/nested/setup"] = true
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, executable); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	process.fact.Resolved = outside
	builder.process = process
	if _, _, err := builder.Rebuild(context.Background(), request); err == nil {
		t.Fatal("portable symlink escape was accepted")
	}
}

func TestHookRetryPortableRelativeExecutableNormalizesWindowsSeparatorsForTrackedAuthority(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	executable := filepath.Join(root, "hooks", "setup")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	if err := config.WriteProjectFile(project.ConfigPath, hookManagementLocal(root)); err != nil {
		t.Fatal(err)
	}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "workspace", Name: "Workspace", RootPath: root, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("a", 40), Mount: ".", ResolvedPath: root}}}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: []config.Hook{{ID: "setup", Command: []string{`hooks\setup`}}}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	git := &hookRetryPortableGit{manifest: manifestBytes, root: root, head: workspace.Checkouts[0].Head, tracked: map[string]bool{"hooks/setup": true}}
	builder := hookRetryDefaultBuilder{git: git, process: hookTestProcess{factSet: true, fact: HookExecutableFact{Resolved: executable, Available: true}}}
	_, verifier, err := builder.Rebuild(context.Background(), HookRetryPlanRequest{Project: project, Workspace: workspace, Record: store.HookRunRecord{Source: "portable", Operation: "clone", Event: "post-clone"}, DataDir: data})
	if err != nil || verifier == nil || git.lastTracked != "hooks/setup" {
		t.Fatalf("windows-separator portable rebuild verifier=%t tracked=%q err=%v", verifier != nil, git.lastTracked, err)
	}
}

type hookRetryWorkspaceFactsGit struct {
	gitadapter.Git
	topLevel, sourcePath, workspaceCommon, sourceCommon, branch, head string
	detached                                                          bool
	fail                                                              string
	failErr                                                           error
}

type hookRetryPortableGit struct {
	gitadapter.Git
	manifest                []byte
	root, head, lastTracked string
	tracked                 map[string]bool
	trackedErr              error
	trackedErrName          string
	trackedCalls            []string
}

func (g *hookRetryPortableGit) TopLevel(context.Context, string) (string, error)     { return g.root, nil }
func (g *hookRetryPortableGit) CommonGitDir(context.Context, string) (string, error) { return "", nil }
func (g *hookRetryPortableGit) CurrentBranch(context.Context, string) (string, bool, error) {
	return "main", false, nil
}
func (g *hookRetryPortableGit) Head(context.Context, string) (string, error) { return g.head, nil }
func (g *hookRetryPortableGit) TrackedFile(_ context.Context, _ string, _ string, name string) ([]byte, error) {
	g.trackedCalls = append(g.trackedCalls, name)
	if g.trackedErr != nil && (g.trackedErrName == "" || g.trackedErrName == name) {
		return nil, g.trackedErr
	}
	g.lastTracked = name
	if name == "project.wtree.yml" {
		return append([]byte(nil), g.manifest...), nil
	}
	if g.tracked[name] {
		return []byte("tracked"), nil
	}
	return nil, errors.New("not tracked")
}

func (g *hookRetryWorkspaceFactsGit) TopLevel(context.Context, string) (string, error) {
	if g.fail == "TopLevel" {
		return "", g.failErr
	}
	return g.topLevel, nil
}
func (g *hookRetryWorkspaceFactsGit) CommonGitDir(_ context.Context, path string) (string, error) {
	if g.fail == "CommonGitDir" {
		return "", g.failErr
	}
	if path == g.sourcePath {
		return g.sourceCommon, nil
	}
	return g.workspaceCommon, nil
}
func (g *hookRetryWorkspaceFactsGit) CurrentBranch(context.Context, string) (string, bool, error) {
	return g.branch, g.detached, nil
}
func (g *hookRetryWorkspaceFactsGit) Head(context.Context, string) (string, error) {
	if g.fail == "Head" {
		return "", g.failErr
	}
	return g.head, nil
}

type hookRetryBuilderFake struct{ calls int }

func (f *hookRetryBuilderFake) Rebuild(_ context.Context, request HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
	f.calls++
	if request.Record.Event == "" {
		return HookPlan{}, nil, errors.New("missing record")
	}
	return HookPlan{}, nil, nil
}

type hookRetryBuilderFunc func(context.Context, HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error)

func (f hookRetryBuilderFunc) Rebuild(ctx context.Context, request HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
	return f(ctx, request)
}

type hookRetryAuthorityBuilderFake struct {
	plan     HookPlan
	snapshot HookGenerationSnapshot
	calls    int
}

func (f *hookRetryAuthorityBuilderFake) Rebuild(_ context.Context, _ HookRetryPlanRequest) (HookPlan, HookGenerationVerifier, error) {
	f.calls++
	return f.plan, func(context.Context) (HookGenerationSnapshot, error) { return f.snapshot, nil }, nil
}

type hookRetryRunnerFake struct {
	calls  int
	record store.HookRunRecord
	result HookRunResult
}

func (f *hookRetryRunnerFake) Resume(ctx context.Context, request HookResumeRequest) (HookRunResult, error) {
	f.calls++
	if request.Prepare == nil {
		return HookRunResult{}, errors.New("missing prepare")
	}
	if _, _, err := request.Prepare(ctx, f.record); err != nil {
		return HookRunResult{}, err
	}
	return f.result, nil
}
