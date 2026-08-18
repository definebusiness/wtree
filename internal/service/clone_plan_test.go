package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

const (
	clonePlanRootCommit  = "0123456789abcdef0123456789abcdef01234567"
	clonePlanChildCommit = "89abcdef0123456789abcdef0123456789abcdef"
)

type clonePlanRemote struct {
	mu      sync.Mutex
	commits map[string]string
	errors  map[string]error
	calls   []string
}

func (remote *clonePlanRemote) AdvertisedCommit(_ context.Context, url, ref string) (string, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	key := url + "\x00" + ref
	remote.calls = append(remote.calls, key)
	if err := remote.errors[key]; err != nil {
		return "", err
	}
	if commit := remote.commits[key]; commit != "" {
		return commit, nil
	}
	return "", errors.New("not advertised")
}

func clonePlanManifest(t *testing.T, rootURL, childURL string) []byte {
	t.Helper()
	manifest := config.PortableManifest{
		Version: config.PortableManifestVersion,
		Project: config.PortableProject{ID: "project-1", Name: "Project space 世界", BaseRepository: "root"},
		Repositories: map[string]config.PortableRepository{
			"root": {
				Clone:    config.CloneSource{Remote: "upstream", URL: rootURL},
				Upstream: config.Upstream{Branch: "local-main", Remote: "upstream", Merge: "refs/heads/published-main"},
				Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}},
				Mount:    ".", DefaultBranch: "local-main",
			},
			"api": {
				Clone:    config.CloneSource{Remote: "source", URL: childURL},
				Upstream: config.Upstream{Branch: "api-local", Remote: "source", Merge: "refs/heads/api-published"},
				Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanChildCommit}},
				Parent:   "root", Mount: "backend/API 世界", DefaultBranch: "api-local",
			},
		},
	}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeClonePlanManifest(t *testing.T, directory string, data []byte) string {
	t.Helper()
	path := filepath.Join(directory, "project.wtree.yml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newClonePlanRemote(rootURL, childURL string) *clonePlanRemote {
	return &clonePlanRemote{commits: map[string]string{
		rootURL + "\x00refs/heads/published-main": clonePlanRootCommit,
		childURL + "\x00refs/heads/api-published": clonePlanChildCommit,
	}, errors: map[string]error{}}
}

func TestClonePlanExplicitDestinationStableParentFirstJSONAndNoMutation(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	manifest := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
	remote := newClonePlanRemote(rootURL, childURL)
	destination := filepath.Join(base, "clone space 世界")
	dataDir := filepath.Join(base, "data-does-not-exist")
	before := mustDirectorySnapshot(t, base)
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote})
	plan, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: manifest, Destination: destination, CWD: base, DataDir: dataDir, WorktreeRoot: "worktrees"})
	if err != nil {
		t.Fatal(err)
	}
	after := mustDirectorySnapshot(t, base)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("planning mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
	}
	canonicalBase, _ := filepath.EvalSymlinks(base)
	canonicalDestination := filepath.Join(canonicalBase, "clone space 世界")
	if plan.Version != 1 || plan.Operation != "clone" || plan.Source.Value != manifest || plan.Destination.Path != canonicalDestination || plan.Project.ID != "project-1" {
		t.Fatalf("plan header = %#v", plan)
	}
	if got := []string{plan.Repositories[0].ID, plan.Repositories[1].ID}; !reflect.DeepEqual(got, []string{"root", "api"}) {
		t.Fatalf("repository order = %v", got)
	}
	if got := plan.Repositories[0]; got.LocalBranch != "local-main" || got.RemoteRef != "refs/heads/published-main" || got.AdvertisedCommit != clonePlanRootCommit {
		t.Fatalf("root plan = %#v", got)
	}
	if got := plan.Repositories[1]; got.Path != filepath.Join(canonicalDestination, "backend", "API 世界") || !got.Verification.CommittedParentIgnore {
		t.Fatalf("child plan = %#v", got)
	}
	first, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := plan.JSON()
	if !reflect.DeepEqual(first, second) || !json.Valid(first) || !strings.Contains(string(first), `"operation": "clone"`) {
		t.Fatalf("unstable/invalid plan JSON: %s", first)
	}
	var decoded ClonePlan
	if err := json.Unmarshal(first, &decoded); err != nil || decoded.Version != 1 || len(decoded.Repositories) != 2 || len(decoded.Actions) == 0 {
		t.Fatalf("decoded plan = %#v, %v", decoded, err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded stable JSON contract is invalid: %v", err)
	}
	mutated := decoded
	mutated.Repositories = append([]ClonePlanRepository(nil), decoded.Repositories...)
	mutated.Repositories[1].Path = filepath.Join(base, "escape")
	if err := mutated.Validate(); err == nil {
		t.Fatal("mutated clone plan bypassed validation")
	}
	if got := string(plan.ManifestBytes()); got != string(clonePlanManifest(t, rootURL, childURL)) {
		t.Fatal("plan did not retain exact manifest bytes")
	}
}

