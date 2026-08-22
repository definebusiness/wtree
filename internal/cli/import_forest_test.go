package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteImportForestJSONAndContextPaths(t *testing.T) {
	forest := newCLIImportForest(t, []string{"api", "alpha", "beta", "gamma", "web"})
	registryPath := filepath.Join(forest.data, "registry.json")
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	dry := runCLIAt(t, forest.workspace["gamma"], "import", "--name", "imported", "--data-dir", forest.data, "--dry-run", "--json")
	if dry.Err != nil || dry.Stderr != "" {
		t.Fatalf("nested dry-run import = %#v", dry)
	}
	dryPlan := decodeCLIImportPlan(t, dry.Stdout)
	assertCLIImportForestPlan(t, dryPlan, forest)
	if _, err := os.Stat(service.WorkspaceStatePath(forest.data, forest.project.ID, dryPlan.WorkspaceID)); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote import state: %v", err)
	}
	registryAfterDry, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(registryBefore, registryAfterDry) {
		t.Fatal("dry-run import mutated registry")
	}
	registry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	registered := registry.Projects[forest.project.ID]
	registered.ConfigPath = filepath.Join(t.TempDir(), "relocated", ".wtree.yml")
	registry.Projects[forest.project.ID] = registered
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}

	completed := runCLIAt(t, forest.workspace["web"], "import", "--project", forest.project.ConfigPath, "--name", "imported", "--data-dir", forest.data, "--json")
	if completed.Err != nil || completed.Stderr != "" {
		t.Fatalf("sibling import = %#v", completed)
	}
	plan := decodeCLIImportPlan(t, completed.Stdout)
	assertCLIImportForestPlan(t, plan, forest)
	if completed.Stdout != dry.Stdout {
		t.Fatalf("dry-run and completed import JSON differ\ndry: %s\ncompleted: %s", dry.Stdout, completed.Stdout)
	}
	state, err := store.ReadWorkspace(service.WorkspaceStatePath(forest.data, forest.project.ID, plan.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Path != plan.LogicalRoot || len(state.Repositories) != len(plan.Repositories) {
		t.Fatalf("imported state = %#v", state)
	}
	reconciled, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reconciled.Projects[forest.project.ID].ConfigPath; got != forest.project.ConfigPath {
		t.Fatalf("forest import registry config path = %q, want %q", got, forest.project.ConfigPath)
	}

	for _, test := range []struct {
		name      string
		contextID string
		requestID string
	}{
		{name: "logical root", contextID: "root", requestID: "api"},
		{name: "base", contextID: "api", requestID: "api"},
		{name: "sibling", contextID: "web", requestID: "web"},
		{name: "alpha", contextID: "alpha", requestID: "alpha"},
		{name: "beta", contextID: "beta", requestID: "beta"},
		{name: "deepest", contextID: "gamma", requestID: "gamma"},
	} {
		t.Run(test.name, func(t *testing.T) {
			contextPath := forest.root
			if test.contextID != "root" {
				contextPath = forest.workspace[test.contextID]
			}
			workspacePath := runCLIAt(t, contextPath, "path", "imported", "--data-dir", forest.data)
			if workspacePath.Err != nil || workspacePath.Stderr != "" || workspacePath.Stdout != plan.LogicalRoot+"\n" {
				t.Fatalf("workspace path = %#v", workspacePath)
			}
			checkout := state.Repositories[test.requestID]
			repositoryPath := runCLIAt(t, contextPath, "repo", "path", test.requestID, "--data-dir", forest.data)
			if repositoryPath.Err != nil || repositoryPath.Stderr != "" || repositoryPath.Stdout != checkout.ResolvedPath+"\n" {
				t.Fatalf("repository path = %#v", repositoryPath)
			}
			repositoryGet := runCLIAt(t, contextPath, "repo", "get", test.requestID, "--data-dir", forest.data, "--json")
			if repositoryGet.Err != nil || repositoryGet.Stderr != "" {
				t.Fatalf("repository get = %#v", repositoryGet)
			}
			var value struct {
				ID        string `json:"id"`
				Mount     string `json:"mount"`
				Path      string `json:"path"`
				Workspace string `json:"workspace"`
			}
			if err := json.Unmarshal([]byte(repositoryGet.Stdout), &value); err != nil || value.ID != test.requestID || value.Mount != checkout.Mount || value.Path != checkout.ResolvedPath || value.Workspace != "imported" {
				t.Fatalf("repository get = %q value=%#v error=%v", repositoryGet.Stdout, value, err)
			}
		})
	}

	failure := runCLIAt(t, forest.workspace["api"], "import", "missing", "--name", "invalid", "--data-dir", forest.data, "--dry-run", "--json")
	if failure.Err == nil {
		t.Fatal("invalid import succeeded")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(failure.Stdout), &envelope); err != nil {
		t.Fatalf("failure JSON = %q: %v", failure.Stdout, err)
	}
	for _, field := range []string{"logicalRoot", "baseRepository", "repositories"} {
		if _, found := envelope[field]; found {
			t.Fatalf("pre-topology failure exposed %q: %s", field, failure.Stdout)
		}
	}

	reordered := newCLIImportForest(t, []string{"web", "gamma", "beta", "alpha", "api"})
	other := runCLIAt(t, reordered.workspace["gamma"], "import", "--name", "imported", "--data-dir", reordered.data, "--dry-run", "--json")
	if other.Err != nil {
		t.Fatalf("reordered dry-run import = %#v", other)
	}
	if got, want := cliImportTopology(decodeCLIImportPlan(t, other.Stdout)), cliImportTopology(dryPlan); !sameCLIImportTopology(got, want) {
		t.Fatalf("reordered topology = %#v, want %#v", got, want)
	}
}

type cliForest struct {
	project   domain.Project
	data      string
	root      string
	workspace map[string]string
}

func newCLIImportForest(t *testing.T, order []string) cliForest {
	t.Helper()
	root := t.TempDir()
	paths := map[string]string{
		"api":   filepath.Join(root, "services", "api"),
		"web":   filepath.Join(root, "grouped", "web"),
		"alpha": filepath.Join(root, "services", "api", "components", "alpha"),
		"beta":  filepath.Join(root, "services", "api", "components", "alpha", "deep", "beta"),
		"gamma": filepath.Join(root, "services", "api", "components", "alpha", "deep", "beta", "tools", "gamma"),
	}
	repositories := make(map[string]testutil.PushedGitRepository, len(paths))
	for _, id := range order {
		repository := testutil.NewPushedGitRepository(t)
		repository.CommitFile("readme", id+"\n", "initial")
		repositories[id] = repository
	}
	// The grouping directories are deliberately made in caller order; repository
	// placement remains parent-first because nested Git checkouts cannot exist
	// before their containing checkout.
	for _, id := range order {
		if id != "api" && id != "web" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(paths[id]), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"api", "alpha", "beta", "gamma", "web"} {
		if err := os.MkdirAll(filepath.Dir(paths[id]), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(repositories[id].Path, paths[id]); err != nil {
			t.Fatalf("place %s: %v", id, err)
		}
	}
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", root, "--base-repository", "api", "--data-dir", data); result.Err != nil {
		t.Fatalf("init forest = %#v", result)
	}
	project, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: paths["api"], DataDir: data})
	if err != nil {
		t.Fatalf("resolve initialized project: %v", err)
	}
	created, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/import-cli", TargetPath: filepath.Join(t.TempDir(), "external logical root"), DataDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("create external forest workspace: %v", err)
	}
	workspace := make(map[string]string, len(created.Repositories))
	for _, repository := range created.Repositories {
		canonicalPath, err := filepath.EvalSymlinks(repository.Path)
		if err != nil {
			t.Fatal(err)
		}
		workspace[repository.ID] = canonicalPath
	}
	canonicalRoot, err := filepath.EvalSymlinks(created.LogicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	return cliForest{project: project, data: data, root: canonicalRoot, workspace: workspace}
}

