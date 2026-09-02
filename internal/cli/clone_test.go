package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestCloneDryRunAndExecutionThroughPublicCLI(t *testing.T) {
	manifest, projectID := publishedCloneFixture(t)
	parent := cloneWorkingDirectory(t)
	destination := filepath.Join(parent, "cloned")
	data := t.TempDir()
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	dryRun := testutil.RunCommand(t, cli.Execute, "clone", manifest, destination, "--data-dir", data, "--worktree-root", worktrees, "--dry-run")
	if dryRun.Err != nil || dryRun.Stderr != "" || !strings.Contains(dryRun.Stdout, "Operation: clone\n") || !strings.Contains(dryRun.Stdout, "observed commit:") || strings.Contains(dryRun.Stdout, "exact commit:") || !strings.Contains(dryRun.Stdout, "No changes made.\n") {
		t.Fatalf("clone dry-run = %#v err=%v", dryRun, dryRun.Err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("dry-run created destination: %v", err)
	}

	result := testutil.RunCommand(t, cli.Execute, "clone", manifest, destination, "--data-dir", data, "--worktree-root", worktrees, "--json")
	var output struct {
		Version         int    `json:"version"`
		Operation       string `json:"operation"`
		Status          string `json:"status"`
		Destination     string `json:"destination"`
		LogicalRoot     string `json:"logicalRoot"`
		BaseRepository  string `json:"baseRepository"`
		RepositoryCount int    `json:"repositoryCount"`
		ManifestSource  string `json:"manifestSource"`
		Repositories    []struct {
			ID           string `json:"id"`
			ActualCommit string `json:"actualCommit"`
			Mount        string `json:"mount"`
			ResolvedPath string `json:"resolvedPath"`
		} `json:"repositories"`
		Project struct{ ID, Name string } `json:"project"`
	}
	if result.Err != nil || result.Stderr != "" || json.Unmarshal([]byte(result.Stdout), &output) != nil {
		t.Fatalf("clone JSON = %#v", result)
	}
	manifestAbs, _ := filepath.Abs(manifest)
	if output.Version != 2 || output.Operation != "clone" || output.Status != "completed" || output.Project.ID != projectID || output.Destination != destination || output.LogicalRoot != destination || output.BaseRepository != "root" || output.RepositoryCount != 2 || output.ManifestSource != manifestAbs || len(output.Repositories) != 2 || output.Repositories[0].ID != "backend" || output.Repositories[1].ID != "root" || output.Repositories[0].ActualCommit == "" || output.Repositories[1].ActualCommit == "" || output.Repositories[0].Mount != "backend" || output.Repositories[0].ResolvedPath != filepath.Join(destination, "backend") || output.Repositories[1].Mount != "." || output.Repositories[1].ResolvedPath != destination {
		t.Fatalf("clone JSON = %#v", output)
	}
	if strings.Contains(result.Stdout, `"exactCommit"`) || strings.Contains(result.Stdout, `"parentCommit"`) || !strings.Contains(result.Stdout, `"observedCommit"`) {
		t.Fatalf("clone JSON contains stale execution claims: %s", result.Stdout)
	}

	human := testutil.RunCommand(t, cli.Execute, "clone", manifest, filepath.Join(parent, "human"), "--data-dir", t.TempDir(), "--worktree-root", worktrees)
	if human.Err != nil || !strings.Contains(human.Stdout, "Actual checkout: backend ") || !strings.Contains(human.Stdout, "Actual checkout: root ") {
		t.Fatalf("human clone actual checkouts = %#v", human)
	}
	for _, path := range []string{filepath.Join(destination, ".git"), filepath.Join(destination, "backend", ".git"), filepath.Join(destination, ".wtree.yml")} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("missing cloned path %q: %v", path, err)
		}
	}

	path := testutil.RunCommand(t, cli.Execute, "path", "--project", destination, "default", "--data-dir", data)
	if path.Err != nil || path.Stdout != destination+"\n" {
		t.Fatalf("path after clone = %#v", path)
	}
	repo := testutil.RunCommand(t, cli.Execute, "repo", "path", "backend", "--project", destination, "--data-dir", data)
	if repo.Err != nil || repo.Stdout != filepath.Join(destination, "backend")+"\n" {
		t.Fatalf("repo path after clone = %#v", repo)
	}
	status := testutil.RunCommand(t, cli.Execute, "status", "default", "--project", destination, "--data-dir", data, "--json")
	if status.Err != nil || !strings.Contains(status.Stdout, `"status":"clean"`) {
		t.Fatalf("status after clone = %#v", status)
	}
}

