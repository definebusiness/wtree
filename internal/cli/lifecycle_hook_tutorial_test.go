package cli_test

import (
	"context"
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

// TestLifecycleHookTutorialAcceptance is deliberately one stateful, local-only
// public-CLI flow.  It keeps authoring, distribution, clone consent, local
// setup recovery, and bypass assertions on the same temporary Git fixture.
func TestLifecycleHookTutorialAcceptance(t *testing.T) {
	author := testutil.NewPushedGitRepository(t)
	author.CommitFile("README.md", "tutorial\n", "seed")
	authorData := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", author.Path, "--data-dir", authorData); result.Err != nil {
		t.Fatalf("author init = %#v", result)
	}
	program, good, failing := "hooks/setup", "#!/bin/sh\nexit 0\n", "#!/bin/sh\nexit 23\n"
	command := []string{program}
	if runtime.GOOS == "windows" {
		program, good, failing = "hooks/setup.exe", "", ""
		command = []string{program, "-test.run=^TestLifecycleHookNativeHelper$"}
	}
	script := filepath.Join(author.Path, filepath.FromSlash(program))
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		copyLifecycleNativeHook(t, script)
	} else if err := os.WriteFile(script, []byte(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(script, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	author.Run(t, "add", ".gitignore", "project.wtree.yml", filepath.ToSlash(program))
	author.Run(t, "commit", "-m", "publish hook executable")
	author.Run(t, "push", "origin", "main")

	localPath := filepath.Join(author.Path, ".wtree.yml")
	local, err := config.ReadProjectFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	local.Hooks = config.HookEvents{config.HookEventPostCreate: {{ID: "setup", Command: command}}}
	if err := config.WriteProjectFile(localPath, local); err != nil {
		t.Fatal(err)
	}
	if result := testutil.RunCommand(t, cli.Execute, "hooks", "share", "post-create", "--project", author.Path, "--data-dir", authorData); result.Err != nil || !strings.Contains(result.Stdout, "added: post-create") {
		t.Fatalf("author share = %#v", result)
	}
	manifestPath := filepath.Join(author.Path, "project.wtree.yml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: {{ID: "clone-setup", Command: command}}}
	manifestBytes, err = config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	author.Run(t, "add", "project.wtree.yml")
	author.Run(t, "commit", "-m", "publish portable clone hook")
	author.Run(t, "push", "origin", "main")
	expectedHead := lifecycleGitOutput(t, author.Path, "rev-parse", "HEAD")
	assertLifecycleCheckout(t, author.Path, "main", expectedHead)

	parent := cloneWorkingDirectory(t)
	dryData, dryDestination := t.TempDir(), filepath.Join(parent, "dry")
	dry := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, dryDestination, "--data-dir", dryData, "--dry-run")
	if dry.Err != nil || !strings.Contains(dry.Stdout, "portable/clone-setup") {
		t.Fatalf("clone observation = %#v", dry)
	}
	if _, err := os.Lstat(dryDestination); !os.IsNotExist(err) {
		t.Fatalf("dry clone mutated destination: %v", err)
	}

	data, destination := t.TempDir(), filepath.Join(parent, "unauthorized")
	unauthorized := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, destination, "--data-dir", data)
	if unauthorized.Err != nil || !strings.Contains(unauthorized.Stdout, "Portable hooks: skipped") {
		t.Fatalf("unauthorized clone = %#v", unauthorized)
	}
	assertLifecycleCheckout(t, destination, "main", expectedHead)
	if result := testutil.RunCommand(t, cli.Execute, "hooks", "install", "--project", destination, "--data-dir", data); result.Err != nil || !strings.Contains(result.Stdout, "added: post-create") {
		t.Fatalf("install shared local consent = %#v", result)
	}
	installedScript := filepath.Join(destination, filepath.FromSlash(program))
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(destination, ".wtree-hook-fail-once"), []byte("fail"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(installedScript, []byte(failing), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(installedScript, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(parent, "failed-create")
	created := testutil.RunCommand(t, cli.Execute, "create", "--project", destination, "feature/tutorial", "--data-dir", data, "--path", target)
	if _, incomplete := service.SetupIncompleteFrom(created.Err); !incomplete {
		t.Fatalf("create failure = %#v", created)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("hook failure removed published workspace: %v", err)
	}
	assertLifecycleCheckout(t, target, "feature/tutorial", expectedHead)
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: destination, ProjectPath: destination, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(resolution.Project, data, "feature/tutorial")
	if err != nil {
		t.Fatal(err)
	}
	assertLifecycleWorkspaceState(t, data, resolution.Project.ID, workspace.ID, "feature/tutorial", target, expectedHead)
	for _, command := range [][]string{{"status", "--project", destination, "feature/tutorial", "--data-dir", data, "--json"}, {"doctor", "--project", destination, "feature/tutorial", "--data-dir", data, "--json"}} {
		result := testutil.RunCommand(t, cli.Execute, command...)
		if result.Err != nil || (!strings.Contains(result.Stdout, "post-create") && !strings.Contains(result.Stdout, "hook-setup-incomplete")) {
			t.Fatalf("diagnostic %v = %#v", command, result)
		}
		assertLifecycleCheckout(t, target, "feature/tutorial", expectedHead)
		assertLifecycleWorkspaceState(t, data, resolution.Project.ID, workspace.ID, "feature/tutorial", target, expectedHead)
	}
	recordPath, err := store.HookRunRecordPath(data, resolution.Project.ID, workspace.ID, config.HookEventPostCreate)
	if err != nil {
		t.Fatal(err)
	}
	if record, err := store.ReadHookRunRecord(recordPath); err != nil || record.NextIndex != 0 || record.Failure == nil {
		t.Fatalf("failed record = %#v, %v", record, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(installedScript, []byte(good), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if result := testutil.RunCommand(t, cli.Execute, "hooks", "retry", "feature/tutorial", "--project", destination, "--data-dir", data); result.Err != nil || !strings.Contains(result.Stdout, "Status: completed") {
		t.Fatalf("retry = %#v", result)
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Fatalf("retry retained completed record: %v", err)
	}
	assertLifecycleCheckout(t, target, "feature/tutorial", expectedHead)
	assertLifecycleWorkspaceState(t, data, resolution.Project.ID, workspace.ID, "feature/tutorial", target, expectedHead)
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", destination, "feature/skip", "--data-dir", data, "--path", filepath.Join(parent, "skip"), "--no-hooks"); result.Err != nil || !strings.Contains(result.Stdout, "Hooks intentionally skipped") {
		t.Fatalf("intentional skip = %#v", result)
	}
	skipPath := filepath.Join(parent, "skip")
	assertLifecycleCheckout(t, skipPath, "feature/skip", expectedHead)
	skipWorkspace, err := service.RequireWorkspace(resolution.Project, data, "feature/skip")
	if err != nil {
		t.Fatal(err)
	}
	assertLifecycleWorkspaceState(t, data, resolution.Project.ID, skipWorkspace.ID, "feature/skip", skipPath, expectedHead)
	authorized := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, filepath.Join(parent, "authorized"), "--data-dir", t.TempDir(), "--run-hooks")
	if authorized.Err != nil || !strings.Contains(authorized.Stdout, "Portable hooks: completed") {
		t.Fatalf("authorized clone = %#v", authorized)
	}
	assertLifecycleCheckout(t, filepath.Join(parent, "authorized"), "main", expectedHead)
}

func copyLifecycleNativeHook(t *testing.T, destination string) {
	t.Helper()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleHookNativeHelper(t *testing.T) {
	if runtime.GOOS != "windows" {
		return
	}
	if os.Getenv("WTREE_TEST_NATIVE_HOOK_FAIL") == "1" {
		os.Exit(23)
	}
	if filepath.Base(os.Args[0]) == "fail.exe" {
		os.Exit(23)
	}
	if filepath.Base(os.Args[0]) != "setup.exe" || os.Getenv("WTREE_HOOK") != "post-create" {
		return
	}
	if err := os.Remove(filepath.Join(os.Getenv("WTREE_SOURCE_LOGICAL_ROOT"), ".wtree-hook-fail-once")); err == nil {
		os.Exit(23)
	}
}

func lifecycleGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}

func assertLifecycleCheckout(t *testing.T, directory, branch, head string) {
	t.Helper()
	if got := lifecycleGitOutput(t, directory, "branch", "--show-current"); got != branch {
		t.Fatalf("%s branch = %q, want %q", directory, got, branch)
	}
	if got := lifecycleGitOutput(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("%s HEAD = %q, want %q", directory, got, head)
	}
}

func assertLifecycleWorkspaceState(t *testing.T, data, projectID, workspaceID, branch, path, head string) {
	t.Helper()
	state, err := store.ReadWorkspace(service.WorkspaceStatePath(data, projectID, workspaceID))
	if err != nil {
		t.Fatal(err)
	}
	checkout, found := state.Repositories["root"]
	if !found || checkout.Branch != branch || checkout.Head != head || checkout.ResolvedPath != path {
		t.Fatalf("workspace state=%#v, want branch=%q head=%q path=%q", state, branch, head, path)
	}
}
