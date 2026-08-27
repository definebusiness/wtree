package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/config"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

type updateFailingWriter struct{ err error }

func (writer updateFailingWriter) Write([]byte) (int, error) { return 0, writer.err }

type partialUpdateWriter struct {
	bytes.Buffer
	calls int
	err   error
}

func (writer *partialUpdateWriter) Write(value []byte) (int, error) {
	writer.calls++
	limit := len(value) / 2
	if limit == 0 {
		limit = 1
	}
	_, _ = writer.Buffer.Write(value[:limit])
	return limit, writer.err
}

func TestUpdateHelpAndHowToDescribeDryRunSafety(t *testing.T) {
	for _, arguments := range [][]string{{"update", "--help"}, {"update", "--how-to"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil || !strings.Contains(result.Stdout, "--dry-run") || !strings.Contains(result.Stdout, "relocat") || !strings.Contains(result.Stdout, "delet") {
			t.Fatalf("%v = %#v", arguments, result)
		}
	}
	if result := testutil.RunCommand(t, cli.Execute, "update"); result.Err == nil || cli.ExitCode(result.Err) == 2 {
		t.Fatalf("update without project context = %#v", result)
	}
}

func TestUpdateCommandRejectsUnsupportedSurface(t *testing.T) {
	for _, arguments := range [][]string{{"sync"}, {"update", "--execute"}, {"update", "--dry-run", "unexpected"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Fatalf("%v = %#v, want argument rejection", arguments, result)
		}
	}
}

func TestExecuteUpdateDryRunRendersStableJSONWithoutMutation(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	candidateDirectory := filepath.Join(t.TempDir(), "releases@2")
	if err := os.MkdirAll(candidateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(candidateDirectory, "candidate.wtree.yml")
	contents, err := os.ReadFile(filepath.Join(project.Path, "project.wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	project.CommitFile("project.wtree.yml", string(contents), "publish manifest")
	project.Run(t, "add", "--", ".gitignore")
	project.Run(t, "commit", "-m", "publish ignore")
	project.RunPanic("push")
	if err := os.WriteFile(candidate, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := config.ReadProjectFile(filepath.Join(project.Path, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, local.Project.ID, "default")
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), project.Path)
	if err != nil {
		t.Fatal(err)
	}
	checkout := state.Repositories["root"]
	checkout.Head = head
	state.Repositories["root"] = checkout
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}
	before := captureUpdateDryRunInventory(t, project.Path, data, candidate, local.Project.ID)
	assertUnchanged := func(stage string) {
		t.Helper()
		if after := captureUpdateDryRunInventory(t, project.Path, data, candidate, local.Project.ID); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s mutated update dry-run inventory:\nbefore=%#v\nafter=%#v", stage, before, after)
		}
	}
	resolved, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: project.Path, ProjectPath: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	defaultWorkspace, err := service.RequireWorkspace(resolved.Project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, snapshotErr := service.CollectUpdateSnapshot(context.Background(), resolved.Project, defaultWorkspace, data, candidate, nil)
	if snapshotErr != nil || !snapshot.MayUpdate() {
		t.Fatalf("snapshot = %#v, %v", snapshot.Failures(), snapshotErr)
	}
	human := testutil.RunCommand(t, cli.Execute, "update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--dry-run")
	if human.Err != nil || !strings.Contains(human.Stdout, "No changes made.") {
		t.Fatalf("human update = %#v", human)
	}
	assertUnchanged("human success")
	result := testutil.RunCommand(t, cli.Execute, "update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--dry-run", "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("update = %#v", result)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["operation"] != "update" || bytes.Contains([]byte(result.Stdout), contents) {
		t.Fatalf("output = %s", result.Stdout)
	}
	assertUnchanged("JSON success")
	verbose := testutil.RunCommand(t, cli.Execute, "update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--dry-run", "--json", "--verbose")
	if verbose.Err != nil || verbose.Stderr != "" || !json.Valid([]byte(verbose.Stdout)) {
		t.Fatalf("verbose JSON update = %#v", verbose)
	}
	assertUnchanged("verbose JSON success")
	canonicalProject, canonicalErr := filepath.EvalSymlinks(project.Path)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	humanExecution := testutil.RunCommand(t, cli.Execute, "update", "--project", project.Path, "--data-dir", data, "--from", candidate)
	wantHuman := "Update complete:\n  root: unchanged (unchanged) status=completed mount=. path=" + canonicalProject + " branch=main head=" + head + "\n"
	if humanExecution.Err != nil || humanExecution.Stderr != "" || humanExecution.Stdout != wantHuman {
		t.Fatalf("human v1 completion=%#v, want %q", humanExecution, wantHuman)
	}
	if execution := testutil.RunCommand(t, cli.Execute, "update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--json", "--verbose"); execution.Err != nil || execution.Stderr != "" || !json.Valid([]byte(execution.Stdout)) || strings.Contains(execution.Stdout, "Update complete:") {
		t.Fatalf("non-dry-run update = %#v", execution)
	} else {
		var completion struct {
			Version      int    `json:"version"`
			Operation    string `json:"operation"`
			Status       string `json:"status"`
			ProjectID    string `json:"projectId"`
			Workspace    string `json:"workspace"`
			Repositories []struct {
				ID             string `json:"id"`
				Mount          string `json:"mount"`
				Path           string `json:"path"`
				Branch         string `json:"branch"`
				Classification string `json:"classification"`
				ActualHead     string `json:"actualHead"`
				Action         string `json:"action"`
				Status         string `json:"status"`
			} `json:"repositories"`
		}
		if err := json.Unmarshal([]byte(execution.Stdout), &completion); err != nil || completion.Version != 1 || completion.Operation != "update" || completion.Status != "completed" || completion.ProjectID != local.Project.ID || completion.Workspace != "default" || len(completion.Repositories) != 1 || completion.Repositories[0].ID != "root" || completion.Repositories[0].Mount != "." || completion.Repositories[0].Path != canonicalProject || completion.Repositories[0].Branch != "main" || completion.Repositories[0].Classification != "unchanged" || completion.Repositories[0].ActualHead != head || completion.Repositories[0].Action != "unchanged" || completion.Repositories[0].Status != "completed" {
			t.Fatalf("decoded v1 update completion=%#v err=%v", completion, err)
		}
	}
	before = captureUpdateDryRunInventory(t, project.Path, data, candidate, local.Project.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if err := cli.ExecuteContext(ctx, []string{"update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--dry-run"}, &stdout, &stderr); err == nil {
		t.Fatal("cancelled update succeeded")
	}
	assertUnchanged("cancellation")
	writerErr := errors.New("stdout writer failed")
	if err := cli.ExecuteContext(context.Background(), []string{"update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--dry-run"}, updateFailingWriter{writerErr}, &stderr); !errors.Is(err, writerErr) {
		t.Fatalf("stdout writer error = %v", err)
	}
	assertUnchanged("human writer failure")
	verboseErr := errors.New("stderr writer failed")
	if err := cli.ExecuteContext(context.Background(), []string{"update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--dry-run", "--json", "--verbose"}, &stdout, updateFailingWriter{verboseErr}); err != nil || !json.Valid(stdout.Bytes()) {
		t.Fatalf("verbose JSON suppression error = %v stdout=%q", err, stdout.String())
	}
	assertUnchanged("verbose writer failure")
	partialErr := errors.New("partial JSON writer failed")
	partial := &partialUpdateWriter{err: partialErr}
	if err := cli.ExecuteContext(context.Background(), []string{"update", "--project", project.Path, "--data-dir", data, "--from", candidate, "--dry-run", "--json"}, partial, &stderr); !errors.Is(err, partialErr) || partial.calls != 1 || json.Valid(partial.Bytes()) {
		t.Fatalf("partial JSON writer error=%v calls=%d bytes=%q", err, partial.calls, partial.String())
	}
	assertUnchanged("partial JSON writer failure")
}

func TestUpdateFailureNeverRendersACompletionDocument(t *testing.T) {
	fixture := newUpdateDryRunFixture(t)
	for _, state := range []string{"active", "incomplete"} {
		t.Run(state, func(t *testing.T) {
			writeUpdateJournalState(t, fixture.data, fixture.projectID, state)
			jsonResult := testutil.RunCommand(t, cli.Execute, "update", "--project", fixture.project, "--data-dir", fixture.data, "--json")
			var envelope struct {
				Success bool `json:"success"`
				Error   struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if jsonResult.Err == nil || cli.ExitCode(jsonResult.Err) != 5 || jsonResult.Stderr != "" || json.Unmarshal([]byte(jsonResult.Stdout), &envelope) != nil || envelope.Success || envelope.Error.Code != "validation" || strings.Count(jsonResult.Stdout, "\n") != 1 || strings.Contains(jsonResult.Stdout, `"repositories"`) || strings.Contains(jsonResult.Stdout, `"operationId"`) || strings.Contains(jsonResult.Stdout, "journal") || strings.Contains(jsonResult.Stdout, "receipt") || strings.Contains(jsonResult.Stdout, "secret") {
				t.Fatalf("%s JSON failure=%#v envelope=%#v", state, jsonResult, envelope)
			}
			human := testutil.RunCommand(t, cli.Execute, "update", "--project", fixture.project, "--data-dir", fixture.data)
			if human.Err == nil || cli.ExitCode(human.Err) != 5 || human.Stdout != "" || human.Stderr != "" {
				t.Fatalf("%s human failure=%#v", state, human)
			}
		})
	}
}

// Every command below completes its normal read-only preflight first, then
// reaches ReconcileProject. The journal guard must reject there before any
// command can write a registry/state generation or touch a checkout.
func TestMutatingCommandsRefuseActiveUpdateJournalBeforeMutation(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	workspaceName, managed := prepareBlockedManagedWorkspace(t, root.Path, data)
	external := prepareBlockedExternalWorkspace(t, root.Path)
	blockedCreate := filepath.Join(t.TempDir(), "blocked-create")
	local, err := config.ReadProjectFile(filepath.Join(root.Path, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	writeActiveUpdateJournal(t, data, local.Project.ID)
	before := captureUpdateDryRunInventory(t, root.Path, data, "", local.Project.ID)
	checkoutHeads := map[string]string{}
	for _, checkout := range []string{managed, external} {
		head, headErr := gitadapter.NewAdapter("git").Head(context.Background(), checkout)
		if headErr != nil {
			t.Fatal(headErr)
		}
		checkoutHeads[checkout] = head
	}
	cases := []struct {
		name      string
		arguments []string
	}{
		{name: "create", arguments: []string{"create", "blocked", "--project", root.Path, "--data-dir", data, "--path", blockedCreate}},
		{name: "remove", arguments: []string{"remove", workspaceName, "--project", root.Path, "--data-dir", data}},
		{name: "delete", arguments: []string{"delete", workspaceName, "--project", root.Path, "--data-dir", data}},
		{name: "import", arguments: []string{"import", "--project", root.Path, external, "--name", "blocked-import", "--data-dir", data}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, test.arguments...)
			if result.Err == nil || cli.ExitCode(result.Err) != 8 {
				t.Fatalf("active journal command=%#v", result)
			}
			if after := captureUpdateDryRunInventory(t, root.Path, data, "", local.Project.ID); !reflect.DeepEqual(after, before) {
				t.Fatalf("active journal %s mutated inventory:\nbefore=%#v\nafter=%#v", test.name, before, after)
			}
			for checkout, wantHead := range checkoutHeads {
				gotHead, headErr := gitadapter.NewAdapter("git").Head(context.Background(), checkout)
				if headErr != nil || gotHead != wantHead {
					t.Fatalf("active journal %s checkout=%q head=%q err=%v, want %q", test.name, checkout, gotHead, headErr, wantHead)
				}
			}
			if _, statErr := os.Lstat(blockedCreate); !os.IsNotExist(statErr) {
				t.Fatalf("active journal %s created checkout %q: %v", test.name, blockedCreate, statErr)
			}
		})
	}
	for _, arguments := range [][]string{{"status", "default", "--project", root.Path, "--data-dir", data, "--json"}, {"doctor", "--project", root.Path, "default", "--data-dir", data, "--json"}} {
		readOnly := testutil.RunCommand(t, cli.Execute, arguments...)
		if readOnly.Err != nil || readOnly.Stderr != "" || !json.Valid([]byte(readOnly.Stdout)) {
			t.Fatalf("read-only command %v = %#v", arguments, readOnly)
		}
	}
}

func prepareBlockedManagedWorkspace(t *testing.T, project, data string) (string, string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "managed")
	if result := testutil.RunCommand(t, cli.Execute, "create", "blocked-workspace", "--project", project, "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("prepare managed workspace = %#v", result)
	}
	return "blocked-workspace", target
}

func prepareBlockedExternalWorkspace(t *testing.T, project string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "external")
	repository := testutil.GitRepository{Path: project}
	repository.Run(t, "branch", "blocked-import")
	repository.Run(t, "worktree", "add", target, "blocked-import")
	return target
}

func writeActiveUpdateJournal(t *testing.T, data, projectID string) {
	writeUpdateJournalState(t, data, projectID, "active")
}

func writeUpdateJournalState(t *testing.T, data, projectID, state string) {
	t.Helper()
	path, err := service.UpdateJournalPath(data, projectID, "update-0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	journal := service.UpdateJournal{Version: service.UpdateJournalVersion, OperationID: "update-0123456789abcdef01234567", ProjectID: projectID, PlanDigest: digest, Generations: service.UpdatePlanGenerations{CurrentManifestSHA256: digest, CandidateManifestSHA256: digest, LocalConfigSHA256: digest, RegistrySHA256: digest, DefaultStateSHA256: digest}, RollbackState: state}
	if state == "incomplete" {
		journal.Failure = "interrupted update"
		journal.Progress = []service.UpdateJournalEffect{{Sequence: 1, Name: "recovery-retained", State: "unreverted"}}
	}
	encoded, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCommandProductionSourceAuthorityAndRedaction(t *testing.T) {
	fixture := newUpdateDryRunFixture(t)
	localDirectory := filepath.Join(t.TempDir(), "releases@2")
	if err := os.MkdirAll(localDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	localCandidate := filepath.Join(localDirectory, "candidate.wtree.yml")
	if err := os.WriteFile(localCandidate, fixture.manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	serveCandidate := func(t *testing.T, expectedPath string) (*httptest.Server, *int) {
		t.Helper()
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requests++
			if request.URL.Path != expectedPath || request.URL.User != nil || request.URL.RawQuery != "" || request.URL.Fragment != "" {
				t.Fatalf("unexpected candidate request %#v", request.URL)
			}
			_, _ = writer.Write(fixture.manifest)
		}))
		return server, &requests
	}
	runSuccess := func(t *testing.T, label string, candidate string, arguments ...string) testutil.Result {
		t.Helper()
		before := captureUpdateDryRunInventory(t, fixture.project, fixture.data, candidate, fixture.projectID)
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil {
			t.Fatalf("%s = %#v", label, result)
		}
		if after := captureUpdateDryRunInventory(t, fixture.project, fixture.data, candidate, fixture.projectID); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s mutated update dry-run inventory:\nbefore=%#v\nafter=%#v", label, before, after)
		}
		return result
	}

	// RED: a stored local source with an @ path must flow through the actual
	// CLI -> resolver -> CollectUpdateSnapshot path, not only planner helpers.
	fixture.setStoredSource(t, localCandidate)
	storedLocal := runSuccess(t, "stored local @", localCandidate, "update", "--project", fixture.project, "--data-dir", fixture.data, "--dry-run")
	if !strings.Contains(storedLocal.Stdout, "Source: "+localCandidate) || !strings.Contains(storedLocal.Stdout, "No changes made.") {
		t.Fatalf("stored local @ output = %#v", storedLocal)
	}

	// RED: stored HTTP @ sources are also production-loaded once, and their
	// exact source survives the JSON rendering without candidate bytes.
	httpStored, storedRequests := serveCandidate(t, "/releases@2/project.wtree.yml")
	defer httpStored.Close()
	storedHTTPSource := httpStored.URL + "/releases@2/project.wtree.yml"
	fixture.setStoredSource(t, storedHTTPSource)
	storedHTTP := runSuccess(t, "stored HTTP @", "", "update", "--project", fixture.project, "--data-dir", fixture.data, "--dry-run", "--json")
	if *storedRequests != 1 || !strings.Contains(storedHTTP.Stdout, storedHTTPSource) || bytes.Contains([]byte(storedHTTP.Stdout), fixture.manifest) {
		t.Fatalf("stored HTTP @ source authority = requests=%d result=%#v", *storedRequests, storedHTTP)
	}

	// RED: local --from must not open the selected stored local source. The
	// absent path makes a mistaken fallback observable without a test seam.
	missingStored := filepath.Join(t.TempDir(), "must-not-open.wtree.yml")
	fixture.setStoredSource(t, missingStored)
	localOverride := runSuccess(t, "local --from override", localCandidate, "update", "--project", fixture.project, "--data-dir", fixture.data, "--from", localCandidate, "--dry-run")
	if !strings.Contains(localOverride.Stdout, "Source: "+localCandidate) || strings.Contains(localOverride.Stdout, missingStored) {
		t.Fatalf("local override source authority = %#v", localOverride)
	}

	// RED: HTTP --from has a counted selected source and a deliberately failing
	// stored endpoint. The stored handler must receive no request at all.
	storedContacted := 0
	failingStored := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { storedContacted++ }))
	defer failingStored.Close()
	overrideHTTP, overrideRequests := serveCandidate(t, "/releases@2/override.wtree.yml")
	defer overrideHTTP.Close()
	overrideHTTPSource := overrideHTTP.URL + "/releases@2/override.wtree.yml"
	fixture.setStoredSource(t, failingStored.URL+"/stored.wtree.yml")
	overrideResult := runSuccess(t, "HTTP --from override", "", "update", "--project", fixture.project, "--data-dir", fixture.data, "--from", overrideHTTPSource, "--dry-run", "--json")
	if storedContacted != 0 || *overrideRequests != 1 || !strings.Contains(overrideResult.Stdout, overrideHTTPSource) {
		t.Fatalf("HTTP override source authority = stored=%d override=%d result=%#v", storedContacted, *overrideRequests, overrideResult)
	}

	// RED: malformed credential-bearing HTTP overrides must fail before a
	// request and never leak the userinfo/query/fragment secret through either
	// human or JSON error rendering.
	for _, source := range []string{
		"https://user:very-secret@example.invalid/project.wtree.yml",
		"https://example.invalid/project.wtree.yml?token=very-secret",
		"https://example.invalid/project.wtree.yml#very-secret",
	} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run("refusal/"+source+"/json="+strconv.FormatBool(jsonOutput), func(t *testing.T) {
				arguments := []string{"update", "--project", fixture.project, "--data-dir", fixture.data, "--from", source, "--dry-run"}
				if jsonOutput {
					arguments = append(arguments, "--json")
				}
				before := captureUpdateDryRunInventory(t, fixture.project, fixture.data, "", fixture.projectID)
				result := testutil.RunCommand(t, cli.Execute, arguments...)
				if result.Err == nil || cli.ExitCode(result.Err) != 5 {
					t.Fatalf("refusal = %#v", result)
				}
				combined := result.Stdout + result.Stderr + result.Err.Error()
				if strings.Contains(combined, "very-secret") || strings.Contains(combined, "user:") || strings.Contains(combined, "token=") {
					t.Fatalf("credential leaked from refusal: %#v", result)
				}
				if jsonOutput {
					var envelope struct {
						Success bool `json:"success"`
						Error   struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					if result.Stderr != "" || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Success || envelope.Error.Code != "validation" {
						t.Fatalf("JSON refusal = %#v envelope=%#v", result, envelope)
					}
				} else if result.Stdout != "" || result.Stderr != "" {
					t.Fatalf("human refusal rendered unexpected output: %#v", result)
				}
				if after := captureUpdateDryRunInventory(t, fixture.project, fixture.data, "", fixture.projectID); !reflect.DeepEqual(after, before) {
					t.Fatalf("refusal mutated update dry-run inventory:\nbefore=%#v\nafter=%#v", before, after)
				}
			})
		}
	}
}