func TestCloneHTTPDefaultDestinationVerboseAndArgumentRejection(t *testing.T) {
	manifest, _ := publishedCloneFixture(t)
	manifestBytes, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(manifestBytes) }))
	defer server.Close()

	working := cloneWorkingDirectory(t)
	data := t.TempDir()
	result := testutil.RunCommand(t, cli.Execute, "clone", server.URL+"/project.wtree.yml", "--data-dir", data, "--verbose")
	if result.Err != nil || !strings.Contains(result.Stdout, "Cloned project:") || !strings.Contains(result.Stderr, "execute_started staging-create") {
		t.Fatalf("HTTP verbose clone = %#v err=%v", result, result.Err)
	}
	portable, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(working, portable.Project.Name, ".wtree.yml")); err != nil {
		t.Fatalf("default destination missing: %v", err)
	}

	for _, arguments := range [][]string{
		{"clone"},
		{"clone", manifest, "one", "two"},
		{"clone", manifest, "other", "--project", working},
		{"clone", manifest, "other", "--force"},
		{"clone", manifest, "other", "--mount", "backend=api"},
		{"clone", manifest, "other", "--from", "main"},
		{"clone", manifest, "other", "--unknown"},
	} {
		attempt := testutil.RunCommand(t, cli.Execute, arguments...)
		if attempt.Err == nil || cli.ExitCode(attempt.Err) != 2 {
			t.Fatalf("arguments %v = %#v", arguments, attempt)
		}
	}
}

