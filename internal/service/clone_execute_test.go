package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
	"github.com/definebusiness/wtree/internal/transaction"
)

func TestCloneExecuteThreeLevelExactPlanPublishesConfigStateAndRegistry(t *testing.T) {
	base := t.TempDir()
	api := newCloneExecutionRemote(t, "api-local", "api-published", map[string]string{"api.txt": "api\n", ".gitignore": "/shared/\n"})
	shared := newCloneExecutionRemote(t, "shared-local", "shared-published", map[string]string{"shared.txt": "shared\n"})
	rootRepository := testutil.NewGitRepository(t)
	writeAndCommitCloneFiles(t, rootRepository.Path, map[string]string{"README.md": "root\n", ".gitignore": "/.wtree.yml\n/backend/\n"}, "root identity")
	rootIdentity := cloneGitOutput(t, rootRepository.Path, "rev-parse", "HEAD")
	rootRemote := testutil.NewBareGitRemote(t)

	manifest := config.PortableManifest{
		Version: config.PortableManifestVersion,
		Project: config.PortableProject{ID: "clone-project", Name: "clone-project", BaseRepository: "root"},
		Repositories: map[string]config.PortableRepository{
			"root": {
				Clone: config.CloneSource{Remote: "source-root", URL: rootRemote}, Upstream: config.Upstream{Branch: "root-local", Remote: "source-root", Merge: "refs/heads/root-published"},
				Identity: config.RepositoryIdentity{InitialCommits: []string{rootIdentity}}, Mount: ".", DefaultBranch: "root-local",
			},
			"api": {
				Clone: config.CloneSource{Remote: "source-api", URL: api.remote}, Upstream: config.Upstream{Branch: "api-local", Remote: "source-api", Merge: "refs/heads/api-published"},
				Identity: config.RepositoryIdentity{InitialCommits: []string{api.identity}}, Parent: "root", Mount: "backend", DefaultBranch: "api-local",
			},
			"shared": {
				Clone: config.CloneSource{Remote: "source-shared", URL: shared.remote}, Upstream: config.Upstream{Branch: "shared-local", Remote: "source-shared", Merge: "refs/heads/shared-published"},
				Identity: config.RepositoryIdentity{InitialCommits: []string{shared.identity}}, Parent: "api", Mount: "shared", DefaultBranch: "shared-local",
			},
		},
	}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, rootRepository.Path, map[string]string{"project.wtree.yml": string(manifestBytes)}, "portable manifest")
	cloneGit(t, rootRepository.Path, "remote", "add", "publish", rootRemote)
	cloneGit(t, rootRepository.Path, "push", "publish", "HEAD:refs/heads/root-published")
	manifestPath := filepath.Join(rootRepository.Path, "project.wtree.yml")
	destination := filepath.Join(base, "materialized")
	dataDir := filepath.Join(base, "data")
	plan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: manifestPath, Destination: destination, CWD: base, DataDir: dataDir, WorktreeRoot: filepath.Join(base, "worktrees")})
	if err != nil {
		t.Fatal(err)
	}

	var events []transaction.Event
	result, err := NewCloneExecutor().Execute(context.Background(), plan, func(event transaction.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != "clone-project" || result.Destination != plan.Destination.Path {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(plan.Destination.Path, "backend", "shared", "shared.txt")); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.ReadProjectFile(filepath.Join(plan.Destination.Path, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Manifest.Source != manifestPath || configuration.Repositories["api"].Source != "backend" || configuration.Repositories["shared"].Source != "backend/shared" {
		t.Fatalf("local config = %#v", configuration)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(dataDir, "clone-project", "default"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Path != plan.Destination.Path || state.Repositories["shared"].Branch != "shared-local" || state.Repositories["shared"].ResolvedPath != filepath.Join(plan.Destination.Path, "backend", "shared") {
		t.Fatalf("state = %#v", state)
	}
	registry, err := store.ReadRegistry(filepath.Join(dataDir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	registered := registry.Projects["clone-project"]
	if registered.ConfigPath != filepath.Join(plan.Destination.Path, ".wtree.yml") || len(registered.RepositoryIDs) != 3 {
		t.Fatalf("registry = %#v", registry)
	}
	adapter := gitadapter.NewAdapter("git")
	for _, repository := range plan.Repositories {
		path := repository.Path
		head, err := adapter.Head(context.Background(), path)
		if err != nil || head != repository.AdvertisedCommit {
			t.Fatalf("%s head = %q, %v", repository.ID, head, err)
		}
		clean, err := adapter.IsClean(context.Background(), path)
		if err != nil || !clean {
			t.Fatalf("%s clean = %v, %v", repository.ID, clean, err)
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".wtree-clone-") {
			t.Fatalf("staging residue %q", entry.Name())
		}
	}
	if len(events) == 0 {
		t.Fatal("expected bounded progress events")
	}
}

func TestCloneExecuteRejectsConcurrentRootMetadataMutationAfterAtomicRename(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	executor := NewCloneExecutorWith(CloneExecutorDependencies{
		Git: &cloneExecutionGit{plan: plan},
		AfterEffect: func(step string) error {
			if step != "destination-rename" {
				return nil
			}
			future := time.Now().Add(time.Second)
			if err := os.Chtimes(plan.Destination.Path, future, future); err != nil {
				return err
			}
			return nil
		},
	})
	_, err := executor.Execute(context.Background(), plan, nil)
	if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
		t.Fatalf("concurrent root metadata mutation error = %v, want rollback-incomplete", err)
	}
	if _, statErr := os.Stat(plan.Destination.Path); statErr != nil {
		t.Fatalf("concurrently changed destination was removed: %v", statErr)
	}
}

func TestCloneExecuteRenameSeamRootChtimesBeforeReturnRetainsDestination(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	executor := NewCloneExecutorWith(CloneExecutorDependencies{
		Git: &cloneExecutionGit{plan: plan},
		Rename: func(staging, destination string) error {
			if err := os.Rename(staging, destination); err != nil {
				return err
			}
			changed := time.Now().Add(time.Hour).Round(0)
			return os.Chtimes(destination, changed, changed)
		},
		WriteWorkspaceCAS: func(string, store.WorkspaceState, func() error) (ClonePublicationReceipt, error) {
			return ClonePublicationReceipt{}, os.ErrPermission
		},
	})

	_, err := executor.Execute(context.Background(), plan, nil)
	if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
		t.Fatalf("error = %v, want rollback-incomplete after retaining changed destination", err)
	}
	if _, statErr := os.Lstat(plan.Destination.Path); statErr != nil {
		t.Fatalf("destination removed after rename-seam root metadata mutation: %v", statErr)
	}
}

func TestCloneExecuteRootOnlyLocalRemote(t *testing.T) {
	base := t.TempDir()
	repository := testutil.NewGitRepository(t)
	writeAndCommitCloneFiles(t, repository.Path, map[string]string{".gitignore": "/.wtree.yml\n", "README.md": "root\n"}, "identity")
	identity := cloneGitOutput(t, repository.Path, "rev-parse", "HEAD")
	remote := testutil.NewBareGitRemote(t)
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "root-only", Name: "root-only", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {
		Clone: config.CloneSource{Remote: "bootstrap", URL: remote}, Upstream: config.Upstream{Branch: "local-main", Remote: "bootstrap", Merge: "refs/heads/published-main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{identity}}, Mount: ".", DefaultBranch: "local-main",
	}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, repository.Path, map[string]string{"project.wtree.yml": string(data)}, "manifest")
	cloneGit(t, repository.Path, "push", remote, "HEAD:refs/heads/published-main")
	dataDir := filepath.Join(base, "data")
	plan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: filepath.Join(repository.Path, "project.wtree.yml"), Destination: filepath.Join(base, "clone"), CWD: base, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewCloneExecutor().Execute(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories["root"].Head != plan.Repositories[0].AdvertisedCommit {
		t.Fatalf("result = %#v", result)
	}
	if _, err := store.ReadWorkspace(WorkspaceStatePath(dataDir, "root-only", "default")); err != nil {
		t.Fatal(err)
	}
}

func TestCloneExecuteRejectsByteDifferentServedV2ManifestAndCleansUp(t *testing.T) {
	base := t.TempDir()
	repository := testutil.NewGitRepository(t)
	writeAndCommitCloneFiles(t, repository.Path, map[string]string{".gitignore": "/.wtree.yml\n", "README.md": "root\n"}, "identity")
	identity := cloneGitOutput(t, repository.Path, "rev-parse", "HEAD")
	remote := testutil.NewBareGitRemote(t)
	tracked := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "served-mismatch", Name: "served-mismatch", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {
		Clone: config.CloneSource{Remote: "source", URL: remote}, Upstream: config.Upstream{Branch: "local-main", Remote: "source", Merge: "refs/heads/published-main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{identity}}, Mount: ".", DefaultBranch: "local-main",
	}}}
	trackedBytes, err := config.MarshalPortableManifest(tracked)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, repository.Path, map[string]string{"project.wtree.yml": string(trackedBytes)}, "tracked manifest")
	cloneGit(t, repository.Path, "push", remote, "HEAD:refs/heads/published-main")

	servedBytes := append(append([]byte(nil), trackedBytes...), []byte("# valid but byte-distinct served manifest\n")...)
	if string(servedBytes) == string(trackedBytes) {
		t.Fatal("served manifest is not byte-distinct")
	}
	if _, err := config.LoadPortableManifest(servedBytes); err != nil {
		t.Fatalf("served v2 manifest is invalid: %v", err)
	}
	source := writeClonePlanManifest(t, base, servedBytes)
	destination := filepath.Join(base, "clone")
	dataDir := filepath.Join(base, "data")
	plan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: destination, CWD: base, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewCloneExecutor().Execute(context.Background(), plan, nil)
	if err == nil || !strings.Contains(err.Error(), "root tracked manifest does not equal the fetched manifest") || !HasCleanRollback(err) {
		t.Fatalf("served manifest mismatch error = %v", err)
	}
	for _, path := range []string{destination, dataDir, filepath.Join(dataDir, "registry.json"), WorkspaceStatePath(dataDir, "served-mismatch", "default")} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("mismatch clone retained published state %q: %v", path, statErr)
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".clone.wtree-clone-") {
			t.Fatalf("mismatch clone retained staging path %q", entry.Name())
		}
	}
}

func TestCloneExecuteFailureAndCancellationRemoveOnlyOwnedStaging(t *testing.T) {
	base := t.TempDir()
	preexisting := filepath.Join(base, "preexisting")
	if err := os.Mkdir(preexisting, 0o755); err != nil {
		t.Fatal(err)
	}
	want := mustDirectorySnapshot(t, preexisting)
	rootURL, childURL := filepath.Join(base, "missing-root.git"), filepath.Join(base, "missing-api.git")
	source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
	plan := mustClonePlan(t, NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)}), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	executor := NewCloneExecutorWith(CloneExecutorDependencies{BeforeEffect: func(step string) error {
		if step == "repository-root-clone" {
			return context.Canceled
		}
		return nil
	}})
	_, err := executor.Execute(context.Background(), plan, nil)
	if err == nil || !HasCleanRollback(err) {
		t.Fatalf("error = %v, want clean rollback", err)
	}
	if got := mustDirectorySnapshot(t, preexisting); !reflect.DeepEqual(got, want) {
		t.Fatalf("pre-existing path changed: %#v", got)
	}
	if _, err := os.Lstat(plan.Destination.Path); !os.IsNotExist(err) {
		t.Fatalf("destination exists after rollback: %v", err)
	}
}

