package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestLoadProjectThreadsV2BaseAndLogicalRoot(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	configuration := strictOneRootLocalConfig("project", "Project")
	path := filepath.Join(repository.Path, ".wtree.yml")
	if err := config.WriteProjectFile(path, configuration); err != nil {
		t.Fatal(err)
	}
	writePortableManifest(t, repository.Path, configuration.Project.ID, configuration.Project.Name, "root", map[string]config.PortableRepository{"root": portableRepository(".")})

	project, err := NewResolver().loadProject(context.Background(), path)
	if err != nil {
		t.Fatalf("loadProject() error = %v", err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	if project.BaseRepository != "root" || project.ConfigPath != canonicalPath || len(project.Repositories) != 1 || project.Repositories[0].SourcePath != canonicalRepository {
		t.Fatalf("loaded project = %#v", project)
	}
}

func TestLoadProjectRequiresCoLocatedPortableManifest(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	configuration := strictOneRootLocalConfig("project", "Project")
	path := filepath.Join(repository.Path, ".wtree.yml")
	if err := config.WriteProjectFile(path, configuration); err != nil {
		t.Fatal(err)
	}
	_, err := NewResolver().loadProject(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "portable manifest") {
		t.Fatalf("loadProject() error = %v, want missing portable manifest rejection", err)
	}
}

func TestLoadProjectRejectsPortableProjectIdentityMismatch(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	configuration := strictOneRootLocalConfig("project", "Project")
	path := filepath.Join(repository.Path, ".wtree.yml")
	if err := config.WriteProjectFile(path, configuration); err != nil {
		t.Fatal(err)
	}
	writePortableManifest(t, repository.Path, "other", configuration.Project.Name, "root", map[string]config.PortableRepository{"root": portableRepository(".")})

	_, err := NewResolver().loadProject(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "project ID") || !strings.Contains(err.Error(), "does not agree") {
		t.Fatalf("loadProject() error = %v, want portable project identity rejection", err)
	}
}

func TestResolverExplicitLogicalRootUsesRegisteredStateEvidence(t *testing.T) {
	forest := newRegisteredForest(t)
	if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", "default"), forest.workspaceState("default", forest.LogicalRoot)); err != nil {
		t.Fatal(err)
	}

	canonicalRoot, err := filepath.EvalSymlinks(forest.LogicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	project, err := forest.Resolver.ResolveProject(context.Background(), ResolveRequest{Path: forest.LogicalRoot, ProjectPath: forest.LogicalRoot, DataDir: forest.Data})
	if err != nil || project.ID != "forest" || project.LogicalRoot != canonicalRoot {
		t.Fatalf("ResolveProject() = %#v, %v", project, err)
	}
	resolution, err := forest.Resolver.Resolve(context.Background(), ResolveRequest{Path: forest.LogicalRoot, ProjectPath: forest.LogicalRoot, DataDir: forest.Data})
	if err != nil || resolution.Project.ID != "forest" || resolution.Workspace.RootPath != canonicalRoot || resolution.RepositoryID != "" {
		t.Fatalf("Resolve() = %#v, %v", resolution, err)
	}
}

func TestResolverRegisteredRootsPreserveExactWorkspaceEvidence(t *testing.T) {
	forest := newRegisteredForest(t)
	defaultState := forest.workspaceState("default", forest.LogicalRoot)
	if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", "default"), defaultState); err != nil {
		t.Fatal(err)
	}
	featureRoot := canonicalTestDirectory(t, t.TempDir())
	featureState := forest.addWorkspaceState(t, "feature", featureRoot)
	if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", "feature"), featureState); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		path        string
		projectPath string
		want        store.WorkspaceState
	}{
		{name: "implicit-project-logical-root", path: forest.LogicalRoot, want: defaultState},
		{name: "explicit-project-logical-root", path: forest.LogicalRoot, projectPath: forest.LogicalRoot, want: defaultState},
		{name: "implicit-non-default-workspace-root", path: featureRoot, want: featureState},
		{name: "explicit-non-default-workspace-root", path: featureRoot, projectPath: featureRoot, want: featureState},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := forest.Resolver.Resolve(context.Background(), ResolveRequest{Path: test.path, ProjectPath: test.projectPath, DataDir: forest.Data})
			if err != nil {
				t.Fatal(err)
			}
			if resolution.Project.ID != "forest" || resolution.Project.LogicalRoot != forest.LogicalRoot {
				t.Fatalf("resolved project = %#v", resolution.Project)
			}
			if resolution.Workspace.ID != test.want.ID || resolution.Workspace.Name != test.want.Name || resolution.Workspace.RootPath != test.want.Path {
				t.Fatalf("resolved workspace = %#v, want state %#v", resolution.Workspace, test.want)
			}
			for repositoryID, checkout := range test.want.Repositories {
				resolvedPath, err := resolution.Workspace.ResolveRepository(repositoryID)
				if err != nil || resolvedPath != checkout.ResolvedPath {
					t.Fatalf("ResolveRepository(%q) = %q, %v; want %q", repositoryID, resolvedPath, err, checkout.ResolvedPath)
				}
			}
			if resolution.RepositoryID != "" {
				t.Fatalf("repository ID = %q, want empty at logical root", resolution.RepositoryID)
			}
		})
	}
}