func TestCloneV3PortableHooksDryRunAndUnauthorizedSkipPublicContracts(t *testing.T) {
	manifestPath, _ := publishedCloneFixture(t)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	program, script := "hooks/setup", "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		program, script = "hooks/setup.exe", ""
	}
	hookPath := filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(program))
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		copyLifecycleNativeHook(t, hookPath)
	} else if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(hookPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, arguments := range [][]string{{"add", filepath.ToSlash(program)}, {"commit", "-m", "add portable hook executable"}} {
		command := exec.Command("git", append([]string{"-C", filepath.Dir(manifestPath)}, arguments...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", arguments, commandErr, output)
		}
	}
	manifest.Version = config.PortableManifestVersion3
	command := []string{program, "--literal"}
	if runtime.GOOS == "windows" {
		command = []string{program, "-test.run=^TestLifecycleHookNativeHelper$"}
	}
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: {{ID: "deferred", Command: command}}}
	manifest.SharedHooks = config.HookEvents{config.HookEventPostCreate: {{ID: "inert", Command: []string{"hooks/shared"}}}}
	manifestBytes, err = config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "project.wtree.yml"}, {"commit", "-m", "add v3 portable hooks"}, {"push", "origin", "main"}} {
		command := exec.Command("git", append([]string{"-C", filepath.Dir(manifestPath)}, arguments...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", arguments, commandErr, output)
		}
	}
	parent := cloneWorkingDirectory(t)
	dryDestination, dryData := filepath.Join(parent, "dry"), t.TempDir()
	dry := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, dryDestination, "--data-dir", dryData, "--dry-run")
	if dry.Err != nil || !strings.Contains(dry.Stdout, "Hooks:\n  portable/deferred (post-clone)") || !strings.Contains(dry.Stdout, "policy: inert") || strings.Contains(dry.Stdout, "Resolved executable") {
		t.Fatalf("hook dry-run human=%#v", dry)
	}
	if _, statErr := os.Lstat(dryDestination); !os.IsNotExist(statErr) {
		t.Fatalf("hook dry-run created destination: %v", statErr)
	}
	jsonDry := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, filepath.Join(parent, "dry-json"), "--data-dir", t.TempDir(), "--dry-run", "--json")
	if jsonDry.Err != nil || !strings.Contains(jsonDry.Stdout, `"availability":"deferred"`) || !strings.Contains(jsonDry.Stdout, `"executionPolicy":"inert"`) || strings.Contains(jsonDry.Stdout, `"resolvedExecutable"`) {
		t.Fatalf("hook dry-run JSON=%#v", jsonDry)
	}
	data, destination := t.TempDir(), filepath.Join(parent, "unauthorized")
	skip := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, destination, "--data-dir", data)
	if skip.Err != nil || !strings.Contains(skip.Stdout, "Portable hooks: skipped (use --run-hooks to authorize)") {
		t.Fatalf("hook skip human=%#v", skip)
	}
	if _, statErr := os.Stat(filepath.Join(data, "projects")); statErr == nil {
		if entries, readErr := os.ReadDir(filepath.Join(data, "projects")); readErr == nil {
			for _, entry := range entries {
				if _, hookErr := os.Stat(filepath.Join(data, "projects", entry.Name(), "hooks")); !os.IsNotExist(hookErr) {
					t.Fatalf("unauthorized clone created hook record: %v", hookErr)
				}
			}
		}
	}
	jsonSkip := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, filepath.Join(parent, "unauthorized-json"), "--data-dir", t.TempDir(), "--json")
	if jsonSkip.Err != nil || !strings.Contains(jsonSkip.Stdout, `"hooksCompleted":false`) || !strings.Contains(jsonSkip.Stdout, `"hooksSkipped":true`) {
		t.Fatalf("hook skip JSON=%#v", jsonSkip)
	}
	success := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, filepath.Join(parent, "authorized"), "--data-dir", t.TempDir(), "--run-hooks")
	if success.Err != nil || !strings.Contains(success.Stdout, "Portable hooks: completed") {
		t.Fatalf("hook authorized human=%#v", success)
	}
	jsonSuccess := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, filepath.Join(parent, "authorized-json"), "--data-dir", t.TempDir(), "--run-hooks", "--json")
	if jsonSuccess.Err != nil || !strings.Contains(jsonSuccess.Stdout, `"hooksCompleted":true`) || !strings.Contains(jsonSuccess.Stdout, `"hooksSkipped":false`) || !strings.Contains(jsonSuccess.Stdout, `"completedHookIds":["deferred"]`) {
		t.Fatalf("hook authorized JSON=%#v", jsonSuccess)
	}

	failingProgram, failingScript := "hooks/fail", "#!/bin/sh\nexit 23\n"
	if runtime.GOOS == "windows" {
		failingProgram, failingScript = "hooks/fail.exe", ""
	}
	failingPath := filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(failingProgram))
	if runtime.GOOS == "windows" {
		copyLifecycleNativeHook(t, failingPath)
	} else if err := os.WriteFile(failingPath, []byte(failingScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(failingPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	failingCommand := []string{failingProgram}
	if runtime.GOOS == "windows" {
		failingCommand = []string{failingProgram, "-test.run=^TestLifecycleHookNativeHelper$"}
	}
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: {{ID: "failure", Command: failingCommand}}}
	manifestBytes, err = config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "project.wtree.yml", filepath.ToSlash(failingProgram)}, {"commit", "-m", "add failing portable hook"}, {"push", "origin", "main"}} {
		command := exec.Command("git", append([]string{"-C", filepath.Dir(manifestPath)}, arguments...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", arguments, commandErr, output)
		}
	}
	failingData := t.TempDir()
	failure := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, filepath.Join(parent, "failure"), "--data-dir", failingData, "--run-hooks")
	if _, incomplete := service.SetupIncompleteFrom(failure.Err); !incomplete || !strings.Contains(failure.Stdout, "Cloned project:") {
		t.Fatalf("hook failure human=%#v", failure)
	}
	jsonFailure := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, filepath.Join(parent, "failure-json"), "--data-dir", t.TempDir(), "--run-hooks", "--json")
	if _, incomplete := service.SetupIncompleteFrom(jsonFailure.Err); !incomplete || !strings.Contains(jsonFailure.Stdout, `"code":"setup_incomplete"`) || !strings.Contains(jsonFailure.Stdout, `"operation":"clone"`) {
		t.Fatalf("hook failure JSON=%#v", jsonFailure)
	}
}

