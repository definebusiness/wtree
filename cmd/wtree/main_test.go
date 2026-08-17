package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
