package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestPushCompletePublicHumanAndJSONResultMatrix(t *testing.T) {
	projectPath, dataRoot := pushOutputProject(t)
	t.Setenv("WTREE_DATA_HOME", dataRoot)
	type scenario struct {
		name       string
		value      service.PushResult
		runErr     error
		exit       int
		humanExact string
	}
	failure := func(code service.ErrorKind, message string) *service.AggregateFailure {
		return &service.AggregateFailure{Code: code, Message: message}
	}
	row := func(status service.PushStatus) service.PushRepositoryResult {
		return service.PushRepositoryResult{ID: "root", Mount: ".", Path: "/private/checkout/must-not-leak", Branch: "main", Head: strings.Repeat("a", 40), ObservedCommit: strings.Repeat("a", 40), Status: status}
	}
	ready := service.PushResult{Version: 1, Operation: "push", Status: service.PushStatusReady, ProjectID: "push-output", Workspace: "default", Repositories: []service.PushRepositoryResult{row(service.PushStatusReady)}}
	blockedRow := row(service.PushStatusBlocked)
	blockedRow.Findings = []service.PushFinding{{Code: "dirty", Message: "checkout has uncommitted changes"}}
	blocked := service.PushResult{Version: 1, Operation: "push", Status: service.PushStatusBlocked, ProjectID: "push-output", Workspace: "default", Repositories: []service.PushRepositoryResult{blockedRow}}
	failedRow := row(service.PushStatusFailed)
	failedRow.Failure = failure(service.ErrorGit, "Git observation failed")
	failed := service.PushResult{Version: 1, Operation: "push", Status: service.PushStatusFailed, ProjectID: "push-output", Workspace: "default", Repositories: []service.PushRepositoryResult{failedRow}, Failure: failure(service.ErrorGit, "Git observation failed")}
	partialRow := row(service.PushStatusBlocked)
	partialRow.Findings = []service.PushFinding{{Code: "partial-workspace", Message: "workspace is partial"}}
	partial := service.PushResult{Version: 1, Operation: "push", Status: service.PushStatusBlocked, ProjectID: "push-output", Workspace: "partial", Partial: true, MissingRepositoryIDs: []string{"leaf"}, Repositories: []service.PushRepositoryResult{partialRow}}
	canceledRow := row(service.PushStatusCanceled)
	canceledRow.Failure = failure(service.ErrorInternal, "push readiness canceled")
	canceled := service.PushResult{Version: 1, Operation: "push", Status: service.PushStatusFailed, ProjectID: "push-output", Workspace: "default", Repositories: []service.PushRepositoryResult{canceledRow}, Failure: failure(service.ErrorInternal, "push readiness canceled")}
	writerRow := row(service.PushStatusCanceled)
	writerRow.Failure = failure(service.ErrorInternal, "push readiness output failed")
	writer := service.PushResult{Version: 1, Operation: "push", Status: service.PushStatusFailed, ProjectID: "push-output", Workspace: "default", Repositories: []service.PushRepositoryResult{writerRow}, Failure: failure(service.ErrorInternal, "push readiness output failed")}
	tests := []scenario{
		{"ready", ready, nil, 0, "Repository: root status=ready\nWorkspace: default status=ready\n"},
		{"blocked", blocked, service.NewError(service.ErrorConflict, errors.New("push readiness is blocked")), 8, "Repository: root status=blocked\nfinding: dirty: checkout has uncommitted changes\nWorkspace: default status=blocked\n"},
		{"failed", failed, service.NewError(service.ErrorGit, errors.New("Git observation failed")), 6, "Repository: root status=failed\nfailure: git: Git observation failed\nWorkspace: default status=failed\n"},
		{"partial", partial, service.NewError(service.ErrorConflict, errors.New("push readiness is blocked")), 8, "Repository: root status=blocked\nfinding: partial-workspace: workspace is partial\nWorkspace: partial status=blocked\n"},
		{"canceled", canceled, context.Canceled, 1, "Repository: root status=canceled\nfailure: internal: push readiness canceled\nWorkspace: default status=failed\n"},
		{"writer", writer, service.NewError(service.ErrorInternal, errors.New("push readiness output failed")), 1, "Repository: root status=canceled\nfailure: internal: push readiness output failed\nWorkspace: default status=failed\n"},
	}
	for _, test := range tests {
		t.Run(test.name+" human", func(t *testing.T) {
			var stdout bytes.Buffer
			err := executePushWithResult(t, &stdout, projectPath, nil, test.value, test.runErr)
			if ExitCode(err) != test.exit || stdout.String() != test.humanExact {
				t.Fatalf("human %s = %q, %v exit=%d", test.name, stdout.String(), err, ExitCode(err))
			}
			pushAssertNoPrivateOutput(t, stdout.String())
		})
		t.Run(test.name+" json", func(t *testing.T) {
			var stdout bytes.Buffer
			err := executePushWithResult(t, &stdout, projectPath, []string{"--json"}, test.value, test.runErr)
			if ExitCode(err) != test.exit || strings.Count(stdout.String(), "\n") != 1 {
				t.Fatalf("JSON %s = %q, %v exit=%d", test.name, stdout.String(), err, ExitCode(err))
			}
			var document map[string]any
			if decodeErr := json.Unmarshal(stdout.Bytes(), &document); decodeErr != nil {
				t.Fatalf("decode %s JSON: %v: %q", test.name, decodeErr, stdout.String())
			}
			pushAssertJSONFields(t, document, test.value)
			pushAssertNoPrivateOutput(t, stdout.String())
		})
	}
}

