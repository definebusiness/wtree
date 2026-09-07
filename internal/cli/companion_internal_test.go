package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/service"
)

func TestCompanionUpdateCommandRendersCompleteHumanAndJSONContracts(t *testing.T) {
	project := domain.Project{ID: "project-1", Repositories: []domain.Repository{{ID: "tools", Companion: true, DefaultBranch: "main"}}}
	dryResult := companionUpdateDryResult()
	realResult := companionUpdateRealResult("completed", nil)
	for _, test := range []struct {
		name      string
		args      []string
		result    service.CompanionUpdateResult
		want      string
		wantCalls int
	}{
		{
			name:   "dry human",
			args:   []string{"tools", "--dry-run"},
			result: dryResult,
			want: "Companion update: project=project-1 repository=tools baseline=main dryRun=true\n" +
				"baseline: status=planned action=none branch=main previousHead=111 deferred=true\n" +
				"workspace: status=planned action=fast-forward workspace=default branch=main previousHead=111 resultingHead=222 deferred=true\n" +
				"Companion update result: status=planned\n",
			wantCalls: 0,
		},
		{
			name:   "real human streams settled entries",
			args:   []string{"tools"},
			result: realResult,
			want: "Companion update: project=project-1 repository=tools baseline=main dryRun=false\n" +
				"baseline: status=completed action=fast-forward branch=main previousHead=111 resultingHead=222\n" +
				"workspace: status=completed action=fast-forward workspace=default branch=main previousHead=111 resultingHead=222\n" +
				"Companion update result: status=completed\n",
			wantCalls: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			calls := 0
			value := test.result
			err := executeCompanionUpdate(t, &stdout, test.args, project, nil, value, nil, func(request service.CompanionUpdateRequest) {
				if request.Progress == nil {
					return
				}
				for _, entry := range value.Entries {
					calls++
					if err := request.Progress(entry); err != nil {
						return
					}
				}
			})
			if err != nil || stdout.String() != test.want || calls != test.wantCalls {
				t.Fatalf("result err=%v calls=%d stdout=%q", err, calls, stdout.String())
			}
		})
	}

	var stdout bytes.Buffer
	progressCalls := 0
	err := executeCompanionUpdate(t, &stdout, []string{"tools", "--json"}, project, nil, realResult, nil, func(request service.CompanionUpdateRequest) {
		if request.Progress != nil {
			progressCalls++
		}
	})
	if err != nil || progressCalls != 0 || strings.Count(stdout.String(), "\n") != 1 {
		t.Fatalf("JSON result err=%v progress=%d stdout=%q", err, progressCalls, stdout.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	companionAssertKeys(t, document, "version", "operation", "status", "dryRun", "projectId", "repositoryId", "baseline", "entries")
	if document["version"] != float64(1) || document["operation"] != "companion-update" || document["status"] != "completed" || document["dryRun"] != false || document["projectId"] != "project-1" || document["repositoryId"] != "tools" || document["baseline"] != "main" {
		t.Fatalf("JSON identity = %#v", document)
	}
	entries := document["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	companionAssertKeys(t, entries[0].(map[string]any), "kind", "branch", "previousHead", "resultingHead", "action", "status")
	companionAssertKeys(t, entries[1].(map[string]any), "kind", "workspace", "branch", "previousHead", "resultingHead", "action", "status")

	stdout.Reset()
	err = executeCompanionUpdate(t, &stdout, []string{"tools", "--dry-run", "--json"}, project, nil, dryResult, nil, nil)
	if err != nil || json.Unmarshal(stdout.Bytes(), &document) != nil || document["dryRun"] != true {
		t.Fatalf("dry JSON err=%v document=%#v", err, document)
	}
	entries = document["entries"].([]any)
	companionAssertKeys(t, entries[0].(map[string]any), "kind", "branch", "previousHead", "action", "status", "deferred")
	companionAssertKeys(t, entries[1].(map[string]any), "kind", "workspace", "branch", "previousHead", "resultingHead", "action", "status", "deferred")
}

func TestCompanionUpdateCommandUsesCommandOwnedJSONForPreResultAndSettledFailures(t *testing.T) {
	configured := domain.Project{ID: "project-1", Repositories: []domain.Repository{{ID: "tools", Companion: true, DefaultBranch: "main"}}}
	for _, test := range []struct {
		name       string
		project    domain.Project
		repository string
		resolveErr error
		result     service.CompanionUpdateResult
		runErr     error
		code       string
		exit       int
	}{
		{"resolver", domain.Project{}, "tools", service.NewError(service.ErrorProjectNotFound, errors.New("no project")), service.CompanionUpdateResult{}, nil, "project_not_found", 3},
		{"unknown", configured, "missing", nil, service.CompanionUpdateResult{}, service.NewError(service.ErrorValidation, errors.New("unknown repository \"missing\"")), "validation", 5},
		{"non-companion", domain.Project{ID: "project-1", Repositories: []domain.Repository{{ID: "tools"}}}, "tools", nil, service.CompanionUpdateResult{}, service.NewError(service.ErrorValidation, errors.New("repository \"tools\" is not a companion")), "validation", 5},
		{"missing baseline", domain.Project{ID: "project-1", Repositories: []domain.Repository{{ID: "tools", Companion: true}}}, "tools", nil, service.CompanionUpdateResult{}, service.NewError(service.ErrorValidation, errors.New("companion has no configured baseline")), "validation", 5},
		{"unresolved authority", configured, "tools", nil, service.CompanionUpdateResult{}, service.NewError(service.ErrorConflict, errors.New("configured companion upstream authority is unavailable")), "conflict", 8},
		{"settled dirty", configured, "tools", nil, companionUpdateRealResult("failed", &service.CompanionUpdateFailure{Code: string(service.ErrorDirtyWorkspace), Message: "dirty checkout"}), nil, "dirty_workspace", 7},
		{"settled rollback", configured, "tools", nil, companionUpdateRealResult("failed", &service.CompanionUpdateFailure{Code: string(service.ErrorRollbackIncomplete), Message: "restore did not complete"}), nil, "rollback_incomplete", 9},
		{"settled dirty fact", configured, "tools", nil, companionUpdateFactFailure("dirty"), nil, "dirty_workspace", 7},
		{"settled conflict fact", configured, "tools", nil, companionUpdateFactFailure("diverged"), nil, "conflict", 8},
		{"settled state publication fact", configured, "tools", nil, companionUpdateFactFailure("state-publication-failed"), nil, "internal", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := executeCompanionUpdate(t, &stdout, []string{test.repository, "--json"}, test.project, test.resolveErr, test.result, test.runErr, nil)
			if err == nil || isOutputFailure(err) || !isRenderedOperationFailure(err) || ExitCode(err) != test.exit || strings.Count(stdout.String(), "\n") != 1 || strings.Contains(stdout.String(), "\n{") {
				t.Fatalf("err=%v exit=%d stdout=%q", err, ExitCode(err), stdout.String())
			}
			var document map[string]any
			if decodeErr := json.Unmarshal(stdout.Bytes(), &document); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			companionAssertKeys(t, document, "version", "operation", "status", "dryRun", "projectId", "repositoryId", "baseline", "entries", "failure")
			companionAssertKeys(t, document["failure"].(map[string]any), "code", "message")
			if document["operation"] != "companion-update" || document["status"] != "failed" || document["failure"].(map[string]any)["code"] != test.code {
				t.Fatalf("document = %#v", document)
			}
		})
	}
}

func TestCompanionUpdateHumanProgressWriterFailureStopsFurtherProgressAndRemainsOutputFailure(t *testing.T) {
	project := domain.Project{ID: "project-1", Repositories: []domain.Repository{{ID: "tools", Companion: true, DefaultBranch: "main"}}}
	writer := &companionFailingWriter{failOn: 2, err: errors.New("writer stopped")}
	result := companionUpdateRealResult("failed", &service.CompanionUpdateFailure{Code: string(service.ErrorInternal), Message: "writer stopped"})
	progress := 0
	err := executeCompanionUpdate(t, writer, []string{"tools"}, project, nil, result, nil, func(request service.CompanionUpdateRequest) {
		if request.Progress == nil {
			t.Fatal("human execution omitted progress callback")
		}
		for _, entry := range result.Entries {
			progress++
			if callbackErr := request.Progress(entry); callbackErr != nil {
				return
			}
		}
	})
	if !errors.Is(err, writer.err) || !isOutputFailure(err) || ExitCode(err) != 1 || progress != 1 || strings.Contains(writer.output.String(), "workspace:") || strings.Contains(writer.output.String(), "Companion update result") {
		t.Fatalf("err=%v progress=%d output=%q", err, progress, writer.output.String())
	}
}

func TestCompanionUpdateHumanOperationFailureIsNotAnOutputFailure(t *testing.T) {
	project := domain.Project{ID: "project-1", Repositories: []domain.Repository{{ID: "tools", Companion: true, DefaultBranch: "main"}}}
	var stdout bytes.Buffer
	result := companionUpdateFactFailure("dirty")
	err := executeCompanionUpdate(t, &stdout, []string{"tools"}, project, nil, result, nil, func(request service.CompanionUpdateRequest) {
		for _, entry := range result.Entries {
			if callbackErr := request.Progress(entry); callbackErr != nil {
				return
			}
		}
	})
	if err == nil || isOutputFailure(err) || isRenderedOperationFailure(err) || ExitCode(err) != 7 || !strings.Contains(stdout.String(), "reason=dirty") || !strings.Contains(stdout.String(), "Failure: dirty_workspace:") {
		t.Fatalf("err=%v exit=%d stdout=%q", err, ExitCode(err), stdout.String())
	}
}

func TestCompanionUpdateHelpAndRootReferenceAreDistinctFromUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := NewRootCommand(&stdout, &stderr)
	root.SetArgs([]string{"companion", "update", "--help"})
	if err := root.Execute(); err != nil || !strings.Contains(stdout.String(), "wtree companion update <repository>") || !strings.Contains(stdout.String(), "distinct from `wtree update`") || !strings.Contains(stdout.String(), "wtree companion update tools --json") {
		t.Fatalf("companion help err=%v stdout=%q", err, stdout.String())
	}
	stdout.Reset()
	root = NewRootCommand(&stdout, &stderr)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil || !strings.Contains(stdout.String(), "companion  fetch and safely advance one configured companion baseline") || !strings.Contains(stdout.String(), "update     reconcile a portable-manifest update safely") {
		t.Fatalf("root help err=%v stdout=%q", err, stdout.String())
	}
}

