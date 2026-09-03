package service

import (
	"bytes"
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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
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
	if plan.Version != ClonePlanVersion || plan.Operation != "clone" || plan.Source.Value != manifest || plan.Destination.Path != canonicalDestination || plan.Project.ID != "project-1" {
		t.Fatalf("plan header = %#v", plan)
	}
	if got := []string{plan.Repositories[0].ID, plan.Repositories[1].ID}; !reflect.DeepEqual(got, []string{"root", "api"}) {
		t.Fatalf("repository order = %v", got)
	}
	if got := plan.Repositories[0]; got.LocalBranch != "local-main" || got.RemoteRef != "refs/heads/published-main" || got.ObservedCommit != clonePlanRootCommit {
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
	if !reflect.DeepEqual(first, second) || !json.Valid(first) || !strings.Contains(string(first), `"operation": "clone"`) || !strings.Contains(string(first), `"observedCommit"`) || strings.Contains(string(first), `"exactCommit"`) || strings.Contains(string(first), `"parentCommit"`) {
		t.Fatalf("unstable/invalid plan JSON: %s", first)
	}
	var decoded ClonePlan
	if err := json.Unmarshal(first, &decoded); err != nil || decoded.Version != ClonePlanVersion || len(decoded.Repositories) != 2 || len(decoded.Actions) == 0 {
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

func TestClonePlanV3HooksAreDeferredAndSharedHooksRemainInert(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	manifest, err := config.LoadPortableManifest(clonePlanManifest(t, rootURL, childURL))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = config.PortableManifestVersion3
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: {{ID: "clone-setup", Command: []string{"hooks/setup", "--literal"}}}}
	manifest.SharedHooks = config.HookEvents{config.HookEventPostCreate: {{ID: "shared-setup", Command: []string{"hooks/shared"}}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	source := writeClonePlanManifest(t, base, data)
	before := mustDirectorySnapshot(t, base)
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)}).Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry hook planning mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
	}
	if len(plan.Hooks) != 2 {
		t.Fatalf("hook entries = %#v", plan.Hooks)
	}
	portable, shared := plan.Hooks[0], plan.Hooks[1]
	if portable.Source != "portable" || portable.Event != config.HookEventPostClone || portable.Availability != "deferred" || portable.ResolvedExecutable != "" || portable.ExecutionPolicy != "requires-run-hooks" || !reflect.DeepEqual(portable.Arguments, []string{"--literal"}) {
		t.Fatalf("portable dry-run entry = %#v", portable)
	}
	if shared.Source != "shared" || shared.Event != config.HookEventPostCreate || shared.Availability != "deferred" || shared.ExecutionPolicy != "inert" {
		t.Fatalf("shared dry-run entry = %#v", shared)
	}
	encoded, err := plan.JSON()
	if err != nil || !strings.Contains(string(encoded), `"executionPolicy": "inert"`) {
		t.Fatalf("hook plan JSON = %s, %v", encoded, err)
	}
	for _, mutate := range []func(*ClonePlan){
		func(value *ClonePlan) { value.Hooks[0].ConfiguredExecutable = "hooks/other" },
		func(value *ClonePlan) { value.Hooks[0].Arguments = []string{"changed"} },
		func(value *ClonePlan) { value.Hooks[0].ID = "changed" },
		func(value *ClonePlan) { value.Hooks[0].Source = "shared" },
		func(value *ClonePlan) { value.Hooks[0].Event = config.HookEventPostCreate },
		func(value *ClonePlan) { value.Hooks[0].ExecutionPolicy = "inert" },
		func(value *ClonePlan) { value.Hooks[0], value.Hooks[1] = value.Hooks[1], value.Hooks[0] },
		func(value *ClonePlan) { value.Hooks = nil },
		func(value *ClonePlan) { value.Hooks = value.Hooks[1:] },
		func(value *ClonePlan) { value.Hooks = append(value.Hooks, HookPlanEntry{ID: "extra"}) },
	} {
		mutated := clonePlanCopy(plan)
		mutated.Hooks = cloneHookPlanEntries(plan.Hooks)
		mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatal("tampered hook projection passed plan validation")
		}
		if _, _, err := NewCloneLifecycleCoordinator().Plan(context.Background(), mutated); err == nil {
			t.Fatal("tampered hook projection reached lifecycle planning")
		}
	}
}