func TestResolverRegisteredRootRejectsAmbiguousValidatedWorkspaceStates(t *testing.T) {
	forest := newRegisteredForest(t)
	featureRoot := canonicalTestDirectory(t, t.TempDir())
	featureState := forest.addWorkspaceState(t, "feature", featureRoot)
	if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", "feature"), featureState); err != nil {
		t.Fatal(err)
	}
	duplicate := featureState
	duplicate.ID = "feature-copy"
	duplicate.Name = "feature-copy"
	if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", "feature-copy"), duplicate); err != nil {
		t.Fatal(err)
	}

	for _, projectPath := range []string{"", featureRoot} {
		_, err := forest.Resolver.Resolve(context.Background(), ResolveRequest{Path: featureRoot, ProjectPath: projectPath, DataDir: forest.Data})
		if !errors.Is(err, ErrAmbiguousProject) || !strings.Contains(err.Error(), "feature feature-copy") {
			t.Fatalf("Resolve(ProjectPath=%q) error = %v, want deterministic workspace-root ambiguity", projectPath, err)
		}
	}
}

func TestResolverExplicitLogicalRootRequiresValidatedWorkspaceEvidence(t *testing.T) {
	for _, test := range []struct {
		name  string
		state func(registeredForest, string) store.WorkspaceState
	}{
		{name: "empty-default", state: func(_ registeredForest, root string) store.WorkspaceState {
			return store.WorkspaceState{Version: store.Version, ID: "default", Name: "default", Path: root, Repositories: map[string]store.CheckoutState{}}
		}},
		{name: "partial", state: func(_ registeredForest, root string) store.WorkspaceState {
			return store.WorkspaceState{Version: store.Version, ID: "custom", Name: "custom", Path: root, Partial: true, MissingRepositoryIDs: []string{"api", "web"}, Repositories: map[string]store.CheckoutState{}}
		}},
		{name: "duplicate-missing-repository", state: func(_ registeredForest, root string) store.WorkspaceState {
			return store.WorkspaceState{Version: store.Version, ID: "custom", Name: "custom", Path: root, Partial: true, MissingRepositoryIDs: []string{"api", "api", "web"}, Repositories: map[string]store.CheckoutState{}}
		}},
		{name: "unknown-repository", state: func(forest registeredForest, root string) store.WorkspaceState {
			state := forest.workspaceState("custom", root)
			state.Repositories["unknown"] = state.Repositories["api"]
			return state
		}},
		{name: "wrong-mount", state: func(forest registeredForest, root string) store.WorkspaceState {
			state := forest.workspaceState("custom", root)
			checkout := state.Repositories["web"]
			checkout.Mount = "wrong"
			state.Repositories["web"] = checkout
			return state
		}},
		{name: "wrong-resolved-path", state: func(forest registeredForest, root string) store.WorkspaceState {
			state := forest.workspaceState("custom", root)
			checkout := state.Repositories["web"]
			checkout.ResolvedPath = forest.APIPath
			state.Repositories["web"] = checkout
			return state
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			forest := newRegisteredForest(t)
			arbitraryRoot := canonicalTestDirectory(t, t.TempDir())
			state := test.state(forest, arbitraryRoot)
			if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", state.ID), state); err != nil {
				t.Fatal(err)
			}

			_, err := forest.Resolver.ResolveProject(context.Background(), ResolveRequest{Path: arbitraryRoot, ProjectPath: arbitraryRoot, DataDir: forest.Data})
			if err == nil {
				t.Fatalf("ResolveProject() accepted stale %s workspace state as project-root evidence", test.name)
			}
		})
	}
	t.Run("mismatched-registered-identities", func(t *testing.T) {
		forest := newRegisteredForest(t)
		arbitraryRoot := canonicalTestDirectory(t, t.TempDir())
		if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", "custom"), forest.workspaceState("custom", arbitraryRoot)); err != nil {
			t.Fatal(err)
		}
		registryPath := filepath.Join(forest.Data, "registry.json")
		registry, err := store.ReadRegistry(registryPath)
		if err != nil {
			t.Fatal(err)
		}
		registry.Projects["forest"] = store.RegistryProject{Name: "Forest", ConfigPath: registry.Projects["forest"].ConfigPath, RepositoryIDs: map[string]string{"wrong": "api"}}
		if err := store.WriteRegistry(registryPath, registry); err != nil {
			t.Fatal(err)
		}

		_, err = forest.Resolver.ResolveProject(context.Background(), ResolveRequest{Path: arbitraryRoot, ProjectPath: arbitraryRoot, DataDir: forest.Data})
		if !errors.Is(err, ErrStaleRegistry) {
			t.Fatalf("ResolveProject() error = %v, want stale registered identity rejection", err)
		}
	})
}

