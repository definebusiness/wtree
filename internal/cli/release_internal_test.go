package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

type releaseLockFake struct {
	result  service.ReleaseLockResult
	err     error
	request service.ReleaseLockRequest
	calls   int
}

type releaseMaterializeFake struct {
	result  service.ReleaseMaterializeResult
	err     error
	request service.ReleaseMaterializeRequest
	calls   int
}

func (f *releaseMaterializeFake) Materialize(_ context.Context, request service.ReleaseMaterializeRequest) (service.ReleaseMaterializeResult, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}

func (f *releaseLockFake) Lock(_ context.Context, request service.ReleaseLockRequest) (service.ReleaseLockResult, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}

func TestReleaseLockCLICancellationDoesNotRenderResultOrInvokeMutation(t *testing.T) {
	fake := &releaseLockFake{}
	previousService, previousResolve := newReleaseLockService, resolveReleaseContext
	newReleaseLockService = func() releaseLocker { return fake }
	resolveReleaseContext = func(command *cobra.Command, _ string, _ string) (service.Resolution, error) {
		return service.Resolution{}, command.Context().Err()
	}
	t.Cleanup(func() { newReleaseLockService = previousService; resolveReleaseContext = previousResolve })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	err := ExecuteContext(ctx, []string{"release", "lock", "v1"}, &stdout, &stderr)
	if err == nil || fake.calls != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("canceled command err=%v calls=%d stdout=%q stderr=%q", err, fake.calls, stdout.String(), stderr.String())
	}
}

func TestReleaseLockCLIIsRegisteredAndRejectsInvalidArguments(t *testing.T) {
	root := NewRootCommand(&bytes.Buffer{}, &bytes.Buffer{})
	var releaseFound bool
	for _, command := range root.Commands() {
		if command.Name() == "release" {
			releaseFound = true
		}
	}
	if !releaseFound {
		t.Fatal("release command is not registered")
	}
	var stdout, stderr bytes.Buffer
	err := ExecuteContext(context.Background(), []string{"release", "lock", "--json"}, &stdout, &stderr)
	if err == nil || !strings.Contains(stdout.String(), `"code":"invalid_arguments"`) {
		t.Fatalf("invalid release lock = %v stdout=%s", err, stdout.String())
	}
}

func TestReleaseLockCLIRendersHumanJSONAndCoreSuccessAfterHookFailure(t *testing.T) {
	fake := &releaseLockFake{result: service.ReleaseLockResult{Version: 1, Operation: "release-lock", Status: "created", ProjectID: "project", ReleaseName: "v1", LockPath: "/workspace/project.wtree.lock.yml", ManifestSHA256: strings.Repeat("a", 64), Repositories: []service.ReleaseLockRepositoryResult{{ID: "api", Revision: strings.Repeat("b", 40)}}, HooksCompleted: true}}
	previousService, previousResolve := newReleaseLockService, resolveReleaseContext
	newReleaseLockService = func() releaseLocker { return fake }
	resolveReleaseContext = func(*cobra.Command, string, string) (service.Resolution, error) { return service.Resolution{}, nil }
	t.Cleanup(func() { newReleaseLockService = previousService; resolveReleaseContext = previousResolve })
	var human, stderr bytes.Buffer
	if err := ExecuteContext(context.Background(), []string{"release", "lock", "v1"}, &human, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "Release lock: created") || !strings.Contains(human.String(), "Hooks: completed") {
		t.Fatalf("human=%s", human.String())
	}
	var json, jsonErr bytes.Buffer
	if err := ExecuteContext(context.Background(), []string{"release", "lock", "v1", "--json"}, &json, &jsonErr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(json.String(), `"version":1`) || !strings.Contains(json.String(), `"operation":"release-lock"`) {
		t.Fatalf("json=%s", json.String())
	}
	fake.result.HooksCompleted = false
	fake.result.HookFailure = "tag"
	fake.err = service.NewError(service.ErrorSetupIncomplete, &service.SetupIncompleteError{Details: service.SetupIncompleteDetails{Operation: "release lock", CoreStatus: "succeeded", SetupStatus: "failed", Event: "post-release", HookID: "tag"}})
	human.Reset()
	if err := ExecuteContext(context.Background(), []string{"release", "lock", "v1"}, &human, &stderr); err == nil || !strings.Contains(human.String(), "Release lock: created") || !strings.Contains(human.String(), "Hooks: failed after lock success") {
		t.Fatalf("hook failure human=%s err=%v", human.String(), err)
	}
}

func TestReleaseMaterializeCLIRendersHumanJSONAndDryRun(t *testing.T) {
	fake := &releaseMaterializeFake{result: service.ReleaseMaterializeResult{Version: 1, Operation: "release-materialize", Status: "planned", ProjectID: "project", ReleaseName: "v1", LockPath: "/workspace/project.wtree.lock.yml", ManifestSHA256: strings.Repeat("a", 64), DryRun: true, Repositories: []service.ReleaseMaterializeRepositoryResult{{ID: "root", Role: "caller-provided-base", Status: "adopted", Expected: strings.Repeat("a", 40), Observed: strings.Repeat("a", 40)}, {ID: "api", Role: "materialized-child", Status: "planned", Expected: strings.Repeat("b", 40)}}}}
	previous := newReleaseMaterializeService
	newReleaseMaterializeService = func() releaseMaterializer { return fake }
	t.Cleanup(func() { newReleaseMaterializeService = previous })
	var human, stderr bytes.Buffer
	if err := ExecuteContext(context.Background(), []string{"release", "materialize", "project.wtree.lock.yml", "--dry-run"}, &human, &stderr); err != nil {
		t.Fatal(err)
	}
	if !fake.request.DryRun || !strings.Contains(human.String(), "Release materialize: planned") || !strings.Contains(human.String(), "Repository: root") || !strings.Contains(human.String(), "expected=") {
		t.Fatalf("request=%#v human=%s", fake.request, human.String())
	}
	var json, jsonErr bytes.Buffer
	if err := ExecuteContext(context.Background(), []string{"release", "materialize", "project.wtree.lock.yml", "--json"}, &json, &jsonErr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(json.String(), `"operation":"release-materialize"`) || !strings.Contains(json.String(), `"role":"caller-provided-base"`) || strings.Contains(json.String(), "Release materialize:") {
		t.Fatalf("json=%s", json.String())
	}
}

func TestReleaseMaterializeCLIClassifiesCompositeCleanupFailureAsRollbackIncomplete(t *testing.T) {
	sentinel := errors.New("original conflict")
	fake := &releaseMaterializeFake{err: service.NewError(service.ErrorRollbackIncomplete, errors.Join(
		service.NewError(service.ErrorConflict, sentinel),
		errors.New("staging cleanup incomplete"),
	))}
	previous := newReleaseMaterializeService
	newReleaseMaterializeService = func() releaseMaterializer { return fake }
	t.Cleanup(func() { newReleaseMaterializeService = previous })
	var stdout, stderr bytes.Buffer
	err := ExecuteContext(context.Background(), []string{"release", "--data-dir", t.TempDir(), "materialize", "project.wtree.lock.yml", "--json"}, &stdout, &stderr)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("CLI error = %#v, want rollback-incomplete", application)
	}
	if !errors.Is(err, sentinel) || ExitCode(err) != 9 {
		t.Fatalf("CLI error lost original cause or exit category: err=%v exit=%d", err, ExitCode(err))
	}
	if !strings.Contains(stdout.String(), `"code":"rollback_incomplete"`) {
		t.Fatalf("JSON classification = %q, want rollback_incomplete", stdout.String())
	}
}
