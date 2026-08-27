package cli_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteDoctorJSONIsReadOnlyAndSupportsFixDryRun(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
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
	beforeObservation := exactDoctorObservation(t, project.Path, data)
	result := testutil.RunCommand(t, cli.Execute, "doctor", "--project", project.Path, "default", "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("doctor = %#v", result)
	}
	var report struct {
		Workspace      string `json:"workspace"`
		LogicalRoot    string `json:"logicalRoot"`
		BaseRepository string `json:"baseRepository"`
		Repositories   []struct {
			ID, Mount, ResolvedPath, Status                                        string
			IdentityMismatch, Missing, MountMismatch, BranchMismatch, HeadMismatch bool
		} `json:"repositories"`
		Findings []struct {
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	canonicalProject, err := filepath.EvalSymlinks(project.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil || report.Workspace != "default" || report.LogicalRoot != canonicalProject || report.BaseRepository != "root" || len(report.Repositories) != 1 || report.Repositories[0].ID != "root" || report.Repositories[0].Mount != "." || report.Repositories[0].ResolvedPath != canonicalProject || report.Repositories[0].Status != "known" {
		t.Fatalf("doctor JSON = %q, report=%#v err=%v", result.Stdout, report, err)
	}
	after, err := os.ReadFile(filepath.Join(statePath, mustSingleDirectory(t, statePath), "default.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("doctor mutated state: %v", err)
	}
	if afterObservation := exactDoctorObservation(t, project.Path, data); !reflect.DeepEqual(beforeObservation, afterObservation) {
		t.Fatalf("doctor changed filesystem, index, status, HEAD, or refs:\nbefore=%#v\nafter=%#v", beforeObservation, afterObservation)
	}
	dryRun := testutil.RunCommand(t, cli.Execute, "doctor", "--project", project.Path, "default", "--data-dir", data, "--fix", "--dry-run", "--json")
	if dryRun.Err != nil || dryRun.Stderr != "" {
		t.Fatalf("doctor --fix --dry-run = %#v", dryRun)
	}
	if afterDryRun := exactDoctorObservation(t, project.Path, data); !reflect.DeepEqual(beforeObservation, afterDryRun) {
		t.Fatalf("doctor --fix --dry-run changed filesystem, index, status, HEAD, or refs:\nbefore=%#v\nafter=%#v", beforeObservation, afterDryRun)
	}
}

func exactDoctorObservation(t *testing.T, repository, dataDir string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for name, arguments := range map[string][]string{
		"head":   {"rev-parse", "HEAD"},
		"refs":   {"show-ref", "--head"},
		"status": {"status", "--porcelain=v1", "--untracked-files=all"},
	} {
		command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
		command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("observe Git %s: %v: %s", name, err, output)
		}
		result["git:"+name] = string(output)
	}
	for label, root := range map[string]string{"repository": repository, "data": dataDir} {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			value := fmt.Sprintf("%s:%o", info.Mode().Type(), info.Mode().Perm())
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				value += ":" + target
			case info.Mode().IsRegular():
				contents, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				value += fmt.Sprintf(":%x", sha256.Sum256(contents))
			}
			result[label+":"+filepath.ToSlash(relative)] = value
			return nil
		}); err != nil {
			t.Fatalf("snapshot %s tree: %v", label, err)
		}
	}
	return result
}

func TestExecuteDoctorReadOnlyResolutionPreservesMovedRegistry(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		arguments func(project, data string) []string
		local     bool
	}{
		{name: "explicit", arguments: func(project, data string) []string {
			return []string{"doctor", "--project", project, "default", "--data-dir", data, "--json"}
		}},
		{name: "local", local: true, arguments: func(_ string, data string) []string {
			return []string{"doctor", "default", "--data-dir", data, "--fix", "--dry-run", "--json"}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project := testutil.NewPushedGitRepository(t)
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