type cliImportPlan struct {
	ProjectID      string `json:"projectId"`
	WorkspaceID    string `json:"workspaceId"`
	WorkspaceName  string `json:"workspaceName"`
	RootPath       string `json:"rootPath"`
	LogicalRoot    string `json:"logicalRoot"`
	BaseRepository string `json:"baseRepository"`
	Partial        bool   `json:"partial"`
	Repositories   []struct {
		ID           string `json:"id"`
		ParentID     string `json:"parentId"`
		Mount        string `json:"mount"`
		Path         string `json:"path"`
		ResolvedPath string `json:"resolvedPath"`
		Branch       string `json:"branch"`
		Head         string `json:"head"`
	} `json:"repositories"`
}

func decodeCLIImportPlan(t *testing.T, output string) cliImportPlan {
	t.Helper()
	var value cliImportPlan
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatalf("decode import JSON %q: %v", output, err)
	}
	return value
}

func assertCLIImportForestPlan(t *testing.T, value cliImportPlan, forest cliForest) {
	t.Helper()
	if value.ProjectID != forest.project.ID || value.WorkspaceName != "imported" || value.RootPath != forest.root || value.LogicalRoot != forest.root || value.BaseRepository != "api" || value.WorkspaceID == "" || value.Partial || len(value.Repositories) != 5 {
		t.Fatalf("import JSON = %#v", value)
	}
	want := []struct{ id, parent, mount string }{
		{"api", "", "services/api"}, {"web", "", "grouped/web"}, {"alpha", "api", "components/alpha"}, {"beta", "alpha", "deep/beta"}, {"gamma", "beta", "tools/gamma"},
	}
	for index, repository := range value.Repositories {
		if repository.ID != want[index].id || repository.ParentID != want[index].parent || repository.Mount != want[index].mount || repository.Path != forest.workspace[repository.ID] || repository.ResolvedPath != repository.Path || repository.Branch == "" || repository.Head == "" {
			t.Fatalf("repository %d = %#v", index, repository)
		}
	}
}

func cliImportTopology(value cliImportPlan) []struct{ ID, ParentID, Mount string } {
	result := make([]struct{ ID, ParentID, Mount string }, 0, len(value.Repositories))
	for _, repository := range value.Repositories {
		result = append(result, struct{ ID, ParentID, Mount string }{repository.ID, repository.ParentID, repository.Mount})
	}
	return result
}

func sameCLIImportTopology(left, right []struct{ ID, ParentID, Mount string }) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func runCLIAt(t *testing.T, directory string, arguments ...string) testutil.Result {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	return testutil.RunCommand(t, cli.Execute, arguments...)
}