func TestCloneHTTPFromNestedContextHonorsCustomRootsAndCleansUpManifestMismatch(t *testing.T) {
	manifest, projectID := publishedCloneFixture(t)
	manifestBytes, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(manifestBytes) }))
	defer server.Close()

	working := cloneWorkingDirectory(t)
	nested := filepath.Join(working, "caller", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	portable, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	data, worktrees := t.TempDir(), filepath.Join(t.TempDir(), "worktrees")
	expectedDestination := filepath.Join(nested, portable.Project.Name)

	dryRun := testutil.RunCommand(t, cli.Execute, "clone", server.URL+"/project.wtree.yml", "--data-dir", data, "--worktree-root", worktrees, "--dry-run", "--json")
	var dryOutput struct {
		Version        int    `json:"version"`
		Status         string `json:"status"`
		DryRun         bool   `json:"dryRun"`
		LogicalRoot    string `json:"logicalRoot"`
		BaseRepository string `json:"baseRepository"`
		Plan           struct {
			Destination struct {
				Path string `json:"path"`
			} `json:"destination"`
		} `json:"plan"`
	}
	if dryRun.Err != nil || dryRun.Stderr != "" || json.Unmarshal([]byte(dryRun.Stdout), &dryOutput) != nil || dryOutput.Version != service.CloneResultVersion || dryOutput.Status != "planned" || !dryOutput.DryRun || dryOutput.LogicalRoot != expectedDestination || dryOutput.BaseRepository != "root" || dryOutput.Plan.Destination.Path != expectedDestination {
		t.Fatalf("nested HTTP dry-run JSON = %#v output=%#v", dryRun, dryOutput)
	}
	if _, statErr := os.Lstat(expectedDestination); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run created default destination: %v", statErr)
	}

	clone := testutil.RunCommand(t, cli.Execute, "clone", server.URL+"/project.wtree.yml", "--data-dir", data, "--worktree-root", worktrees, "--verbose")
	if clone.Err != nil || !strings.Contains(clone.Stdout, "Destination: "+expectedDestination+"\n") || !strings.Contains(clone.Stderr, "execute_started staging-create\n") {
		t.Fatalf("nested HTTP clone = %#v", clone)
	}
	local, err := config.ReadProjectFile(filepath.Join(expectedDestination, ".wtree.yml"))
	if err != nil || local.Project.ID != projectID || local.Worktrees.Root != worktrees {
		t.Fatalf("nested clone local config = %#v, %v", local, err)
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil || registry.Projects[projectID].ConfigPath != filepath.Join(expectedDestination, ".wtree.yml") {
		t.Fatalf("nested clone registry = %#v, %v", registry, err)
	}

	// This is valid YAML but not byte-identical to the root's tracked manifest.
	// The public command must report the verification error only after removing
	// its private staging tree, never publishing the requested destination.
	mismatch := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(append(append([]byte(nil), manifestBytes...), []byte("# distinct served bytes\n")...))
	}))
	defer mismatch.Close()
	failedDestination := filepath.Join(nested, "mismatch")
	failed := testutil.RunCommand(t, cli.Execute, "clone", mismatch.URL+"/project.wtree.yml", failedDestination, "--data-dir", t.TempDir())
	if failed.Err == nil || cli.ExitCode(failed.Err) != 5 || !strings.Contains(failed.Stderr, "Rollback complete.\n") || strings.Contains(failed.Stdout, "secret") {
		t.Fatalf("manifest mismatch rollback = %#v", failed)
	}
	if _, statErr := os.Lstat(failedDestination); !os.IsNotExist(statErr) {
		t.Fatalf("failed clone published destination: %v", statErr)
	}
	entries, err := os.ReadDir(nested)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mismatch.wtree-clone-") {
			t.Fatalf("failed clone retained staging path %q", entry.Name())
		}
	}
}

func TestCloneValidationJSONHasOneEnvelopeAndNoMutation(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "clone")
	result := testutil.RunCommand(t, cli.Execute, "clone", "ftp://user:secret@example.invalid/manifest", destination, "--data-dir", t.TempDir(), "--json")
	if result.Err == nil || cli.ExitCode(result.Err) != 5 || result.Stderr != "" || strings.Count(result.Stdout, "\n") != 1 || strings.Contains(result.Stdout, "secret") || !strings.Contains(result.Stdout, `"code":"validation"`) {
		t.Fatalf("validation JSON = %#v", result)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("validation failure created destination: %v", err)
	}
}

