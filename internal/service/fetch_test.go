package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestFetchUsesConfiguredRefsInParentFirstOrder(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	for _, repository := range project.Repositories {
		advanceFetchSource(t, repository.SourcePath, repository.ID)
	}
	beforeHeads := map[string]string{}
	for _, checkout := range workspace.Checkouts {
		beforeHeads[checkout.RepositoryID] = checkout.Head
	}
	value, err := NewFetchService().Fetch(context.Background(), project, workspace, FetchRequest{DryRun: true})
	if err != nil || !value.DryRun || fetchIDs(value) != "alpha,beta" || value.Status != AggregateStatusCompleted {
		t.Fatalf("dry fetch = %#v, %v", value, err)
	}
	for _, entry := range value.Repositories {
		if entry.Status != AggregateStatusCompleted || entry.Remote != "origin" || entry.RemoteRef != "refs/heads/main" || entry.ActualRemoteCommit == "" {
			t.Fatalf("dry fetch entry = %#v", entry)
		}
	}
	value, err = NewFetchService().Fetch(context.Background(), project, workspace, FetchRequest{})
	if err != nil || value.Status != AggregateStatusCompleted || fetchIDs(value) != "alpha,beta" {
		t.Fatalf("fetch = %#v, %v", value, err)
	}
	for _, entry := range value.Repositories {
		if entry.PreviousRemoteCommit != "" || entry.ActualRemoteCommit == "" || entry.Status != AggregateStatusCompleted {
			t.Fatalf("fetch receipt = %#v", entry)
		}
		if head, headErr := gitadapter.NewAdapter("git").Head(context.Background(), entry.Path); headErr != nil || head != beforeHeads[entry.ID] {
			t.Fatalf("fetch moved local HEAD %s: %s, %v", entry.ID, head, headErr)
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || !strings.Contains(string(encoded), `"operation":"fetch"`) || !strings.Contains(string(encoded), `"dryRun":false`) {
		t.Fatalf("fetch JSON = %s, %v", encoded, err)
	}
}

func advanceFetchSource(t *testing.T, path, id string) {
	t.Helper()
	file := path + "/fetch-" + id + ".txt"
	if err := os.WriteFile(file, []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fetchGit(t, path, "add", "--", file)
	fetchGit(t, path, "commit", "-m", "advance configured ref")
}

func TestFetchCancellationMarksUnstartedRepositories(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	value, err := NewFetchService().Fetch(ctx, project, workspace, FetchRequest{})
	if err == nil || value.Status != AggregateStatusFailed || len(value.Repositories) != 2 || value.Repositories[0].Status != AggregateStatusCanceled || value.Repositories[1].Status != AggregateStatusCanceled || value.Failure == nil || value.Failure.Code != ErrorInternal {
		t.Fatalf("canceled fetch = %#v, %v", value, err)
	}
}

func TestFetchContinuesAfterOrdinaryConfiguredRefFailuresAndRedacts(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git"), fetchErrors: map[string]error{"alpha": fmt.Errorf("transport https://user:super-secret@example.invalid/repo failed")}}
	value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{})
	if err == nil || fetchIDs(value) != "alpha,beta" || fetchCalls(git) != "alpha,beta" || value.Repositories[0].Status != AggregateStatusFailed || value.Repositories[1].Status != AggregateStatusCompleted || value.Repositories[0].Failure == nil || strings.Contains(value.Repositories[0].Failure.Message, "super-secret") {
		t.Fatalf("continued fetch = %#v, %v, calls=%s", value, err, fetchCalls(git))
	}
}

func TestFetchCancellationAndWriterFailureStopLaterNetworkCalls(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		project, workspace := fetchConfiguredWorkspace(t)
		ctx, cancel := context.WithCancel(context.Background())
		git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git"), cancelAfterFetch: true, cancel: cancel}
		value, err := NewFetchServiceWith(git).Fetch(ctx, project, workspace, FetchRequest{})
		if !errors.Is(err, context.Canceled) || fetchCalls(git) != "alpha" || value.Repositories[0].Status != AggregateStatusCanceled || value.Repositories[1].Status != AggregateStatusCanceled {
			t.Fatalf("canceled fetch = %#v, %v, calls=%s", value, err, fetchCalls(git))
		}
	})
	t.Run("writer", func(t *testing.T) {
		project, workspace := fetchConfiguredWorkspace(t)
		git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
		value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{OnComplete: func(FetchRepositoryResult) error { return errors.New("writer stopped") }})
		if err == nil || fetchCalls(git) != "alpha" || value.Repositories[1].Status != AggregateStatusCanceled {
			t.Fatalf("writer fetch = %#v, %v, calls=%s", value, err, fetchCalls(git))
		}
	})
}