func TestResolverForestRelocationReconcilesOnlyAfterReadOnlyResolution(t *testing.T) {
	forest := newRegisteredForest(t)
	if err := store.WriteWorkspace(WorkspaceStatePath(forest.Data, "forest", "default"), forest.workspaceState("default", forest.LogicalRoot)); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(forest.Data, "registry.json")
	registry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	registry.Projects["forest"] = store.RegistryProject{Name: "Forest", ConfigPath: filepath.Join(t.TempDir(), "missing", ".wtree.yml"), RepositoryIDs: registry.Projects["forest"].RepositoryIDs}
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}

	readOnly, err := forest.Resolver.ResolveReadOnly(context.Background(), ResolveRequest{Path: forest.APIPath, ProjectPath: forest.APIPath, DataDir: forest.Data})
	if err != nil || readOnly.Project.ID != "forest" {
		t.Fatalf("ResolveReadOnly() = %#v, %v", readOnly, err)
	}
	afterReadOnly, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterReadOnly) {
		t.Fatal("ResolveReadOnly() mutated registry")
	}

	resolved, err := forest.Resolver.Resolve(context.Background(), ResolveRequest{Path: forest.APIPath, ProjectPath: forest.APIPath, DataDir: forest.Data})
	if err != nil || resolved.Project.ID != "forest" {
		t.Fatalf("Resolve() = %#v, %v", resolved, err)
	}
	afterResolve, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterResolve.Projects["forest"].ConfigPath != filepath.Join(forest.APIPath, ".wtree.yml") || !sameRepositoryIDs(afterResolve.Projects["forest"].RepositoryIDs, repositoryIDs(resolved.Project)) {
		t.Fatalf("Resolve() reconciled registry = %#v", afterResolve.Projects["forest"])
	}
}

func TestLoadProjectRejectsBaseLogicalRootInversionMismatch(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	configuration := strictOneRootLocalConfig("project", "Project")
	configuration.LogicalRoot = ".."
	path := filepath.Join(repository.Path, ".wtree.yml")
	if err := config.WriteProjectFile(path, configuration); err != nil {
		t.Fatal(err)
	}
	writePortableManifest(t, repository.Path, configuration.Project.ID, configuration.Project.Name, "root", map[string]config.PortableRepository{"root": portableRepository(".")})

	_, err := NewResolver().loadProject(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "does not invert logical root") {
		t.Fatalf("loadProject() error = %v, want base inversion mismatch", err)
	}
}

func writePortableManifest(t *testing.T, directory, id, name, base string, repositories map[string]config.PortableRepository) {
	t.Helper()
	data, err := config.MarshalPortableManifest(config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: id, Name: name, BaseRepository: base}, Repositories: repositories})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "project.wtree.yml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func portableRepository(mount string) config.PortableRepository {
	return config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: "https://example.test/project.git"}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{"0123456789012345678901234567890123456789"}}, Mount: mount, DefaultBranch: "main"}
}