func TestCloneExecuteEveryInjectedBoundaryRollsBack(t *testing.T) {
	steps := []string{"staging-create", "repository-root-clone", "repository-root-fetch", "repository-root-checkout", "repository-root-verify", "repository-api-parent-ignore", "repository-api-clone", "repository-api-fetch", "repository-api-checkout", "repository-api-verify", "local-config-write", "publication-lock", "destination-rename", "final-identity", "state-write", "registry-write"}
	for _, timing := range []string{"before", "after"} {
		for _, step := range steps {
			t.Run(timing+"-"+step, func(t *testing.T) {
				plan := syntheticExecutableClonePlan(t)
				fake := &cloneExecutionGit{plan: plan}
				hook := func(actual string) error {
					if actual == step {
						return context.Canceled
					}
					return nil
				}
				dependencies := CloneExecutorDependencies{Git: fake}
				if timing == "before" {
					dependencies.BeforeEffect = hook
				} else {
					dependencies.AfterEffect = hook
				}
				var events []transaction.Event
				_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, func(event transaction.Event) { events = append(events, event) })
				if err == nil || !HasCleanRollback(err) {
					t.Fatalf("error = %v, want clean rollback", err)
				}
				assertCloneProgressPairs(t, events, step, transaction.ExecuteFailed)
				assertCloneExecutionAbsent(t, plan)
			})
		}
	}
}

func assertCloneProgressPairs(t *testing.T, events []transaction.Event, terminalStep string, terminalKind transaction.EventKind) {
	t.Helper()
	if len(events)%2 != 0 {
		t.Fatalf("progress event count is not paired: %#v", events)
	}
	for index := 0; index < len(events); index += 2 {
		started, terminal := events[index], events[index+1]
		if started.Kind != transaction.ExecuteStarted || terminal.Step != started.Step || terminal.Kind != transaction.ExecuteSucceeded && terminal.Kind != transaction.ExecuteFailed {
			t.Fatalf("unpaired progress at %d: %#v", index, events)
		}
		if started.Step == terminalStep && terminal.Kind != terminalKind {
			t.Fatalf("terminal event for %q = %#v", terminalStep, terminal)
		}
	}
}