func TestClonePlanJSONRoundTripRejectsTamperedBaseRepository(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded ClonePlan
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, baseRepository := range []string{"api", "unknown"} {
		t.Run(baseRepository, func(t *testing.T) {
			mutated := decoded
			mutated.Project.BaseRepository = baseRepository
			if err := mutated.Validate(); err == nil {
				t.Fatalf("tampered base repository %q bypassed validation", baseRepository)
			}
		})
	}
}

func TestClonePlanRejectsLogicalRootFormatBeforeDestinationRegistryOrRemote(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	valid := string(clonePlanManifest(t, rootURL, childURL))
	fixtures := map[string]string{
		"version one":   strings.Replace(valid, "version: 2", "version: 1", 1),
		"missing base":  strings.Replace(valid, "base_repository: root", "base_repository: \"\"", 1),
		"unknown base":  strings.Replace(valid, "base_repository: root", "base_repository: unknown", 1),
		"non-root base": strings.Replace(valid, "base_repository: root", "base_repository: api", 1),
	}
	for name, data := range fixtures {
		t.Run(name, func(t *testing.T) {
			source := writeClonePlanManifest(t, base, []byte(data))
			destination := filepath.Join(base, "clone-"+strings.ReplaceAll(name, " ", "-"))
			before := mustDirectorySnapshot(t, base)
			remote := newClonePlanRemote(rootURL, childURL)
			registry := &staticCloneRegistryFactsReader{}
			_, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote, RegistryFacts: registry}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: destination, CWD: base, DataDir: filepath.Join(base, "data")})
			if err == nil || !strings.Contains(err.Error(), "logical-root manifest format") {
				t.Fatalf("logical-root format error = %v", err)
			}
			remote.mu.Lock()
			remoteCalls := len(remote.calls)
			remote.mu.Unlock()
			registry.mu.Lock()
			registryCalls := registry.calls
			registry.mu.Unlock()
			if remoteCalls != 0 || registryCalls != 0 {
				t.Fatalf("invalid manifest reached remote/registry: remote=%d registry=%d", remoteCalls, registryCalls)
			}
			if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
				t.Fatalf("invalid manifest created destination: %v", statErr)
			}
			if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid manifest mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

func TestClonePlanLocalAndHTTPManifestSourcesYieldEquivalentValidatedPlan(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	data := clonePlanManifest(t, rootURL, childURL)
	localSource := writeClonePlanManifest(t, base, data)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/octet-stream")
		_, _ = response.Write(data)
	}))
	defer server.Close()
	remote := newClonePlanRemote(rootURL, childURL)
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote})
	request := ClonePlanRequest{Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")}
	request.ManifestSource = localSource
	localPlan, err := planner.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.ManifestSource = server.URL
	httpPlan, err := planner.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if localPlan.Source.Kind != ManifestSourceLocal || httpPlan.Source.Kind != ManifestSourceHTTP || httpPlan.Source.Value != server.URL {
		t.Fatalf("source provenance local=%#v http=%#v", localPlan.Source, httpPlan.Source)
	}
	if !reflect.DeepEqual(localPlan.Project, httpPlan.Project) || !reflect.DeepEqual(localPlan.Repositories, httpPlan.Repositories) || !reflect.DeepEqual(localPlan.Actions, httpPlan.Actions) || localPlan.Source.SHA256 != httpPlan.Source.SHA256 {
		t.Fatal("equivalent local and HTTP manifests yielded different clone decisions")
	}
}

