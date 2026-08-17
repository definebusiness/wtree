package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestInitManifestFlagsRenderDryRunWithoutMutation(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", t.TempDir(), "--manifest-source", "https://EXAMPLE.invalid/acme/project.wtree.yml", "--clone-url", "root=file:///tmp/root.git", "--dry-run", "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("init dry-run = %#v", result)
	}
	var value struct {
		DryRun         bool   `json:"dryRun"`
		ManifestSource string `json:"manifestSource"`
		Portable       struct {
			Repositories map[string]struct {
				Clone struct {
					URL string `json:"url"`
				} `json:"clone"`
			} `json:"repositories"`
		} `json:"portableManifest"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if !value.DryRun || value.ManifestSource != "https://example.invalid/acme/project.wtree.yml" || value.Portable.Repositories["root"].Clone.URL != "file:///tmp/root.git" {
		t.Fatalf("dry-run JSON = %s", result.Stdout)
	}
}

func TestInitDryRunJSONIsByteStableAndConvergesToExecution(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	arguments := []string{"init", repository.Path, "--data-dir", data, "--dry-run", "--json"}
	first := testutil.RunCommand(t, cli.Execute, arguments...)
	second := testutil.RunCommand(t, cli.Execute, arguments...)
	if first.Err != nil || second.Err != nil || first.Stderr != "" || second.Stderr != "" {
		t.Fatalf("dry-runs = %#v, %#v", first, second)
	}
	if first.Stdout != second.Stdout {
		t.Fatalf("dry-run JSON differs:\nfirst:  %s\nsecond: %s", first.Stdout, second.Stdout)
	}
	var planned, published struct {
		ProjectID string `json:"projectId"`
	}
	if err := json.Unmarshal([]byte(first.Stdout), &planned); err != nil {
		t.Fatal(err)
	}
	execution := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", data, "--json")
	if execution.Err != nil || execution.Stderr != "" {
		t.Fatalf("execution = %#v", execution)
	}
	if err := json.Unmarshal([]byte(execution.Stdout), &published); err != nil {
		t.Fatal(err)
	}
	if published.ProjectID != planned.ProjectID {
		t.Fatalf("execution project ID = %q, dry-run project ID = %q", published.ProjectID, planned.ProjectID)
	}
}

func TestInitRejectsDuplicateCloneURLOverridesAsArguments(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", t.TempDir(), "--clone-url", "root=file:///tmp/a.git", "--clone-url", "root=file:///tmp/b.git")
	if result.Err == nil || cli.ExitCode(result.Err) != 2 {
		t.Fatalf("duplicate override = %#v", result)
	}
}

func TestInitCloneURLAcceptsCommaAndHumanDryRunShowsCompleteProposal(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", t.TempDir(), "--clone-url", "root=https://example.invalid/repo,a.git", "--dry-run")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("init dry-run = %#v", result)
	}
	for _, expected := range []string{
		"clone url: https://example.invalid/repo,a.git",
		"upstream remote:",
		"upstream branch:",
		"upstream merge:",
		"initial roots:",
		"Proposed local configuration:",
		"Proposed portable manifest:",
		"No changes made.\n",
	} {
		if !strings.Contains(result.Stdout, expected) {
			t.Fatalf("dry-run output missing %q:\n%s", expected, result.Stdout)
		}
	}
}
