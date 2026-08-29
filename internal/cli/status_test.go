package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteStatusRendersCleanWorkspaceJSON(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/status", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	checkout := testutil.GitRepository{Path: target}
	checkout.Run(t, "config", "branch.feature/status.remote", ".")
	checkout.Run(t, "config", "branch.feature/status.merge", "refs/heads/main")
	project.CommitFile("behind.txt", "behind\n", "advance main")
	// State directories are keyed by project ID; obtain it from the project
	// configuration rather than reconstructing a checkout path.
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: project.Path, ProjectPath: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(resolution.Project, data, "feature/status")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, resolution.Project.ID, workspace.ID)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	result := testutil.RunCommand(t, cli.Execute, "status", "--project", project.Path, "feature/status", "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("status = %#v", result)
	}
	var value struct {
		Workspace      string `json:"workspace"`
		LogicalRoot    string `json:"logicalRoot"`
		BaseRepository string `json:"baseRepository"`
		Repositories   []struct {
			ID           string `json:"id"`
			ParentID     string `json:"parentId"`
			Branch       string `json:"branch"`
			Path         string `json:"path"`
			ResolvedPath string `json:"resolvedPath"`
			Clean        bool   `json:"clean"`
			Ahead        int    `json:"ahead"`
			Behind       int    `json:"behind"`
			Upstream     bool   `json:"upstream"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if value.Workspace != "feature/status" || value.LogicalRoot != target || value.BaseRepository != "root" || len(value.Repositories) != 1 || value.Repositories[0].ID != "root" || value.Repositories[0].Branch != "feature/status" || value.Repositories[0].Path != target || value.Repositories[0].ResolvedPath != target || !value.Repositories[0].Clean || value.Repositories[0].Ahead != 0 || value.Repositories[0].Behind != 1 || !value.Repositories[0].Upstream {
		t.Fatalf("status JSON = %s", result.Stdout)
	}
	human := testutil.RunCommand(t, cli.Execute, "status", "--project", project.Path, "feature/status", "--data-dir", data)
	wantHuman := "Workspace: feature/status\n\nREPOSITORY  BRANCH          MOUNT  STATUS  UPSTREAM\nroot        feature/status  .      clean   behind 1\n"
	if human.Err != nil || human.Stderr != "" || human.Stdout != wantHuman {
		t.Fatalf("status human = %#v, want %q", human, wantHuman)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("status mutated state: before=%q after=%q error=%v", before, after, err)
	}
}

func TestExecuteStatusInfersCurrentWorkspaceAndRendersHumanTable(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom mount")
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", root.Path, "feature/inferred", "--data-dir", data, "--path", target, "--mount", "backend=api"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(target, "api")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	result := testutil.RunCommand(t, cli.Execute, "status", "--data-dir", data)
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("status = %#v", result)
	}
	want := "Workspace: feature/inferred\n\nREPOSITORY  BRANCH            MOUNT  STATUS  UPSTREAM\nroot        feature/inferred  .      clean   none\nbackend     feature/inferred  api    clean   none\n"
	if result.Stdout != want {
		t.Fatalf("status stdout = %q, want %q", result.Stdout, want)
	}
}

func TestExecuteStatusRendersTrackedManifestAbsentAndReplacementDrift(t *testing.T) {
	for _, test := range []struct {
		name        string
		replace     bool
		jsonNeedle  string
		humanNeedle string
	}{
		{name: "absent checkout", jsonNeedle: `"check":"checkout"`, humanNeedle: "declared-absent"},
		{name: "replacement identity", replace: true, jsonNeedle: `"identityMismatch":true`, humanNeedle: "unknown-repository"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := testutil.NewPushedGitRepository(t)
			project.CommitFile("root.txt", "root\n", "root")
			data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
			if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("init = %#v", result)
			}
			project.Run(t, "add", "-f", ".wtree.yml", "project.wtree.yml")
			project.Run(t, "commit", "-m", "track portable manifest")
			if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/status-drift", "--data-dir", data, "--path", target); result.Err != nil {
				t.Fatalf("create = %#v", result)
			}
			resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: project.Path, ProjectPath: project.Path, DataDir: data})
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := service.RequireWorkspace(resolution.Project, data, "feature/status-drift")
			if err != nil {
				t.Fatal(err)
			}
			statePath := service.WorkspaceStatePath(data, resolution.Project.ID, workspace.ID)
			stateBefore, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			project.Run(t, "worktree", "remove", "--force", target)
			var replacementIdentity string
			if test.replace {
				replacement := testutil.NewPushedGitRepository(t)
				replacement.CommitFile("replacement.txt", "replacement\n", "replacement")
				if err := os.Rename(replacement.Path, target); err != nil {
					t.Fatal(err)
				}
				replacementIdentity, err = gitadapter.NewAdapter("git").CommonGitDir(context.Background(), target)
				if err != nil {
					t.Fatal(err)
				}
			}

			jsonResult := testutil.RunCommand(t, cli.Execute, "status", "--project", project.Path, "feature/status-drift", "--data-dir", data, "--json")
			if jsonResult.Err != nil || jsonResult.Stderr != "" || !strings.Contains(jsonResult.Stdout, test.jsonNeedle) {
				t.Fatalf("JSON status = %#v, want %q", jsonResult, test.jsonNeedle)
			}
			if strings.Count(jsonResult.Stdout, `"check":"checkout"`) > 1 || strings.Count(jsonResult.Stdout, `"check":"identity"`) > 1 {
				t.Fatalf("JSON status duplicated canonical drift: %s", jsonResult.Stdout)
			}
			var statusValue service.WorkspaceStatus
			if err := json.Unmarshal([]byte(jsonResult.Stdout), &statusValue); err != nil {
				t.Fatal(err)
			}
			if len(statusValue.Repositories) != 1 {
				t.Fatalf("JSON repository count = %d, want 1: %s", len(statusValue.Repositories), jsonResult.Stdout)
			}
			rootStatus := statusValue.Repositories[0]
			if test.replace {
				if rootStatus.ActualIdentity != replacementIdentity || rootStatus.ActualIdentity == rootStatus.ExpectedIdentity || !rootStatus.IdentityMismatch {
					t.Fatalf("replacement JSON identity = %#v, replacement common Git dir %q", rootStatus, replacementIdentity)
				}
			} else if rootStatus.ActualIdentity != "" || !rootStatus.Missing {
				t.Fatalf("absent JSON identity = %#v", rootStatus)
			}
			if !test.replace && strings.Contains(jsonResult.Stdout, `"actualIdentity"`) {
				t.Fatalf("absent JSON emitted actualIdentity: %s", jsonResult.Stdout)
			}
			humanResult := testutil.RunCommand(t, cli.Execute, "status", "--project", project.Path, "feature/status-drift", "--data-dir", data)
			if humanResult.Err != nil || humanResult.Stderr != "" || !strings.Contains(humanResult.Stdout, test.humanNeedle) || !strings.Contains(humanResult.Stdout, "n/a") || !strings.Contains(humanResult.Stdout, "Local drift:") {
				t.Fatalf("human status = %#v, want %q and local n/a drift", humanResult, test.humanNeedle)
			}
			if again := testutil.RunCommand(t, cli.Execute, "status", "--project", project.Path, "feature/status-drift", "--data-dir", data, "--json"); again.Err != nil || again.Stdout != jsonResult.Stdout || again.Stderr != "" {
				t.Fatalf("JSON status was not deterministic: first=%#v again=%#v", jsonResult, again)
			}
			if stateAfter, err := os.ReadFile(statePath); err != nil || !bytes.Equal(stateBefore, stateAfter) {
				t.Fatalf("status mutated workspace state: before=%q after=%q error=%v", stateBefore, stateAfter, err)
			}
		})
	}
}

func TestExecuteStatusKeepsDefaultIdentityDriftSeparateFromHealthySelectedWorkspace(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom mount")
	root.Run(t, "add", "-f", ".wtree.yml", "project.wtree.yml")
	root.Run(t, "commit", "-m", "track portable manifest")
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	defaultStatePath := service.WorkspaceStatePath(data, resolution.Project.ID, "default")
	defaultStateBytes, err := os.ReadFile(defaultStatePath)
	if err != nil {
		t.Fatal(err)
	}
	defaultState, err := store.DecodeWorkspace(defaultStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	base := defaultState.Repositories[resolution.Project.BaseRepository]
	base.Head, err = gitadapter.NewAdapter("git").Head(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	defaultState.Repositories[resolution.Project.BaseRepository] = base
	if err := store.WriteWorkspace(defaultStatePath, defaultState); err != nil {
		t.Fatal(err)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", root.Path, "feature/healthy-status", "--data-dir", data, "--path", target, "--mount", "backend=api"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	workspace, err := service.RequireWorkspace(resolution.Project, data, "feature/healthy-status")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, resolution.Project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var expectedBackendIdentity string
	for _, repository := range resolution.Project.Repositories {
		if repository.ID == "backend" {
			backendPath = repository.SourcePath
			expectedBackendIdentity = repository.CommonGitDir
		}
	}
	if expectedBackendIdentity == "" {
		t.Fatal("resolved project has no backend repository")
	}
	fakeIdentity := filepath.Join(t.TempDir(), "replacement.git")
	if err := os.Mkdir(fakeIdentity, 0o700); err != nil {
		t.Fatal(err)
	}
	installStatusDefaultIdentityGitWrapper(t, backendPath, fakeIdentity)

	jsonResult := testutil.RunCommand(t, cli.Execute, "status", "--project", root.Path, "feature/healthy-status", "--data-dir", data, "--json")
	if jsonResult.Err != nil || jsonResult.Stderr != "" {
		t.Fatalf("JSON status = %#v", jsonResult)
	}
	var value service.WorkspaceStatus
	if err := json.Unmarshal([]byte(jsonResult.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Repositories) != 2 || value.Repositories[0].ID != resolution.Project.BaseRepository || value.Repositories[1].ID != "backend" {
		t.Fatalf("repository order = %#v", value.Repositories)
	}
	child := value.Repositories[1]
	if child.ActualIdentity != expectedBackendIdentity || child.ExpectedIdentity != expectedBackendIdentity || child.IdentityMismatch || child.UnknownRepository || child.Status != "clean" || !child.Clean || child.Branch != child.ExpectedBranch {
		t.Fatalf("healthy selected backend was contaminated: %#v", child)
	}
	if len(value.Drift) != 1 || value.Drift[0].ID != "backend" || value.Drift[0].Origin != "default-checkout" || value.Drift[0].Check != "identity" || value.Drift[0].Status != "mismatch" {
		t.Fatalf("default-only JSON drift = %#v", value.Drift)
	}
	humanResult := testutil.RunCommand(t, cli.Execute, "status", "--project", root.Path, "feature/healthy-status", "--data-dir", data)
	if humanResult.Err != nil || humanResult.Stderr != "" || !strings.Contains(humanResult.Stdout, "backend") || !strings.Contains(humanResult.Stdout, "clean") || !strings.Contains(humanResult.Stdout, "default-checkout") || !strings.Contains(humanResult.Stdout, "identity") || strings.Contains(humanResult.Stdout, "unknown-repository") {
		t.Fatalf("human status = %#v", humanResult)
	}
	if again := testutil.RunCommand(t, cli.Execute, "status", "--project", root.Path, "feature/healthy-status", "--data-dir", data, "--json"); again.Err != nil || again.Stderr != "" || again.Stdout != jsonResult.Stdout {
		t.Fatalf("JSON status was not deterministic: first=%#v again=%#v", jsonResult, again)
	}
	if stateAfter, err := os.ReadFile(statePath); err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("status mutated selected state: before=%q after=%q error=%v", stateBefore, stateAfter, err)
	}
	if registryAfter, err := os.ReadFile(registryPath); err != nil || !bytes.Equal(registryBefore, registryAfter) {
		t.Fatalf("status mutated registry: before=%q after=%q error=%v", registryBefore, registryAfter, err)
	}
}

func TestExecuteStatusJSONPartialWriterIsOnlyOutputFailure(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	writer := &partialStatusJSONWriter{err: io.ErrClosedPipe}
	var stderr bytes.Buffer
	err := cli.ExecuteContext(context.Background(), []string{"status", "default", "--project", project.Path, "--data-dir", data, "--json"}, writer, &stderr)
	if !errors.Is(err, io.ErrClosedPipe) || writer.calls != 1 || writer.String() == "" || stderr.Len() != 0 {
		t.Fatalf("status JSON writer: err=%v calls=%d stdout=%q stderr=%q", err, writer.calls, writer.String(), stderr.String())
	}
	if strings.Contains(writer.String(), `"success":false`) || strings.Contains(writer.String(), "\n{") {
		t.Fatalf("status JSON writer produced a second document: %q", writer.String())
	}
}

type partialStatusJSONWriter struct {
	bytes.Buffer
	err   error
	calls int
}

func installStatusDefaultIdentityGitWrapper(t *testing.T, defaultPath, identity string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	wrapper := filepath.Join(directory, "git")
	counter := filepath.Join(directory, "default-common-count")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
		source := filepath.Join(directory, "main.go")
		program := fmt.Sprintf(`package main
import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)
func main() {
	args := os.Args[1:]
	repository, common := "", false
	for index, argument := range args {
		if index > 0 && args[index-1] == "-C" { repository = argument }
		if argument == "--git-common-dir" { common = true }
		if argument == "fetch" || argument == "ls-remote" { os.Exit(97) }
	}
	observed, observedErr := filepath.EvalSymlinks(repository)
	want, wantErr := filepath.EvalSymlinks(%q)
	if common && observedErr == nil && wantErr == nil && strings.EqualFold(filepath.Clean(observed), filepath.Clean(want)) {
		count := 0
		if data, err := os.ReadFile(%q); err == nil { count, _ = strconv.Atoi(strings.TrimSpace(string(data))) }
		count++
		_ = os.WriteFile(%q, []byte(strconv.Itoa(count)+"\n"), 0600)
		if count%%2 == 0 { fmt.Println(%q); return }
	}
	command := exec.Command(%q, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok { os.Exit(exit.ExitCode()) }
		os.Exit(1)
	}
}
`, defaultPath, counter, counter, identity, realGit)
		if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("go", "build", "-o", wrapper, source).CombinedOutput(); err != nil {
			t.Fatalf("build status identity Git wrapper: %v\n%s", err, output)
		}
		t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
		return
	}
	script := fmt.Sprintf(`#!/bin/sh
repository=
previous=
common=false
for argument in "$@"; do
	if [ "$previous" = "-C" ]; then
		repository=$argument
	fi
	if [ "$argument" = "--git-common-dir" ]; then
		common=true
	fi
	case "$argument" in
	fetch|ls-remote)
		exit 97
		;;
	esac
	previous=$argument
done
if [ "$repository" = %q ] && [ "$common" = true ]; then
	count=0
	if [ -f %q ]; then
		read -r count < %q
	fi
	count=$((count + 1))
	printf '%%s\n' "$count" > %q
	if [ $((count %% 2)) -eq 0 ]; then
		printf '%%s\n' %q
		exit 0
	fi
fi
exec %q "$@"
`, defaultPath, counter, counter, counter, identity, realGit)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (w *partialStatusJSONWriter) Write(value []byte) (int, error) {
	w.calls++
	if w.calls != 1 {
		return 0, w.err
	}
	count := len(value) / 2
	if count == 0 {
		count = 1
	}
	_, _ = w.Buffer.Write(value[:count])
	return count, w.err
}
