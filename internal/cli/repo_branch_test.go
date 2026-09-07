package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestRepoBranchCLIJSONAndNoopContract(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("README.md", "root\n", "root")
	repository.Run(t, "branch", "next")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	localPath := filepath.Join(repository.Path, ".wtree.yml")
	local, err := config.ReadProjectFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion4
	entry := local.Repositories["root"]
	entry.Companion = true
	local.Repositories["root"] = entry
	if err := config.WriteProjectFile(localPath, local); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repository.Path, local.Manifest.Path)
	manifest, err := config.LoadPortableManifest(mustReadRepoBranchFile(t, manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = config.PortableManifestVersion4
	portable := manifest.Repositories["root"]
	portable.Companion = true
	manifest.Repositories["root"] = portable
	encoded, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	dryHuman := testutil.RunCommand(t, cli.Execute, "repo", "branch", "root", "next", "--project", repository.Path, "--data-dir", data, "--dry-run")
	if dryHuman.Err != nil || dryHuman.Stderr != "" || dryHuman.Stdout != "Repository branch: planned\nProject: "+local.Project.ID+"\nRepository: root\nPrevious baseline: main\nBaseline: next\nPortable changed: true\nLocal changed: true\n" {
		t.Fatalf("dry human = %#v", dryHuman)
	}
	dryJSON := testutil.RunCommand(t, cli.Execute, "repo", "branch", "root", "next", "--project", repository.Path, "--data-dir", data, "--dry-run", "--json")
	var dryWire map[string]any
	if dryJSON.Err != nil || json.Unmarshal([]byte(dryJSON.Stdout), &dryWire) != nil || !reflect.DeepEqual(sortedRepoBranchKeys(dryWire), []string{"baseline", "dryRun", "localChanged", "operation", "portableChanged", "previousBaseline", "projectId", "repositoryId", "status", "version"}) || dryWire["status"] != "planned" {
		t.Fatalf("dry JSON = %#v wire=%#v", dryJSON, dryWire)
	}
	completedHuman := testutil.RunCommand(t, cli.Execute, "repo", "branch", "root", "next", "--project", repository.Path, "--data-dir", data)
	if completedHuman.Err != nil || completedHuman.Stdout != "Repository branch: completed\nProject: "+local.Project.ID+"\nRepository: root\nPrevious baseline: main\nBaseline: next\nPortable changed: true\nLocal changed: true\n" || completedHuman.Stderr != "" {
		t.Fatalf("completed human = %#v", completedHuman)
	}
	arguments := []string{"repo", "branch", "root", "main", "--project", repository.Path, "--data-dir", data, "--json"}
	first := testutil.RunCommand(t, cli.Execute, arguments...)
	if first.Err != nil {
		t.Fatalf("branch JSON = %#v", first)
	}
	var value struct {
		Version                                                                int `json:"version"`
		Operation, Status, ProjectID, RepositoryID, PreviousBaseline, Baseline string
		DryRun, PortableChanged, LocalChanged                                  bool
	}
	var firstWire map[string]any
	if err := json.Unmarshal([]byte(first.Stdout), &value); err != nil || json.Unmarshal([]byte(first.Stdout), &firstWire) != nil || !reflect.DeepEqual(sortedRepoBranchKeys(firstWire), []string{"baseline", "dryRun", "localChanged", "operation", "portableChanged", "previousBaseline", "projectId", "repositoryId", "status", "version"}) || value.Version != 1 || value.Operation != "repo-branch" || value.Status != "completed" || value.DryRun || value.RepositoryID != "root" || value.PreviousBaseline != "next" || value.Baseline != "main" || !value.PortableChanged || !value.LocalChanged {
		t.Fatalf("JSON = %q value=%#v err=%v", first.Stdout, value, err)
	}
	second := testutil.RunCommand(t, cli.Execute, arguments...)
	if second.Err != nil || !jsonContainsRepoBranchStatus(t, second.Stdout, "unchanged") {
		t.Fatalf("no-op JSON = %#v", second)
	}
	humanNoop := testutil.RunCommand(t, cli.Execute, "repo", "branch", "root", "main", "--project", repository.Path, "--data-dir", data)
	if humanNoop.Err != nil || humanNoop.Stderr != "" || humanNoop.Stdout != "Repository branch: unchanged\nProject: "+local.Project.ID+"\nRepository: root\nPrevious baseline: main\nBaseline: main\nPortable changed: false\nLocal changed: false\n" {
		t.Fatalf("human no-op = %#v", humanNoop)
	}
	failed := testutil.RunCommand(t, cli.Execute, "repo", "branch", "root", "missing", "--project", repository.Path, "--data-dir", data, "--json")
	var failedWire map[string]any
	if failed.Err == nil || failed.Stderr != "" || json.Unmarshal([]byte(failed.Stdout), &failedWire) != nil || !reflect.DeepEqual(sortedRepoBranchKeys(failedWire), []string{"baseline", "dryRun", "failure", "localChanged", "operation", "portableChanged", "previousBaseline", "projectId", "repositoryId", "status", "version"}) || !jsonContainsRepoBranchStatus(t, failed.Stdout, "failed") || !jsonContainsRepoBranchFailure(t, failed.Stdout) {
		t.Fatalf("failed JSON = %#v", failed)
	}
	humanFailed := testutil.RunCommand(t, cli.Execute, "repo", "branch", "root", "missing", "--project", repository.Path, "--data-dir", data)
	if humanFailed.Err == nil || humanFailed.Stdout != "" || cli.ExitCode(humanFailed.Err) != 5 {
		t.Fatalf("failed human = %#v", humanFailed)
	}
}

func TestRepoBranchCLIHelpAndArgumentFailuresKeepOneJSONDocument(t *testing.T) {
	help := testutil.RunCommand(t, cli.Execute, "repo", "branch", "--help")
	if help.Err != nil || !strings.Contains(help.Stdout, "Change one companion repository's future baseline") || !strings.Contains(help.Stdout, "wtree repo branch tools main --dry-run") {
		t.Fatalf("help = %#v", help)
	}
	for _, arguments := range [][]string{{"repo", "branch", "tools", "--json"}, {"repo", "branch", "tools", "next", "extra", "--json"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		var envelope struct {
			Success bool `json:"success"`
			Error   *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if result.Err == nil || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Success || envelope.Error == nil || envelope.Error.Code != "invalid_arguments" || strings.Contains(result.Stdout, "\n{") {
			t.Fatalf("argument failure %#v = %#v", arguments, result)
		}
	}
}

func mustReadRepoBranchFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func jsonContainsRepoBranchStatus(t *testing.T, data, want string) bool {
	t.Helper()
	var value struct {
		Status string `json:"status"`
	}
	return json.Unmarshal([]byte(data), &value) == nil && value.Status == want
}
func jsonContainsRepoBranchFailure(t *testing.T, data string) bool {
	t.Helper()
	var value struct {
		Failure *struct{ Code, Message string } `json:"failure"`
	}
	return json.Unmarshal([]byte(data), &value) == nil && value.Failure != nil && value.Failure.Code == "validation" && value.Failure.Message != ""
}

func sortedRepoBranchKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