type updateDryRunFixture struct {
	project   string
	data      string
	projectID string
	manifest  []byte
}

func newUpdateDryRunFixture(t *testing.T) updateDryRunFixture {
	t.Helper()
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	manifest, err := os.ReadFile(filepath.Join(project.Path, "project.wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	project.CommitFile("project.wtree.yml", string(manifest), "publish manifest")
	project.Run(t, "add", "--", ".gitignore")
	project.Run(t, "commit", "-m", "publish ignore")
	project.RunPanic("push")
	local, err := config.ReadProjectFile(filepath.Join(project.Path, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, local.Project.ID, "default")
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), project.Path)
	if err != nil {
		t.Fatal(err)
	}
	checkout := state.Repositories["root"]
	checkout.Head = head
	state.Repositories["root"] = checkout
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}
	return updateDryRunFixture{project: project.Path, data: data, projectID: local.Project.ID, manifest: manifest}
}

func (fixture updateDryRunFixture) setStoredSource(t *testing.T, source string) {
	t.Helper()
	path := filepath.Join(fixture.project, ".wtree.yml")
	local, err := config.ReadProjectFile(path)
	if err != nil {
		t.Fatal(err)
	}
	local.Manifest.Source = source
	if err := config.WriteProjectFile(path, local); err != nil {
		t.Fatal(err)
	}
}

