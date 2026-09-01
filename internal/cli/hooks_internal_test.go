package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

type hookRenderFailWriter struct{ err error }

func (writer hookRenderFailWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestHookRenderersStopOnWriterFailure(t *testing.T) {
	want := errors.New("writer failed")
	list := service.HookListResult{Groups: []service.HookListGroup{{Source: "portable", Events: []service.HookListEvent{}}}}
	if err := renderHookList(hookRenderFailWriter{err: want}, list); !errors.Is(err, want) {
		t.Fatalf("renderHookList = %v", err)
	}
	mutation := service.HookMutationResult{Added: []string{}, Replaced: []string{}, Unchanged: []string{}, Skipped: []string{}, Conflicting: []string{}}
	if err := renderHookMutation(hookRenderFailWriter{err: want}, "share", mutation); !errors.Is(err, want) {
		t.Fatalf("renderHookMutation = %v", err)
	}
}

func TestRenderHookRetryUsesExactBoundedGrammar(t *testing.T) {
	var output bytes.Buffer
	result := service.HookRetryResult{
		Workspace:        "feature/login",
		Event:            "post-create",
		Source:           "local",
		Status:           "completed",
		ResumedAt:        1,
		CompletedHookIDs: []string{"prepare", "setup"},
	}
	if err := renderHookRetry(&output, result); err != nil {
		t.Fatal(err)
	}
	want := "Workspace: feature/login\nEvent: post-create\nSource: local\nStatus: completed\nResumed at: 1\nCompleted hook IDs: prepare,setup\n"
	if got := output.String(); got != want {
		t.Fatalf("retry render = %q, want %q", got, want)
	}
	output.Reset()
	result.CompletedHookIDs = nil
	if err := renderHookRetry(&output, result); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "Workspace: feature/login\nEvent: post-create\nSource: local\nStatus: completed\nResumed at: 1\nCompleted hook IDs: (none)\n"; got != want {
		t.Fatalf("empty retry render = %q, want %q", got, want)
	}
}

func TestRenderHookRetryStopsOnWriterFailure(t *testing.T) {
	want := errors.New("writer failed")
	err := renderHookRetry(hookRenderFailWriter{err: want}, service.HookRetryResult{CompletedHookIDs: []string{}})
	if !errors.Is(err, want) {
		t.Fatalf("renderHookRetry = %v, want %v", err, want)
	}
}

func TestHookRetryCommandRendersHumanJSONAndPropagatesBoundedFailures(t *testing.T) {
	projectPath, dataDir := pushOutputProject(t)
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath, ProjectPath: projectPath, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	request := func(*cobra.Command) (service.HookManagementRequest, error) {
		return service.HookManagementRequest{Project: resolution.Project, DataDir: dataDir}, nil
	}
	original := newHookRetryService
	t.Cleanup(func() { newHookRetryService = original })
	var received service.HookRetryRequest
	newHookRetryService = func() hookRetryService {
		return hookRetryCLIStub{run: func(_ context.Context, value service.HookRetryRequest) (service.HookRetryResult, error) {
			received = value
			return service.HookRetryResult{Version: 1, Operation: "hooks-retry", Status: "completed", Workspace: "default", Event: "post-create", Source: "local", ResumedAt: 1, CompletedHookIDs: []string{"setup"}}, nil
		}}
	}
	for _, test := range []struct {
		name string
		json bool
		want string
	}{
		{name: "human", want: "Workspace: default\nEvent: post-create\nSource: local\nStatus: completed\nResumed at: 1\nCompleted hook IDs: setup\n"},
		{name: "json", json: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			jsonOutput := test.json
			command := newHooksRetryCommand(&output, &jsonOutput, request)
			command.SetArgs([]string{"default"})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if test.json {
				var got service.HookRetryResult
				if err := json.Unmarshal(output.Bytes(), &got); err != nil || got.Operation != "hooks-retry" || got.CompletedHookIDs == nil || got.CompletedHookIDs[0] != "setup" {
					t.Fatalf("retry JSON = %q, %#v, %v", output.String(), got, err)
				}
			} else if output.String() != test.want {
				t.Fatalf("retry human = %q, want %q", output.String(), test.want)
			}
		})
	}
	if received.Project.ID != resolution.Project.ID || received.Workspace.ID == "" || received.DataDir != dataDir || received.Sink == nil {
		t.Fatalf("retry request = %#v", received)
	}
	newHookRetryService = func() hookRetryService {
		return hookRetryCLIStub{run: func(context.Context, service.HookRetryRequest) (service.HookRetryResult, error) {
			return service.HookRetryResult{}, service.NewError(service.ErrorConflict, errors.New("hooks retry: hook run is stale; a fresh run is required"))
		}}
	}
	command := newHooksRetryCommand(io.Discard, new(bool), request)
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"default"})
	err = command.Execute()
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorConflict {
		t.Fatalf("retry conflict = %v", err)
	}
}

