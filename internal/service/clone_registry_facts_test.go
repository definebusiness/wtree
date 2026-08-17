package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/store"
)

type staticCloneRegistryFactsReader struct {
	mu       sync.Mutex
	snapshot CloneRegistrySnapshot
	err      error
	calls    int
}

func (reader *staticCloneRegistryFactsReader) Read(string) (CloneRegistrySnapshot, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.calls++
	return reader.snapshot, reader.err
}

func TestClonePlanUsesOnlyInjectedCompleteRegistryFacts(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "state", "poison"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "registry.json"), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "state", "poison", "bad.json"), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootURL := filepath.Join(base, "root.git")
	manifest := config.PortableManifest{Version: 1, Project: config.PortableProject{ID: "project-1", Name: "safe"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: rootURL}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main"}}}
	data, _ := config.MarshalPortableManifest(manifest)
	source := writeClonePlanManifest(t, base, data)
	reader := &staticCloneRegistryFactsReader{snapshot: CloneRegistrySnapshot{Registry: store.Registry{Version: 1, Projects: map[string]store.RegistryProject{}}, RegistrySHA256: "absent", GenerationSHA256: strings.Repeat("a", 64)}}
	remote := &clonePlanRemote{commits: map[string]string{rootURL + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote, RegistryFacts: reader}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: dataDir})
	if err != nil {
		t.Fatalf("planner bypassed injected registry facts: %v", err)
	}
	if plan.Destination.RegistryGenerationSHA256 != strings.Repeat("a", 64) || reader.calls != 1 {
		t.Fatalf("registry generation/calls = %q/%d", plan.Destination.RegistryGenerationSHA256, reader.calls)
	}
}

func TestCloneRegistryAliasReportsEveryOwnerDeterministically(t *testing.T) {
	base := t.TempDir()
	destination := filepath.Join(base, "clone")
	snapshot := CloneRegistrySnapshot{
		Registry: store.Registry{Version: 1, Projects: map[string]store.RegistryProject{
			"z-project": {ConfigPath: filepath.Join(destination, ".wtree.yml")},
			"a-project": {ConfigPath: filepath.Join(destination, ".wtree.yml")},
		}}, RegistrySHA256: strings.Repeat("b", 64), GenerationSHA256: strings.Repeat("c", 64),
		Workspaces: []CloneWorkspaceFact{
			{ProjectID: "z-project", FileName: "two.json", State: store.WorkspaceState{Version: 1, ID: "two", Path: destination}},
			{ProjectID: "a-project", FileName: "one.json", State: store.WorkspaceState{Version: 1, ID: "one", Path: destination}},
		},
	}
	var expected string
	for iteration := 0; iteration < 100; iteration++ {
		err := validateCloneRegistryFacts("new-project", destination, snapshot, osCloneFileSystemFacts{})
		if err == nil {
			t.Fatal("duplicate registry owners accepted")
		}
		if iteration == 0 {
			expected = err.Error()
		} else if err.Error() != expected {
			t.Fatalf("duplicate owner diagnostic changed:\n%s\n%s", expected, err)
		}
	}
	for _, owner := range []string{"registered project a-project", "registered project z-project", "registered workspace a-project/one", "registered workspace z-project/two"} {
		if !strings.Contains(expected, owner) {
			t.Fatalf("duplicate owner diagnostic %q omitted %q", expected, owner)
		}
	}
}

func TestCloneRegistryFactsRejectConcurrentGeneration(t *testing.T) {
	dataDir := t.TempDir()
	registryPath := filepath.Join(dataDir, "registry.json")
	registry := store.Registry{Version: 1, Projects: map[string]store.RegistryProject{"project": {Name: "project", ConfigPath: "/project/.wtree.yml"}}}
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(dataDir, "project", "default")
	if err := store.WriteWorkspace(statePath, store.WorkspaceState{Version: 1, ID: "default", Name: "default", Path: "/project", Repositories: map[string]store.CheckoutState{}}); err != nil {
		t.Fatal(err)
	}
	reader := stableCloneRegistryFactsReader{fs: osCloneRegistryFileSystem{}, beforeRevalidate: func() {
		registry.Projects["racing"] = store.RegistryProject{Name: "racing", ConfigPath: "/racing/.wtree.yml"}
		if err := store.WriteRegistry(registryPath, registry); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := reader.Read(dataDir); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("concurrent registry generation error = %v", err)
	}
}

func TestCloneRegistryFactsRejectConcurrentWorkspaceGeneration(t *testing.T) {
	dataDir := t.TempDir()
	registry := store.Registry{Version: 1, Projects: map[string]store.RegistryProject{"project": {Name: "project", ConfigPath: "/project/.wtree.yml"}}}
	if err := store.WriteRegistry(filepath.Join(dataDir, "registry.json"), registry); err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(dataDir, "project", "default")
	state := store.WorkspaceState{Version: 1, ID: "default", Name: "default", Path: "/project", Repositories: map[string]store.CheckoutState{}}
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}
	reader := stableCloneRegistryFactsReader{fs: osCloneRegistryFileSystem{}, beforeRevalidate: func() {
		state.Path = "/project-moved"
		if err := store.WriteWorkspace(statePath, state); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := reader.Read(dataDir); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("concurrent workspace generation error = %v", err)
	}
}

func TestCloneRegistryFactsReaderPropagatesInjectedFailure(t *testing.T) {
	reader := &staticCloneRegistryFactsReader{err: errors.New("registry fact seam unavailable")}
	if _, err := reader.Read("/unused"); err == nil {
		t.Fatal("injected registry fact failure accepted")
	}
}
