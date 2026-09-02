package cli_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
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

func TestExecuteDoctorRendersIncompleteHookRunWithoutFixingIt(t *testing.T) {
	projectPath := testutil.NewPushedGitRepository(t)
	projectPath.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", projectPath.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	executable, command := filepath.Join(projectPath.Path, "fail-hook"), []string{}
	if runtime.GOOS == "windows" {
		executable, command = os.Args[0], []string{"-test.run=^TestLifecycleHookNativeHelper$"}
		t.Setenv("WTREE_TEST_NATIVE_HOOK_FAIL", "1")
	} else if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	} else {
		if err := os.Chmod(executable, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(projectPath.Path, ".wtree.yml")
	local, err := config.ReadProjectFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	local.Hooks = config.HookEvents{config.HookEventPostCreate: {{ID: "setup", Command: append([]string{executable}, command...)}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	created := testutil.RunCommand(t, cli.Execute, "create", "--project", projectPath.Path, "feature/doctor-hook", "--data-dir", data, "--path", target)
	if _, incomplete := service.SetupIncompleteFrom(created.Err); !incomplete {
		t.Fatalf("create incomplete hook run = %#v", created)
	}
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath.Path, ProjectPath: projectPath.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(resolution.Project, data, "feature/doctor-hook")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "projects", resolution.Project.ID, "hooks", workspace.ID, "post-create.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jsonResult := testutil.RunCommand(t, cli.Execute, "doctor", "--project", projectPath.Path, workspace.Name, "--data-dir", data, "--json")
	if jsonResult.Err != nil || jsonResult.Stderr != "" {
		t.Fatalf("doctor JSON = %#v", jsonResult)
	}
	var report struct {
		Findings []struct {
			Code, Severity, Message string
			Fixable                 bool
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(jsonResult.Stdout), &report); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "hook-setup-incomplete" {
			found = finding.Severity == "warning" && !finding.Fixable && finding.Message == "hook setup is incomplete and can be resumed with wtree hooks retry "+workspace.Name
		}
	}
	if !found {
		t.Fatalf("hook doctor JSON = %s", jsonResult.Stdout)
	}
	human := testutil.RunCommand(t, cli.Execute, "doctor", "--project", projectPath.Path, workspace.Name, "--data-dir", data)
	if human.Err != nil || !strings.Contains(human.Stdout, "warning: hook-setup-incomplete — hook setup is incomplete and can be resumed with wtree hooks retry "+workspace.Name+"\n") {
		t.Fatalf("hook doctor human = %#v", human)
	}
	if fixed := testutil.RunCommand(t, cli.Execute, "doctor", "--project", projectPath.Path, workspace.Name, "--data-dir", data, "--fix"); fixed.Err != nil {
		t.Fatalf("doctor fix = %#v", fixed)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("doctor fix changed hook run record: %v\nbefore=%s\nafter=%s", err, before, after)
	}
}

func TestExecuteDoctorRendersInvalidHookRunWithoutFixingIt(t *testing.T) {
	projectPath := testutil.NewPushedGitRepository(t)
	projectPath.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", projectPath.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath.Path, ProjectPath: projectPath.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(resolution.Project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(data, "projects", resolution.Project.ID, "hooks", workspace.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "unexpected.txt")
	if err := os.WriteFile(path, []byte("unsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jsonResult := testutil.RunCommand(t, cli.Execute, "doctor", "--project", projectPath.Path, "default", "--data-dir", data, "--json")
	if jsonResult.Err != nil || jsonResult.Stderr != "" || !strings.Contains(jsonResult.Stdout, `"code":"invalid-hook-run-record"`) || !strings.Contains(jsonResult.Stdout, `"severity":"error"`) {
		t.Fatalf("doctor invalid JSON = %#v", jsonResult)
	}
	human := testutil.RunCommand(t, cli.Execute, "doctor", "--project", projectPath.Path, "default", "--data-dir", data)
	if human.Err != nil || !strings.Contains(human.Stdout, "error: invalid-hook-run-record — hook run record is invalid and requires manual inspection\n") {
		t.Fatalf("doctor invalid human = %#v", human)
	}
	if fixed := testutil.RunCommand(t, cli.Execute, "doctor", "--project", projectPath.Path, "default", "--data-dir", data, "--fix"); fixed.Err != nil {
		t.Fatalf("doctor fix = %#v", fixed)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("doctor fix changed invalid hook entry: %v\nbefore=%s\nafter=%s", err, before, after)
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