func TestCloneExecuteRejectsEveryRepositoryVerificationClass(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cloneExecutionGit)
	}{
		{"stale-root-manifest", func(git *cloneExecutionGit) { git.staleManifest = true }},
		{"missing-parent-ignore", func(git *cloneExecutionGit) { git.missingIgnore = true }},
		{"wrong-initial-roots", func(git *cloneExecutionGit) { git.wrongRoots = true }},
		{"dirty", func(git *cloneExecutionGit) { git.dirty = true }},
		{"submodule", func(git *cloneExecutionGit) { git.submodule = true }},
		{"unexpected-branch", func(git *cloneExecutionGit) { git.wrongBranch = true }},
		{"unexpected-upstream", func(git *cloneExecutionGit) { git.wrongUpstream = true }},
		{"planned-commit-unavailable", func(git *cloneExecutionGit) { git.fetchFailure = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			fake := &cloneExecutionGit{plan: plan}
			test.mutate(fake)
			_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: fake}).Execute(context.Background(), plan, nil)
			if err == nil || !HasCleanRollback(err) {
				t.Fatalf("error = %v, want rejected clean rollback", err)
			}
			assertCloneExecutionAbsent(t, plan)
		})
	}
}

func TestCloneExecutePartialStoreWritesAreRemovedExactly(t *testing.T) {
	for _, boundary := range []string{"state", "registry"} {
		t.Run(boundary, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			dependencies := CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}
			if boundary == "state" {
				dependencies.WriteWorkspaceCAS = func(path string, value store.WorkspaceState, compare func() error) (ClonePublicationReceipt, error) {
					if err := compare(); err != nil {
						return ClonePublicationReceipt{}, err
					}
					if err := store.WriteWorkspace(path, value); err != nil {
						return ClonePublicationReceipt{}, err
					}
					snapshot, err := secureCloneFileSnapshot(path)
					return ClonePublicationReceipt{snapshot: snapshot}, errors.Join(context.Canceled, err)
				}
			} else {
				dependencies.WriteRegistryCAS = func(path string, value store.Registry, compare func() error) (ClonePublicationReceipt, error) {
					if err := compare(); err != nil {
						return ClonePublicationReceipt{}, err
					}
					if err := store.WriteRegistry(path, value); err != nil {
						return ClonePublicationReceipt{}, err
					}
					snapshot, err := secureCloneFileSnapshot(path)
					return ClonePublicationReceipt{snapshot: snapshot}, errors.Join(context.Canceled, err)
				}
			}
			_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
			if err == nil || !HasCleanRollback(err) {
				t.Fatalf("error = %v, want clean rollback", err)
			}
			assertCloneExecutionAbsent(t, plan)
		})
	}
}

func TestCloneExecuteCleanupFailureLeavesDurableRecoveryEvidence(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	executor := NewCloneExecutorWith(CloneExecutorDependencies{
		Git: &cloneExecutionGit{plan: plan}, RemoveAll: func(string) error { return os.ErrPermission },
		AfterEffect: func(step string) error {
			if step == "registry-write" {
				return context.Canceled
			}
			return nil
		},
	})
	_, err := executor.Execute(context.Background(), plan, nil)
	if err == nil || HasCleanRollback(err) || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
		t.Fatalf("error = %v, want rollback-incomplete", err)
	}
	if _, statErr := os.Stat(plan.Destination.Path); statErr != nil {
		t.Fatalf("retained destination missing: %v", statErr)
	}
	recoveryPath := filepath.Join(plan.DataDir, "projects", plan.Project.ID, "recovery", "default.json")
	recovery, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || recovery.Operation != "clone" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"destination"}) {
		t.Fatalf("recovery = %#v, %v", recovery, readErr)
	}
}

func TestCloneExecuteSameDestinationRaceHasExactlyOneWinner(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	var wait sync.WaitGroup
	wait.Add(2)
	start := make(chan struct{})
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			<-start
			_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}).Execute(context.Background(), plan, nil)
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	wins := 0
	for err := range errors {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful clones = %d, want exactly one", wins)
	}
	if _, err := os.Stat(plan.Destination.Path); err != nil {
		t.Fatal(err)
	}
}

func TestCloneStagingPathSafetyUsesParentIdentity(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ".clone.wtree-clone-"
	staging, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		t.Fatal(err)
	}
	stagingInfo, err := os.Lstat(staging)
	if err != nil {
		t.Fatal(err)
	}
	if !cloneStagingPathIsSafe(staging, prefix, stagingInfo, parentInfo, os.Lstat) {
		t.Fatal("owned staging directory was rejected")
	}

	externalParent := t.TempDir()
	external, err := os.MkdirTemp(externalParent, prefix)
	if err != nil {
		t.Fatal(err)
	}
	externalInfo, err := os.Lstat(external)
	if err != nil {
		t.Fatal(err)
	}
	if cloneStagingPathIsSafe(external, prefix, externalInfo, parentInfo, os.Lstat) {
		t.Fatal("staging directory under an external sibling was accepted")
	}
	if cloneStagingPathIsSafe(filepath.Base(staging), prefix, stagingInfo, parentInfo, os.Lstat) {
		t.Fatal("relative staging path was accepted")
	}
	traversal := filepath.Join(parent, "child") + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(staging)
	if cloneStagingPathIsSafe(traversal, prefix, stagingInfo, parentInfo, os.Lstat) {
		t.Fatal("unclean staging path was accepted")
	}

	link := filepath.Join(parent, prefix+"link")
	if err := os.Symlink(staging, link); err == nil {
		linkInfo, statErr := os.Lstat(link)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if cloneStagingPathIsSafe(link, prefix, linkInfo, parentInfo, os.Lstat) {
			t.Fatal("staging symlink was accepted")
		}
	}
}

func TestCloneExecuteDifferentProjectsDoNotSerializeRemoteEffects(t *testing.T) {
	dataDir := t.TempDir()
	var plans []ClonePlan
	for _, projectID := range []string{"parallel-a", "parallel-b"} {
		base := t.TempDir()
		url := filepath.Join(base, projectID+".git")
		manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: projectID, Name: projectID, BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {
			Clone: config.CloneSource{Remote: "source", URL: url}, Upstream: config.Upstream{Branch: "main", Remote: "source", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main",
		}}}
		manifestData, err := config.MarshalPortableManifest(manifest)
		if err != nil {
			t.Fatal(err)
		}
		source := writeClonePlanManifest(t, base, manifestData)
		remote := &clonePlanRemote{commits: map[string]string{url + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
		plans = append(plans, mustClonePlan(t, NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: dataDir}))
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	errors := make(chan error, 2)
	for _, plan := range plans {
		plan := plan
		go func() {
			fake := &cloneExecutionGit{plan: plan, onClone: func() { entered <- struct{}{}; <-release }}
			_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: fake}).Execute(context.Background(), plan, nil)
			errors <- err
		}()
	}
	waitForCloneEntries(t, entered, 2)
	close(release)
	for range 2 {
		if err := waitForCloneResult(t, errors); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := store.ReadRegistry(filepath.Join(dataDir, "registry.json"))
	if err != nil || len(registry.Projects) != 2 {
		t.Fatalf("registry = %#v, %v", registry, err)
	}
}

