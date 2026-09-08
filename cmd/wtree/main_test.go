package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestRunMapsInvalidArgumentsToExitCodeTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if got := run([]string{"unknown-command"}, &stdout, &stderr); got != 2 {
		t.Errorf("run() = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr = %q, want unknown-command diagnostic", stderr.String())
	}
}

func TestRunRejectsTrailingArgumentsAfterVersionOrHelp(t *testing.T) {
	for _, args := range [][]string{
		{"--version", "x"},
		{"-v", "x"},
		{"--help", "x"},
		{"-h", "x"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if got := run(args, &stdout, &stderr); got != 2 {
				t.Errorf("run() = %d, want 2", got)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "does not accept arguments") {
				t.Errorf("stderr = %q, want invalid-argument diagnostic", stderr.String())
			}
		})
	}
}

func TestRunJSONOperationalFailureDoesNotWriteHumanStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if got := run([]string{"init", t.TempDir(), "--data-dir", t.TempDir(), "--json"}, &stdout, &stderr); got != 1 {
		t.Errorf("run() = %d, want 1", got)
	}
	if !strings.Contains(stdout.String(), `"success":false`) || strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("stdout = %q, want one JSON error object", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunClassifiesLegacyRootErrorForJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"--version", "--json"}, &stdout, &stderr); got != 2 {
		t.Fatalf("run() = %d, want 2", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode JSON error: %v; stdout=%q", err, stdout.String())
	}
	if envelope.Error.Code != "invalid_arguments" {
		t.Fatalf("JSON code = %q, want invalid_arguments", envelope.Error.Code)
	}
}