func TestFetchPartialWorkspaceUsesOnlyPersistedPresentCheckouts(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	for _, repository := range project.Repositories {
		advanceFetchSource(t, repository.SourcePath, repository.ID)
	}
	workspace.Partial, workspace.MissingRepositoryIDs, workspace.Checkouts = true, []string{"beta"}, workspace.Checkouts[1:]
	before := snapshotFetchInventory(t, workspace)
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
	value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{})
	if err != nil || !value.Partial || strings.Join(value.MissingRepositoryIDs, ",") != "beta" || fetchIDs(value) != "alpha" || fetchCalls(git) != "alpha" {
		t.Fatalf("partial fetch = %#v, %v, calls=%s", value, err, fetchCalls(git))
	}
	assertFetchInventoryDelta(t, before, snapshotFetchInventory(t, workspace), map[string]string{"alpha": "refs/remotes/origin/main"})
}

func TestFetchPreflightDriftPreventsEveryNetworkOperation(t *testing.T) {
	for _, mutate := range []func(*domain.Project, *domain.Workspace){
		func(project *domain.Project, _ *domain.Workspace) {
			project.Repositories[0].CommonGitDir = "/wrong/common"
		},
		func(_ *domain.Project, workspace *domain.Workspace) { workspace.Checkouts[0].Branch = "wrong" },
		func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[0].Head = strings.Repeat("0", 40)
		},
	} {
		project, workspace := fetchConfiguredWorkspace(t)
		mutate(&project, &workspace)
		git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
		if _, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{}); err == nil || fetchCalls(git) != "alpha" {
			t.Fatalf("preflight err=%v calls=%s", err, fetchCalls(git))
		}
	}
}

func TestFetchReportsCompletePreflightFailuresBeforeAnyNetwork(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	// alpha is parent-first even though this fixture stores beta first.
	fetchGit(t, workspace.Checkouts[1].ResolvedPath, "config", "--unset", "branch.exec-alpha.merge")
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
	var callbacks []FetchRepositoryResult
	value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{OnComplete: func(entry FetchRepositoryResult) error {
		callbacks = append(callbacks, entry)
		return nil
	}})
	if err == nil || fetchCalls(git) != "beta" || fetchObserveCalls(git) != "" || fetchCallbackIDs(callbacks) != "alpha,beta" || callbacks[0].Status != AggregateStatusFailed || callbacks[1].Status != AggregateStatusCompleted || value.Status != AggregateStatusFailed {
		t.Fatalf("preflight callback result=%#v err=%v calls=%s callbacks=%#v", value, err, fetchCalls(git), callbacks)
	}
}

func TestFetchMalformedChildSettlesAtItsParentFirstPosition(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	// This fixture deliberately stores beta before alpha. ParentFirst is alpha,
	// beta, and a malformed child must not be streamed ahead of its valid parent.
	advanceFetchSource(t, project.Repositories[1].SourcePath, "alpha")
	fetchGit(t, workspace.Checkouts[0].ResolvedPath, "config", "--unset", "branch.exec-beta.merge")
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
	var callbacks []FetchRepositoryResult
	value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{OnComplete: func(entry FetchRepositoryResult) error {
		callbacks = append(callbacks, entry)
		return nil
	}})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorGit {
		t.Fatalf("malformed child error = %v", err)
	}
	if fetchCalls(git) != "alpha" || fetchObserveCalls(git) != "" || fetchCallbackIDs(callbacks) != "alpha,beta" || len(callbacks) != 2 {
		t.Fatalf("malformed child calls=%q observe=%q callbacks=%#v", fetchCalls(git), fetchObserveCalls(git), callbacks)
	}
	if fetchIDs(value) != "alpha,beta" || value.Status != AggregateStatusFailed || value.Failure == nil || value.Failure.Code != ErrorGit || !strings.Contains(value.Failure.Message, `fetch preflight "beta": read configured upstream`) {
		t.Fatalf("malformed child result = %#v", value)
	}
	if value.Repositories[0].Status != AggregateStatusCompleted || value.Repositories[0].ActualRemoteCommit == "" || value.Repositories[1].Status != AggregateStatusFailed || value.Repositories[1].Failure == nil || value.Repositories[1].Failure.Code != ErrorGit {
		t.Fatalf("malformed child repositories = %#v", value.Repositories)
	}
}

