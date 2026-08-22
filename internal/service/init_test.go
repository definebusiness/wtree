package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
	"github.com/google/uuid"
	"time"
)

func TestInitUsesExplicitLogicalRootAndPersistsDiscoveryIgnores(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	canonicalRoot, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, Ignores: []string{"examples/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.ConfigPath, filepath.Join(canonicalRoot, ".wtree.yml"); got != want {
		t.Fatalf("config path = %q, want %q", got, want)
	}
	configurationBytes, err := os.ReadFile(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.LoadProject(configurationBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := configuration.Discovery.Ignore, []string{"examples/**"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("discovery ignores = %#v, want %#v", got, want)
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	common, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), canonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Projects[result.ProjectID].RepositoryIDs[common]; got != "root" {
		t.Fatalf("registry repository ID = %q, want root", got)
	}
	state, err := store.ReadWorkspace(filepath.Join(data, "state", result.ProjectID, "default.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Name != "default" || state.Path != canonicalRoot || state.Repositories["root"].ResolvedPath != canonicalRoot {
		t.Fatalf("default state = %#v", state)
	}
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, DryRun: true}); !errors.Is(err, service.ErrAlreadyInitialized) {
		t.Fatalf("dry-run repeat error = %v, want already initialized", err)
	}
}

func TestInitPreflightsDiscoversWritesConfigAndRegistersProject(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := service.NewInitializer()
	result, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, WorktreeRoot: filepath.Join(data, "worktrees")})
	if err != nil {
		t.Fatal(err)
	}
	if result.DryRun || len(result.Repositories) != 1 {
		t.Fatalf("result=%#v", result)
	}
	parsed, err := uuid.Parse(result.ProjectID)
	if err != nil || parsed.String() != result.ProjectID {
		t.Fatalf("project ID is not canonical UUID: %q", result.ProjectID)
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); err != nil {
		t.Fatal(err)
	}
	configurationBytes, err := os.ReadFile(filepath.Join(root.Path, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.LoadProject(configurationBytes)
	if err != nil || configuration.Version != config.ProjectConfigVersion || configuration.Project.BaseRepository != "root" || configuration.LogicalRoot != "." || configuration.Repositories["root"].DefaultBranch != "main" || configuration.Repositories["root"].Source != "." || configuration.Repositories["root"].DefaultMount != "." || configuration.Worktrees.Root != filepath.Join(data, "worktrees") || configuration.Manifest.Path != "project.wtree.yml" || configuration.Manifest.Source == "" {
		t.Fatalf("config=%#v %v", configuration, err)
	}
	if _, err := os.Stat(filepath.Join(data, "state", result.ProjectID, "default.json")); err != nil {
		t.Fatalf("default workspace missing: %v", err)
	}
	if _, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); !errors.Is(err, service.ErrAlreadyInitialized) {
		t.Fatalf("repeat error=%v", err)
	}
}