func TestHookRetryExecuteUsesOneJSONDocumentForSuccessConflictSetupAndWriterFailure(t *testing.T) {
	projectPath, dataDir := pushOutputProject(t)
	original := newHookRetryService
	t.Cleanup(func() { newHookRetryService = original })
	arguments := []string{"hooks", "retry", "default", "--data-dir", dataDir, "--project", projectPath}
	for _, test := range []struct {
		name     string
		result   service.HookRetryResult
		runErr   error
		wantExit int
		wantCode string
	}{
		{name: "success", result: service.HookRetryResult{Version: 1, Operation: "hooks-retry", Status: "completed", Workspace: "default", Event: "post-create", Source: "local", CompletedHookIDs: []string{}}, wantExit: 0},
		{name: "conflict", runErr: service.NewError(service.ErrorConflict, errors.New("hooks retry: hook run is stale; a fresh run is required")), wantExit: 8, wantCode: "conflict"},
		{name: "setup", runErr: service.NewError(service.ErrorSetupIncomplete, &service.SetupIncompleteError{Details: service.SetupIncompleteDetails{Operation: "retry", CoreStatus: "completed", SetupStatus: "incomplete", Event: "post-create", FailureKind: service.HookFailureRecord, CompletedHookIDs: []string{}, RetryCommand: "wtree hooks retry default"}}), wantExit: 10, wantCode: "setup_incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			newHookRetryService = func() hookRetryService {
				return hookRetryCLIStub{run: func(context.Context, service.HookRetryRequest) (service.HookRetryResult, error) {
					return test.result, test.runErr
				}}
			}
			var output, stderr bytes.Buffer
			err := Execute(append(append([]string(nil), arguments...), "--json"), &output, &stderr)
			if ExitCode(err) != test.wantExit || strings.Count(output.String(), "\n") != 1 {
				t.Fatalf("retry %s output=%q stderr=%q err=%v exit=%d", test.name, output.String(), stderr.String(), err, ExitCode(err))
			}
			var value map[string]any
			if decodeErr := json.Unmarshal(output.Bytes(), &value); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if test.wantCode == "" {
				if value["operation"] != "hooks-retry" || value["completedHookIds"] == nil {
					t.Fatalf("retry success JSON = %#v", value)
				}
			} else if errorValue, ok := value["error"].(map[string]any); !ok || errorValue["code"] != test.wantCode {
				t.Fatalf("retry error JSON = %#v", value)
			}
		})
	}
	newHookRetryService = func() hookRetryService {
		return hookRetryCLIStub{run: func(context.Context, service.HookRetryRequest) (service.HookRetryResult, error) {
			return service.HookRetryResult{Version: 1, Operation: "hooks-retry", Status: "completed", Workspace: "default", Event: "post-create", Source: "local", CompletedHookIDs: []string{}}, nil
		}}
	}
	writer := &pushFailingWriter{cause: errors.New("writer failed")}
	err := Execute(append(append([]string(nil), arguments...), "--json"), writer, io.Discard)
	if !errors.Is(err, writer.cause) || !isOutputFailure(err) || strings.Contains(writer.output.String(), `"error"`) || strings.Count(writer.output.String(), "\n{") != 0 {
		t.Fatalf("retry writer error output=%q err=%v", writer.output.String(), err)
	}
}

func TestHookRetryCommandPropagatesCanceledContextWithoutRendering(t *testing.T) {
	projectPath, dataDir := pushOutputProject(t)
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath, ProjectPath: projectPath, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	original := newHookRetryService
	t.Cleanup(func() { newHookRetryService = original })
	called := false
	newHookRetryService = func() hookRetryService {
		return hookRetryCLIStub{run: func(ctx context.Context, _ service.HookRetryRequest) (service.HookRetryResult, error) {
			called = true
			return service.HookRetryResult{}, ctx.Err()
		}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	command := newHooksRetryCommand(&output, new(bool), func(*cobra.Command) (service.HookManagementRequest, error) {
		return service.HookManagementRequest{Project: resolution.Project, DataDir: dataDir}, nil
	})
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetContext(ctx)
	command.SetArgs([]string{"default"})
	if err := command.Execute(); !errors.Is(err, context.Canceled) || !called || output.Len() != 0 {
		t.Fatalf("canceled retry err=%v called=%t output=%q", err, called, output.String())
	}
}

func TestHookRetryCommandPropagatesDeadlineWithoutRendering(t *testing.T) {
	projectPath, dataDir := pushOutputProject(t)
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath, ProjectPath: projectPath, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	original := newHookRetryService
	t.Cleanup(func() { newHookRetryService = original })
	called := false
	newHookRetryService = func() hookRetryService {
		return hookRetryCLIStub{run: func(ctx context.Context, _ service.HookRetryRequest) (service.HookRetryResult, error) {
			called = true
			return service.HookRetryResult{}, ctx.Err()
		}}
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var output bytes.Buffer
	command := newHooksRetryCommand(&output, new(bool), func(*cobra.Command) (service.HookManagementRequest, error) {
		return service.HookManagementRequest{Project: resolution.Project, DataDir: dataDir}, nil
	})
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetContext(ctx)
	command.SetArgs([]string{"default"})
	if err := command.Execute(); !errors.Is(err, context.DeadlineExceeded) || !called || output.Len() != 0 {
		t.Fatalf("deadline retry err=%v called=%t output=%q", err, called, output.String())
	}
}

type hookRetryCLIStub struct {
	run func(context.Context, service.HookRetryRequest) (service.HookRetryResult, error)
}

func (stub hookRetryCLIStub) Retry(ctx context.Context, request service.HookRetryRequest) (service.HookRetryResult, error) {
	return stub.run(ctx, request)
}