func TestFetchMalformedChildWriterStopsTheTrueRemainingSuffix(t *testing.T) {
	project, workspace := fetchConfiguredWorkspaceWithGrandchild(t)
	advanceFetchSource(t, project.Repositories[1].SourcePath, "alpha")
	fetchGit(t, workspace.Checkouts[0].ResolvedPath, "config", "--unset", "branch.exec-beta.merge")
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
	want := errors.New("malformed child writer stopped")
	var callbacks []FetchRepositoryResult
	value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{OnComplete: func(entry FetchRepositoryResult) error {
		callbacks = append(callbacks, entry)
		if entry.ID == "beta" {
			return want
		}
		return nil
	}})
	if !errors.Is(err, want) || fetchCalls(git) != "alpha" || fetchObserveCalls(git) != "" || fetchCallbackIDs(callbacks) != "alpha,beta" || len(callbacks) != 2 {
		t.Fatalf("writer result=%#v err=%v calls=%q observe=%q callbacks=%#v", value, err, fetchCalls(git), fetchObserveCalls(git), callbacks)
	}
	if fetchIDs(value) != "alpha,beta,gamma" || value.Status != AggregateStatusFailed || value.Failure == nil || !strings.Contains(value.Failure.Message, want.Error()) {
		t.Fatalf("writer envelope = %#v", value)
	}
	if value.Repositories[0].Status != AggregateStatusCompleted || value.Repositories[0].ActualRemoteCommit == "" || value.Repositories[1].Status != AggregateStatusFailed || value.Repositories[2].Status != AggregateStatusCanceled {
		t.Fatalf("writer repositories = %#v", value.Repositories)
	}
	for _, entry := range value.Repositories {
		if entry.Status == AggregateStatusPlanned {
			t.Fatalf("writer left planned repository = %#v", value.Repositories)
		}
	}
}

func TestFetchPreflightCallbackWriterFailureStopsBeforeEveryNetworkOperation(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	fetchGit(t, workspace.Checkouts[1].ResolvedPath, "config", "--unset", "branch.exec-alpha.merge")
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
	want := errors.New("preflight writer stopped")
	var callbacks []FetchRepositoryResult
	value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{OnComplete: func(entry FetchRepositoryResult) error {
		callbacks = append(callbacks, entry)
		return want
	}})
	if !errors.Is(err, want) || fetchCalls(git) != "" || fetchObserveCalls(git) != "" || fetchCallbackIDs(callbacks) != "alpha" || value.Repositories[0].Status != AggregateStatusFailed || value.Repositories[1].Status != AggregateStatusCanceled {
		t.Fatalf("preflight writer result=%#v err=%v fetch=%s observe=%s callbacks=%#v", value, err, fetchCalls(git), fetchObserveCalls(git), callbacks)
	}
}

func fetchCallbackIDs(values []FetchRepositoryResult) string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	return strings.Join(ids, ",")
}

// The first complete plan is intentionally followed by another complete
// observation immediately before each network operation. This table makes the
// second observation adversarial: alpha has already settled, while beta must
// never reach FetchConfiguredRef after any authority fact changes.
func TestFetchRevalidatesEveryAuthorityFactBeforeLaterRepositoryFetch(t *testing.T) {
	for _, field := range []string{"common", "top", "branch", "detached", "head", "upstream-branch", "upstream-remote", "upstream-ref"} {
		t.Run(field, func(t *testing.T) {
			project, workspace := fetchConfiguredWorkspace(t)
			target, targetErr := filepath.EvalSymlinks(workspace.Checkouts[0].ResolvedPath)
			if targetErr != nil {
				t.Fatal(targetErr)
			}
			base := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
			git := &fetchRevalidationDriftGit{fetchRecordingGit: base, target: target, field: field, counts: map[string]int{}}
			value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{})
			if err == nil || fetchCalls(base) != "alpha" || value.Repositories[0].Status != AggregateStatusCompleted || value.Repositories[1].Status != AggregateStatusFailed {
				t.Fatalf("%s revalidation = %#v, %v, calls=%s", field, value, err, fetchCalls(base))
			}
		})
	}
}

func TestFetchDryRunCancellationStopsLaterAdvertisements(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git"), cancelAfterObserve: true, cancel: cancel}
	value, err := NewFetchServiceWith(git).Fetch(ctx, project, workspace, FetchRequest{DryRun: true})
	if !errors.Is(err, context.Canceled) || fetchObserveCalls(git) != "alpha" || value.Repositories[0].Status != AggregateStatusCanceled || value.Repositories[1].Status != AggregateStatusCanceled {
		t.Fatalf("dry cancellation = %#v, %v, calls=%s", value, err, fetchObserveCalls(git))
	}
}