func TestCloneExecuteSameProjectDifferentDestinationsHasOneWinner(t *testing.T) {
	dataDir := t.TempDir()
	url := filepath.Join(t.TempDir(), "same.git")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "same-project", Name: "same-project", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {
		Clone: config.CloneSource{Remote: "source", URL: url}, Upstream: config.Upstream{Branch: "main", Remote: "source", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{clonePlanRootCommit}}, Mount: ".", DefaultBranch: "main",
	}}}
	manifestData, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var plans []ClonePlan
	for range 2 {
		base := t.TempDir()
		source := writeClonePlanManifest(t, base, manifestData)
		remote := &clonePlanRemote{commits: map[string]string{url + "\x00refs/heads/main": clonePlanRootCommit}, errors: map[string]error{}}
		plans = append(plans, mustClonePlan(t, NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: dataDir}))
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	errors := make(chan error, 2)
	for _, plan := range plans {
		plan := plan
		go func() {
			fake := &cloneExecutionGit{plan: plan, onClone: func() { entered <- struct{}{}; <-release }}
			_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: fake}).Execute(context.Background(), plan, nil)
			errors <- err
		}()
	}
	waitForCloneEntries(t, entered, 2)
	close(release)
	wins := 0
	for range 2 {
		if err := waitForCloneResult(t, errors); err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful same-project clones = %d, want one", wins)
	}
}

func waitForCloneEntries(t *testing.T, entered <-chan struct{}, count int) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for range count {
		select {
		case <-entered:
		case <-timer.C:
			t.Fatal("clone remote effects did not become live within 5 seconds")
		}
	}
}

func waitForCloneResult(t *testing.T, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("clone execution did not complete within 5 seconds after release")
		return nil
	}
}

func TestCloneExecutePublicationRevalidationPreservesDestinationAttacker(t *testing.T) {
	for _, boundary := range []string{"publication-lock", "destination-rename"} {
		for _, kind := range []string{"directory", "symlink"} {
			t.Run(boundary+"-"+kind, func(t *testing.T) {
				plan := syntheticExecutableClonePlan(t)
				executor := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, BeforeEffect: func(step string) error {
					if step != boundary {
						return nil
					}
					if kind == "directory" {
						return os.Mkdir(plan.Destination.Path, 0o755)
					}
					return os.Symlink(plan.Destination.Parent, plan.Destination.Path)
				}})
				_, err := executor.Execute(context.Background(), plan, nil)
				if err == nil || !HasCleanRollback(err) {
					t.Fatalf("error = %v, want clean conflict rollback", err)
				}
				info, statErr := os.Lstat(plan.Destination.Path)
				if statErr != nil {
					t.Fatal(statErr)
				}
				if kind == "symlink" && info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("attacker symlink replaced: %v", info.Mode())
				}
				if kind == "directory" && !info.IsDir() {
					t.Fatalf("attacker directory replaced: %v", info.Mode())
				}
			})
		}
	}
}

func TestCloneExecutePublicationRevalidationMergesUnrelatedConcurrentRegistry(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	registryPath := filepath.Join(plan.DataDir, "registry.json")
	concurrent := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"other": {Name: "other", ConfigPath: filepath.Join(plan.Destination.Parent, "other", ".wtree.yml"), RepositoryIDs: map[string]string{"identity": "root"}}}}
	executor := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, BeforeEffect: func(step string) error {
		if step == "publication-lock" {
			return store.WriteRegistry(registryPath, concurrent)
		}
		return nil
	}})
	_, err := executor.Execute(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := store.ReadRegistry(registryPath)
	if readErr != nil || got.Projects["other"].Name != "other" || got.Projects[plan.Project.ID].Name != plan.Project.Name {
		t.Fatalf("registry = %#v, %v; want merged values", got, readErr)
	}
	if _, statErr := os.Lstat(plan.Destination.Path); statErr != nil {
		t.Fatalf("destination missing: %v", statErr)
	}
}

func TestCloneExecutePublicationRevalidationPreservesConflictingRegistry(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	registryPath := filepath.Join(plan.DataDir, "registry.json")
	conflicting := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{plan.Project.ID: {Name: "attacker", ConfigPath: filepath.Join(plan.Destination.Parent, "attacker", ".wtree.yml"), RepositoryIDs: map[string]string{"attacker": "root"}}}}
	executor := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, BeforeEffect: func(step string) error {
		if step == "publication-lock" {
			return store.WriteRegistry(registryPath, conflicting)
		}
		return nil
	}})
	_, err := executor.Execute(context.Background(), plan, nil)
	if err == nil || !HasCleanRollback(err) {
		t.Fatalf("error = %v, want clean conflict rollback", err)
	}
	got, readErr := store.ReadRegistry(registryPath)
	if readErr != nil || !reflect.DeepEqual(got, conflicting) {
		t.Fatalf("registry = %#v, %v; want attacker value", got, readErr)
	}
	if _, statErr := os.Lstat(plan.Destination.Path); !os.IsNotExist(statErr) {
		t.Fatalf("destination residue: %v", statErr)
	}
}

func TestCloneExecutePreEffectRegistryConflictCreatesNoStaging(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	registryPath := filepath.Join(plan.DataDir, "registry.json")
	conflicting := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{plan.Project.ID: {Name: "attacker", ConfigPath: filepath.Join(plan.Destination.Parent, "attacker", ".wtree.yml"), RepositoryIDs: map[string]string{"attacker": "root"}}}}
	if err := store.WriteRegistry(registryPath, conflicting); err != nil {
		t.Fatal(err)
	}
	_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}).Execute(context.Background(), plan, nil)
	if err == nil || HasCleanRollback(err) {
		t.Fatalf("error = %v, want pre-effect conflict without rollback claim", err)
	}
	got, readErr := store.ReadRegistry(registryPath)
	if readErr != nil || !reflect.DeepEqual(got, conflicting) {
		t.Fatalf("registry = %#v, %v", got, readErr)
	}
	entries, readDirErr := os.ReadDir(plan.Destination.Parent)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".wtree-clone-") {
			t.Fatalf("staging residue %q", entry.Name())
		}
	}
}

func TestCloneExecuteStateAndRegistryAttackersAreNeverOverwritten(t *testing.T) {
	for _, boundary := range []string{"state-write", "registry-write"} {
		for _, timing := range []string{"before", "after"} {
			t.Run(timing+"-"+boundary, func(t *testing.T) {
				plan := syntheticExecutableClonePlan(t)
				statePath := WorkspaceStatePath(plan.DataDir, plan.Project.ID, "default")
				registryPath := filepath.Join(plan.DataDir, "registry.json")
				attackerState := store.WorkspaceState{Version: store.Version, ID: "attacker", Name: "attacker", Path: plan.Destination.Parent, Repositories: map[string]store.CheckoutState{}}
				attackerRegistry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"attacker": {Name: "attacker", ConfigPath: filepath.Join(plan.Destination.Parent, "attacker", ".wtree.yml"), RepositoryIDs: map[string]string{"attacker": "root"}}}}
				hook := func(step string) error {
					if step != boundary {
						return nil
					}
					if boundary == "state-write" {
						return store.WriteWorkspace(statePath, attackerState)
					}
					return store.WriteRegistry(registryPath, attackerRegistry)
				}
				dependencies := CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}
				if timing == "before" {
					dependencies.BeforeEffect = hook
				} else {
					dependencies.AfterEffect = hook
				}
				_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
				if err == nil {
					t.Fatal("attacker mutation unexpectedly succeeded")
				}
				if boundary == "state-write" {
					got, readErr := store.ReadWorkspace(statePath)
					if readErr != nil || !reflect.DeepEqual(got, attackerState) {
						t.Fatalf("state = %#v, %v", got, readErr)
					}
				} else {
					got, readErr := store.ReadRegistry(registryPath)
					if readErr != nil || !reflect.DeepEqual(got, attackerRegistry) {
						t.Fatalf("registry = %#v, %v", got, readErr)
					}
				}
			})
		}
	}
}

