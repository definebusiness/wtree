package cli_test

import (
	"bytes"
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

func TestWorkspaceSelectionPathAndExplicitStatus(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	alpha, beta := filepath.Join(t.TempDir(), "alpha"), filepath.Join(t.TempDir(), "beta")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	for _, value := range []struct{ name, path string }{{"feature/alpha-search", alpha}, {"feature/beta-search", beta}} {
		if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, value.name, "--data-dir", data, "--path", value.path); result.Err != nil {
			t.Fatalf("create %s = %#v", value.name, result)
		}
	}

	if result := testutil.RunCommand(t, cli.Execute, "path", "alpha", "--exact=false", "--project", project.Path, "--data-dir", data); result.Err != nil || result.Stderr != "" || result.Stdout != alpha+"\n" {
		t.Fatalf("substring path = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "path", "feature/alpha-search", "--exact", "--project", project.Path, "--data-dir", data); result.Err != nil || result.Stdout != alpha+"\n" {
		t.Fatalf("exact-name path = %#v", result)
	}
	resolution, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: project.Path, ProjectPath: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(resolution, data, "feature/alpha-search")
	if err != nil {
		t.Fatal(err)
	}
	if result := testutil.RunCommand(t, cli.Execute, "path", workspace.ID, "--exact", "--project", project.Path, "--data-dir", data); result.Err != nil || result.Stdout != alpha+"\n" {
		t.Fatalf("exact-id path = %#v", result)
	}
	betaWorkspace, err := service.RequireWorkspace(resolution, data, "feature/beta-search")
	if err != nil {
		t.Fatal(err)
	}
	status := testutil.RunCommand(t, cli.Execute, "status", workspace.ID, "--exact", "--json", "--project", project.Path, "--data-dir", data)
	if status.Err != nil || status.Stderr != "" || !strings.Contains(status.Stdout, `"workspace":"feature/alpha-search"`) {
		t.Fatalf("exact-id status = %#v", status)
	}
	for _, command := range []string{"path", "status"} {
		for _, exact := range []string{"--exact=false", "--exact"} {
			result := testutil.RunCommand(t, cli.Execute, command, "", exact, "--project", project.Path, "--data-dir", data)
			if result.Err == nil || cli.ExitCode(result.Err) != 2 || result.Stdout != "" || result.Stderr != "" {
				t.Fatalf("empty %s %s = %#v", command, exact, result)
			}
		}
	}
	other := testutil.NewPushedGitRepository(t)
	other.CommitFile("other.txt", "other\n", "other")
	otherData, otherTarget := t.TempDir(), filepath.Join(t.TempDir(), "other-alpha")
	if result := testutil.RunCommand(t, cli.Execute, "init", other.Path, "--data-dir", otherData); result.Err != nil {
		t.Fatalf("other init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", other.Path, "feature/alpha-search", "--data-dir", otherData, "--path", otherTarget); result.Err != nil {
		t.Fatalf("other create = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "path", "alpha", "--project", other.Path, "--data-dir", otherData); result.Err != nil || result.Stdout != otherTarget+"\n" {
		t.Fatalf("project-scoped shorthand path = %#v", result)
	}
	statePaths := []string{
		service.WorkspaceStatePath(data, resolution.ID, workspace.ID),
		service.WorkspaceStatePath(data, resolution.ID, betaWorkspace.ID),
	}
	stateBefore := make([][]byte, len(statePaths))
	for index, statePath := range statePaths {
		stateBefore[index], err = os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
	}
	registryPath := filepath.Join(data, "registry.json")
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	refsBefore, err := testutil.GitCommand(t, "-C", project.Path, "show-ref").Output()
	if err != nil {
		t.Fatal(err)
	}
	worktreesBefore, err := testutil.GitCommand(t, "-C", project.Path, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"create", "new-workspace", "--exact", "--project", project.Path, "--data-dir", data},
		{"import", project.Path, "--exact", "--project", project.Path, "--data-dir", data},
		{"hooks", "share", "post-create", "--exact", "--project", project.Path, "--data-dir", data},
		{"config", "set", "worktrees.root", filepath.Join(t.TempDir(), "would-write"), "--exact"},
		{"companion", "update", "backend", "--exact", "--project", project.Path, "--data-dir", data},
	} {
		if result := testutil.RunCommand(t, cli.Execute, arguments...); result.Err == nil || cli.ExitCode(result.Err) != 2 || !strings.Contains(result.Err.Error(), "unknown flag: --exact") {
			t.Fatalf("mutating unsupported exact %v = %#v", arguments, result)
		}
	}
	for _, command := range []string{"remove", "delete"} {
		if result := testutil.RunCommand(t, cli.Execute, command, "alpha", "--project", project.Path, "--data-dir", data); result.Err == nil || cli.ExitCode(result.Err) != 4 {
			t.Fatalf("%s retained exact lookup = %#v", command, result)
		}
		if result := testutil.RunCommand(t, cli.Execute, command, "alpha", "--exact", "--project", project.Path, "--data-dir", data); result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Fatalf("%s unexpectedly accepts --exact = %#v", command, result)
		}
	}
	for index, statePath := range statePaths {
		after, readErr := os.ReadFile(statePath)
		if readErr != nil || string(after) != string(stateBefore[index]) {
			t.Fatalf("destructive shorthand changed state %q: error=%v", statePath, readErr)
		}
	}
	for _, root := range []string{alpha, beta} {
		if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
			t.Fatalf("destructive shorthand changed workspace %q: info=%v error=%v", root, info, statErr)
		}
	}
	if after, readErr := os.ReadFile(registryPath); readErr != nil || !bytes.Equal(registryBefore, after) {
		t.Fatalf("destructive shorthand changed registry: error=%v", readErr)
	}
	if after, runErr := testutil.GitCommand(t, "-C", project.Path, "show-ref").Output(); runErr != nil || !bytes.Equal(refsBefore, after) {
		t.Fatalf("destructive shorthand changed refs: error=%v", runErr)
	}
	if after, runErr := testutil.GitCommand(t, "-C", project.Path, "worktree", "list", "--porcelain").Output(); runErr != nil || !bytes.Equal(worktreesBefore, after) {
		t.Fatalf("destructive shorthand changed worktree registrations: error=%v", runErr)
	}
	if result := testutil.RunCommand(t, cli.Execute, "delete", "search", "--exact", "--project", project.Path, "--data-dir", data); result.Err == nil || cli.ExitCode(result.Err) != 2 {
		t.Fatalf("delete unexpectedly accepts --exact = %#v", result)
	}
	project.Run(t, "worktree", "remove", "--force", alpha)
	if err := os.Symlink(beta, alpha); err != nil {
		t.Fatal(err)
	}
	if result := testutil.RunCommand(t, cli.Execute, "path", "alpha", "--project", project.Path, "--data-dir", data); result.Err == nil || cli.ExitCode(result.Err) != 5 || result.Stdout != "" {
		t.Fatalf("symlink root path = %#v", result)
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
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom mount")
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", root.Path, "feature/restore", "--data-dir", data, "--path", target, "--mount", "backend=api"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	backend.Run(t, "worktree", "remove", "--force", filepath.Join(target, "api"))
	root.Run(t, "worktree", "remove", "--force", target)

	checkout := testutil.RunCommand(t, cli.Execute, "checkout", "--project", root.Path, "rest", "--data-dir", data)
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

func TestWorkspaceSelectionCheckoutCanonicalShorthandAndExactID(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}
	backend.Path = filepath.Join(root.Path, "backend")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "canonical")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom mount")
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", root.Path, "feature/canonical", "--data-dir", data, "--path", target, "--mount", "backend=api"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	project, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/canonical")
	if err != nil {
		t.Fatal(err)
	}
	if result := testutil.RunCommand(t, cli.Execute, "remove", "--project", root.Path, "feature/canonical", "--data-dir", data); result.Err != nil {
		t.Fatalf("remove = %#v", result)
	}
	shorthand := testutil.RunCommand(t, cli.Execute, "checkout", "--project", root.Path, "canon", "--data-dir", data, "--dry-run", "--json")
	if shorthand.Err != nil || shorthand.Stderr != "" || !strings.Contains(shorthand.Stdout, `"workspaceName":"feature/canonical"`) || !strings.Contains(shorthand.Stdout, `"mount":"api"`) {
		t.Fatalf("shorthand checkout dry-run = %#v", shorthand)
	}
	exactID := testutil.RunCommand(t, cli.Execute, "checkout", "--project", root.Path, workspace.ID, "--exact", "--data-dir", data, "--dry-run", "--json")
	if exactID.Err != nil || exactID.Stderr != "" || exactID.Stdout != shorthand.Stdout {
		t.Fatalf("exact ID checkout dry-run = %#v, want canonical shorthand output %q", exactID, shorthand.Stdout)
	}
	result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", root.Path, "canon", "--data-dir", data)
	if result.Err != nil || !strings.Contains(result.Stdout, "Checked out workspace: feature/canonical\n") {
		t.Fatalf("shorthand checkout = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(target, "api", ".git")); err != nil {
		t.Fatalf("shorthand did not restore retained custom mount: %v", err)
	}
}