func TestFetchJSONAndCallbackFactsAreDefensive(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git"), fetchErrors: map[string]error{"alpha": errors.New("first failure")}}
	value, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{OnComplete: func(entry FetchRepositoryResult) error {
		if entry.Failure != nil {
			entry.Failure.Message = "mutated callback"
		}
		return nil
	}})
	if err == nil || value.Repositories[0].Failure == nil || value.Repositories[0].Failure.Message == "mutated callback" {
		t.Fatalf("callback mutated result = %#v, %v", value, err)
	}
	encoded, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "repositories", "failure"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("fetch wire missing %q: %#v", key, wire)
		}
	}
	if wire["operation"] != "fetch" || wire["dryRun"] != false {
		t.Fatalf("fetch wire = %#v", wire)
	}
}

func TestFetchRefreshesNetworkFreeStatusFacts(t *testing.T) {
	project, workspace := fetchConfiguredWorkspace(t)
	for _, repository := range project.Repositories {
		advanceFetchSource(t, repository.SourcePath, repository.ID)
	}
	if _, err := NewFetchService().Fetch(context.Background(), project, workspace, FetchRequest{}); err != nil {
		t.Fatal(err)
	}
	tripwire := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
	status, err := NewStatusServiceWith(tripwire).Status(context.Background(), project, workspace)
	if err != nil || fetchCalls(tripwire) != "" || fetchObserveCalls(tripwire) != "" {
		t.Fatalf("status contacted remote: %#v, %v, fetch=%s observe=%s", status, err, fetchCalls(tripwire), fetchObserveCalls(tripwire))
	}
	for _, repository := range status.Repositories {
		if !repository.Upstream || repository.Behind != 1 || repository.Ahead != 0 {
			t.Fatalf("status after fetch %s = %#v", repository.ID, repository)
		}
	}
}