func TestInitDryRunDoesNotWrite(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, DryRun: true})
	if err != nil || !result.DryRun {
		t.Fatalf("result=%#v %v", result, err)
	}
	if result.PortableManifest.Version != config.PortableManifestVersion || result.PortableManifest.Project.BaseRepository != "root" {
		t.Fatalf("dry-run portable manifest = %#v", result.PortableManifest)
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("config exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root.Path, "project.wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run published portable manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("dry-run published registry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "state")); !os.IsNotExist(err) {
		t.Fatalf("dry-run published workspace state: %v", err)
	}
}

func TestInitPortableProjectIDIsIndependentOfCheckoutParent(t *testing.T) {
	source := testutil.NewPushedGitRepository(t)
	source.CommitFile("readme", "x\n", "initial")
	base := t.TempDir()
	checkouts := []string{filepath.Join(base, "one", "project"), filepath.Join(base, "two", "elsewhere", "project")}
	for _, checkout := range checkouts {
		if err := os.MkdirAll(filepath.Dir(checkout), 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("git", "clone", source.Path, checkout).CombinedOutput(); err != nil {
			t.Fatalf("clone fixture: %v\n%s", err, output)
		}
	}
	first, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: checkouts[0], DataDir: filepath.Join(base, "data-one"), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: checkouts[1], DataDir: filepath.Join(base, "data-two"), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectID != second.ProjectID {
		t.Fatalf("portable project IDs differ by checkout parent: %q != %q", first.ProjectID, second.ProjectID)
	}
}

func TestInitAfterUnregisterUsesDistinctIDWithoutOverwritingRetainedState(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	first, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(data, "state", first.ProjectID, "default.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	remover := service.NewProjectRegistryRemovalService()
	plan, err := remover.PlanUnregister(context.Background(), data, first.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remover.Unregister(context.Background(), data, plan); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first.ConfigPath); err != nil {
		t.Fatal(err)
	}
	second, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if second.ProjectID == first.ProjectID {
		t.Fatalf("re-init reused retained project ID %q", second.ProjectID)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("retained state was changed: %q, %v", after, err)
	}
}

func TestInitDryRunIsByteStableAndConvergesToPublication(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	request := service.InitRequest{Path: root.Path, DataDir: data, WorktreeRoot: filepath.Join(data, "worktrees"), DryRun: true}

	first, err := service.NewInitializer().Init(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.NewInitializer().Init(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("dry-run JSON differs:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
	firstConfig, err := config.MarshalProject(first.LocalConfig)
	if err != nil {
		t.Fatal(err)
	}
	secondConfig, err := config.MarshalProject(second.LocalConfig)
	if err != nil {
		t.Fatal(err)
	}
	firstManifest, err := config.MarshalPortableManifest(first.PortableManifest)
	if err != nil {
		t.Fatal(err)
	}
	secondManifest, err := config.MarshalPortableManifest(second.PortableManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstConfig, secondConfig) || !bytes.Equal(firstManifest, secondManifest) {
		t.Fatal("dry-run planned file bytes differ")
	}

	request.DryRun = false
	published, err := service.NewInitializer().Init(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if published.ProjectID != first.ProjectID {
		t.Fatalf("published project ID = %q, dry-run project ID = %q", published.ProjectID, first.ProjectID)
	}
	gotConfig, err := os.ReadFile(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	gotManifest, err := os.ReadFile(first.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotConfig, firstConfig) || !bytes.Equal(gotManifest, firstManifest) {
		t.Fatal("published files do not match the dry-run plan")
	}
}

func TestInitDryRunPreflightsExistingRegistryWithoutWriting(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "registry.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, DryRun: true}); err == nil {
		t.Fatal("dry-run accepted malformed registry")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote config: %v", err)
	}
}

func TestInitRejectsDuplicateCommonGitDirectoriesBeforeWriting(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	duplicate := filepath.Join(root.Path, "duplicate")
	if err := os.MkdirAll(duplicate, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(duplicate, ".git"), []byte("gitdir: ../.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()

	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil || !strings.Contains(err.Error(), "duplicate repository identity") {
		t.Fatalf("error = %v, want duplicate repository identity", err)
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("duplicate identity wrote config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("duplicate identity wrote registry: %v", err)
	}
}

func TestInitRegistrationFailureLeavesNoConfig(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := service.NewInitializerWithRegistryWriter(func(string, store.Registry) error { return errors.New("registry unavailable") })
	_, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err == nil {
		t.Fatal("registration failure accepted")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("config remained after rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("registry remained after failed publication: %v", err)
	}
}

func TestInitRollsBackRegistryWhenWriterPublishesThenFails(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := service.NewInitializerWithRegistryWriter(func(path string, registry store.Registry) error {
		if err := store.WriteRegistry(path, registry); err != nil {
			return err
		}
		return errors.New("registry durability unavailable after publish")
	})

	if _, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("post-publication registry failure accepted")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("registry failure left config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("registry failure left registry: %v", err)
	}
}

func TestInitStatePublicationFailureRollsBackConfigAndRegistry(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := service.NewInitializerWithWriters(store.WriteRegistry, func(string, store.WorkspaceState) error { return errors.New("state unavailable") })
	if _, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("state failure accepted")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("config left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("registry left: %v", err)
	}
}

func TestInitRollsBackPublishedDefaultStateWhenWriterFailsAfterPublish(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := service.NewInitializerWithWriters(store.WriteRegistry, func(path string, workspace store.WorkspaceState) error {
		if err := store.WriteWorkspace(path, workspace); err != nil {
			return err
		}
		return errors.New("workspace durability unavailable after publish")
	})

	if _, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("post-publication workspace failure accepted")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("workspace failure left config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("workspace failure left registry: %v", err)
	}
	stateFiles, err := filepath.Glob(filepath.Join(data, "state", "*", "default.json"))
	if err != nil || len(stateFiles) != 0 {
		t.Fatalf("workspace failure left default state: files=%v error=%v", stateFiles, err)
	}
}

func TestInitReportsCleanupFailureAfterPublishedWorkspaceFailure(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := service.NewInitializerWithPublishersAndRemover(
		store.WriteRegistry,
		func(path string, workspace store.WorkspaceState) error {
			if err := store.WriteWorkspace(path, workspace); err != nil {
				return err
			}
			return errors.New("workspace durability unavailable after publish")
		},
		os.Rename,
		func(path string) error {
			if filepath.Base(path) == ".wtree.yml" {
				return errors.New("remove project config denied")
			}
			return os.Remove(path)
		},
	)

	_, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "cleanup failed") || !strings.Contains(err.Error(), "remove project config") {
		t.Fatalf("error = %v, want reported cleanup failure", err)
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); err != nil {
		t.Fatalf("expected config retained after reported cleanup failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("cleanup failure left registry: %v", err)
	}
	stateFiles, err := filepath.Glob(filepath.Join(data, "state", "*", "default.json"))
	if err != nil || len(stateFiles) != 0 {
		t.Fatalf("cleanup failure left default state: files=%v error=%v", stateFiles, err)
	}
}

func TestInitConfigRenameFailureRollsBackRegistry(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := service.NewInitializerWithPublishers(store.WriteRegistry, store.WriteWorkspace, func(string, string) error { return errors.New("rename unavailable") })
	if _, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("rename failure accepted")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("config left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("registry left: %v", err)
	}
}

func TestInitRollbackPreservesAnExistingRegistryExactly(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	existing := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{
		"existing": {Name: "existing", ConfigPath: "/existing/.wtree.yml", RepositoryIDs: map[string]string{"/existing/.git": "root"}},
	}}
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), existing); err != nil {
		t.Fatal(err)
	}
	initializer := service.NewInitializerWithPublishers(store.WriteRegistry, store.WriteWorkspace, func(string, string) error { return errors.New("rename unavailable") })
	if _, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("rename failure accepted")
	}
	got, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, existing) {
		t.Fatalf("registry after rollback = %#v, want %#v", got, existing)
	}
}

func TestInitRespectsRegistryLockContention(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	handle, err := (lock.Manager{}).RegistryLock(context.Background(), data, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Unlock()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("init ignored registry lock")
	}
}