func strictOneRootLocalConfig(id, name string) config.ProjectConfig {
	return config.ProjectConfig{
		Version:      config.ProjectConfigVersion,
		Project:      config.Project{ID: id, Name: name, BaseRepository: "root"},
		LogicalRoot:  ".",
		Repositories: map[string]config.Repository{"root": {Source: ".", Parent: "", DefaultMount: ".", DefaultBranch: "main"}},
		Manifest:     config.ManifestMetadata{Path: "project.wtree.yml", Source: "/manifests/project.wtree.yml"},
	}
}

type registeredForest struct {
	Resolver             *Resolver
	Data                 string
	LogicalRoot, APIPath string
	WebPath, APIHead     string
	WebHead              string
}

func newRegisteredForest(t *testing.T) registeredForest {
	t.Helper()
	logicalRoot := t.TempDir()
	api, web := testutil.NewGitRepository(t), testutil.NewGitRepository(t)
	api.CommitFile("api.txt", "api\n", "api")
	web.CommitFile("web.txt", "web\n", "web")
	apiPath, webPath := filepath.Join(logicalRoot, "api"), filepath.Join(logicalRoot, "web")
	if err := os.Rename(api.Path, apiPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(web.Path, webPath); err != nil {
		t.Fatal(err)
	}
	logicalRoot = canonicalTestDirectory(t, logicalRoot)
	apiPath = canonicalTestDirectory(t, apiPath)
	webPath = canonicalTestDirectory(t, webPath)
	configuration := config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "forest", Name: "Forest", BaseRepository: "api"}, LogicalRoot: "..", Repositories: map[string]config.Repository{
		"api": {Source: "api", Parent: "", DefaultMount: "api", DefaultBranch: "main"},
		"web": {Source: "web", Parent: "", DefaultMount: "web", DefaultBranch: "main"},
	}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: "/manifests/project.wtree.yml"}}
	configPath := filepath.Join(apiPath, ".wtree.yml")
	if err := config.WriteProjectFile(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	writePortableManifest(t, apiPath, "forest", "Forest", "api", map[string]config.PortableRepository{"api": portableRepository("api"), "web": portableRepository("web")})
	resolver := NewResolver()
	apiIdentity, err := resolver.git.CommonGitDir(context.Background(), apiPath)
	if err != nil {
		t.Fatal(err)
	}
	webIdentity, err := resolver.git.CommonGitDir(context.Background(), webPath)
	if err != nil {
		t.Fatal(err)
	}
	apiHead, err := resolver.git.Head(context.Background(), apiPath)
	if err != nil {
		t.Fatal(err)
	}
	webHead, err := resolver.git.Head(context.Background(), webPath)
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"forest": {Name: "Forest", ConfigPath: configPath, RepositoryIDs: map[string]string{apiIdentity: "api", webIdentity: "web"}}}}); err != nil {
		t.Fatal(err)
	}
	return registeredForest{Resolver: resolver, Data: data, LogicalRoot: logicalRoot, APIPath: apiPath, WebPath: webPath, APIHead: apiHead, WebHead: webHead}
}

func (forest registeredForest) workspaceState(id, root string) store.WorkspaceState {
	return store.WorkspaceState{Version: store.Version, ID: id, Name: id, Path: root, Repositories: map[string]store.CheckoutState{
		"api": {Branch: "main", Head: forest.APIHead, Mount: "api", ResolvedPath: forest.APIPath},
		"web": {Branch: "main", Head: forest.WebHead, Mount: "web", ResolvedPath: forest.WebPath},
	}}
}

func (forest registeredForest) addWorkspaceState(t *testing.T, id, root string) store.WorkspaceState {
	t.Helper()
	apiPath, webPath := filepath.Join(root, "api"), filepath.Join(root, "web")
	testutil.GitRepository{Path: forest.APIPath}.Run(t, "worktree", "add", "-b", id, apiPath)
	testutil.GitRepository{Path: forest.WebPath}.Run(t, "worktree", "add", "-b", id, webPath)
	apiHead, err := forest.Resolver.git.Head(context.Background(), apiPath)
	if err != nil {
		t.Fatal(err)
	}
	webHead, err := forest.Resolver.git.Head(context.Background(), webPath)
	if err != nil {
		t.Fatal(err)
	}
	return store.WorkspaceState{Version: store.Version, ID: id, Name: id, Path: root, Repositories: map[string]store.CheckoutState{
		"api": {Branch: id, Head: apiHead, Mount: "api", ResolvedPath: apiPath},
		"web": {Branch: id, Head: webHead, Mount: "web", ResolvedPath: webPath},
	}}
}

func canonicalTestDirectory(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