func TestFetchInventoryPermitsOnlyDeclaredTrackingRefs(t *testing.T) {
	t.Run("dry-run", func(t *testing.T) {
		project, workspace := fetchConfiguredWorkspace(t)
		before := snapshotFetchInventory(t, workspace)
		if _, err := NewFetchService().Fetch(context.Background(), project, workspace, FetchRequest{DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if after := snapshotFetchInventory(t, workspace); !reflect.DeepEqual(before, after) {
			t.Fatalf("dry-run changed inventory: before=%#v after=%#v", before, after)
		}
	})
	t.Run("execution", func(t *testing.T) {
		project, workspace := fetchConfiguredWorkspace(t)
		for _, repository := range project.Repositories {
			advanceFetchSource(t, repository.SourcePath, repository.ID)
		}
		before := snapshotFetchInventory(t, workspace)
		if _, err := NewFetchService().Fetch(context.Background(), project, workspace, FetchRequest{}); err != nil {
			t.Fatal(err)
		}
		after := snapshotFetchInventory(t, workspace)
		assertFetchInventoryDelta(t, before, after, map[string]string{"alpha": "refs/remotes/origin/main", "beta": "refs/remotes/origin/main"})
	})
}

// These are the non-transactional boundary cases.  They deliberately use the
// production adapter after a local bare-remote setup, so the same exhaustive
// inventory catches a broad fetch, FETCH_HEAD rewrite, index mutation, or any
// persisted-worktree mutation that a recording fake would hide.
func TestFetchInventoryFailureAndStopBoundaries(t *testing.T) {
	t.Run("ordinary first failure leaves later selected generation", func(t *testing.T) {
		project, workspace := fetchConfiguredWorkspace(t)
		for _, repository := range project.Repositories {
			advanceFetchSource(t, repository.SourcePath, repository.ID)
		}
		before := snapshotFetchInventory(t, workspace)
		git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git"), fetchErrors: map[string]error{"alpha": errors.New("first configured fetch failed")}}
		if _, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{}); err == nil || fetchCalls(git) != "alpha,beta" {
			t.Fatalf("ordinary failure error=%v calls=%s", err, fetchCalls(git))
		}
		assertFetchInventoryDelta(t, before, snapshotFetchInventory(t, workspace), map[string]string{"beta": "refs/remotes/origin/main"})
	})
	t.Run("malformed first upstream preflight leaves later selected generation", func(t *testing.T) {
		project, workspace := fetchConfiguredWorkspace(t)
		for _, repository := range project.Repositories {
			advanceFetchSource(t, repository.SourcePath, repository.ID)
		}
		fetchGit(t, workspace.Checkouts[1].ResolvedPath, "config", "--unset", "branch.exec-alpha.merge")
		before := snapshotFetchInventory(t, workspace)
		git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
		if _, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{}); err == nil || fetchCalls(git) != "beta" {
			t.Fatalf("malformed-upstream error=%v calls=%s", err, fetchCalls(git))
		}
		assertFetchInventoryDelta(t, before, snapshotFetchInventory(t, workspace), map[string]string{"beta": "refs/remotes/origin/main"})
	})
	t.Run("cancellation before and during work leaves no unowned mutation", func(t *testing.T) {
		for _, test := range []struct {
			name string
			run  func(context.Context, *fetchRecordingGit, domain.Project, domain.Workspace) error
		}{
			{name: "before", run: func(ctx context.Context, git *fetchRecordingGit, project domain.Project, workspace domain.Workspace) error {
				_, err := NewFetchServiceWith(git).Fetch(ctx, project, workspace, FetchRequest{})
				return err
			}},
			{name: "during", run: func(ctx context.Context, git *fetchRecordingGit, project domain.Project, workspace domain.Workspace) error {
				_, err := NewFetchServiceWith(git).Fetch(ctx, project, workspace, FetchRequest{})
				return err
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				project, workspace := fetchConfiguredWorkspace(t)
				before := snapshotFetchInventory(t, workspace)
				ctx, cancel := context.WithCancel(context.Background())
				git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git"), cancel: cancel}
				if test.name == "before" {
					cancel()
				} else {
					git.cancelAfterFetch = true
				}
				if err := test.run(ctx, git, project, workspace); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error=%v", err)
				}
				if after := snapshotFetchInventory(t, workspace); !reflect.DeepEqual(before, after) {
					t.Fatalf("%s cancellation changed inventory: before=%#v after=%#v", test.name, before, after)
				}
			})
		}
	})
	t.Run("writer after first settled row preserves only its completed generation", func(t *testing.T) {
		project, workspace := fetchConfiguredWorkspace(t)
		for _, repository := range project.Repositories {
			advanceFetchSource(t, repository.SourcePath, repository.ID)
		}
		before := snapshotFetchInventory(t, workspace)
		writerErr := errors.New("writer stopped after alpha")
		git := &fetchRecordingGit{Git: gitadapter.NewAdapter("git")}
		if _, err := NewFetchServiceWith(git).Fetch(context.Background(), project, workspace, FetchRequest{OnComplete: func(FetchRepositoryResult) error { return writerErr }}); !errors.Is(err, writerErr) || fetchCalls(git) != "alpha" {
			t.Fatalf("writer error=%v calls=%s", err, fetchCalls(git))
		}
		assertFetchInventoryDelta(t, before, snapshotFetchInventory(t, workspace), map[string]string{"alpha": "refs/remotes/origin/main"})
	})
}

func TestFetchPlanningObservationCancellationPrecedence(t *testing.T) {
	for _, observation := range []string{"common", "top", "branch", "head", "upstream"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			for _, later := range []bool{false, true} {
				t.Run(observation+"/"+cause.Error()+fmt.Sprintf("/later=%t", later), func(t *testing.T) {
					project, workspace := fetchConfiguredWorkspace(t)
					target := workspace.Checkouts[1].ResolvedPath
					if later {
						target = workspace.Checkouts[0].ResolvedPath
					}
					canonical, canonicalErr := filepath.EvalSymlinks(target)
					if canonicalErr != nil {
						t.Fatal(canonicalErr)
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					git := &fetchContextObservationGit{Git: gitadapter.NewAdapter("git"), target: canonical, observation: observation, cause: cause, cancel: cancel}
					value, err := NewFetchServiceWith(git).Fetch(ctx, project, workspace, FetchRequest{})
					if !errors.Is(err, context.Canceled) || value.Failure == nil || value.Failure.Code != ErrorInternal {
						t.Fatalf("%s = %#v, %v", observation, value, err)
					}
					for _, entry := range value.Repositories {
						if entry.Status != AggregateStatusCanceled || entry.Failure == nil || entry.Failure.Code != ErrorInternal {
							t.Fatalf("cancellation entry = %#v", entry)
						}
					}
				})
			}
		}
	}
}

// fetchInventory is intentionally broader than the service result. It is the
// fetch mutation boundary: every worktree path (including its .git pointer),
// every local Git authority, and the exact index/FETCH_HEAD bytes are captured
// before an operation. Fetch is allowed to change only the declared selected
// remote-tracking generation during execution.
type fetchInventory struct {
	Worktree map[string]fetchTreeEntry
	Git      map[string]fetchGitInventory
}