func TestCloneRejectsLogicalRootManifestFormatsWithoutPublishingState(t *testing.T) {
	base := t.TempDir()
	valid := "version: 2\nproject:\n  id: project-1\n  name: safe\n  base_repository: root\nrepositories:\n  root:\n    clone:\n      remote: origin\n      url: https://example.invalid/root.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - 0123456789abcdef0123456789abcdef01234567\n    parent: \"\"\n    mount: .\n    default_branch: main\n  api:\n    clone:\n      remote: origin\n      url: https://example.invalid/api.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - 89abcdef0123456789abcdef0123456789abcdef\n    parent: root\n    mount: api\n    default_branch: main\n"
	fixtures := map[string]string{
		"version one":   strings.Replace(valid, "version: 2", "version: 1", 1),
		"missing base":  strings.Replace(valid, "base_repository: root", "base_repository: \"\"", 1),
		"unknown base":  strings.Replace(valid, "base_repository: root", "base_repository: unknown", 1),
		"non-root base": strings.Replace(valid, "base_repository: root", "base_repository: api", 1),
	}
	for name, manifest := range fixtures {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(base, strings.ReplaceAll(name, " ", "-")+".yml")
			if err := os.WriteFile(source, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(base, "clone-"+strings.ReplaceAll(name, " ", "-"))
			dataDir := filepath.Join(base, "data-"+strings.ReplaceAll(name, " ", "-"))
			result := testutil.RunCommand(t, cli.Execute, "clone", source, destination, "--data-dir", dataDir)
			if result.Err == nil || !strings.Contains(result.Err.Error(), "logical-root manifest format") {
				t.Fatalf("clone logical-root error = %#v", result)
			}
			for _, path := range []string{destination, dataDir} {
				if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
					t.Fatalf("invalid manifest published %q: %v", path, statErr)
				}
			}
		})
	}
}

func TestCloneServedThreeLevelProjectKeepsNestedRepositoriesIgnored(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("shared.txt", "shared\n", "shared")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	sharedPath := filepath.Join(backend.Path, "shared")
	if err := os.Rename(shared.Path, sharedPath); err != nil {
		t.Fatal(err)
	}
	shared.Path = sharedPath
	authorData := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", authorData); result.Err != nil {
		t.Fatalf("three-level init = %#v", result)
	}
	backend.Run(t, "add", ".gitignore")
	backend.Run(t, "commit", "-m", "ignore shared mount")
	backend.Run(t, "push", "origin", "main")
	root.Run(t, "add", ".gitignore", "project.wtree.yml")
	root.Run(t, "commit", "-m", "publish manifest")
	root.Run(t, "push", "origin", "main")
	manifestBytes, err := os.ReadFile(filepath.Join(root.Path, "project.wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(manifestBytes) }))
	defer server.Close()
	parent := cloneWorkingDirectory(t)
	destination := filepath.Join(parent, "served-clone")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "clone", server.URL+"/project.wtree.yml", destination, "--data-dir", data); result.Err != nil {
		t.Fatalf("three-level clone = %#v err=%v", result, result.Err)
	}
	for _, check := range []struct{ repository, mount string }{{destination, "backend"}, {filepath.Join(destination, "backend"), "shared"}} {
		command := exec.Command("git", "-C", check.repository, "check-ignore", check.mount)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("check-ignore %s in %s: %v\n%s", check.mount, check.repository, err, output)
		}
	}
	for _, repository := range []string{destination, filepath.Join(destination, "backend")} {
		command := exec.Command("git", "-C", repository, "add", ".")
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git add in %s: %v\n%s", repository, err, output)
		}
		command = exec.Command("git", "-C", repository, "diff", "--cached", "--name-only")
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
			t.Fatalf("outer git add staged nested repository in %s: %v %q", repository, err, output)
		}
	}
	workspace := filepath.Join(parent, "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "create", "feature/after-clone", "--project", destination, "--data-dir", data, "--path", workspace); result.Err != nil {
		t.Fatalf("create after clone = %#v err=%v", result, result.Err)
	}
	if result := testutil.RunCommand(t, cli.Execute, "status", "feature/after-clone", "--project", destination, "--data-dir", data, "--json"); result.Err != nil {
		t.Fatalf("status after clone workspace = %#v", result)
	}
}

func publishedCloneFixture(t *testing.T) (string, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	authorData := t.TempDir()
	init := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", authorData)
	if init.Err != nil {
		t.Fatalf("init clone fixture = %#v", init)
	}
	manifestPath := filepath.Join(root.Path, "project.wtree.yml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil || manifest.Version != 2 || manifest.Project.BaseRepository != "root" {
		t.Fatalf("published clone fixture manifest = %#v, %v", manifest, err)
	}
	root.Run(t, "add", ".gitignore", "project.wtree.yml")
	root.Run(t, "commit", "-m", "publish portable manifest")
	root.Run(t, "push", "origin", "main")
	project, err := service.NewResolver().ResolveProject(t.Context(), service.ResolveRequest{Path: root.Path, DataDir: authorData})
	if err != nil {
		t.Fatal(err)
	}
	return manifestPath, project.ID
}

func cloneWorkingDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return path
}
