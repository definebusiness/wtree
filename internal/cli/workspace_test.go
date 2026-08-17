package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteListIncludesDefaultAndCreatedWorkspace(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/list", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	plain := testutil.RunCommand(t, cli.Execute, "list", "--project", project.Path, "--data-dir", data)
	if plain.Err != nil || plain.Stderr != "" {
		t.Fatalf("plain list = %#v", plain)
	}
	canonicalProjectPath, err := filepath.EvalSymlinks(project.Path)
	if err != nil {
		t.Fatal(err)
	}
	wantPlain := "default       " + canonicalProjectPath + "\nfeature/list  " + target + "\n"
	if plain.Stdout != wantPlain {
		t.Fatalf("plain list = %q, want %q", plain.Stdout, wantPlain)
	}
	result := testutil.RunCommand(t, cli.Execute, "list", "--project", project.Path, "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("list = %#v", result)
	}
	var workspaces []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &workspaces); err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 2 || workspaces[0].Name != "default" || workspaces[1].Name != "feature/list" || workspaces[1].Path != target {
		t.Fatalf("list = %#v", workspaces)
	}
}

func TestExecutePathDefaultFromUnmanagedLinkedWorktree(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}

	project.Run(t, "branch", "unmanaged")
	linked := filepath.Join(t.TempDir(), "unmanaged")
	project.Run(t, "worktree", "add", linked, "unmanaged")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(linked); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	result := testutil.RunCommand(t, cli.Execute, "path", "default", "--data-dir", data)
	canonicalProjectPath, err := filepath.EvalSymlinks(project.Path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Err != nil || result.Stderr != "" || result.Stdout != canonicalProjectPath+"\n" {
		t.Fatalf("path default = %#v, want %q", result, canonicalProjectPath)
	}
}

func TestExecuteCheckoutRestoresRetainedRenamedMountAndLookupFromNestedCheckout(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data, "--add-ignore"); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom mount")
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", root.Path, "feature/restore", "--data-dir", data, "--path", target, "--mount", "backend=api"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	backend.Run(t, "worktree", "remove", "--force", filepath.Join(target, "api"))
	root.Run(t, "worktree", "remove", "--force", target)

	checkout := testutil.RunCommand(t, cli.Execute, "checkout", "--project", root.Path, "feature/restore", "--data-dir", data)
	if checkout.Err != nil || checkout.Stderr != "" || !strings.Contains(checkout.Stdout, "Checked out workspace: feature/restore\n") {
		t.Fatalf("checkout = %#v", checkout)
	}
	if _, err := os.Stat(filepath.Join(target, "api", ".git")); err != nil {
		t.Fatalf("restored renamed backend: %v", err)
	}

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(target, "api")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	repoPath := testutil.RunCommand(t, cli.Execute, "repo", "path", "backend", "--data-dir", data)
	if repoPath.Err != nil || repoPath.Stderr != "" || repoPath.Stdout != filepath.Join(target, "api")+"\n" {
		t.Fatalf("repo path = %#v", repoPath)
	}
	repoGet := testutil.RunCommand(t, cli.Execute, "repo", "get", "backend", "--data-dir", data, "--json")
	if repoGet.Err != nil || repoGet.Stderr != "" {
		t.Fatalf("repo get = %#v", repoGet)
	}
	var checkoutState struct {
		ID        string `json:"id"`
		Path      string `json:"path"`
		Workspace string `json:"workspace"`
	}
	if err := json.Unmarshal([]byte(repoGet.Stdout), &checkoutState); err != nil || checkoutState.ID != "backend" || checkoutState.Path != filepath.Join(target, "api") || checkoutState.Workspace != "feature/restore" {
		t.Fatalf("repo get = %q state=%#v error=%v", repoGet.Stdout, checkoutState, err)
	}
	workspacePath := testutil.RunCommand(t, cli.Execute, "path", "feature/restore", "--data-dir", data)
	if workspacePath.Err != nil || workspacePath.Stdout != target+"\n" || workspacePath.Stderr != "" {
		t.Fatalf("path = %#v", workspacePath)
	}
}

func TestExecuteCheckoutOverlayRetainsUnspecifiedMounts(t *testing.T) {
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
	sharedPath := filepath.Join(backendPath, "shared")
	if err := os.Rename(shared.Path, sharedPath); err != nil {
		t.Fatal(err)
	}
	shared.Path = sharedPath
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data, "--add-ignore"); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.CommitFile(".gitignore", "/api/\n/services/\n", "ignore custom backend mounts")
	backend.CommitFile(".gitignore", "/common/\n", "ignore custom shared mount")
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", root.Path, "feature/overlay", "--data-dir", data, "--path", target, "--mount", "backend=api", "--mount", "shared=common"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	shared.Run(t, "worktree", "remove", "--force", filepath.Join(target, "api", "common"))
	backend.Run(t, "worktree", "remove", "--force", filepath.Join(target, "api"))
	root.Run(t, "worktree", "remove", "--force", target)
	result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", root.Path, "feature/overlay", "--data-dir", data, "--mount", "backend=services")
	if result.Err != nil {
		t.Fatalf("checkout = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(target, "services", "common", ".git")); err != nil {
		t.Fatalf("unspecified shared mount was not retained: %v", err)
	}
}