type fetchTreeEntry struct {
	Kind, Mode, Bytes string
}

type fetchGitInventory struct {
	Head, WriteTree, LocalRefs, RemoteRefs, Config string
	Index, FetchHead                               fetchTreeEntry
}

func snapshotFetchInventory(t *testing.T, workspace domain.Workspace) fetchInventory {
	t.Helper()
	value := fetchInventory{
		Worktree: snapshotFetchTree(t, workspace.RootPath),
		Git:      map[string]fetchGitInventory{},
	}
	for _, checkout := range workspace.Checkouts {
		id, path := checkout.RepositoryID, checkout.ResolvedPath
		value.Git[id] = fetchGitInventory{
			Head:       fetchGitOutput(t, path, "rev-parse", "HEAD"),
			WriteTree:  fetchGitOutput(t, path, "write-tree"),
			LocalRefs:  fetchGitOutput(t, path, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads"),
			RemoteRefs: fetchGitOutput(t, path, "for-each-ref", "--format=%(refname):%(objectname)", "refs/remotes"),
			Config:     fetchGitOutput(t, path, "config", "--list", "--local"),
			Index:      snapshotFetchPath(t, strings.TrimSpace(fetchGitOutput(t, path, "rev-parse", "--path-format=absolute", "--git-path", "index"))),
			FetchHead:  snapshotFetchPath(t, strings.TrimSpace(fetchGitOutput(t, path, "rev-parse", "--path-format=absolute", "--git-path", "FETCH_HEAD"))),
		}
	}
	return value
}

func assertFetchInventoryDelta(t *testing.T, before, after fetchInventory, allowedRemoteRefs map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before.Worktree, after.Worktree) {
		t.Fatalf("fetch changed worktree paths: before=%#v after=%#v", before.Worktree, after.Worktree)
	}
	if !reflect.DeepEqual(sortedFetchInventoryIDs(before.Git), sortedFetchInventoryIDs(after.Git)) {
		t.Fatalf("fetch changed checkout inventory membership: before=%#v after=%#v", before.Git, after.Git)
	}
	for id, beforeGit := range before.Git {
		afterGit := after.Git[id]
		if beforeGit.Head != afterGit.Head || beforeGit.WriteTree != afterGit.WriteTree || beforeGit.LocalRefs != afterGit.LocalRefs || beforeGit.Config != afterGit.Config || !reflect.DeepEqual(beforeGit.Index, afterGit.Index) || !reflect.DeepEqual(beforeGit.FetchHead, afterGit.FetchHead) {
			t.Fatalf("fetch changed protected Git authority for %s: before=%#v after=%#v", id, beforeGit, afterGit)
		}
		allowed, found := allowedRemoteRefs[id]
		if !found {
			if beforeGit.RemoteRefs != afterGit.RemoteRefs {
				t.Fatalf("fetch changed undeclared remote refs for %s: before=%q after=%q", id, beforeGit.RemoteRefs, afterGit.RemoteRefs)
			}
			continue
		}
		if beforeGit.RemoteRefs == afterGit.RemoteRefs || !fetchRemoteRefDeltaOnly(beforeGit.RemoteRefs, afterGit.RemoteRefs, allowed) {
			t.Fatalf("fetch changed remote refs outside %s for %s: before=%q after=%q", allowed, id, beforeGit.RemoteRefs, afterGit.RemoteRefs)
		}
	}
}

func sortedFetchInventoryIDs(values map[string]fetchGitInventory) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func fetchRemoteRefDeltaOnly(before, after, allowed string) bool {
	parse := func(value string) map[string]string {
		entries := map[string]string{}
		for _, line := range strings.Fields(value) {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				entries[parts[0]] = parts[1]
			}
		}
		return entries
	}
	left, right := parse(before), parse(after)
	for ref, value := range left {
		if ref != allowed && right[ref] != value {
			return false
		}
	}
	for ref, value := range right {
		if ref != allowed && left[ref] != value {
			return false
		}
	}
	return true
}

func snapshotFetchTree(t *testing.T, root string) map[string]fetchTreeEntry {
	t.Helper()
	files := map[string]fetchTreeEntry{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if relative == "." {
			return nil
		}
		key := filepath.ToSlash(relative)
		files[key] = snapshotFetchPath(t, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func snapshotFetchPath(t *testing.T, path string) fetchTreeEntry {
	t.Helper()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fetchTreeEntry{Kind: "absent"}
	}
	if err != nil {
		t.Fatal(err)
	}
	value := fetchTreeEntry{Mode: fmt.Sprintf("%#o", info.Mode())}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, targetErr := os.Readlink(path)
		if targetErr != nil {
			t.Fatal(targetErr)
		}
		value.Kind, value.Bytes = "symlink", fmt.Sprintf("%x", target)
	case info.IsDir():
		value.Kind = "directory"
	default:
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		value.Kind, value.Bytes = "file", fmt.Sprintf("%x", data)
	}
	return value
}
func fetchGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return string(output)
}