func TestClonePlanDefaultDestinationAndDestinationSafety(t *testing.T) {
	base := t.TempDir()
	rootURL := filepath.Join(base, "root.git")
	data := clonePlanManifest(t, rootURL, filepath.Join(base, "api.git"))
	manifest, err := config.LoadPortableManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	delete(manifest.Repositories, "api")
	manifest.Project.Name = "safe-project"
	data, _ = config.MarshalPortableManifest(manifest)
	source := writeClonePlanManifest(t, base, data)
	remote := &clonePlanRemote{commits: map[string]string{rootURL + "\x00refs/heads/published-main": clonePlanRootCommit}, errors: map[string]error{}}
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote})
	plan, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, CWD: base, DataDir: filepath.Join(base, "data")})
	canonicalBase, _ := filepath.EvalSymlinks(base)
	if err != nil || plan.Destination.Path != filepath.Join(canonicalBase, "safe-project") {
		t.Fatalf("default destination = %q, %v", plan.Destination.Path, err)
	}

	unsafe := manifest
	unsafe.Project.Name = "../unsafe"
	unsafeData, _ := config.MarshalPortableManifest(unsafe)
	// Marshal rejects the project name only at destination policy, not schema.
	if unsafeData == nil {
		unsafeData = []byte(strings.Replace(string(data), "safe-project", "../unsafe", 1))
	}
	unsafeSource := filepath.Join(base, "unsafe.yml")
	if err := os.WriteFile(unsafeSource, unsafeData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: unsafeSource, CWD: base}); err == nil || !strings.Contains(err.Error(), "explicit destination") {
		t.Fatalf("unsafe default error = %v", err)
	}

	existing := filepath.Join(base, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: existing, CWD: base}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination error = %v", err)
	}
	nonDirectory := filepath.Join(base, "file-parent")
	if err := os.WriteFile(nonDirectory, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(nonDirectory, "child"), CWD: base}); err == nil {
		t.Fatal("non-directory parent accepted")
	}
	unwritable := filepath.Join(base, "unwritable")
	if err := os.Mkdir(unwritable, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(unwritable, "child"), CWD: base}); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("unwritable parent error = %v", err)
	}
	realParent := filepath.Join(base, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkParent := filepath.Join(base, "symlink-parent")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(symlinkParent, "child"), CWD: base}); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink parent error = %v", err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: string(filepath.Separator), CWD: base}); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("broad destination error = %v", err)
	}
}