func TestExecuteCheckoutAndLookupFailuresDoNotMutate(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	missing := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature/missing", "--data-dir", data, "--path", target, "--json")
	if missing.Err == nil || cli.ExitCode(missing.Err) != 5 || !strings.Contains(missing.Stdout, "\"code\":\"validation\"") {
		t.Fatalf("missing checkout = %#v", missing)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("missing checkout created target: %v", err)
	}
	project.Run(t, "branch", "feature/held")
	held := filepath.Join(t.TempDir(), "held")
	project.Run(t, "worktree", "add", held, "feature/held")
	checkedOut := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature/held", "--data-dir", data, "--path", target)
	if checkedOut.Err == nil || cli.ExitCode(checkedOut.Err) != 8 || checkedOut.Stdout != "" {
		t.Fatalf("checked-out branch = %#v", checkedOut)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("checked-out branch created target: %v", err)
	}
	unknownWorkspace := testutil.RunCommand(t, cli.Execute, "path", "--project", project.Path, "unknown", "--data-dir", data)
	if unknownWorkspace.Err == nil || cli.ExitCode(unknownWorkspace.Err) != 4 || unknownWorkspace.Stdout != "" {
		t.Fatalf("unknown workspace path = %#v", unknownWorkspace)
	}
	unknownRepo := testutil.RunCommand(t, cli.Execute, "repo", "--project", project.Path, "path", "unknown", "--data-dir", data)
	if unknownRepo.Err == nil || cli.ExitCode(unknownRepo.Err) != 5 || unknownRepo.Stdout != "" {
		t.Fatalf("unknown repo path = %#v", unknownRepo)
	}
}

func TestExecuteCheckoutExistingUnmappedBranchCreatesState(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	project.Run(t, "branch", "feature/existing")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature/existing", "--data-dir", data, "--path", target, "--json")
	if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "\"operation\":\"checkout\"") {
		t.Fatalf("checkout = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatalf("checkout worktree: %v", err)
	}
	listed := testutil.RunCommand(t, cli.Execute, "list", "--project", project.Path, "--data-dir", data)
	if listed.Err != nil || !strings.Contains(listed.Stdout, "feature/existing  "+target+"\n") {
		t.Fatalf("list after checkout = %#v", listed)
	}
}

func TestExecuteListIncludesPartialWorkspaceJSON(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	initialized := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data, "--add-ignore")
	if initialized.Err != nil {
		t.Fatalf("init = %#v", initialized)
	}
	projectID := strings.Fields(initialized.Stdout)[1]
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	partialPath := filepath.Join(t.TempDir(), "partial")
	if err := store.WriteWorkspace(service.WorkspaceStatePath(data, projectID, "partial"), store.WorkspaceState{
		ID: "partial", Name: "partial", Path: partialPath, Partial: true, MissingRepositoryIDs: []string{"backend"},
		Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Head: head, Mount: ".", ResolvedPath: partialPath}},
	}); err != nil {
		t.Fatal(err)
	}
	result := testutil.RunCommand(t, cli.Execute, "list", "--project", root.Path, "--data-dir", data, "--json")
	if result.Err != nil {
		t.Fatalf("list = %#v", result)
	}
	var workspaces []struct {
		Name    string   `json:"name"`
		Partial bool     `json:"partial"`
		Missing []string `json:"missingRepositoryIds"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &workspaces); err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 2 || workspaces[1].Name != "partial" || !workspaces[1].Partial || len(workspaces[1].Missing) != 1 || workspaces[1].Missing[0] != "backend" {
		t.Fatalf("partial list = %#v", workspaces)
	}
}

func TestExecuteCheckoutRefusesPersistedPartialDetachedAndDivergentStateWithoutMutation(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	project.Run(t, "branch", "feature/state")
	data := t.TempDir()
	initialized := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data)
	if initialized.Err != nil {
		t.Fatalf("init = %#v", initialized)
	}
	projectID := strings.Fields(initialized.Stdout)[1]
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), project.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name  string
		state store.WorkspaceState
	}{
		{name: "partial", state: store.WorkspaceState{ID: "partial", Name: "feature/state", Path: filepath.Join(t.TempDir(), "partial"), Partial: true, MissingRepositoryIDs: []string{"root"}, Repositories: map[string]store.CheckoutState{}}},
		{name: "detached", state: store.WorkspaceState{ID: "detached", Name: "feature/state", Path: filepath.Join(t.TempDir(), "detached"), Repositories: map[string]store.CheckoutState{"root": {Head: head, Detached: true, Mount: ".", ResolvedPath: filepath.Join(t.TempDir(), "detached")}}}},
		{name: "divergent", state: store.WorkspaceState{ID: "divergent", Name: "feature/state", Path: filepath.Join(t.TempDir(), "divergent"), Repositories: map[string]store.CheckoutState{"root": {Branch: "other", Head: head, Mount: ".", ResolvedPath: filepath.Join(t.TempDir(), "divergent")}}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			state := scenario.state
			// Keep root and checkout paths identical for the persisted-state validator.
			if checkout, found := state.Repositories["root"]; found {
				checkout.ResolvedPath = state.Path
				state.Repositories["root"] = checkout
			}
			if err := store.WriteWorkspace(service.WorkspaceStatePath(data, projectID, scenario.name), state); err != nil {
				t.Fatal(err)
			}
			result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature/state", "--data-dir", data, "--path", state.Path)
			if result.Err == nil || cli.ExitCode(result.Err) != 5 {
				t.Fatalf("checkout %s = %#v, want validation refusal", scenario.name, result)
			}
			if _, err := os.Stat(state.Path); !os.IsNotExist(err) {
				t.Fatalf("checkout %s mutated target: %v", scenario.name, err)
			}
			if err := os.Remove(service.WorkspaceStatePath(data, projectID, scenario.name)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