func TestPushWriterFailureKeepsOriginalIdentityAndNeverAppendsSecondDocument(t *testing.T) {
	projectPath, dataRoot := pushOutputProject(t)
	t.Setenv("WTREE_DATA_HOME", dataRoot)
	sentinel := errors.New("writer sentinel")
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			writer := &pushFailingWriter{cause: sentinel}
			value := service.PushResult{Version: 1, Operation: "push", Status: service.PushStatusReady, ProjectID: "push-output", Workspace: "default", Repositories: []service.PushRepositoryResult{{ID: "root", Mount: ".", Path: "/private/must-not-leak", Status: service.PushStatusReady}}}
			arguments := []string(nil)
			if jsonOutput {
				arguments = []string{"--json"}
			}
			err := executePushWithResult(t, writer, projectPath, arguments, value, nil)
			if !errors.Is(err, sentinel) || !isOutputFailure(err) || writer.writes != 1 || strings.Contains(writer.output.String(), `"error"`) || strings.Count(writer.output.String(), "\n{") > 0 {
				t.Fatalf("writer JSON=%v = %q, %v writes=%d", jsonOutput, writer.output.String(), err, writer.writes)
			}
		})
	}
}

func executePushWithResult(t *testing.T, stdout io.Writer, projectPath string, arguments []string, value service.PushResult, runErr error) error {
	t.Helper()
	runner := func(_ context.Context, _ domain.Project, _ domain.Workspace, request service.PushRequest) (service.PushResult, error) {
		if request.OnComplete != nil {
			for _, repository := range value.Repositories {
				if err := request.OnComplete(repository); err != nil {
					return value, err
				}
			}
		}
		return value, runErr
	}
	command := newPushCommandWithRunner(stdout, &projectPath, runner)
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs(arguments)
	return command.Execute()
}

func pushOutputProject(t *testing.T) (string, string) {
	t.Helper()
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	dataRoot := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", repository.Path, "--data-dir", dataRoot}, &stdout, &stderr); err != nil {
		t.Fatalf("initialize output fixture: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	return repository.Path, dataRoot
}

func pushAssertJSONFields(t *testing.T, document map[string]any, want service.PushResult) {
	t.Helper()
	allowedRoot := map[string]bool{"version": true, "operation": true, "status": true, "projectId": true, "workspace": true, "partial": true, "missingRepositoryIds": true, "repositories": true, "failure": true}
	for key := range document {
		if !allowedRoot[key] {
			t.Fatalf("unapproved push root field %q in %#v", key, document)
		}
	}
	if document["version"] != float64(1) || document["operation"] != "push" || document["status"] != string(want.Status) || document["workspace"] != want.Workspace {
		t.Fatalf("push JSON identity = %#v, want %#v", document, want)
	}
	repositories, ok := document["repositories"].([]any)
	if !ok || len(repositories) != len(want.Repositories) {
		t.Fatalf("push JSON repositories = %#v", document["repositories"])
	}
	allowedRepository := map[string]bool{"id": true, "parentId": true, "mount": true, "branch": true, "head": true, "observedCommit": true, "status": true, "findings": true, "failure": true}
	for index, raw := range repositories {
		repository, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("push repository %d = %#v", index, raw)
		}
		for key := range repository {
			if !allowedRepository[key] {
				t.Fatalf("unapproved push repository field %q in %#v", key, repository)
			}
		}
		if repository["id"] != want.Repositories[index].ID || repository["status"] != string(want.Repositories[index].Status) {
			t.Fatalf("push repository %d = %#v, want %#v", index, repository, want.Repositories[index])
		}
	}
}

func pushAssertNoPrivateOutput(t *testing.T, output string) {
	t.Helper()
	for _, forbidden := range []string{"/private/", "https://", "super-secret", "remote.origin.url", ".wtree.yml"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("push output leaked %q: %q", forbidden, output)
		}
	}
}

type pushFailingWriter struct {
	cause  error
	writes int
	output bytes.Buffer
}

func (writer *pushFailingWriter) Write(value []byte) (int, error) {
	writer.writes++
	_, _ = writer.output.Write(value)
	return 0, writer.cause
}