func TestClonePlanRejectsNestedSymlinkAncestorBeforeAnyRemote(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real", "nested")
	if err := os.MkdirAll(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Fatal(err)
	}
	rootURL := filepath.Join(base, "root.git")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "project-1", Name: "safe", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: rootURL}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main"}}}
	data, _ := config.MarshalPortableManifest(manifest)
	source := writeClonePlanManifest(t, base, data)
	remote := &clonePlanRemote{commits: map[string]string{rootURL + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
	_, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(link, "nested", "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("nested ancestor error = %v", err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.calls) != 0 {
		t.Fatal("unsafe ancestor contacted a Git remote")
	}
}

func TestClonePlanCapturesEverySafeAncestorForRevalidation(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "level one", "世界")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	rootURL := filepath.Join(base, "root.git")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "project-1", Name: "safe", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: rootURL}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main"}}}
	data, _ := config.MarshalPortableManifest(manifest)
	source := writeClonePlanManifest(t, base, data)
	remote := &clonePlanRemote{commits: map[string]string{rootURL + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(parent, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	canonicalBase, _ := filepath.EvalSymlinks(base)
	want := []string{canonicalBase, filepath.Join(canonicalBase, "level one"), filepath.Join(canonicalBase, "level one", "世界")}
	var got []string
	for _, fact := range plan.Destination.AncestorFacts {
		got = append(got, fact.Path)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestor facts = %v, want %v", got, want)
	}
}

func TestClonePlanRegistryProjectAndPathAliases(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rootURL := filepath.Join(base, "root.git")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "project-1", Name: "safe", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: rootURL}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main"}}}
	data, _ := config.MarshalPortableManifest(manifest)
	source := writeClonePlanManifest(t, base, data)
	remote := &clonePlanRemote{commits: map[string]string{rootURL + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote})
	destination := filepath.Join(base, "missing-destination")
	registry := store.Registry{Version: 1, Projects: map[string]store.RegistryProject{"other": {Name: "other", ConfigPath: filepath.Join(destination, ".wtree.yml")}}}
	if err := store.WriteRegistry(filepath.Join(dataDir, "registry.json"), registry); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: destination, CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "aliases registered project") {
		t.Fatalf("registered path alias error = %v", err)
	}
	registry.Projects = map[string]store.RegistryProject{"project-1": {Name: "prior", ConfigPath: filepath.Join(base, "prior", ".wtree.yml")}}
	if err := store.WriteRegistry(filepath.Join(dataDir, "registry.json"), registry); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: destination, CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("project ID collision error = %v", err)
	}

	workspaceDestination := filepath.Join(base, "missing-workspace")
	registry.Projects = map[string]store.RegistryProject{"other": {Name: "other", ConfigPath: filepath.Join(base, "other", ".wtree.yml")}}
	if err := store.WriteRegistry(filepath.Join(dataDir, "registry.json"), registry); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(WorkspaceStateDirectory(dataDir, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(WorkspaceStatePath(dataDir, "other", "default"), store.WorkspaceState{Version: 1, ID: "default", Name: "default", Path: workspaceDestination, Repositories: map[string]store.CheckoutState{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: workspaceDestination, CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "aliases registered workspace") {
		t.Fatalf("registered workspace alias error = %v", err)
	}
}

func TestClonePlanRejectsMalformedRegistryBeforeRemoteAndWithoutMutation(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "registry.json"), []byte(`{"version":1,"projects":{},"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
	remote := newClonePlanRemote(rootURL, childURL)
	before := mustDirectorySnapshot(t, base)
	_, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).DryRun(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: dataDir})
	if err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("malformed registry error = %v", err)
	}
	remote.mu.Lock()
	callCount := len(remote.calls)
	remote.mu.Unlock()
	if callCount != 0 {
		t.Fatalf("malformed registry contacted %d remotes", callCount)
	}
	if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed dry run mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestClonePlanInvalidRepositoryCredentialIsNeverReported(t *testing.T) {
	base := t.TempDir()
	secret := "repository-secret-canary"
	data := []byte("version: 1\nproject:\n  id: project-1\n  name: safe\nrepositories:\n  root:\n    clone:\n      remote: origin\n      url: https://user:" + secret + "@example.invalid/repo.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - " + clonePlanRootCommit + "\n    parent: \"\"\n    mount: .\n    default_branch: main\n")
	source := writeClonePlanManifest(t, base, data)
	remote := &clonePlanRemote{commits: map[string]string{}, errors: map[string]error{}}
	_, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential-bearing repository error = %v", err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.calls) != 0 {
		t.Fatal("invalid manifest contacted a Git remote")
	}
}

func TestClonePlanQueriesEveryRemoteAndRedactsFailures(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
	secret := "remote-secret-canary"
	remote := newClonePlanRemote(rootURL, childURL)
	remote.errors[rootURL+"\x00refs/heads/published-main"] = errors.New("transport https://user:" + secret + "@example.invalid/root failed " + strings.Repeat("x", 20000))
	remote.errors[childURL+"\x00refs/heads/api-published"] = errors.New("branch missing")
	_, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	remote.mu.Lock()
	calls := append([]string(nil), remote.calls...)
	remote.mu.Unlock()
	if err == nil || len(calls) != 2 || !strings.Contains(err.Error(), `repository "api"`) || !strings.Contains(err.Error(), `repository "root"`) || strings.Contains(err.Error(), secret) || len(err.Error()) > 9000 {
		t.Fatalf("remote accumulated error = %v; calls=%v", err, remote.calls)
	}
	if _, statErr := os.Stat(filepath.Join(base, "clone")); !os.IsNotExist(statErr) {
		t.Fatalf("failed plan created destination: %v", statErr)
	}
}

func TestClonePlanManyRemoteFailuresAreTotallyBoundedAndDeterministic(t *testing.T) {
	base := t.TempDir()
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "many-remotes", Name: "many-remotes", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{}}
	remote := &clonePlanRemote{commits: map[string]string{}, errors: map[string]error{}}
	secret := "many-remote-secret-canary"
	for index := 0; index < 81; index++ {
		id := "root"
		parent, mount := "", "."
		if index > 0 {
			id = fmt.Sprintf("child-%03d", index)
			parent, mount = "root", id
		}
		url := filepath.Join(base, id+".git")
		ref := "refs/heads/main"
		manifest.Repositories[id] = config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: url}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: ref}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Parent: parent, Mount: mount, DefaultBranch: "main"}
		remote.errors[url+"\x00"+ref] = errors.New("transport https://user:" + secret + "@example.invalid/" + id + " " + strings.Repeat("hostile", 2000))
	}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	source := writeClonePlanManifest(t, base, data)
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote})
	request := ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")}
	_, firstErr := planner.Plan(context.Background(), request)
	remote.mu.Lock()
	firstCalls := len(remote.calls)
	remote.calls = nil
	remote.mu.Unlock()
	_, secondErr := planner.Plan(context.Background(), request)
	remote.mu.Lock()
	secondCalls := len(remote.calls)
	remote.mu.Unlock()
	if firstErr == nil || secondErr == nil || firstErr.Error() != secondErr.Error() {
		t.Fatalf("many-remote errors are absent or unstable:\n%v\n%v", firstErr, secondErr)
	}
	if firstCalls != 81 || secondCalls != 81 {
		t.Fatalf("queried remotes = %d/%d, want 81/81", firstCalls, secondCalls)
	}
	message := firstErr.Error()
	if len(message) > 17000 || strings.Contains(message, secret) || !strings.Contains(message, "additional repository remote errors omitted") || !strings.Contains(message, `repository "child-001"`) || !strings.Contains(message, "all 81 remotes were queried") {
		t.Fatalf("bounded aggregate error length=%d: %s", len(message), message)
	}
}

func TestClonePlanThreeLevelOrderIsParentFirstAndLexicallyStable(t *testing.T) {
	base := t.TempDir()
	urls := map[string]string{"root": filepath.Join(base, "root.git"), "api": filepath.Join(base, "api.git"), "shared": filepath.Join(base, "shared.git")}
	manifest, err := config.LoadPortableManifest(clonePlanManifest(t, urls["root"], urls["api"]))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Repositories["shared"] = config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: urls["shared"]}, Upstream: config.Upstream{Branch: "shared-local", Remote: "origin", Merge: "refs/heads/shared-published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{"abcdef0123456789abcdef0123456789abcdef01"}}, Parent: "api", Mount: "libraries/shared", DefaultBranch: "shared-local"}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	source := writeClonePlanManifest(t, base, data)
	remote := newClonePlanRemote(urls["root"], urls["api"])
	remote.commits[urls["shared"]+"\x00refs/heads/shared-published"] = "abcdef0123456789abcdef0123456789abcdef01"
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, repository := range plan.Repositories {
		ids = append(ids, repository.ID)
	}
	if !reflect.DeepEqual(ids, []string{"root", "api", "shared"}) {
		t.Fatalf("three-level order = %v", ids)
	}
	var ignoreActions []ClonePlanAction
	for _, action := range plan.Actions {
		if action.Action == "verify_parent_ignore" {
			ignoreActions = append(ignoreActions, action)
		}
	}
	if len(ignoreActions) != 2 {
		t.Fatalf("ignore actions = %#v", ignoreActions)
	}
	api := ignoreActions[0]
	if api.RepositoryID != "api" || api.ParentRepositoryID != "root" || api.ParentPath != plan.Repositories[0].Path || api.ParentCommit != clonePlanRootCommit || api.ExactCommit != clonePlanChildCommit || api.ChildMount != "backend/API 世界" || api.IgnoreRuleSubject != "backend/API 世界" || !reflect.DeepEqual(api.ChildInitialCommits, []string{clonePlanChildCommit}) {
		t.Fatalf("self-contained API ignore action = %#v", api)
	}
	shared := ignoreActions[1]
	if shared.ParentRepositoryID != "api" || shared.ParentCommit != clonePlanChildCommit || shared.ChildMount != "libraries/shared" {
		t.Fatalf("self-contained shared ignore action = %#v", shared)
	}
	mutated := plan
	mutated.Actions = append([]ClonePlanAction(nil), plan.Actions...)
	for index := range mutated.Actions {
		if mutated.Actions[index].Action == "verify_parent_ignore" {
			mutated.Actions[index].ParentCommit = clonePlanRootCommit
			if mutated.Actions[index].ParentRepositoryID == "api" {
				break
			}
		}
	}
	if err := mutated.Validate(); err == nil {
		t.Fatal("mutated parent-ignore action bypassed plan validation")
	}
}

func TestClonePlanCapturesFactsForLaterRevalidationDuringConcurrentChange(t *testing.T) {
	base := t.TempDir()
	rootURL := filepath.Join(base, "root.git")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "project-1", Name: "safe", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: rootURL}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main"}}}
	data, _ := config.MarshalPortableManifest(manifest)
	source := writeClonePlanManifest(t, base, data)
	destination := filepath.Join(base, "racing-destination")
	remote := &clonePlanRemote{commits: map[string]string{rootURL + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote, BeforeRemoteRead: func(string) {
		if err := os.Mkdir(destination, 0o700); err != nil && !os.IsExist(err) {
			t.Error(err)
		}
	}})
	plan, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: destination, CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Destination.DestinationDidNotExist || plan.Destination.ParentModTime == "" || plan.Destination.RegistrySHA256 != "absent" {
		t.Fatalf("missing later-revalidation facts: %#v", plan.Destination)
	}
}

func TestCloneDryRunConcurrentPlannersRemainReadOnly(t *testing.T) {
	base := t.TempDir()
	rootURL := filepath.Join(base, "root.git")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "project-1", Name: "safe", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: rootURL}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main"}}}
	data, _ := config.MarshalPortableManifest(manifest)
	source := writeClonePlanManifest(t, base, data)
	remote := &clonePlanRemote{commits: map[string]string{rootURL + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote})
	before := mustDirectorySnapshot(t, base)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			plan, err := planner.DryRun(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone-"+string(rune('a'+index))), CWD: base, DataDir: filepath.Join(base, "data")})
			if err == nil && (plan.Operation != "clone" || !plan.Destination.DestinationDidNotExist) {
				err = errors.New("incomplete dry-run plan")
			}
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("concurrent dry runs mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestClonePlanUsesReadOnlyGitLsRemoteForDifferentlyNamedBranch(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("README.md", "root\n", "root")
	remotePath := testutil.NewBareGitRemote(t)
	repository.Run(t, "remote", "add", "publish", remotePath)
	repository.Run(t, "push", "publish", "HEAD:refs/heads/release-published")
	command := exec.Command("git", "-C", repository.Path, "rev-parse", "HEAD")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C", "LANG=C"}
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(output))
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "real-remote", Name: "real-remote", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "publish", URL: remotePath}, Upstream: config.Upstream{Branch: "local-release", Remote: "publish", Merge: "refs/heads/release-published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{commit}}, Mount: ".", DefaultBranch: "local-release"}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	source := writeClonePlanManifest(t, base, data)
	beforeHead := commit
	beforeSourceGit := mustDirectorySnapshot(t, filepath.Join(repository.Path, ".git"))
	beforeRemoteGit := mustDirectorySnapshot(t, remotePath)
	plan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Repositories[0].AdvertisedCommit != commit || plan.Repositories[0].RemoteRef != "refs/heads/release-published" {
		t.Fatalf("real remote plan = %#v", plan.Repositories[0])
	}
	afterCommand := exec.Command("git", "-C", repository.Path, "rev-parse", "HEAD")
	afterCommand.Env = command.Env
	after, err := afterCommand.Output()
	if err != nil || strings.TrimSpace(string(after)) != beforeHead {
		t.Fatalf("planning mutated source Git HEAD: %q, %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(base, "clone")); !os.IsNotExist(err) {
		t.Fatalf("planning created destination: %v", err)
	}
	if after := mustDirectorySnapshot(t, filepath.Join(repository.Path, ".git")); !reflect.DeepEqual(beforeSourceGit, after) {
		t.Fatal("planning changed source Git refs, index, configuration, or timestamps")
	}
	if after := mustDirectorySnapshot(t, remotePath); !reflect.DeepEqual(beforeRemoteGit, after) {
		t.Fatal("planning changed remote Git refs or timestamps")
	}
}

type directorySnapshotEntry struct {
	Path    string
	Mode    os.FileMode
	Size    int64
	ModTime int64
}

func mustDirectorySnapshot(t *testing.T, root string) []directorySnapshotEntry {
	t.Helper()
	var result []directorySnapshotEntry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		result = append(result, directorySnapshotEntry{Path: relative, Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
