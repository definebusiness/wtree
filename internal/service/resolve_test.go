package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/service"
	"github.com/marcel/wtree/internal/store"
	"github.com/marcel/wtree/internal/testutil"
)

func TestResolverFindsProjectAndDefaultWorkspaceFromNestedRepository(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("api.go", "package backend\n", "initial")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(backendPath, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}

	result, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: filepath.Join(backendPath, "internal"), DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Workspace.ID, "default"; got != want {
		t.Fatalf("workspace ID = %q, want %q", got, want)
	}
	if got, want := result.RepositoryID, "backend"; got != want {
		t.Fatalf("repository ID = %q, want %q", got, want)
	}
	canonicalBackend, err := filepath.EvalSymlinks(backendPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := result.Workspace.ResolveRepository("backend"); err != nil || got != canonicalBackend {
		t.Fatalf("ResolveRepository(backend) = %q, %v; want %q", got, err, canonicalBackend)
	}
}

func TestResolverExplicitProjectTakesPrecedenceOutsideGitContext(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}

	result, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: t.TempDir(), ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Workspace.ID, "default"; got != want {
		t.Fatalf("workspace ID = %q, want %q", got, want)
	}
	if result.RepositoryID != "" {
		t.Fatalf("repository ID = %q, want empty outside a checkout", result.RepositoryID)
	}
}

func TestResolverExplicitProjectIgnoresUnrelatedGitCheckout(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: project.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	unrelated := testutil.NewGitRepository(t)
	unrelated.CommitFile("other", "x\n", "initial")

	result, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: unrelated.Path, ProjectPath: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if result.RepositoryID != "" || result.Workspace.ID != "default" {
		t.Fatalf("explicit resolution = %#v, want selected project default workspace", result)
	}
}

func TestResolverFindsLocalConfigFromRootAndOrdinaryNestedDirectory(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	nested := filepath.Join(root.Path, "cmd", "wtree")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{root.Path, nested} {
		t.Run(path, func(t *testing.T) {
			result, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: path, DataDir: data})
			if err != nil {
				t.Fatal(err)
			}
			if result.RepositoryID != "root" || result.Workspace.ID != "default" {
				t.Fatalf("resolution = %#v", result)
			}
		})
	}
}

