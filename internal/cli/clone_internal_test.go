package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/testutil"
)

type cloneLifecycleStub struct {
	run func(context.Context, service.CloneLifecycleRequest) (service.CloneLifecycleResult, error)
}

func (stub cloneLifecycleStub) Clone(ctx context.Context, request service.CloneLifecycleRequest) (service.CloneLifecycleResult, error) {
	return stub.run(ctx, request)
}

func TestCloneSetupIncompleteCancellationAndDeadlineRenderCommittedCore(t *testing.T) {
	manifest := cloneSetupIncompleteManifest(t)
	original := newCloneLifecycleCoordinator
	t.Cleanup(func() { newCloneLifecycleCoordinator = original })
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "canceled", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			newCloneLifecycleCoordinator = func() cloneLifecycleService {
				return cloneLifecycleStub{run: func(_ context.Context, request service.CloneLifecycleRequest) (service.CloneLifecycleResult, error) {
					core := service.CloneExecutionResult{ProjectID: request.Plan.Project.ID, Destination: request.Plan.Destination.Path, LogicalRoot: request.Plan.LogicalRoot, BaseRepository: request.Plan.BaseRepository}
					details := service.SetupIncompleteDetails{Operation: "clone", CoreStatus: "completed", SetupStatus: "incomplete", Event: config.HookEventPostClone, FailureKind: service.HookFailureCanceled, CompletedHookIDs: []string{}, RetryCommand: "wtree hooks retry default"}
					if errors.Is(test.cause, context.DeadlineExceeded) {
						details.FailureKind, details.Timeout = service.HookFailureTimeout, true
					}
					return service.CloneLifecycleResult{Core: core, HooksApplicable: true, CompletedHookIDs: []string{}}, service.NewError(service.ErrorSetupIncomplete, &service.SetupIncompleteError{Details: details, Cause: test.cause})
				}}
			}
			humanRoot, humanData := canonicalCloneSetupTempDir(t), canonicalCloneSetupTempDir(t)
			human := testutil.RunCommand(t, Execute, "clone", manifest, filepath.Join(humanRoot, "human"), "--data-dir", humanData, "--run-hooks")
			if !errors.Is(human.Err, test.cause) || !strings.Contains(human.Stdout, "Cloned project:") || strings.Contains(human.Stdout, "rollback") {
				t.Fatalf("human post-publication %s = %#v", test.name, human)
			}
			jsonRoot, jsonData := canonicalCloneSetupTempDir(t), canonicalCloneSetupTempDir(t)
			json := testutil.RunCommand(t, Execute, "clone", manifest, filepath.Join(jsonRoot, "json"), "--data-dir", jsonData, "--run-hooks", "--json")
			if !errors.Is(json.Err, test.cause) || !strings.Contains(json.Stdout, `"code":"setup_incomplete"`) || !strings.Contains(json.Stdout, `"operation":"clone"`) || strings.Contains(json.Stdout, "rollback") {
				t.Fatalf("JSON post-publication %s = %#v", test.name, json)
			}
		})
	}
}

func cloneSetupIncompleteManifest(t *testing.T) string {
	t.Helper()
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("README.md", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, Execute, "init", repository.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init clone setup fixture = %#v", result)
	}
	repository.Run(t, "add", ".gitignore", "project.wtree.yml")
	repository.Run(t, "commit", "-m", "publish manifest")
	repository.Run(t, "push", "origin", "main")
	manifest := filepath.Join(repository.Path, "project.wtree.yml")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal(err)
	}
	return canonicalCloneSetupPath(t, manifest)
}

func canonicalCloneSetupTempDir(t *testing.T) string {
	t.Helper()
	return canonicalCloneSetupPath(t, t.TempDir())
}

func canonicalCloneSetupPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