func TestWorkspaceSelectionErrorsStayAtTheProcessBoundary(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if err := cli.Execute([]string{"init", project.Path, "--data-dir", data}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The logical state name may contain JSON-escaped punctuation independently
	// of its persisted ID, branch, and checkout path. Keep those physical
	// fixture values portable for native Windows execution.
	physicalNames := []string{"feature/search-one", "feature/search-two"}
	for _, name := range physicalNames {
		if err := cli.Execute([]string{"create", "--project", project.Path, name, "--data-dir", data, "--path", filepath.Join(t.TempDir(), strings.ReplaceAll(name, "/", "-"))}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil || len(registry.Projects) != 1 {
		t.Fatalf("registry = %#v, %v", registry, err)
	}
	var projectID string
	for projectID = range registry.Projects {
	}
	statePath := service.WorkspaceStatePath(data, projectID, pathutil.StorageName(physicalNames[0]))
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, physical := range append([]string{state.Path}, state.Repositories["root"].Branch) {
		if strings.Contains(physical, `"`) {
			t.Fatalf("portable fixture physical path or ref contains a quote: %q", physical)
		}
	}
	state.Name = `feature/search-"one"`
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"path", "search", "--project", project.Path, "--data-dir", data}
	var stdout, stderr bytes.Buffer
	if code := run(arguments, &stdout, &stderr); code != 8 || stdout.Len() != 0 {
		t.Fatalf("human selection exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{`"search"`, `feature/search-\"one\"`, `"feature/search-two"`, "narrow the query", "--exact"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("human stderr = %q, missing %q", stderr.String(), want)
		}
	}

	stdout.Reset()
	stderr.Reset()
	jsonArguments := []string{"status", "search", "--project", project.Path, "--data-dir", data, "--json"}
	if code := run(jsonArguments, &stdout, &stderr); code != 8 || stderr.Len() != 0 || strings.Count(stdout.String(), "\n") != 1 {
		t.Fatalf("JSON selection exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Selection struct {
				Query      string `json:"query"`
				Mode       string `json:"mode"`
				Candidates []struct {
					Name string `json:"name"`
				} `json:"candidates"`
			} `json:"selection"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.Error.Code != "conflict" || envelope.Error.Selection.Query != "search" || envelope.Error.Selection.Mode != "substring" || len(envelope.Error.Selection.Candidates) != 2 || envelope.Error.Selection.Candidates[0].Name != `feature/search-"one"` {
		t.Fatalf("JSON selection = %q decode=%v", stdout.String(), err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"status", "absent", "--project", project.Path, "--data-dir", data, "--json"}, &stdout, &stderr); code != 4 || stderr.Len() != 0 || strings.Count(stdout.String(), "\n") != 1 || !strings.Contains(stdout.String(), `"candidates":[]`) {
		t.Fatalf("JSON no-match exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestWorkspaceSelectionEOFProcessKeepsScalarPathOutput(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if err := cli.Execute([]string{"init", project.Path, "--data-dir", data}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := cli.Execute([]string{"create", "--project", project.Path, "feature/alpha-eof", "--data-dir", data, "--path", target}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		mode       string
		code       int
		stdout     string
		stderrPart string
	}{
		{name: "success", mode: "success", code: 0, stdout: target + "\n"},
		{name: "human no match", mode: "no-match", code: 4, stdout: "", stderrPart: `workspace selector "absent" was not found`},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceSelectionEOFSubprocessHelper$")
			command.Env = append(os.Environ(), "WTREE_EOF_PROJECT="+project.Path, "WTREE_EOF_DATA="+data, "WTREE_EOF_MODE="+test.mode)
			command.Stdin = strings.NewReader("")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if test.code == 0 && err != nil {
				t.Fatalf("EOF process = %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if test.code != 0 {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != test.code {
					t.Fatalf("EOF process error = %v, want exit %d", err, test.code)
				}
			}
			if stdout.String() != test.stdout || (test.stderrPart == "" && stderr.Len() != 0) || (test.stderrPart != "" && (!strings.Contains(stderr.String(), test.stderrPart) || strings.Count(stderr.String(), "wtree: ") != 1 || strings.Count(stderr.String(), "\n") != 1 || strings.Contains(strings.ToLower(stderr.String()), "prompt"))) {
				t.Fatalf("EOF process stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkspaceSelectionEOFSubprocessHelper(t *testing.T) {
	project, data := os.Getenv("WTREE_EOF_PROJECT"), os.Getenv("WTREE_EOF_DATA")
	if project == "" || data == "" {
		return
	}
	arguments := []string{"path", "alpha-eof", "--project", project, "--data-dir", data}
	if os.Getenv("WTREE_EOF_MODE") == "no-match" {
		arguments[1] = "absent"
	}
	os.Exit(run(arguments, os.Stdout, os.Stderr))
}

func TestWorkspaceSelectionExecutableProgressionAcrossPathStatusRemoveAndCheckout(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "acceptance")

	runCommand := func(wantCode int, arguments ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != wantCode {
			t.Fatalf("%v exit=%d, want %d; stdout=%q stderr=%q", arguments, got, wantCode, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	runCommand(0, "init", project.Path, "--data-dir", data)
	runCommand(0, "create", "--project", project.Path, "feature/acceptance", "--data-dir", data, "--path", target)

	for _, arguments := range [][]string{
		{"path", "accept", "--project", project.Path, "--data-dir", data},
		{"path", "feature/acceptance", "--exact", "--project", project.Path, "--data-dir", data},
	} {
		stdout, stderr := runCommand(0, arguments...)
		if stdout != target+"\n" || stderr != "" {
			t.Fatalf("path %v stdout=%q stderr=%q", arguments, stdout, stderr)
		}
	}
	for _, arguments := range [][]string{
		{"status", "accept", "--project", project.Path, "--data-dir", data, "--json"},
		{"status", "feature/acceptance", "--exact", "--project", project.Path, "--data-dir", data, "--json"},
	} {
		stdout, stderr := runCommand(0, arguments...)
		if stderr != "" || !strings.Contains(stdout, `"workspace":"feature/acceptance"`) {
			t.Fatalf("status %v stdout=%q stderr=%q", arguments, stdout, stderr)
		}
	}

	if stdout, stderr := runCommand(0, "remove", "feature/acceptance", "--project", project.Path, "--data-dir", data); stderr != "" || !strings.Contains(stdout, "Workspace: feature/acceptance\n") {
		t.Fatalf("exact remove streams stdout=%q stderr=%q", stdout, stderr)
	}
	for _, command := range []string{"path", "status"} {
		stdout, stderr := runCommand(4, command, "accept", "--project", project.Path, "--data-dir", data)
		if stdout != "" || !strings.Contains(stderr, `workspace selector "accept" was not found`) {
			t.Fatalf("removed %s stdout=%q stderr=%q", command, stdout, stderr)
		}
	}

	stdout, stderr := runCommand(0, "checkout", "accept", "--project", project.Path, "--data-dir", data)
	if stderr != "" || !strings.Contains(stdout, "Checked out workspace: feature/acceptance\n") || !strings.Contains(stdout, "Target: "+target+"\n") {
		t.Fatalf("shorthand checkout stdout=%q stderr=%q", stdout, stderr)
	}
	stdout, stderr = runCommand(0, "path", "accept", "--project", project.Path, "--data-dir", data)
	if stdout != target+"\n" || stderr != "" {
		t.Fatalf("restored path stdout=%q stderr=%q", stdout, stderr)
	}
	stdout, stderr = runCommand(0, "status", "accept", "--project", project.Path, "--data-dir", data, "--json")
	if stderr != "" || !strings.Contains(stdout, `"workspace":"feature/acceptance"`) {
		t.Fatalf("restored status stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunCloneLocalAndHTTPThroughProcessBoundary(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("README.md", "root\n", "root")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", repository.Path, "--data-dir", t.TempDir()}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	repository.Run(t, "add", ".gitignore", "project.wtree.yml")
	repository.Run(t, "commit", "-m", "publish manifest")
	repository.Run(t, "push", "origin", "main")
	manifest := filepath.Join(repository.Path, "project.wtree.yml")
	manifestBytes, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	working, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	for _, source := range []string{manifest, "http"} {
		var server *httptest.Server
		if source == "http" {
			server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(manifestBytes) }))
			source = server.URL + "/project.wtree.yml"
		}
		name := "local-clone"
		if server != nil {
			name = "http-clone"
		}
		stdout.Reset()
		stderr.Reset()
		destination := filepath.Join(working, name)
		code := run([]string{"clone", source, destination, "--data-dir", t.TempDir(), "--json"}, &stdout, &stderr)
		if server != nil {
			server.Close()
		}
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"operation":"clone"`) {
			t.Fatalf("%s process clone exit=%d stdout=%q stderr=%q", name, code, stdout.String(), stderr.String())
		}
		if _, err := os.Stat(filepath.Join(destination, ".wtree.yml")); err != nil {
			t.Fatalf("%s process clone destination: %v", name, err)
		}
	}
}