func TestClonePlanHTTPV3HooksRemainDeferredWithoutCoreMutation(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := "https://example.test/root.git", "https://example.test/api.git"
	manifest, err := config.LoadPortableManifest(clonePlanManifest(t, rootURL, childURL))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = config.PortableManifestVersion3
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: {{ID: "portable", Command: []string{"hooks/setup"}}}}
	manifest.SharedHooks = config.HookEvents{config.HookEventPostCreate: {{ID: "shared", Command: []string{"hooks/shared"}}}}
	body, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/project.wtree.yml" {
			t.Errorf("unexpected manifest request %q", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	destination, data := filepath.Join(base, "clone"), filepath.Join(base, "data")
	before := mustDirectorySnapshot(t, base)
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)}).DryRun(context.Background(), ClonePlanRequest{ManifestSource: server.URL + "/project.wtree.yml", Destination: destination, CWD: base, DataDir: data})
	if err != nil || len(plan.Hooks) != 2 || plan.Hooks[0].Availability != "deferred" || plan.Hooks[1].ExecutionPolicy != "inert" {
		t.Fatalf("HTTP v3 dry-run plan=%#v err=%v", plan.Hooks, err)
	}
	if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("HTTP dry-run mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestCloneSetupIncompleteDetailsRetainsPortableRetryContract(t *testing.T) {
	exit := 17
	details := cloneSetupIncompleteDetails(CloneExecutionResult{}, HookRunResult{CompletedIDs: []string{"first"}, Failure: &HookRunFailure{Kind: HookFailureNonZero, HookID: "second", RepositoryID: "root", ExitCode: &exit}}, nil)
	if details.Operation != "clone" || details.CoreStatus != "completed" || details.Event != config.HookEventPostClone || details.RetryCommand != "wtree hooks retry default" || details.FailureKind != HookFailureNonZero || details.ExitCode == nil || *details.ExitCode != exit || !reflect.DeepEqual(details.CompletedHookIDs, []string{"first"}) {
		t.Fatalf("clone incomplete details = %#v", details)
	}
	if got := (&SetupIncompleteError{Details: details}).Error(); !strings.HasPrefix(got, "clone setup incomplete") {
		t.Fatalf("clone incomplete message = %q", got)
	}
	wrapped := NewError(ErrorSetupIncomplete, &SetupIncompleteError{Details: details, Cause: context.Canceled})
	if _, ok := SetupIncompleteFrom(wrapped); !ok || !errors.Is(wrapped, context.Canceled) {
		t.Fatalf("setup-incomplete cancellation wrapper lost contract: %v", wrapped)
	}
}

func TestCloneLifecycleRequiresExactCompletedRunnerIDs(t *testing.T) {
	plan := mustHookRunnerPlanEntries(t, "first", "second")
	for _, ids := range [][]string{nil, {}, {"first"}, {"second", "first"}, {"first", "unknown"}, {"first", "first"}} {
		if cloneHookRunCompleted(plan, HookRunResult{Status: "completed", CompletedIDs: ids}, nil) {
			t.Fatalf("invalid completed IDs accepted: %v", ids)
		}
		if prefix := orderedHookPrefix(plan, ids); len(prefix) > 1 || (len(prefix) == 1 && prefix[0] != "first") {
			t.Fatalf("invalid completed IDs produced non-prefix: %v => %v", ids, prefix)
		}
	}
	if !cloneHookRunCompleted(plan, HookRunResult{Status: "completed", CompletedIDs: []string{"first", "second"}}, nil) {
		t.Fatal("exact completed IDs rejected")
	}
}