func fetchConfiguredWorkspace(t *testing.T) (domain.Project, domain.Workspace) {
	t.Helper()
	project, workspace := execTestWorkspace(t)
	for _, checkout := range workspace.Checkouts {
		repository := project.Repositories[0]
		for _, candidate := range project.Repositories {
			if candidate.ID == checkout.RepositoryID {
				repository = candidate
				break
			}
		}
		fetchGit(t, checkout.ResolvedPath, "remote", "add", "origin", repository.SourcePath)
		fetchGit(t, checkout.ResolvedPath, "config", "branch."+checkout.Branch+".remote", "origin")
		fetchGit(t, checkout.ResolvedPath, "config", "branch."+checkout.Branch+".merge", "refs/heads/main")
		fetchHead := strings.TrimSpace(fetchGitOutput(t, checkout.ResolvedPath, "rev-parse", "--path-format=absolute", "--git-path", "FETCH_HEAD"))
		if err := os.WriteFile(fetchHead, []byte("sentinel FETCH_HEAD for fetch boundary\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return project, workspace
}

func fetchConfiguredWorkspaceWithGrandchild(t *testing.T) (domain.Project, domain.Workspace) {
	t.Helper()
	project, workspace := fetchConfiguredWorkspace(t)
	gamma := testutil.NewGitRepository(t)
	gamma.CommitFile("gamma.txt", "gamma\n", "gamma")
	gammaPath := filepath.Join(workspace.RootPath, "alpha", "beta", "gamma")
	gamma.Run(t, "worktree", "add", "-b", "exec-gamma", gammaPath, "HEAD")
	adapter := gitadapter.NewAdapter("git")
	gammaCommon, err := adapter.CommonGitDir(context.Background(), gammaPath)
	if err != nil {
		t.Fatal(err)
	}
	gammaHead, err := adapter.Head(context.Background(), gammaPath)
	if err != nil {
		t.Fatal(err)
	}
	project.Repositories = append(project.Repositories, domain.Repository{ID: "gamma", ParentID: "beta", CommonGitDir: gammaCommon, SourcePath: gamma.Path, DefaultMount: "gamma", DefaultBranch: "main"})
	workspace.Checkouts = append(workspace.Checkouts, domain.Checkout{RepositoryID: "gamma", Branch: "exec-gamma", Head: gammaHead, Mount: "gamma", ResolvedPath: gammaPath})
	fetchGit(t, gammaPath, "remote", "add", "origin", gamma.Path)
	fetchGit(t, gammaPath, "config", "branch.exec-gamma.remote", "origin")
	fetchGit(t, gammaPath, "config", "branch.exec-gamma.merge", "refs/heads/main")
	return project, workspace
}

type fetchRecordingGit struct {
	gitadapter.Git
	mu                 sync.Mutex
	calls              []string
	observeCalls       []string
	fetchErrors        map[string]error
	cancelAfterFetch   bool
	cancelAfterObserve bool
	cancel             func()
}

type fetchRevalidationDriftGit struct {
	*fetchRecordingGit
	target, field string
	counts        map[string]int
}

type fetchContextObservationGit struct {
	gitadapter.Git
	target, observation string
	cause               error
	cancel              func()
}

func (git *fetchContextObservationGit) fail(path, name string) error {
	if path != git.target || name != git.observation {
		return nil
	}
	git.cancel()
	return fmt.Errorf("adapter %s: %w", name, git.cause)
}
func (git *fetchContextObservationGit) CommonGitDir(ctx context.Context, path string) (string, error) {
	if err := git.fail(path, "common"); err != nil {
		return "", err
	}
	return git.Git.CommonGitDir(ctx, path)
}
func (git *fetchContextObservationGit) TopLevel(ctx context.Context, path string) (string, error) {
	if err := git.fail(path, "top"); err != nil {
		return "", err
	}
	return git.Git.TopLevel(ctx, path)
}
func (git *fetchContextObservationGit) CurrentBranch(ctx context.Context, path string) (string, bool, error) {
	if err := git.fail(path, "branch"); err != nil {
		return "", false, err
	}
	return git.Git.CurrentBranch(ctx, path)
}
func (git *fetchContextObservationGit) Head(ctx context.Context, path string) (string, error) {
	if err := git.fail(path, "head"); err != nil {
		return "", err
	}
	return git.Git.Head(ctx, path)
}
func (git *fetchContextObservationGit) Upstream(ctx context.Context, path string) (gitadapter.Upstream, error) {
	if err := git.fail(path, "upstream"); err != nil {
		return gitadapter.Upstream{}, err
	}
	return git.Git.Upstream(ctx, path)
}

func (git *fetchRevalidationDriftGit) drifting(method, path string) bool {
	if path != git.target {
		return false
	}
	git.counts[method]++
	return git.counts[method] > 1
}
func (git *fetchRevalidationDriftGit) CommonGitDir(ctx context.Context, path string) (string, error) {
	value, err := git.Git.CommonGitDir(ctx, path)
	if err == nil && git.field == "common" && git.drifting("common", path) {
		return "/changed/common", nil
	}
	return value, err
}
func (git *fetchRevalidationDriftGit) TopLevel(ctx context.Context, path string) (string, error) {
	value, err := git.Git.TopLevel(ctx, path)
	if err == nil && git.field == "top" && git.drifting("top", path) {
		return path + "/changed", nil
	}
	return value, err
}
func (git *fetchRevalidationDriftGit) CurrentBranch(ctx context.Context, path string) (string, bool, error) {
	branch, detached, err := git.Git.CurrentBranch(ctx, path)
	if err == nil && git.drifting("branch", path) {
		if git.field == "branch" {
			return "changed", false, nil
		}
		if git.field == "detached" {
			return "", true, nil
		}
	}
	return branch, detached, err
}
func (git *fetchRevalidationDriftGit) Head(ctx context.Context, path string) (string, error) {
	value, err := git.Git.Head(ctx, path)
	if err == nil && git.field == "head" && git.drifting("head", path) {
		return strings.Repeat("0", 40), nil
	}
	return value, err
}
func (git *fetchRevalidationDriftGit) Upstream(ctx context.Context, path string) (gitadapter.Upstream, error) {
	value, err := git.Git.Upstream(ctx, path)
	if err == nil && git.drifting("upstream", path) {
		switch git.field {
		case "upstream-branch":
			value.LocalBranch = "changed"
		case "upstream-remote":
			value.Remote = "changed"
		case "upstream-ref":
			value.Merge = "refs/heads/changed"
		}
	}
	return value, err
}

func (git *fetchRecordingGit) FetchConfiguredRef(ctx context.Context, path, remote, ref string) (gitadapter.ConfiguredRefFetch, error) {
	git.mu.Lock()
	git.calls = append(git.calls, fetchIDForPath(path))
	failure := git.fetchErrors[fetchIDForPath(path)]
	cancel := git.cancelAfterFetch
	stop := git.cancel
	git.mu.Unlock()
	if cancel {
		stop()
		return gitadapter.ConfiguredRefFetch{}, fmt.Errorf("late cancellation: %w", context.Canceled)
	}
	if failure != nil {
		return gitadapter.ConfiguredRefFetch{}, failure
	}
	return git.Git.FetchConfiguredRef(ctx, path, remote, ref)
}
func (git *fetchRecordingGit) ObserveConfiguredRef(ctx context.Context, path, remote, ref string) (gitadapter.ConfiguredRefObservation, error) {
	git.mu.Lock()
	git.observeCalls = append(git.observeCalls, fetchIDForPath(path))
	cancel := git.cancelAfterObserve
	stop := git.cancel
	git.mu.Unlock()
	if cancel {
		stop()
		return gitadapter.ConfiguredRefObservation{}, fmt.Errorf("late cancellation: %w", context.Canceled)
	}
	return git.Git.ObserveConfiguredRef(ctx, path, remote, ref)
}
func (git *fetchRecordingGit) callsCopy() []string {
	git.mu.Lock()
	defer git.mu.Unlock()
	return append([]string(nil), git.calls...)
}
func fetchCalls(git *fetchRecordingGit) string { return strings.Join(git.callsCopy(), ",") }
func fetchObserveCalls(git *fetchRecordingGit) string {
	git.mu.Lock()
	defer git.mu.Unlock()
	return strings.Join(append([]string(nil), git.observeCalls...), ",")
}
func fetchIDForPath(path string) string {
	return filepath.Base(path)
}

func fetchIDs(value FetchResult) string {
	ids := make([]string, 0, len(value.Repositories))
	for _, entry := range value.Repositories {
		ids = append(ids, entry.ID)
	}
	return strings.Join(ids, ",")
}
func fetchGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