func TestCloneExecuteWriterErrorNeverRollsBackOverNewerGeneration(t *testing.T) {
	for _, boundary := range []string{"state", "registry"} {
		t.Run(boundary, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			statePath := WorkspaceStatePath(plan.DataDir, plan.Project.ID, "default")
			registryPath := filepath.Join(plan.DataDir, "registry.json")
			attackerState := store.WorkspaceState{Version: store.Version, ID: "newer", Name: "newer", Path: plan.Destination.Parent, Repositories: map[string]store.CheckoutState{}}
			attackerRegistry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"newer": {Name: "newer", ConfigPath: filepath.Join(plan.Destination.Parent, "newer", ".wtree.yml"), RepositoryIDs: map[string]string{"newer": "root"}}}}
			dependencies := CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}
			if boundary == "state" {
				dependencies.WriteWorkspaceCAS = func(path string, _ store.WorkspaceState, compare func() error) (ClonePublicationReceipt, error) {
					if err := store.WriteWorkspace(path, attackerState); err != nil {
						return ClonePublicationReceipt{}, err
					}
					return ClonePublicationReceipt{}, compare()
				}
			} else {
				dependencies.WriteRegistryCAS = func(path string, _ store.Registry, compare func() error) (ClonePublicationReceipt, error) {
					if err := store.WriteRegistry(path, attackerRegistry); err != nil {
						return ClonePublicationReceipt{}, err
					}
					return ClonePublicationReceipt{}, compare()
				}
			}
			_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
			if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
				t.Fatalf("error = %v, want rollback-incomplete", err)
			}
			if boundary == "state" {
				got, readErr := store.ReadWorkspace(statePath)
				if readErr != nil || !reflect.DeepEqual(got, attackerState) {
					t.Fatalf("state = %#v, %v", got, readErr)
				}
			} else {
				got, readErr := store.ReadRegistry(registryPath)
				if readErr != nil || !reflect.DeepEqual(got, attackerRegistry) {
					t.Fatalf("registry = %#v, %v", got, readErr)
				}
			}
		})
	}
}

func TestCloneExecuteErroringWriterExactPlannedBytesWithoutReceiptIsPreserved(t *testing.T) {
	for _, boundary := range []string{"state", "registry"} {
		t.Run(boundary, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			dependencies := CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}
			var publishedPath string
			if boundary == "state" {
				dependencies.WriteWorkspaceCAS = func(path string, value store.WorkspaceState, compare func() error) (ClonePublicationReceipt, error) {
					if err := compare(); err != nil {
						return ClonePublicationReceipt{}, err
					}
					publishedPath = path
					return ClonePublicationReceipt{}, errors.Join(store.WriteWorkspace(path, value), context.Canceled)
				}
			} else {
				dependencies.WriteRegistryCAS = func(path string, value store.Registry, compare func() error) (ClonePublicationReceipt, error) {
					if err := compare(); err != nil {
						return ClonePublicationReceipt{}, err
					}
					publishedPath = path
					return ClonePublicationReceipt{}, errors.Join(store.WriteRegistry(path, value), context.Canceled)
				}
			}
			_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
			if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
				t.Fatalf("error = %v, want rollback-incomplete", err)
			}
			if _, statErr := os.Lstat(publishedPath); statErr != nil {
				t.Fatalf("exact-byte writer generation was removed: %v", statErr)
			}
		})
	}
}

func TestCloneExecuteReceiptRollbackCASPreservesFinalBoundaryReplacement(t *testing.T) {
	t.Run("state-remove", func(t *testing.T) {
		plan := syntheticExecutableClonePlan(t)
		statePath := WorkspaceStatePath(plan.DataDir, plan.Project.ID, "default")
		attacker := store.WorkspaceState{Version: store.Version, ID: "attacker", Name: "attacker", Path: plan.Destination.Parent, Repositories: map[string]store.CheckoutState{}}
		dependencies := CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}
		dependencies.WriteWorkspaceCAS = func(path string, value store.WorkspaceState, compare func() error) (ClonePublicationReceipt, error) {
			if err := compare(); err != nil {
				return ClonePublicationReceipt{}, err
			}
			if err := store.WriteWorkspace(path, value); err != nil {
				return ClonePublicationReceipt{}, err
			}
			snapshot, err := secureCloneFileSnapshot(path)
			return ClonePublicationReceipt{snapshot: snapshot}, errors.Join(context.Canceled, err)
		}
		dependencies.RemoveFileCAS = func(path string, compare func() error) error {
			if path == statePath {
				if err := store.WriteWorkspace(path, attacker); err != nil {
					return err
				}
			}
			return compare()
		}
		_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
		if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
			t.Fatalf("error = %v, want rollback-incomplete", err)
		}
		got, readErr := store.ReadWorkspace(statePath)
		if readErr != nil || !reflect.DeepEqual(got, attacker) {
			t.Fatalf("state = %#v, %v", got, readErr)
		}
	})

	t.Run("registry-restore", func(t *testing.T) {
		plan := syntheticExecutableClonePlan(t)
		registryPath := filepath.Join(plan.DataDir, "registry.json")
		original := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"original": {Name: "original", RepositoryIDs: map[string]string{}}}}
		attacker := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"attacker": {Name: "attacker", RepositoryIDs: map[string]string{}}}}
		if err := store.WriteRegistry(registryPath, original); err != nil {
			t.Fatal(err)
		}
		plan = mustClonePlan(t, NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(plan.Repositories[0].CloneURL, plan.Repositories[1].CloneURL)}), ClonePlanRequest{
			ManifestSource: plan.Source.Value, Destination: plan.Destination.Path, CWD: plan.Destination.Parent, DataDir: plan.DataDir,
		})
		dependencies := CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, AfterEffect: func(step string) error {
			if step == "registry-write" {
				return context.Canceled
			}
			return nil
		}}
		dependencies.WriteRawModeCAS = func(path string, _ []byte, _ os.FileMode, compare func() error) error {
			if path == registryPath {
				if err := store.WriteRegistry(path, attacker); err != nil {
					return err
				}
			}
			return compare()
		}
		_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
		if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
			t.Fatalf("error = %v, want rollback-incomplete", err)
		}
		got, readErr := store.ReadRegistry(registryPath)
		if readErr != nil || !reflect.DeepEqual(got, attacker) {
			t.Fatalf("registry = %#v, %v", got, readErr)
		}
	})
}