func TestCloneLifecyclePlanRendersSharedOnlyDeclarationsAsInert(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	manifest, err := config.LoadPortableManifest(clonePlanManifest(t, rootURL, childURL))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = config.PortableManifestVersion3
	manifest.SharedHooks = config.HookEvents{config.HookEventPostCreate: {{ID: "shared-only", Command: []string{"hooks/shared"}}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)}).Plan(context.Background(), ClonePlanRequest{ManifestSource: writeClonePlanManifest(t, base, data), Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	entries, applicable, err := NewCloneLifecycleCoordinator().Plan(context.Background(), plan)
	if err != nil || applicable || len(entries) != 1 || entries[0].Source != "shared" || entries[0].ExecutionPolicy != "inert" || entries[0].Availability != "deferred" {
		t.Fatalf("shared-only lifecycle plan = %#v applicable=%v err=%v", entries, applicable, err)
	}
}

func TestCloneLifecycleAuthorizedPortableHooksRunAfterPublicationWithFinalFacts(t *testing.T) {
	base := t.TempDir()
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	base = canonicalBase
	repository := testutil.NewGitRepository(t)
	first, setup := "hooks/first", "hooks/setup"
	files := map[string]string{".gitignore": "/.wtree.yml\n", "README.md": "root\n", first: "portable first helper\n", setup: "portable helper\n"}
	if runtime.GOOS == "windows" {
		first, setup = "hooks/first.exe", "hooks/setup.exe"
		files = map[string]string{".gitignore": "/.wtree.yml\n", "README.md": "root\n"}
	}
	writeAndCommitCloneFiles(t, repository.Path, files, "root identity")
	if runtime.GOOS == "windows" {
		for _, name := range []string{first, setup} {
			path := filepath.Join(repository.Path, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cloneGit(t, repository.Path, "add", first, setup)
		cloneGit(t, repository.Path, "commit", "-m", "add native hook helpers")
	} else {
		for _, name := range []string{"first", "setup"} {
			if err := os.Chmod(filepath.Join(repository.Path, "hooks", name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cloneGit(t, repository.Path, "add", setup)
		cloneGit(t, repository.Path, "commit", "-m", "make helper executable")
	}
	identity := cloneGitOutput(t, repository.Path, "rev-parse", "HEAD")
	remote := testutil.NewBareGitRemote(t)
	manifest := config.PortableManifest{Version: config.PortableManifestVersion3, Project: config.PortableProject{ID: "portable-hooks", Name: "portable-hooks", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: remote}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{identity}}, Mount: ".", DefaultBranch: "main"}}, Hooks: config.HookEvents{config.HookEventPostClone: {{ID: "first", Command: []string{first}}, {ID: "setup", Command: []string{setup, "--literal"}}}}, SharedHooks: config.HookEvents{config.HookEventPostCreate: {{ID: "never", Command: []string{"hooks/shared"}}}}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, repository.Path, map[string]string{"project.wtree.yml": string(manifestBytes)}, "portable manifest")
	cloneGit(t, repository.Path, "push", remote, "HEAD:refs/heads/main")
	data, destination := filepath.Join(base, "data"), filepath.Join(base, "clone")
	plan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: filepath.Join(repository.Path, "project.wtree.yml"), Destination: destination, CWD: base, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	runner := &createHooksRunnerFake{run: func(ctx context.Context, request HookRunRequest) (HookRunResult, error) {
		if handle, lockErr := (lock.Manager{}).ProjectLock(ctx, data, plan.Project.ID, time.Second); lockErr != nil {
			t.Fatalf("project lock remained held during portable hook: %v", lockErr)
		} else if unlockErr := handle.Unlock(); unlockErr != nil {
			t.Fatal(unlockErr)
		}
		if request.Plan.Operation != "clone" || request.Plan.authority.source != "portable" || request.Plan.authority.event != config.HookEventPostClone || len(request.Plan.Entries()) != 2 || request.Plan.Entries()[1].WorkingDirectory != plan.Destination.Path || request.Plan.Entries()[1].Availability != "deferred" || request.Plan.Entries()[1].ResolvedExecutable != "" {
			t.Fatalf("portable runner plan = %#v", request.Plan)
		}
		if _, err := store.ReadWorkspace(WorkspaceStatePath(data, plan.Project.ID, "default")); err != nil {
			t.Fatalf("workspace was not published before portable hook: %v", err)
		}
		if registry, err := store.ReadRegistry(filepath.Join(data, "registry.json")); err != nil || registry.Projects[plan.Project.ID].ConfigPath == "" {
			t.Fatalf("registry was not published before portable hook: %#v %v", registry, err)
		}
		snapshot, verifyErr := request.Revalidate(ctx)
		if verifyErr != nil || request.Plan.SourceSHA256() != digest(snapshot.SourceBytes) || request.Plan.WorkspaceStateSHA256() != digest(snapshot.WorkspaceStateBytes) {
			t.Fatalf("portable runner generations = %#v, %v", snapshot, verifyErr)
		}
		return HookRunResult{Status: "completed", CompletedIDs: []string{"first", "setup"}}, nil
	}}
	result, err := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Runner: runner}).Clone(context.Background(), CloneLifecycleRequest{Plan: plan, RunHooks: true, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil || !result.HooksApplicable || !result.HooksCompleted || result.HooksSkipped || runner.calls != 1 || !reflect.DeepEqual(result.CompletedHookIDs, []string{"first", "setup"}) || len(result.Hooks) != 3 || result.Hooks[2].ExecutionPolicy != "inert" {
		t.Fatalf("authorized portable clone result = %#v, %v", result, err)
	}
	for _, test := range []struct {
		name string
		ids  []string
		want []string
		ok   bool
	}{
		{name: "empty", ids: []string{}, want: []string{}},
		{name: "valid-subset", ids: []string{"first"}, want: []string{"first"}},
		{name: "reordered", ids: []string{"setup", "first"}, want: []string{}},
		{name: "valid-prefix-unknown", ids: []string{"first", "unknown"}, want: []string{"first"}},
		{name: "duplicate", ids: []string{"first", "first"}, want: []string{"first"}},
		{name: "exact-full", ids: []string{"first", "setup"}, want: []string{"first", "setup"}, ok: true},
	} {
		t.Run("runner-completion-"+test.name, func(t *testing.T) {
			completionPlan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: filepath.Join(repository.Path, "project.wtree.yml"), Destination: filepath.Join(base, "completion-"+test.name), CWD: base, DataDir: filepath.Join(base, "completion-data-"+test.name)})
			if err != nil {
				t.Fatal(err)
			}
			completionRunner := &createHooksRunnerFake{run: func(context.Context, HookRunRequest) (HookRunResult, error) {
				return HookRunResult{Status: "completed", CompletedIDs: append([]string(nil), test.ids...)}, nil
			}}
			completion, completionErr := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Runner: completionRunner}).Clone(context.Background(), CloneLifecycleRequest{Plan: completionPlan, RunHooks: true, Environment: []string{"PATH=" + os.Getenv("PATH")}})
			if test.ok {
				if completionErr != nil || !completion.HooksCompleted || !reflect.DeepEqual(completion.CompletedHookIDs, test.want) {
					t.Fatalf("exact completion=%#v err=%v", completion, completionErr)
				}
				return
			}
			details, setup := SetupIncompleteFrom(completionErr)
			if !setup || completion.HooksCompleted || !reflect.DeepEqual(completion.CompletedHookIDs, test.want) || !reflect.DeepEqual(details.CompletedHookIDs, test.want) {
				t.Fatalf("invalid completion=%#v details=%#v err=%v", completion, details, completionErr)
			}
			encoded, err := json.Marshal(details)
			if err != nil {
				t.Fatal(err)
			}
			var rendered struct {
				CompletedHookIDs []string `json:"completedHookIds"`
			}
			if err := json.Unmarshal(encoded, &rendered); err != nil || !reflect.DeepEqual(rendered.CompletedHookIDs, test.want) {
				t.Fatalf("invalid completion JSON=%s rendered=%#v err=%v", encoded, rendered, err)
			}
		})
	}
	// Rebuild the published authority with controllable read-only facts. Every
	// generation change is rejected before HookRunner can launch a process or
	// alter its durable record.
	verifierProcess := &cloneLifecycleVerifierProcess{}
	verifierGit := &cloneLifecycleVerifierGit{Git: gitadapter.NewAdapter("git")}
	verification := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Process: verifierProcess, Git: verifierGit})
	hookPlan, verifier, verifyPlanErr := verification.publishedPlan(context.Background(), plan, result.Core, []string{"PATH=" + os.Getenv("PATH")}, false)
	if verifyPlanErr != nil || verifier == nil || hookPlan.SourceSHA256() == "" {
		t.Fatalf("published portable verifier plan=%#v verifier=%v err=%v", hookPlan, verifier != nil, verifyPlanErr)
	}
	publishedStatePath := WorkspaceStatePath(data, plan.Project.ID, "default")
	originalPublishedState, stateReadErr := os.ReadFile(publishedStatePath)
	if stateReadErr != nil {
		t.Fatal(stateReadErr)
	}
	originalPublishedManifest, manifestReadErr := verifierGit.TrackedFile(context.Background(), plan.Destination.Path, result.Core.Repositories[plan.BaseRepository].Head, "project.wtree.yml")
	if manifestReadErr != nil {
		t.Fatal(manifestReadErr)
	}
	assertPublishedRejection := func(name string) {
		snapshot, verifyErr := verifier(context.Background())
		if verifyErr == nil && digest(snapshot.SourceBytes) == hookPlan.SourceSHA256() && digest(snapshot.WorkspaceStateBytes) == hookPlan.WorkspaceStateSHA256() {
			t.Fatalf("%s generation change was accepted", name)
		}
		if verifierProcess.runs != 0 {
			t.Fatalf("%s generation change ran portable code", name)
		}
	}
	if err := os.WriteFile(publishedStatePath, append(append([]byte(nil), originalPublishedState...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPublishedRejection("workspace state")
	if err := os.WriteFile(publishedStatePath, originalPublishedState, 0o600); err != nil {
		t.Fatal(err)
	}
	verifierGit.manifestOverride = append(append([]byte(nil), originalPublishedManifest...), '\n')
	assertPublishedRejection("portable manifest")
	verifierGit.manifestOverride = nil
	verifierGit.headOverride = strings.Repeat("f", 40)
	assertPublishedRejection("checkout HEAD")
	verifierGit.headOverride = ""
	verifierProcess.suffix = "-changed"
	assertPublishedRejection("executable path")
	verifierProcess.suffix = ""
	unauthorizedData, unauthorizedDestination := filepath.Join(base, "unauthorized-data"), filepath.Join(base, "unauthorized-clone")
	unauthorizedPlan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: filepath.Join(repository.Path, "project.wtree.yml"), Destination: unauthorizedDestination, CWD: base, DataDir: unauthorizedData})
	if err != nil {
		t.Fatal(err)
	}
	unauthorizedRunner := &createHooksRunnerFake{run: func(context.Context, HookRunRequest) (HookRunResult, error) {
		t.Fatal("unauthorized portable hook reached runner")
		return HookRunResult{}, nil
	}}
	unauthorized, err := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Runner: unauthorizedRunner}).Clone(context.Background(), CloneLifecycleRequest{Plan: unauthorizedPlan, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil || !unauthorized.HooksApplicable || !unauthorized.HooksSkipped || unauthorized.HooksCompleted || unauthorizedRunner.calls != 0 {
		t.Fatalf("unauthorized portable clone result = %#v, %v", unauthorized, err)
	}
	if recordPath, recordErr := store.HookRunRecordPath(unauthorizedData, unauthorizedPlan.Project.ID, "default", config.HookEventPostClone); recordErr != nil {
		t.Fatal(recordErr)
	} else if _, statErr := os.Stat(recordPath); !os.IsNotExist(statErr) {
		t.Fatalf("unauthorized clone created hook record: %v", statErr)
	}
	failureData, failureDestination := filepath.Join(base, "failure-data"), filepath.Join(base, "failure-clone")
	failurePlan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: filepath.Join(repository.Path, "project.wtree.yml"), Destination: failureDestination, CWD: base, DataDir: failureData})
	if err != nil {
		t.Fatal(err)
	}
	process := &cloneLifecycleProcess{failID: "setup"}
	runnerWithRecord := NewHookRunnerWith(HookRunnerDependencies{Process: process})
	failed, failureErr := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Runner: runnerWithRecord, Process: process}).Clone(context.Background(), CloneLifecycleRequest{Plan: failurePlan, RunHooks: true, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	var failedSetup *Error
	if !errors.As(failureErr, &failedSetup) || failedSetup.Kind != ErrorSetupIncomplete || failed.Core.ProjectID == "" || !failed.HooksApplicable || process.runs != 2 {
		t.Fatalf("portable hook failure=%#v err=%v runs=%d", failed, failureErr, process.runs)
	}
	recordPath, recordErr := store.HookRunRecordPath(failureData, failurePlan.Project.ID, "default", config.HookEventPostClone)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	record, recordErr := store.ReadHookRunRecord(recordPath)
	if recordErr != nil || record.Source != "portable" || record.Operation != "clone" || record.Event != config.HookEventPostClone || record.NextIndex != 1 || record.State != "failed" || !reflect.DeepEqual(record.CompletedHookIDs, []string{"first"}) {
		t.Fatalf("portable failure record=%#v err=%v", record, recordErr)
	}
	resolution, resolveErr := NewResolver().Resolve(context.Background(), ResolveRequest{Path: failurePlan.Destination.Path, ProjectPath: failurePlan.Destination.Path, DataDir: failureData})
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	builder := hookRetryDefaultBuilder{process: process, git: gitadapter.NewAdapter("git")}
	prepare := func(ctx context.Context, current store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
		return builder.Rebuild(ctx, HookRetryPlanRequest{Project: resolution.Project, Workspace: resolution.Workspace, Record: current, DataDir: failureData, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	}
	statePath := WorkspaceStatePath(failureData, failurePlan.Project.ID, "default")
	originalState, stateErr := os.ReadFile(statePath)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	originalRecord, recordReadErr := os.ReadFile(recordPath)
	if recordReadErr != nil {
		t.Fatal(recordReadErr)
	}
	if err := os.WriteFile(statePath, append(append([]byte(nil), originalState...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	stale, staleErr := runnerWithRecord.Resume(context.Background(), HookResumeRequest{DataDir: failureData, ProjectID: resolution.Project.ID, WorkspaceID: resolution.Workspace.ID, Event: config.HookEventPostClone, Environment: []string{"PATH=" + os.Getenv("PATH")}, Prepare: prepare})
	if staleErr != nil || stale.Failure == nil || stale.Failure.Kind != HookFailureGeneration || process.runs != 2 {
		t.Fatalf("state-changed portable retry=%#v err=%v runs=%d", stale, staleErr, process.runs)
	}
	if got, readErr := os.ReadFile(recordPath); readErr != nil || !bytes.Equal(got, originalRecord) {
		t.Fatalf("stale retry mutated record=%q err=%v", got, readErr)
	}
	if err := os.WriteFile(statePath, originalState, 0o600); err != nil {
		t.Fatal(err)
	}
	process.failID = ""
	if rebuilt, verifier, rebuildErr := builder.Rebuild(context.Background(), HookRetryPlanRequest{Project: resolution.Project, Workspace: resolution.Workspace, Record: record, DataDir: failureData, Environment: []string{"PATH=" + os.Getenv("PATH")}}); rebuildErr != nil || verifier == nil || !matchesHookRunRecord(record, rebuilt) {
		t.Fatalf("portable retry rebuild plan=%#v verifier=%v err=%v", rebuilt, verifier != nil, rebuildErr)
	}
	resumed, resumeErr := runnerWithRecord.Resume(context.Background(), HookResumeRequest{DataDir: failureData, ProjectID: resolution.Project.ID, WorkspaceID: resolution.Workspace.ID, Event: config.HookEventPostClone, Environment: []string{"PATH=" + os.Getenv("PATH")}, Prepare: prepare})
	if resumeErr != nil || resumed.Status != "completed" || !reflect.DeepEqual(resumed.CompletedIDs, []string{"first", "setup"}) || process.runs != 3 {
		t.Fatalf("portable resume=%#v failure=%#v err=%v runs=%d", resumed, resumed.Failure, resumeErr, process.runs)
	}
	if _, statErr := os.Stat(recordPath); !os.IsNotExist(statErr) {
		t.Fatalf("completed portable retry retained record: %v", statErr)
	}

	// A post-publication cancellation during executable resolution is setup
	// incomplete, not a clone rollback. Its record is established before the
	// locked verifier and survives an interrupted retry byte-for-byte.
	canceledData, canceledDestination := filepath.Join(base, "canceled-data"), filepath.Join(base, "canceled-clone")
	canceledPlan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: filepath.Join(repository.Path, "project.wtree.yml"), Destination: canceledDestination, CWD: base, DataDir: canceledData})
	if err != nil {
		t.Fatal(err)
	}
	canceledProcess := &cloneLifecycleProcess{resolveErr: context.Canceled}
	canceledRunner := NewHookRunnerWith(HookRunnerDependencies{Process: canceledProcess})
	canceled, canceledErr := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Runner: canceledRunner, Process: canceledProcess}).Clone(context.Background(), CloneLifecycleRequest{Plan: canceledPlan, RunHooks: true, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if !errors.Is(canceledErr, context.Canceled) || canceled.Core.ProjectID == "" || canceled.Core.Destination != canceledDestination || !canceled.HooksApplicable || canceled.HooksCompleted {
		t.Fatalf("post-publication resolve cancellation result=%#v err=%v", canceled, canceledErr)
	}
	canceledDetails, canceledSetup := SetupIncompleteFrom(canceledErr)
	if !canceledSetup || canceledDetails.CoreStatus != "completed" || canceledDetails.FailureKind != HookFailureCanceled || canceledDetails.RetryCommand != "wtree hooks retry default" {
		t.Fatalf("post-publication resolve cancellation details=%#v setup=%v", canceledDetails, canceledSetup)
	}
	canceledRecordPath, err := store.HookRunRecordPath(canceledData, canceledPlan.Project.ID, "default", config.HookEventPostClone)
	if err != nil {
		t.Fatal(err)
	}
	canceledRecord, err := store.ReadHookRunRecord(canceledRecordPath)
	if err != nil || canceledRecord.State != "failed" || canceledRecord.NextIndex != 0 || !reflect.DeepEqual(canceledRecord.CompletedHookIDs, []string{}) || canceledRecord.Failure == nil || canceledRecord.Failure.Kind != string(HookFailureCanceled) || canceledRecord.PlanSHA256 == "" || canceledRecord.SourceSHA256 != digest(canceledPlan.ManifestBytes()) {
		t.Fatalf("post-publication resolve cancellation record=%#v err=%v", canceledRecord, err)
	}
	canceledBytes, err := os.ReadFile(canceledRecordPath)
	if err != nil {
		t.Fatal(err)
	}
	canceledResolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: canceledDestination, ProjectPath: canceledDestination, DataDir: canceledData})
	if err != nil {
		t.Fatal(err)
	}
	canceledBuilder := hookRetryDefaultBuilder{process: canceledProcess, git: gitadapter.NewAdapter("git")}
	canceledRetry := NewHookRetryServiceWith(NewHookRunInventoryServiceWith(canceledBuilder), canceledBuilder, canceledRunner)
	canceledProcess.resolveErr = context.DeadlineExceeded
	if _, err := canceledRetry.Retry(context.Background(), HookRetryRequest{Project: canceledResolution.Project, Workspace: canceledResolution.Workspace, DataDir: canceledData, Environment: []string{"PATH=" + os.Getenv("PATH")}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("post-publication resolve deadline retry error = %v", err)
	}
	if got, err := os.ReadFile(canceledRecordPath); err != nil || !bytes.Equal(got, canceledBytes) {
		t.Fatalf("canceled retry changed durable record=%q err=%v", got, err)
	}
	canceledProcess.resolveErr = nil
	resumedRetry, err := canceledRetry.Retry(context.Background(), HookRetryRequest{Project: canceledResolution.Project, Workspace: canceledResolution.Workspace, DataDir: canceledData, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil || resumedRetry.Status != "completed" || !reflect.DeepEqual(resumedRetry.CompletedHookIDs, []string{"first", "setup"}) || canceledProcess.runs != 2 {
		t.Fatalf("post-publication resolve retry result=%#v err=%v runs=%d", resumedRetry, err, canceledProcess.runs)
	}
	if _, err := os.Stat(canceledRecordPath); !os.IsNotExist(err) {
		t.Fatalf("completed post-publication resolve retry retained record: %v", err)
	}

	stateReadFailure := errors.New("published state temporarily unavailable")
	stateProcess := &cloneLifecycleProcess{}
	stateRunner := NewHookRunnerWith(HookRunnerDependencies{Process: stateProcess})
	stateCoordinator := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Process: stateProcess, ReadFile: func(path string) ([]byte, error) {
		if path == canceled.Core.StatePath {
			return nil, stateReadFailure
		}
		return os.ReadFile(path)
	}})
	statePlan, stateVerifier, err := stateCoordinator.publishedPlan(context.Background(), canceledPlan, canceled.Core, []string{"PATH=" + os.Getenv("PATH")}, false)
	if err != nil || stateVerifier == nil {
		t.Fatalf("state-read portable plan=%#v verifier=%t err=%v", statePlan, stateVerifier != nil, err)
	}
	stateRun, err := stateRunner.Run(context.Background(), HookRunRequest{DataDir: canceledData, Plan: statePlan, InheritedEnvironment: []string{"PATH=" + os.Getenv("PATH")}, Revalidate: stateVerifier})
	if err != nil || stateRun.Failure == nil || stateRun.Failure.Kind != HookFailureGeneration || stateProcess.runs != 0 {
		t.Fatalf("post-publication state-read run=%#v err=%v runs=%d", stateRun, err, stateProcess.runs)
	}
	stateRecord, err := store.ReadHookRunRecord(canceledRecordPath)
	if err != nil || stateRecord.State != "failed" || stateRecord.NextIndex != 0 || stateRecord.Failure == nil || stateRecord.Failure.Kind != string(HookFailureGeneration) || stateRecord.PlanSHA256 != statePlan.Digest() || stateRecord.WorkspaceStateSHA256 != statePlan.WorkspaceStateSHA256() {
		t.Fatalf("post-publication state-read record=%#v err=%v", stateRecord, err)
	}
	if err := os.Remove(canceledRecordPath); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		headErr    error
		trackedErr error
		kind       HookFailureKind
	}{
		{name: "head-canceled", headErr: context.Canceled, kind: HookFailureCanceled},
		{name: "manifest-deadline", trackedErr: context.DeadlineExceeded, kind: HookFailureTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			git := &cloneLifecycleVerifierGit{Git: gitadapter.NewAdapter("git"), headErr: test.headErr, trackedErr: test.trackedErr}
			process := &cloneLifecycleProcess{}
			coordinator := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Process: process, Git: git})
			planValue, verifier, err := coordinator.publishedPlan(context.Background(), canceledPlan, canceled.Core, []string{"PATH=" + os.Getenv("PATH")}, false)
			if err != nil || verifier == nil {
				t.Fatalf("Git-failure portable plan=%#v verifier=%t err=%v", planValue, verifier != nil, err)
			}
			run, runErr := NewHookRunnerWith(HookRunnerDependencies{Process: process}).Run(context.Background(), HookRunRequest{DataDir: canceledData, Plan: planValue, InheritedEnvironment: []string{"PATH=" + os.Getenv("PATH")}, Revalidate: verifier})
			if !errors.Is(runErr, test.headErr) && !errors.Is(runErr, test.trackedErr) || run.Failure == nil || run.Failure.Kind != test.kind || process.runs != 0 {
				t.Fatalf("post-publication Git failure run=%#v err=%v", run, runErr)
			}
			record, err := store.ReadHookRunRecord(canceledRecordPath)
			if err != nil || record.State != "failed" || record.NextIndex != 0 || record.Failure == nil || record.Failure.Kind != string(test.kind) {
				t.Fatalf("post-publication Git failure record=%#v err=%v", record, err)
			}
			if err := os.Remove(canceledRecordPath); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, test := range []struct {
		name        string
		unavailable bool
		suffix      string
	}{
		{name: "executable-unavailable", unavailable: true},
		{name: "executable-retarget", suffix: "-retargeted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := &cloneLifecycleProcess{unavailable: test.unavailable, suffix: test.suffix}
			coordinator := NewCloneLifecycleCoordinatorWith(CloneLifecycleDependencies{Process: process})
			planValue, verifier, err := coordinator.publishedPlan(context.Background(), canceledPlan, canceled.Core, []string{"PATH=" + os.Getenv("PATH")}, false)
			if err != nil || verifier == nil {
				t.Fatalf("executable-failure portable plan=%#v verifier=%t err=%v", planValue, verifier != nil, err)
			}
			run, runErr := NewHookRunnerWith(HookRunnerDependencies{Process: process}).Run(context.Background(), HookRunRequest{DataDir: canceledData, Plan: planValue, InheritedEnvironment: []string{"PATH=" + os.Getenv("PATH")}, Revalidate: verifier})
			if runErr != nil || run.Failure == nil || run.Failure.Kind != HookFailureGeneration || process.runs != 0 {
				t.Fatalf("post-publication executable failure run=%#v err=%v", run, runErr)
			}
			record, err := store.ReadHookRunRecord(canceledRecordPath)
			if err != nil || record.State != "failed" || record.NextIndex != 0 || record.Failure == nil || record.Failure.Kind != string(HookFailureGeneration) {
				t.Fatalf("post-publication executable failure record=%#v err=%v", record, err)
			}
			if err := os.Remove(canceledRecordPath); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type cloneLifecycleProcess struct {
	failID      string
	resolveErr  error
	unavailable bool
	suffix      string
	runs        int
}

type cloneLifecycleVerifierProcess struct {
	suffix string
	runs   int
}

func (p *cloneLifecycleVerifierProcess) Resolve(_ context.Context, request HookExecutableRequest) (HookExecutableFact, error) {
	return HookExecutableFact{Resolved: filepath.Join(request.Directory, filepath.FromSlash(request.Program)) + p.suffix, Available: true}, nil
}
func (p *cloneLifecycleVerifierProcess) Run(_ context.Context, _ HookProcessRequest) (HookProcessResult, error) {
	p.runs++
	return HookProcessResult{Started: true}, nil
}

type cloneLifecycleVerifierGit struct {
	gitadapter.Git
	headOverride     string
	manifestOverride []byte
	headErr          error
	trackedErr       error
}

func (g *cloneLifecycleVerifierGit) Head(ctx context.Context, path string) (string, error) {
	if g.headErr != nil {
		return "", g.headErr
	}
	if g.headOverride != "" {
		return g.headOverride, nil
	}
	return g.Git.Head(ctx, path)
}
func (g *cloneLifecycleVerifierGit) TrackedFile(ctx context.Context, repository, commit, name string) ([]byte, error) {
	if g.trackedErr != nil {
		return nil, g.trackedErr
	}
	if name == "project.wtree.yml" && g.manifestOverride != nil {
		return append([]byte(nil), g.manifestOverride...), nil
	}
	return g.Git.TrackedFile(ctx, repository, commit, name)
}

func (p *cloneLifecycleProcess) Resolve(_ context.Context, request HookExecutableRequest) (HookExecutableFact, error) {
	if p.resolveErr != nil {
		return HookExecutableFact{}, p.resolveErr
	}
	return HookExecutableFact{Resolved: filepath.Join(request.Directory, filepath.FromSlash(request.Program)) + p.suffix, Available: !p.unavailable}, nil
}
func (p *cloneLifecycleProcess) Run(_ context.Context, request HookProcessRequest) (HookProcessResult, error) {
	p.runs++
	if p.failID == request.HookID {
		return HookProcessResult{Started: true, ExitCode: 23}, nil
	}
	return HookProcessResult{Started: true}, nil
}

func TestCloneRunHooksRejectsMissingStagedExecutableBeforePublication(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	manifest, err := config.LoadPortableManifest(plan.ManifestBytes())
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = config.PortableManifestVersion3
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: {{ID: "missing", Command: []string{"hooks/missing"}}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	plan.manifestData, plan.Source.SHA256, plan.runHooks = data, digest(data), true
	plan.Hooks = cloneDryRunHookEntries(plan, manifest)
	_, err = NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}).Execute(context.Background(), plan, nil)
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorValidation {
		t.Fatalf("missing staged executable error = %v", err)
	}
	assertCloneExecutionAbsent(t, plan)
}

func TestCloneRunHooksRejectsUntrackedStagedExecutableBeforePublication(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	manifest, err := config.LoadPortableManifest(plan.ManifestBytes())
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = config.PortableManifestVersion3
	manifest.Hooks = config.HookEvents{config.HookEventPostClone: {{ID: "untracked", Command: []string{"hooks/setup"}}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	plan.manifestData, plan.Source.SHA256, plan.runHooks = data, digest(data), true
	plan.Hooks = cloneDryRunHookEntries(plan, manifest)
	base := &cloneExecutionGit{plan: plan}
	_, err = NewCloneExecutorWith(CloneExecutorDependencies{Git: cloneStagedHookGit{cloneExecutionGit: base}}).Execute(context.Background(), plan, nil)
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorValidation {
		t.Fatalf("untracked staged executable error = %v", err)
	}
	assertCloneExecutionAbsent(t, plan)
}

// cloneStagedHookGit produces a regular executable in private staging but
// refuses its tracked-file proof. It keeps the test at the publication seam:
// invalid portable content cannot leave destination, registry, state, or a
// hook record behind.
type cloneStagedHookGit struct{ *cloneExecutionGit }

func (g cloneStagedHookGit) Clone(ctx context.Context, remote, destination, name string) error {
	if err := g.cloneExecutionGit.Clone(ctx, remote, destination, name); err != nil {
		return err
	}
	path := filepath.Join(destination, "hooks", "setup")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("not tracked\n"), 0o755)
}
func (g cloneStagedHookGit) TrackedFile(ctx context.Context, repository, commit, name string) ([]byte, error) {
	if name != "project.wtree.yml" {
		return nil, os.ErrNotExist
	}
	return g.cloneExecutionGit.TrackedFile(ctx, repository, commit, name)
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
	dataDir := filepath.Join(base, "data")
	plan, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, CWD: base, DataDir: dataDir})
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
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: unsafeSource, CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "explicit destination") {
		t.Fatalf("unsafe default error = %v", err)
	}

	existing := filepath.Join(base, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: existing, CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination error = %v", err)
	}
	nonDirectory := filepath.Join(base, "file-parent")
	if err := os.WriteFile(nonDirectory, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(nonDirectory, "child"), CWD: base, DataDir: dataDir}); err == nil {
		t.Fatal("non-directory parent accepted")
	}
	unwritable := filepath.Join(base, "unwritable")
	if err := os.Mkdir(unwritable, 0o500); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(unwritable, "child"), CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Fatalf("unwritable parent error = %v", err)
		}
	}
	realParent := filepath.Join(base, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkParent := filepath.Join(base, "symlink-parent")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(symlinkParent, "child"), CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink parent error = %v", err)
	}
	broadDestination := filepath.VolumeName(base) + string(filepath.Separator)
	if _, err := planner.Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: broadDestination, CWD: base, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "too broad") {
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
	if api.RepositoryID != "api" || api.ParentRepositoryID != "root" || api.ParentPath != plan.Repositories[0].Path || api.ChildMount != "backend/API 世界" || api.IgnoreRuleSubject != "backend/API 世界" || !reflect.DeepEqual(api.ChildInitialCommits, []string{clonePlanChildCommit}) {
		t.Fatalf("self-contained API ignore action = %#v", api)
	}
	shared := ignoreActions[1]
	if shared.ParentRepositoryID != "api" || shared.ChildMount != "libraries/shared" {
		t.Fatalf("self-contained shared ignore action = %#v", shared)
	}
	mutated := plan
	mutated.Actions = append([]ClonePlanAction(nil), plan.Actions...)
	for index := range mutated.Actions {
		if mutated.Actions[index].Action == "verify_parent_ignore" {
			mutated.Actions[index].ParentPath = "tampered"
			if mutated.Actions[index].ParentRepositoryID == "api" {
				break
			}
		}
	}
	if err := mutated.Validate(); err == nil {
		t.Fatal("mutated parent-ignore action bypassed plan validation")
	}
}

func TestPortableRepositoryOrderIsIndependentOfForestMapInsertionOrder(t *testing.T) {
	values := []struct {
		id     string
		parent string
	}{
		{"deep", "child"},
		{"z-base", ""},
		{"a-top", ""},
		{"child", "z-base"},
	}
	for _, reverse := range []bool{false, true} {
		repositories := make(map[string]config.PortableRepository, len(values))
		for index := range values {
			at := index
			if reverse {
				at = len(values) - 1 - index
			}
			repositories[values[at].id] = config.PortableRepository{Parent: values[at].parent}
		}
		order, err := portableRepositoryOrder(config.PortableManifest{Repositories: repositories})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"a-top", "z-base", "child", "deep"}; !reflect.DeepEqual(order, want) {
			t.Fatalf("reverse=%t order = %v, want %v", reverse, order, want)
		}
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
	if plan.Repositories[0].ObservedCommit != commit || plan.Repositories[0].RemoteRef != "refs/heads/release-published" {
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
