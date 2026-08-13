package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcel/wtree/internal/cli"
	"github.com/marcel/wtree/internal/testutil"
)

func TestExecuteDoctorJSONIsReadOnlyAndSupportsFixDryRun(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	statePath := filepath.Join(data, "state")
	before, err := os.ReadFile(filepath.Join(statePath, mustSingleDirectory(t, statePath), "default.json"))
	if err != nil {
		t.Fatal(err)
	}
	result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "doctor", "default", "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("doctor = %#v", result)
	}
	var report struct {
		Workspace string `json:"workspace"`
		Findings  []struct {
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil || report.Workspace != "default" {
		t.Fatalf("doctor JSON = %q, report=%#v err=%v", result.Stdout, report, err)
	}
	after, err := os.ReadFile(filepath.Join(statePath, mustSingleDirectory(t, statePath), "default.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("doctor mutated state: %v", err)
	}
	dryRun := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "doctor", "default", "--data-dir", data, "--fix", "--dry-run", "--json")
	if dryRun.Err != nil || dryRun.Stderr != "" {
		t.Fatalf("doctor --fix --dry-run = %#v", dryRun)
	}
}

func TestExecuteDoctorReadOnlyResolutionPreservesMovedRegistry(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		arguments func(project, data string) []string
		local     bool
	}{
		{name: "explicit", arguments: func(project, data string) []string {
			return []string{"--project", project, "doctor", "default", "--data-dir", data, "--json"}
		}},
		{name: "local", local: true, arguments: func(_ string, data string) []string {
			return []string{"doctor", "default", "--data-dir", data, "--fix", "--dry-run", "--json"}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project := testutil.NewGitRepository(t)
			project.CommitFile("root.txt", "root\n", "root")
			data := t.TempDir()
			if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("init = %#v", result)
			}
			registryPath := filepath.Join(data, "registry.json")
			before, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(registryPath, []byte(string(before)), 0o600); err != nil {
				t.Fatal(err)
			}
			// Make the existing registry stale without changing its representation again.
			registryBytes := string(before)
			registryBytes = strings.Replace(registryBytes, filepath.Join(project.Path, ".wtree.yml"), filepath.Join(t.TempDir(), "moved", ".wtree.yml"), 1)
			if err := os.WriteFile(registryPath, []byte(registryBytes), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err = os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.local {
				previous, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chdir(project.Path); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chdir(previous) })
			}
			result := testutil.RunCommand(t, cli.Execute, scenario.arguments(project.Path, data)...)
			if result.Err != nil || result.Stderr != "" {
				t.Fatalf("doctor = %#v", result)
			}
			after, err := os.ReadFile(registryPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("doctor rewrote registry: %v\nbefore=%s\nafter=%s", err, before, after)
			}
		})
	}
}

func mustSingleDirectory(t *testing.T, path string) string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("state entries = %#v, %v", entries, err)
	}
	return entries[0].Name()
}