func TestCloneExecuteRejectsReplacedStagingIdentityBeforeRename(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	var retained string
	executor := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, BeforeEffect: func(step string) error {
		if step != "destination-rename" {
			return nil
		}
		entries, err := os.ReadDir(plan.Destination.Parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "."+filepath.Base(plan.Destination.Path)+".wtree-clone-") {
				original := filepath.Join(plan.Destination.Parent, entry.Name())
				retained = original + "-retained"
				if err := os.Rename(original, retained); err != nil {
					return err
				}
				return os.Mkdir(original, 0o700)
			}
		}
		return errors.New("staging not found")
	}})
	_, err := executor.Execute(context.Background(), plan, nil)
	if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
		t.Fatalf("error = %v, want rollback-incomplete", err)
	}
	if _, statErr := os.Stat(retained); statErr != nil {
		t.Fatalf("owned retained staging missing: %v", statErr)
	}
}

func TestCloneExecuteRejectsReplacedParentIdentityBeforeRename(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	moved := plan.Destination.Parent + "-moved"
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	executor := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, BeforeEffect: func(step string) error {
		if step != "destination-rename" {
			return nil
		}
		if err := os.Rename(plan.Destination.Parent, moved); err != nil {
			return err
		}
		return os.Mkdir(plan.Destination.Parent, 0o700)
	}})
	_, err := executor.Execute(context.Background(), plan, nil)
	if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
		t.Fatalf("error = %v, want rollback-incomplete", err)
	}
	if _, statErr := os.Stat(plan.Destination.Parent); statErr != nil {
		t.Fatalf("attacker parent missing: %v", statErr)
	}
}

func TestCloneExecutePostIdentityUserChangesAreRetainedWithRecovery(t *testing.T) {
	for _, mutation := range []string{"addition", "config-edit"} {
		t.Run(mutation, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			executor := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, AfterEffect: func(step string) error {
				if step != "final-identity" {
					return nil
				}
				path := filepath.Join(plan.Destination.Path, "user-added.txt")
				if mutation == "config-edit" {
					path = filepath.Join(plan.Destination.Path, ".wtree.yml")
				}
				return os.WriteFile(path, []byte("user bytes\n"), 0o600)
			}})
			_, err := executor.Execute(context.Background(), plan, nil)
			if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
				t.Fatalf("error = %v, want retained destination", err)
			}
			if _, statErr := os.Stat(plan.Destination.Path); statErr != nil {
				t.Fatalf("destination removed: %v", statErr)
			}
			recovery, readErr := store.ReadRecovery(filepath.Join(plan.DataDir, "projects", plan.Project.ID, "recovery", "default.json"))
			if readErr != nil || recovery.Operation != "clone" {
				t.Fatalf("recovery = %#v, %v", recovery, readErr)
			}
		})
	}
}

func TestCloneExecutePostIdentityMetadataAndIdentityChangesAreRetained(t *testing.T) {
	mutations := []string{"byte-identical-inode", "mtime-only", "mode-only", "exact-config-inode"}
	for _, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			ownedPath := ""
			executor := NewCloneExecutorWith(CloneExecutorDependencies{
				Git: &cloneExecutionGit{plan: plan},
				AfterEffect: func(step string) error {
					switch step {
					case "local-config-write":
						ownedPath = filepath.Join(findCloneStaging(t, plan), "owned.txt")
						return os.WriteFile(ownedPath, []byte("owned\n"), 0o600)
					case "final-identity":
						if mutation == "exact-config-inode" {
							path := filepath.Join(plan.Destination.Path, ".wtree.yml")
							data, err := os.ReadFile(path)
							if err != nil {
								return err
							}
							return fsutil.WriteFileAtomicMode(path, data, 0o600)
						}
						path := filepath.Join(plan.Destination.Path, "owned.txt")
						switch mutation {
						case "byte-identical-inode":
							return fsutil.WriteFileAtomicMode(path, []byte("owned\n"), 0o600)
						case "mtime-only":
							future := time.Now().Add(time.Hour)
							return os.Chtimes(path, future, future)
						case "mode-only":
							return os.Chmod(path, 0o400)
						}
					}
					return nil
				},
			})
			_, err := executor.Execute(context.Background(), plan, nil)
			if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
				t.Fatalf("error = %v, want retained destination", err)
			}
			if _, statErr := os.Lstat(plan.Destination.Path); statErr != nil {
				t.Fatalf("destination removed: %v", statErr)
			}
		})
	}
}

func TestCloneExecuteRejectsMaliciousSuccessfulConfigWriter(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	writer := func(path string, value config.ProjectConfig) error {
		data, err := config.MarshalProject(value)
		if err != nil {
			return err
		}
		return os.WriteFile(path, append(data, []byte("# attacker\n")...), 0o600)
	}
	_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, WriteConfig: writer}).Execute(context.Background(), plan, nil)
	if err == nil {
		t.Fatal("malicious successful config writer accepted")
	}
	assertCloneExecutionAbsent(t, plan)
}

func TestCloneExecutePreExistingRecoveryIsConflictAndPreserved(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	path := filepath.Join(plan.DataDir, "projects", plan.Project.ID, "recovery", "default.json")
	attacker := store.RecoveryRecord{Version: store.Version, ProjectID: "attacker", WorkspaceID: "default", Operation: "other", FailedStep: "attacker"}
	if err := store.WriteRecovery(path, attacker); err != nil {
		t.Fatal(err)
	}
	_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}).Execute(context.Background(), plan, nil)
	if err == nil {
		t.Fatal("pre-existing recovery accepted")
	}
	got, readErr := store.ReadRecovery(path)
	if readErr != nil || !reflect.DeepEqual(got, attacker) {
		t.Fatalf("recovery = %#v, %v", got, readErr)
	}
}

func TestCloneExecuteConcurrentRecoveryInstallIsPreserved(t *testing.T) {
	for _, timing := range []string{"before", "after"} {
		t.Run(timing, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			path := filepath.Join(plan.DataDir, "projects", plan.Project.ID, "recovery", "default.json")
			attacker := store.RecoveryRecord{Version: store.Version, ProjectID: "attacker", WorkspaceID: "default", Operation: "other", FailedStep: "attacker"}
			hook := func(step string) error {
				if step == "state-write" {
					return store.WriteRecovery(path, attacker)
				}
				return nil
			}
			dependencies := CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}
			if timing == "before" {
				dependencies.BeforeEffect = hook
			} else {
				dependencies.AfterEffect = hook
			}
			_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
			if err == nil {
				t.Fatal("concurrent recovery install accepted")
			}
			got, readErr := store.ReadRecovery(path)
			if readErr != nil || !reflect.DeepEqual(got, attacker) {
				t.Fatalf("recovery = %#v, %v", got, readErr)
			}
		})
	}
}