func TestResolverPrefersLocalConfigOverPersistedWorkspaceRegistry(t *testing.T) {
	registered := testutil.NewGitRepository(t)
	registered.CommitFile("readme", "x\n", "initial")
	localProject := testutil.NewGitRepository(t)
	localProject.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: registered.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	localResult, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: localProject.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}

	localConfigDirectory := filepath.Join(registered.Path, "nested", "configuration")
	if err := os.MkdirAll(localConfigDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	relativeSource, err := filepath.Rel(localConfigDirectory, localProject.Path)
	if err != nil {
		t.Fatal(err)
	}
	configuration := "version: 1\nproject:\n  id: " + localResult.ProjectID + "\n  name: local-project\nrepositories:\n  root:\n    source: " + filepath.ToSlash(relativeSource) + "\n    mount: .\n"
	if err := os.WriteFile(filepath.Join(localConfigDirectory, ".wtree.yml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: localConfigDirectory, DataDir: data})
	if !errors.Is(err, service.ErrStaleRegistry) {
		t.Fatalf("Resolve() error = %v, want the local project's Git identity error", err)
	}
}

func TestResolverFindsGeneratedCheckoutFromRegistryState(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	root.Run(t, "branch", "feature/login")
	checkout := filepath.Join(t.TempDir(), "feature-login")
	root.Run(t, "worktree", "add", checkout, "feature/login")
	canonicalCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), canonicalCheckout)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(service.WorkspaceStatePath(data, result.ProjectID, "feature-login"), store.WorkspaceState{
		ID: "feature-login", Name: "feature/login", Path: canonicalCheckout,
		Repositories: map[string]store.CheckoutState{"root": {Branch: "feature/login", Head: head, Mount: ".", ResolvedPath: canonicalCheckout}},
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: canonicalCheckout, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.Workspace.ID, "feature-login"; got != want {
		t.Fatalf("workspace ID = %q, want %q", got, want)
	}
	if got, err := resolved.Workspace.ResolveRepository("root"); err != nil || got != canonicalCheckout {
		t.Fatalf("ResolveRepository(root) = %q, %v; want %q", got, err, canonicalCheckout)
	}
}

func TestResolverRejectsNoContextAmbiguousAndStaleRegistry(t *testing.T) {
	t.Run("no context", func(t *testing.T) {
		_, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: t.TempDir(), DataDir: t.TempDir()})
		if err == nil || !errors.Is(err, service.ErrNoProjectContext) {
			t.Fatalf("Resolve() error = %v, want no project context", err)
		}
		for _, want := range []string{"wtree init", "wtree --project <path>"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("no-context error = %q, want guidance %q", err, want)
			}
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		root := testutil.NewGitRepository(t)
		root.CommitFile("readme", "x\n", "initial")
		data := t.TempDir()
		result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
		if err != nil {
			t.Fatal(err)
		}
		registryPath := filepath.Join(data, "registry.json")
		registry, err := store.ReadRegistry(registryPath)
		if err != nil {
			t.Fatal(err)
		}
		duplicate := registry.Projects[result.ProjectID]
		duplicate.Name = "other-project"
		registry.Projects["other-project"] = duplicate
		if err := store.WriteRegistry(registryPath, registry); err != nil {
			t.Fatal(err)
		}
		checkout := filepath.Join(t.TempDir(), "checkout")
		root.Run(t, "branch", "feature")
		root.Run(t, "worktree", "add", checkout, "feature")
		if _, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: checkout, DataDir: data}); err == nil || !errors.Is(err, service.ErrAmbiguousProject) {
			t.Fatalf("Resolve() error = %v, want ambiguity", err)
		}
	})

	t.Run("stale config path", func(t *testing.T) {
		root := testutil.NewGitRepository(t)
		root.CommitFile("readme", "x\n", "initial")
		data := t.TempDir()
		result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
		if err != nil {
			t.Fatal(err)
		}
		registryPath := filepath.Join(data, "registry.json")
		registry, err := store.ReadRegistry(registryPath)
		if err != nil {
			t.Fatal(err)
		}
		project := registry.Projects[result.ProjectID]
		project.ConfigPath = filepath.Join(t.TempDir(), "missing.yml")
		registry.Projects[result.ProjectID] = project
		if err := store.WriteRegistry(registryPath, registry); err != nil {
			t.Fatal(err)
		}
		checkout := filepath.Join(t.TempDir(), "checkout")
		root.Run(t, "branch", "feature")
		root.Run(t, "worktree", "add", checkout, "feature")
		if _, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: checkout, DataDir: data}); err == nil || !errors.Is(err, service.ErrStaleRegistry) {
			t.Fatalf("Resolve() error = %v, want stale registry", err)
		}
	})
}

func TestResolverRelocatesProjectByIDAndGitIdentity(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "renamed-project")
	if err := os.Rename(root.Path, moved); err != nil {
		t.Fatal(err)
	}
	canonicalMoved, err := filepath.EvalSymlinks(moved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: canonicalMoved, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	project := registry.Projects[result.ProjectID]
	if got, want := project.ConfigPath, filepath.Join(canonicalMoved, ".wtree.yml"); got != want {
		t.Fatalf("relocated config path = %q, want %q", got, want)
	}
	common, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), canonicalMoved)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := project.RepositoryIDs[common], "root"; got != want {
		t.Fatalf("relocated root identity = %q, want %q", got, want)
	}
}