func TestWorkspaceSelectionCheckoutEmptySelectorBeatsCorruptInventory(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	resolution, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: project.Path, ProjectPath: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.WorkspaceStatePath(data, resolution.ID, "corrupt"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "exact"}[exact], func(t *testing.T) {
			args := []string{"checkout", "", "--project", project.Path, "--data-dir", data, "--json"}
			if exact {
				args = append(args, "--exact")
			}
			result := testutil.RunCommand(t, cli.Execute, args...)
			if result.Err == nil || cli.ExitCode(result.Err) != 2 || result.Stderr != "" || !strings.Contains(result.Stdout, `"code":"invalid_arguments"`) {
				t.Fatalf("empty %s checkout with corrupt inventory = %#v", map[bool]string{false: "default", true: "exact"}[exact], result)
			}
		})
	}
}

func TestWorkspaceSelectionCheckoutOverlayRetainsUnspecifiedMounts(t *testing.T) {
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
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
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
	result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", root.Path, "overl", "--data-dir", data, "--mount", "backend=services")
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
	project.Run(t, "branch", "feature/missing")
	missing := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature/missing", "--data-dir", data, "--path", target, "--json")
	if missing.Err == nil || cli.ExitCode(missing.Err) != 4 || !strings.Contains(missing.Stdout, "\"code\":\"workspace_not_found\"") || !strings.Contains(missing.Stdout, "--exact") {
		t.Fatalf("missing checkout = %#v", missing)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("missing checkout created target: %v", err)
	}
	project.Run(t, "branch", "feature/held")
	held := filepath.Join(t.TempDir(), "held")
	project.Run(t, "worktree", "add", held, "feature/held")
	checkedOut := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature/held", "--exact", "--data-dir", data, "--path", target)
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

func TestWorkspaceSelectionCheckoutLookupFailuresHaveNoEffectsAcrossModes(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	for _, name := range []string{"feature/lookup-one", "feature/lookup-two"} {
		if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, name, "--data-dir", data, "--path", filepath.Join(t.TempDir(), strings.ReplaceAll(name, "/", "-"))); result.Err != nil {
			t.Fatalf("create %s = %#v", name, result)
		}
	}
	resolved, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: project.Path, ProjectPath: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	states, err := service.ListWorkspaces(resolved, data)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore := map[string][]byte{}
	for _, workspace := range states {
		path := service.WorkspaceStatePath(data, resolved.ID, workspace.ID)
		bytes, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		stateBefore[path] = bytes
	}
	registryBefore, err := os.ReadFile(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	refsBefore, err := testutil.GitCommand(t, "-C", project.Path, "show-ref").Output()
	if err != nil {
		t.Fatal(err)
	}
	worktreesBefore, err := testutil.GitCommand(t, "-C", project.Path, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, query, code     string
		dryRun, verbose, json bool
	}{
		{"ambiguity human", "lookup", "conflict", false, false, false},
		{"ambiguity dry-run", "lookup", "conflict", true, false, false},
		{"ambiguity verbose", "lookup", "conflict", false, true, false},
		{"ambiguity json", "lookup", "conflict", false, false, true},
		{"no-match human", "branch-only", "workspace_not_found", false, false, false},
		{"no-match dry-run", "branch-only", "workspace_not_found", true, false, false},
		{"no-match verbose", "branch-only", "workspace_not_found", false, true, false},
		{"no-match json", "branch-only", "workspace_not_found", false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"checkout", test.query, "--project", project.Path, "--data-dir", data}
			if test.dryRun {
				args = append(args, "--dry-run")
			}
			if test.verbose {
				args = append(args, "--verbose")
			}
			if test.json {
				args = append(args, "--json")
			}
			result := testutil.RunCommand(t, cli.Execute, args...)
			wantExit := 8
			if test.code == "workspace_not_found" {
				wantExit = 4
			}
			if result.Err == nil || cli.ExitCode(result.Err) != wantExit {
				t.Fatalf("%s lookup failure = %#v", test.name, result)
			}
			if test.json {
				if result.Stderr != "" || !strings.Contains(result.Stdout, `"code":"`+test.code+`"`) || !strings.Contains(result.Stdout, `"candidates"`) {
					t.Fatalf("%s JSON lookup output = %#v", test.name, result)
				}
			} else if result.Stdout != "" || result.Stderr != "" {
				t.Fatalf("%s human ambiguity wrote streams before process boundary: %#v", test.name, result)
			}
		})
	}
	for path, before := range stateBefore {
		after, readErr := os.ReadFile(path)
		if readErr != nil || string(after) != string(before) {
			t.Fatalf("lookup failure changed state %q: before=%q after=%q err=%v", path, before, after, readErr)
		}
	}
	registryAfter, err := os.ReadFile(filepath.Join(data, "registry.json"))
	if err != nil || string(registryAfter) != string(registryBefore) {
		t.Fatalf("lookup failure changed registry: before=%q after=%q err=%v", registryBefore, registryAfter, err)
	}
	refsAfter, err := testutil.GitCommand(t, "-C", project.Path, "show-ref").Output()
	if err != nil || string(refsAfter) != string(refsBefore) {
		t.Fatalf("lookup failure changed refs: before=%s after=%s err=%v", refsBefore, refsAfter, err)
	}
	worktreesAfter, err := testutil.GitCommand(t, "-C", project.Path, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(worktreesAfter) != string(worktreesBefore) {
		t.Fatalf("lookup failure changed worktrees:\nbefore=%s\nafter=%s", worktreesBefore, worktreesAfter)
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
	result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature/existing", "--exact", "--data-dir", data, "--path", target, "--json")
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
	initialized := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data)
	if initialized.Err != nil {
		t.Fatalf("init = %#v", initialized)
	}
	projectID := strings.Fields(initialized.Stdout)[1]
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	partialPath := root.Path
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
	status := testutil.RunCommand(t, cli.Execute, "status", "arti", "--project", root.Path, "--data-dir", data, "--json")
	if status.Err != nil || status.Stderr != "" || !strings.Contains(status.Stdout, `"workspace":"partial"`) {
		t.Fatalf("partial substring status = %#v", status)
	}
}

func TestWorkspaceSelectionCheckoutRefusesPersistedPartialDetachedAndDivergentStateWithoutMutation(t *testing.T) {
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
			result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "state", "--data-dir", data, "--path", state.Path)
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