func executeCompanionUpdate(t *testing.T, stdout io.Writer, arguments []string, project domain.Project, resolveErr error, result service.CompanionUpdateResult, runErr error, progress func(service.CompanionUpdateRequest)) error {
	t.Helper()
	projectPath, dataDir := "project", "data"
	resolve := func(context.Context, string, string) (domain.Project, string, error) {
		return project, dataDir, resolveErr
	}
	run := func(_ context.Context, request service.CompanionUpdateRequest) (service.CompanionUpdateResult, error) {
		if progress != nil {
			progress(request)
		}
		return result, runErr
	}
	command := newCompanionUpdateCommandWithRunner(stdout, &projectPath, &dataDir, resolve, run)
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetArgs(arguments)
	return command.Execute()
}

func companionUpdateDryResult() service.CompanionUpdateResult {
	return service.CompanionUpdateResult{Version: 1, Operation: "companion-update", Status: "planned", DryRun: true, ProjectID: "project-1", RepositoryID: "tools", Baseline: "main", Entries: []service.CompanionUpdateEntry{
		{Kind: "baseline", Branch: "main", PreviousHEAD: "111", Action: "none", Status: "planned", Deferred: true},
		{Kind: "workspace", Workspace: "default", Branch: "main", PreviousHEAD: "111", ResultingHEAD: "222", Action: "fast-forward", Status: "planned", Deferred: true},
	}}
}