type updateDryRunFileFact struct {
	Mode  fs.FileMode
	Bytes []byte
}

type updateDryRunInventory struct {
	Files  map[string]updateDryRunFileFact
	Absent map[string]bool
	Git    map[string]string
}

// captureUpdateDryRunInventory records every command-owned data entry and
// project working-tree entry (excluding Git's internal object store), plus
// exact Git facts and all currently absent journal/lock authorities. It makes
// a dry-run mutation observable even when the original file bytes are later
// restored by accident.
func captureUpdateDryRunInventory(t *testing.T, project, data, candidate, projectID string) updateDryRunInventory {
	t.Helper()
	result := updateDryRunInventory{Files: map[string]updateDryRunFileFact{}, Absent: map[string]bool{}, Git: map[string]string{}}
	captureTree := func(label, root string, skipGit bool) {
		t.Helper()
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if skipGit && path != root && entry.IsDir() && entry.Name() == ".git" {
				return filepath.SkipDir
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			key := label + ":" + filepath.ToSlash(strings.TrimPrefix(path, root))
			fact := updateDryRunFileFact{Mode: info.Mode()}
			if info.Mode().IsRegular() {
				fact.Bytes, err = os.ReadFile(path)
				if err != nil {
					return err
				}
			}
			result.Files[key] = fact
			return nil
		})
		if err != nil {
			t.Fatalf("capture %s tree: %v", label, err)
		}
	}
	captureTree("project", project, true)
	captureTree("data", data, false)
	if candidate != "" {
		if info, err := os.Lstat(candidate); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("candidate source disappeared or changed type: %v", err)
		} else if bytes, readErr := os.ReadFile(candidate); readErr != nil {
			t.Fatal(readErr)
		} else {
			result.Files["candidate"] = updateDryRunFileFact{Mode: info.Mode(), Bytes: bytes}
		}
	}
	for _, path := range []string{
		filepath.Join(data, "projects", projectID, "reconciliation.json"),
		filepath.Join(data, "projects", projectID, "recovery"),
		filepath.Join(data, "projects", projectID, "update"),
		filepath.Join(data, "projects", projectID, "update.lock"),
		filepath.Join(data, "projects", projectID, "lock"),
		filepath.Join(data, "registry.lock"),
	} {
		_, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("inspect %q: %v", path, err)
		}
		result.Absent[path] = os.IsNotExist(err)
	}
	for name, arguments := range map[string][]string{
		"head":     {"rev-parse", "HEAD"},
		"show-ref": {"show-ref", "--head"},
		"status":   {"status", "--porcelain=v1", "--untracked-files=all"},
		"index":    {"rev-parse", "--git-path", "index"},
	} {
		output, err := exec.Command("git", append([]string{"-C", project}, arguments...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", name, err, output)
		}
		result.Git[name] = string(output)
	}
	indexPath := strings.TrimSpace(result.Git["index"])
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(project, indexPath)
	}
	if index, err := os.ReadFile(indexPath); err != nil {
		t.Fatalf("read Git index %q: %v", indexPath, err)
	} else {
		result.Files["git-index"] = updateDryRunFileFact{Mode: 0, Bytes: index}
	}
	return result
}