func TestCloneExecuteRecoveryWriterCannotInstallDifferentRecordAsOwned(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	path := filepath.Join(plan.DataDir, "projects", plan.Project.ID, "recovery", "default.json")
	attacker := store.RecoveryRecord{Version: store.Version, ProjectID: "attacker", WorkspaceID: "default", Operation: "other", FailedStep: "attacker"}
	dependencies := CloneExecutorDependencies{
		Git: &cloneExecutionGit{plan: plan}, RemoveAll: func(string) error { return os.ErrPermission },
		AfterEffect: func(step string) error {
			if step == "registry-write" {
				return context.Canceled
			}
			return nil
		},
		WriteRecoveryCAS: func(target string, _ store.RecoveryRecord, compare func() error) error {
			if err := compare(); err != nil {
				return err
			}
			return store.WriteRecovery(target, attacker)
		},
	}
	_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, nil)
	if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
		t.Fatalf("error = %v", err)
	}
	got, readErr := store.ReadRecovery(path)
	if readErr != nil || !reflect.DeepEqual(got, attacker) {
		t.Fatalf("recovery = %#v, %v", got, readErr)
	}
}

func TestCloneExecuteRealRemoteBranchDeletionNeverUsesReplacementTip(t *testing.T) {
	base := t.TempDir()
	repository := testutil.NewGitRepository(t)
	writeAndCommitCloneFiles(t, repository.Path, map[string]string{".gitignore": "/.wtree.yml\n", "README.md": "root\n"}, "identity")
	identity := cloneGitOutput(t, repository.Path, "rev-parse", "HEAD")
	remote := testutil.NewBareGitRemote(t)
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "deleted-ref", Name: "deleted-ref", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {
		Clone: config.CloneSource{Remote: "source", URL: remote}, Upstream: config.Upstream{Branch: "local-main", Remote: "source", Merge: "refs/heads/published-main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{identity}}, Mount: ".", DefaultBranch: "local-main",
	}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, repository.Path, map[string]string{"project.wtree.yml": string(data)}, "manifest")
	cloneGit(t, repository.Path, "push", remote, "HEAD:refs/heads/published-main")
	plan, err := NewClonePlanner().Plan(context.Background(), ClonePlanRequest{ManifestSource: filepath.Join(repository.Path, "project.wtree.yml"), Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	cloneGit(t, repository.Path, "push", remote, ":refs/heads/published-main")
	_, err = NewCloneExecutor().Execute(context.Background(), plan, nil)
	if err == nil {
		t.Fatal("deleted planned branch unexpectedly cloned")
	}
	if _, statErr := os.Lstat(plan.Destination.Path); !os.IsNotExist(statErr) {
		t.Fatalf("destination residue: %v", statErr)
	}
}

func TestCloneExecuteProgressIsExactOrderedAndHasNoFalseSuccess(t *testing.T) {
	plan := syntheticExecutableClonePlan(t)
	var events []transaction.Event
	_, err := NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}}).Execute(context.Background(), plan, func(event transaction.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	steps := []string{"staging-create", "repository-root-clone", "repository-root-fetch", "repository-root-checkout", "repository-root-verify", "repository-api-parent-ignore", "repository-api-clone", "repository-api-fetch", "repository-api-checkout", "repository-api-verify", "local-config-write", "publication-lock", "destination-rename", "final-identity", "state-write", "registry-write"}
	if len(events) != len(steps)*2 {
		t.Fatalf("events = %#v", events)
	}
	for index, step := range steps {
		if events[index*2].Kind != transaction.ExecuteStarted || events[index*2].Step != step || events[index*2+1].Kind != transaction.ExecuteSucceeded || events[index*2+1].Step != step {
			t.Fatalf("events around %q = %#v", step, events[index*2:index*2+2])
		}
	}

	plan = syntheticExecutableClonePlan(t)
	events = nil
	_, err = NewCloneExecutorWith(CloneExecutorDependencies{Git: &cloneExecutionGit{plan: plan}, AfterEffect: func(step string) error {
		if step == "state-write" {
			return context.Canceled
		}
		return nil
	}}).Execute(context.Background(), plan, func(event transaction.Event) { events = append(events, event) })
	if err == nil {
		t.Fatal("injected progress failure succeeded")
	}
	for _, event := range events {
		if event.Step == "state-write" && event.Kind == transaction.ExecuteSucceeded {
			t.Fatalf("false state success in %#v", events)
		}
	}
}

func TestCloneExecuteRealEffectFailuresHaveExactlyOneFailedTerminalEvent(t *testing.T) {
	tests := []struct {
		name   string
		step   string
		mutate func(ClonePlan, *CloneExecutorDependencies, *cloneExecutionGit)
	}{
		{"clone", "repository-root-clone", func(_ ClonePlan, _ *CloneExecutorDependencies, git *cloneExecutionGit) { git.cloneFailure = true }},
		{"fetch", "repository-root-fetch", func(_ ClonePlan, _ *CloneExecutorDependencies, git *cloneExecutionGit) { git.fetchFailure = true }},
		{"checkout", "repository-root-checkout", func(_ ClonePlan, _ *CloneExecutorDependencies, git *cloneExecutionGit) { git.checkoutFailure = true }},
		{"verify", "repository-root-verify", func(_ ClonePlan, _ *CloneExecutorDependencies, git *cloneExecutionGit) { git.verifyFailure = true }},
		{"config", "local-config-write", func(_ ClonePlan, dependencies *CloneExecutorDependencies, _ *cloneExecutionGit) {
			dependencies.WriteConfig = func(string, config.ProjectConfig) error { return os.ErrPermission }
		}},
		{"lock", "publication-lock", func(_ ClonePlan, dependencies *CloneExecutorDependencies, _ *cloneExecutionGit) {
			dependencies.Locker = cloneFailLocker{}
		}},
		{"rename", "destination-rename", func(_ ClonePlan, dependencies *CloneExecutorDependencies, _ *cloneExecutionGit) {
			dependencies.Rename = func(string, string) error { return os.ErrPermission }
		}},
		{"final-identity", "final-identity", func(_ ClonePlan, _ *CloneExecutorDependencies, git *cloneExecutionGit) {
			git.finalIdentityFailure = true
		}},
		{"state", "state-write", func(_ ClonePlan, dependencies *CloneExecutorDependencies, _ *cloneExecutionGit) {
			dependencies.WriteWorkspaceCAS = func(string, store.WorkspaceState, func() error) (ClonePublicationReceipt, error) {
				return ClonePublicationReceipt{}, os.ErrPermission
			}
		}},
		{"registry", "registry-write", func(_ ClonePlan, dependencies *CloneExecutorDependencies, _ *cloneExecutionGit) {
			dependencies.WriteRegistryCAS = func(string, store.Registry, func() error) (ClonePublicationReceipt, error) {
				return ClonePublicationReceipt{}, os.ErrPermission
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := syntheticExecutableClonePlan(t)
			git := &cloneExecutionGit{plan: plan}
			dependencies := CloneExecutorDependencies{Git: git}
			test.mutate(plan, &dependencies, git)
			var events []transaction.Event
			_, err := NewCloneExecutorWith(dependencies).Execute(context.Background(), plan, func(event transaction.Event) { events = append(events, event) })
			if err == nil {
				t.Fatal("real effect failure succeeded")
			}
			assertCloneProgressPairs(t, events, test.step, transaction.ExecuteFailed)
		})
	}
}

type cloneExecutionGit struct {
	gitadapter.Git
	plan                                                                                                                                               ClonePlan
	staleManifest, missingIgnore, wrongRoots, dirty, submodule, wrongBranch, wrongUpstream, cloneFailure, fetchFailure, checkoutFailure, verifyFailure bool
	finalIdentityFailure                                                                                                                               bool
	onClone                                                                                                                                            func()
}

func (git *cloneExecutionGit) repository(path string) ClonePlanRepository {
	for index := len(git.plan.Repositories) - 1; index >= 0; index-- {
		repository := git.plan.Repositories[index]
		if repository.Parent != "" && strings.HasSuffix(filepath.Clean(path), filepath.FromSlash(repository.Mount)) {
			return repository
		}
	}
	return git.plan.Repositories[0]
}
func (git *cloneExecutionGit) Clone(_ context.Context, _ string, destination, _ string) error {
	if git.cloneFailure {
		return os.ErrPermission
	}
	if git.onClone != nil {
		git.onClone()
	}
	return os.MkdirAll(destination, 0o700)
}
func (git *cloneExecutionGit) FetchCommit(_ context.Context, _ string, _ string, _ string) error {
	if git.fetchFailure {
		return os.ErrNotExist
	}
	return nil
}
func (git *cloneExecutionGit) CheckoutTrackingBranch(context.Context, string, string, string, string, string) error {
	if git.checkoutFailure {
		return os.ErrPermission
	}
	return nil
}
func (git *cloneExecutionGit) IsIgnoredAt(context.Context, string, string, string) (bool, error) {
	return !git.missingIgnore, nil
}
func (git *cloneExecutionGit) Head(_ context.Context, path string) (string, error) {
	return git.repository(path).AdvertisedCommit, nil
}
func (git *cloneExecutionGit) TopLevel(_ context.Context, path string) (string, error) {
	if git.verifyFailure {
		return "", os.ErrPermission
	}
	return path, nil
}
func (git *cloneExecutionGit) CurrentBranch(_ context.Context, path string) (string, bool, error) {
	value := git.repository(path).LocalBranch
	if git.wrongBranch {
		value += "-wrong"
	}
	return value, false, nil
}
func (git *cloneExecutionGit) Upstream(_ context.Context, path string) (gitadapter.Upstream, error) {
	repository := git.repository(path)
	value := gitadapter.Upstream{LocalBranch: repository.LocalBranch, Remote: repository.CloneRemote, Merge: repository.RemoteRef, FetchURL: repository.CloneURL}
	if git.wrongUpstream {
		value.Remote += "-wrong"
	}
	return value, nil
}
func (git *cloneExecutionGit) ContainsCommits(context.Context, string, []string) (bool, error) {
	return !git.wrongRoots, nil
}
func (git *cloneExecutionGit) IsClean(context.Context, string) (bool, error) { return !git.dirty, nil }
func (git *cloneExecutionGit) HasSubmodules(context.Context, string) (bool, error) {
	return git.submodule, nil
}
func (git *cloneExecutionGit) TrackedFile(context.Context, string, string, string) ([]byte, error) {
	if git.staleManifest {
		return []byte("stale\n"), nil
	}
	return git.plan.ManifestBytes(), nil
}
func (git *cloneExecutionGit) CommonGitDir(_ context.Context, path string) (string, error) {
	if git.finalIdentityFailure {
		for _, repository := range git.plan.Repositories {
			if filepath.Clean(path) == filepath.Clean(repository.Path) {
				return "", os.ErrPermission
			}
		}
	}
	return filepath.Join(git.plan.DataDir, "fake-common-"+git.plan.Project.ID+"-"+git.repository(path).ID), nil
}

type cloneFailLocker struct{}

func (cloneFailLocker) RegistryLock(context.Context, string, time.Duration) (lock.Handle, error) {
	return nil, os.ErrPermission
}
func (cloneFailLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	return nil, os.ErrPermission
}

func assertCloneExecutionAbsent(t *testing.T, plan ClonePlan) {
	t.Helper()
	if _, err := os.Lstat(plan.Destination.Path); !os.IsNotExist(err) {
		t.Fatalf("destination residue: %v", err)
	}
	if _, err := os.Lstat(WorkspaceStatePath(plan.DataDir, plan.Project.ID, "default")); !os.IsNotExist(err) {
		t.Fatalf("state residue: %v", err)
	}
	registry, err := store.ReadRegistry(filepath.Join(plan.DataDir, "registry.json"))
	if err == nil {
		if _, exists := registry.Projects[plan.Project.ID]; exists {
			t.Fatalf("registry residue: %#v", registry)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(plan.Destination.Parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+filepath.Base(plan.Destination.Path)+".wtree-clone-") {
			t.Fatalf("staging residue: %s", entry.Name())
		}
	}
}

func findCloneStaging(t *testing.T, plan ClonePlan) string {
	t.Helper()
	entries, err := os.ReadDir(plan.Destination.Parent)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "." + filepath.Base(plan.Destination.Path) + ".wtree-clone-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			return filepath.Join(plan.Destination.Parent, entry.Name())
		}
	}
	t.Fatal("clone staging directory not found")
	return ""
}

type cloneExecutionRemote struct{ remote, identity string }

func newCloneExecutionRemote(t *testing.T, localBranch, remoteBranch string, files map[string]string) cloneExecutionRemote {
	t.Helper()
	repository := testutil.NewGitRepository(t)
	writeAndCommitCloneFiles(t, repository.Path, files, "identity")
	identity := cloneGitOutput(t, repository.Path, "rev-parse", "HEAD")
	remote := testutil.NewBareGitRemote(t)
	cloneGit(t, repository.Path, "push", remote, "HEAD:refs/heads/"+remoteBranch)
	_ = localBranch // the manifest intentionally gives the checkout a different local name
	return cloneExecutionRemote{remote: remote, identity: identity}
}

func writeAndCommitCloneFiles(t *testing.T, repository string, files map[string]string, message string) {
	t.Helper()
	for name, contents := range files {
		path := filepath.Join(repository, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cloneGit(t, repository, "add", "--all")
	cloneGit(t, repository, "commit", "-m", message)
}

func cloneGit(t *testing.T, repository string, arguments ...string) {
	t.Helper()
	_ = cloneGitOutput(t, repository, arguments...)
}
func cloneGitOutput(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func syntheticExecutableClonePlan(t *testing.T) ClonePlan {
	t.Helper()
	base := t.TempDir()
	data := clonePlanManifest(t, filepath.Join(base, "missing-root.git"), filepath.Join(base, "missing-api.git"))
	source := writeClonePlanManifest(t, base, data)
	return mustClonePlan(t, NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(filepath.Join(base, "missing-root.git"), filepath.Join(base, "missing-api.git"))}), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")})
}

func mustClonePlan(t *testing.T, planner *ClonePlanner, request ClonePlanRequest) ClonePlan {
	t.Helper()
	plan, err := planner.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