func TestResolverDoesNotRewriteLiveRegistryForCloneWithSameProjectID(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	root.Run(t, "add", ".wtree.yml")
	root.Run(t, "commit", "-m", "track wtree config")
	clone := filepath.Join(t.TempDir(), "clone")
	root.Run(t, "clone", root.Path, clone)
	canonicalRoot, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: clone, ProjectPath: clone, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := registry.Projects[result.ProjectID].ConfigPath, filepath.Join(canonicalRoot, ".wtree.yml"); got != want {
		t.Fatalf("registry config path = %q, want live project config %q", got, want)
	}
}

func TestResolverRejectsMalformedDefaultWorkspaceState(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, result.ProjectID, "default")
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state.ID, state.Name = "other", "other"
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}

	_, err = service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: t.TempDir(), ProjectPath: root.Path, DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "default workspace state") {
		t.Fatalf("Resolve() error = %v, want malformed default state rejection", err)
	}
}

func TestResolverUsesRegistryProjectForTrackedConfigInGeneratedWorkspace(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	api := testutil.NewGitRepository(t)
	api.CommitFile("api.go", "package api\n", "initial")
	apiSource := filepath.Join(root.Path, "api")
	if err := os.Rename(api.Path, apiSource); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	root.Run(t, "add", ".wtree.yml")
	root.Run(t, "commit", "-m", "track wtree config")
	root.Run(t, "branch", "feature")
	apiSourceRepository := testutil.GitRepository{Path: apiSource}
	apiSourceRepository.Run(t, "branch", "feature")
	generatedRoot := filepath.Join(t.TempDir(), "feature")
	root.Run(t, "worktree", "add", generatedRoot, "feature")
	generatedAPI := filepath.Join(generatedRoot, "services", "api")
	if err := os.MkdirAll(filepath.Dir(generatedAPI), 0o755); err != nil {
		t.Fatal(err)
	}
	apiSourceRepository.Run(t, "worktree", "add", generatedAPI, "feature")
	canonicalRoot, err := filepath.EvalSymlinks(generatedRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalAPI, err := filepath.EvalSymlinks(generatedAPI)
	if err != nil {
		t.Fatal(err)
	}
	rootHead, err := gitadapter.NewAdapter("git").Head(context.Background(), canonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	apiHead, err := gitadapter.NewAdapter("git").Head(context.Background(), canonicalAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(service.WorkspaceStatePath(data, result.ProjectID, "feature"), store.WorkspaceState{
		ID: "feature", Name: "feature", Path: canonicalRoot,
		Repositories: map[string]store.CheckoutState{
			"root": {Branch: "feature", Head: rootHead, Mount: ".", ResolvedPath: canonicalRoot},
			"api":  {Branch: "feature", Head: apiHead, Mount: "services/api", ResolvedPath: canonicalAPI},
		},
	}); err != nil {
		t.Fatal(err)
	}
	registryBefore, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: canonicalAPI, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolved.Workspace.ResolveRepository("api"); err != nil || got != canonicalAPI {
		t.Fatalf("ResolveRepository(api) = %q, %v; want persisted mount %q", got, err, canonicalAPI)
	}
	registryAfter, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := registryAfter.Projects[result.ProjectID].ConfigPath, registryBefore.Projects[result.ProjectID].ConfigPath; got != want {
		t.Fatalf("registry config path = %q, want source project config %q", got, want)
	}
}

func TestResolverUsesPersistedRenamedMount(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("api.go", "package backend\n", "initial")
	renamedBackend := filepath.Join(root.Path, "services", "api")
	if err := os.MkdirAll(filepath.Dir(renamedBackend), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backend.Path, renamedBackend); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBackend, err := filepath.EvalSymlinks(renamedBackend)
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, result.ProjectID, "default")
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state.Path = canonicalRoot
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}

	resolved, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: canonicalBackend, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolved.Workspace.ResolveRepository("api"); err != nil || got != canonicalBackend {
		t.Fatalf("ResolveRepository(api) = %q, %v; want %q", got, err, canonicalBackend)
	}
}