func companionUpdateRealResult(status string, failure *service.CompanionUpdateFailure) service.CompanionUpdateResult {
	return service.CompanionUpdateResult{Version: 1, Operation: "companion-update", Status: status, ProjectID: "project-1", RepositoryID: "tools", Baseline: "main", Entries: []service.CompanionUpdateEntry{
		{Kind: "baseline", Branch: "main", PreviousHEAD: "111", ResultingHEAD: "222", Action: "fast-forward", Status: "completed"},
		{Kind: "workspace", Workspace: "default", Branch: "main", PreviousHEAD: "111", ResultingHEAD: "222", Action: "fast-forward", Status: "completed"},
	}, Failure: failure}
}

func companionUpdateFactFailure(reason string) service.CompanionUpdateResult {
	return service.CompanionUpdateResult{Version: 1, Operation: "companion-update", Status: "failed", ProjectID: "project-1", RepositoryID: "tools", Baseline: "main", Entries: []service.CompanionUpdateEntry{{Kind: "workspace", Workspace: "default", Action: "none", Status: "failed", Reason: reason}}}
}

func companionAssertKeys(t *testing.T, value map[string]any, want ...string) {
	t.Helper()
	if len(value) != len(want) {
		t.Fatalf("keys = %#v, want %#v", value, want)
	}
	for _, key := range want {
		if _, ok := value[key]; !ok {
			t.Fatalf("missing key %q in %#v", key, value)
		}
	}
}

type companionFailingWriter struct {
	failOn int
	err    error
	writes int
	output bytes.Buffer
}

func (writer *companionFailingWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes >= writer.failOn {
		return 0, writer.err
	}
	return writer.output.Write(value)
}
