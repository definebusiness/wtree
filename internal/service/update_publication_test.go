package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestUpdateReconciliationRoundTripIsSortedAndSecretFree(t *testing.T) {
	facts := []UpdateRetainedFact{
		{RepositoryID: "web", Path: driftFixturePath("/work/web"), CommonGitDir: driftFixturePath("/git/web")},
		{RepositoryID: "api", Path: driftFixturePath("/work/api"), CommonGitDir: driftFixturePath("/git/api")},
	}
	encoded, err := EncodeUpdateReconciliation(facts)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("url")) || bytes.Contains(encoded, []byte("credential")) {
		t.Fatalf("reconciliation leaked connection data: %s", encoded)
	}
	decoded, err := DecodeUpdateReconciliation(encoded)
	if err != nil || len(decoded) != 2 || decoded[0].RepositoryID != "api" || decoded[1].RepositoryID != "web" {
		t.Fatalf("round trip = %#v, %v", decoded, err)
	}
}

// RED: M03 deliberately stops with completed repository effects and opaque
// backups. These are the first direct production tests proving that M04 turns
// that handoff into one coherent public generation, for both an added checkout
// and a nested retained checkout with a real fast-forward.
func TestUpdatePublicationCompletesRealAddedAndRetainedTransactions(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "added"
		if nested {
			name = "nested-retained-fast-forward"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newUpdateExecutionCrashFixture(t, nested)
			if nested {
				fixture = prepareUpdatePublicationBaseCandidate(t, fixture)
			}
			executor := NewUpdateExecutor()
			if _, err := executor.Execute(context.Background(), fixture.request); err != nil {
				t.Fatalf("M03 repository effects: %v", err)
			}
			result, err := executor.CompleteUpdate(context.Background(), fixture.request)
			if err != nil || len(result.Completed) < 4 {
				t.Fatalf("M04 completion = %#v, %v", result, err)
			}
			assertPublishedUpdateAgreement(t, fixture, nested, result)
		})
	}
}

func assertPublishedUpdateAgreement(t *testing.T, fixture updateExecutionCrashFixture, nested bool, result UpdatePublicationResult) {
	t.Helper()
	baseline := fixture.request.Plan.executionBaseline()
	candidate, err := config.LoadPortableManifest(fixture.request.Plan.CandidateManifestBytes())
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.ReadProjectFile(baseline.project.ConfigPath)
	if err != nil || local.Manifest.Source != fixture.request.Plan.Source.Value || len(local.Repositories) != len(candidate.Repositories) {
		t.Fatalf("published config=%#v err=%v", local, err)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(fixture.request.DataDir, fixture.request.ProjectID, "default"))
	if err != nil || len(state.Repositories) != len(candidate.Repositories) {
		t.Fatalf("published state=%#v err=%v", state, err)
	}
	registry, err := store.ReadRegistry(filepath.Join(fixture.request.DataDir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	for id := range candidate.Repositories {
		checkout, found := state.Repositories[id]
		if !found {
			t.Fatalf("state lacks candidate repository %q", id)
		}
		head, headErr := fixture.git.Head(context.Background(), fixture.paths[id])
		common, commonErr := fixture.git.CommonGitDir(context.Background(), fixture.paths[id])
		if headErr != nil || commonErr != nil || checkout.Head != head || registry.Projects[fixture.request.ProjectID].RepositoryIDs[common] != id {
			t.Fatalf("published identity %q state=%#v head=%q/%v common=%q/%v", id, checkout, head, headErr, common, commonErr)
		}
	}
	actions := fixture.request.Plan.Actions()
	if len(result.Repositories) != len(actions) {
		t.Fatalf("completion repositories=%#v actions=%#v", result.Repositories, actions)
	}
	for index, repository := range result.Repositories {
		if repository.ID != actions[index].RepositoryID || repository.Action != actions[index].Action || repository.Status != "completed" {
			t.Fatalf("completion repository[%d]=%#v action=%#v", index, repository, actions[index])
		}
		actual, headErr := fixture.git.Head(context.Background(), repository.Path)
		if headErr != nil || repository.ActualHead != actual {
			t.Fatalf("completion repository %q head=%q actual=%q err=%v", repository.ID, repository.ActualHead, actual, headErr)
		}
	}
	tracked, err := os.ReadFile(filepath.Join(fixture.paths["root"], "project.wtree.yml"))
	if err != nil || !bytes.Equal(tracked, fixture.request.Plan.CandidateManifestBytes()) {
		t.Fatalf("tracked candidate manifest err=%v", err)
	}
	journalPath, _ := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
	if _, err := os.Lstat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("successful M04 retained journal: %v", err)
	}
	reconciliationPath := filepath.Join(fixture.request.DataDir, "projects", fixture.request.ProjectID, "reconciliation.json")
	if nested {
		facts, err := DecodeUpdateReconciliation(mustReadUpdatePublicationFile(t, reconciliationPath))
		common, commonErr := fixture.git.CommonGitDir(context.Background(), fixture.paths["removed"])
		if err != nil || commonErr != nil || !reflect.DeepEqual(facts, []RetainedUnmanagedFact{{RepositoryID: "removed", Path: fixture.paths["removed"], CommonGitDir: common}}) {
			t.Fatalf("retained reconciliation=%#v err=%v common=%v", facts, err, commonErr)
		}
	} else {
		facts, err := DecodeUpdateReconciliation(mustReadUpdatePublicationFile(t, reconciliationPath))
		if err != nil || len(facts) != 0 {
			t.Fatalf("empty reconciliation=%#v err=%v", facts, err)
		}
	}
}

// The base repository must contain the selected candidate at its actual
// execution HEAD. The M03 fixture already does this for the added case; the
// nested retained case needs the same real upstream transition explicitly.
func prepareUpdatePublicationBaseCandidate(t *testing.T, fixture updateExecutionCrashFixture) updateExecutionCrashFixture {
	t.Helper()
	fixture.rootSource.CommitFile("project.wtree.yml", string(fixture.request.Plan.CandidateManifestBytes()), "candidate manifest for publication")
	if err := fixture.git.FetchTrackingBranch(context.Background(), fixture.paths["root"], "origin", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	baseline := fixture.request.Plan.executionBaseline()
	candidate := LoadedManifestSource{Kind: fixture.request.Plan.Source.Kind, Source: fixture.request.Plan.Source.Value, data: fixture.request.Plan.CandidateManifestBytes()}
	input, err := NewUpdateSnapshotCollector(baseline.project, baseline.workspace, fixture.request.DataDir, candidate).CollectDriftSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("refreshed publication snapshot=%#v err=%v", snapshot.Failures(), err)
	}
	plan, err := BuildUpdatePlan(snapshot, candidate)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.Plan = plan
	return fixture
}

func mustReadUpdatePublicationFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUpdatePublicationBoundaryInventoryHasRollbackCoverage(t *testing.T) {
	boundaries := updatePublicationBoundaryInventory()
	for _, fault := range []struct {
		name        string
		cancel      bool
		returnError bool
	}{
		{name: "fault", returnError: true},
		{name: "cancellation", cancel: true},
	} {
		for _, boundary := range boundaries {
			t.Run(fault.name+"/"+boundary, func(t *testing.T) {
				t.Parallel()
				// Publication boundaries own metadata CAS, journal, cleanup, and
				// resolver postconditions, not repository construction. This fixture
				// starts at a strict M03 completed-effects handoff with real metadata
				// files and opaque backups, while its single local Git directory only
				// supplies the production read-only resolver's identity check. The
				// dedicated added, fast-forward, retained, identity, and moved-HEAD
				// tests retain their full real-Git topologies.
				fixture := newUpdatePublicationMetadataFixture(t)
				active := ""
				ctx := context.Background()
				cancel := func() {}
				if fault.cancel {
					ctx, cancel = context.WithCancel(ctx)
				}
				defer cancel()
				executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fixture.git, Before: func(step string) error {
					if step == active {
						if fault.cancel {
							cancel()
							return nil
						}
						if fault.returnError {
							return errors.New("injected publication boundary")
						}
					}
					return nil
				}})
				active = boundary
				_, err := executor.CompleteUpdate(ctx, fixture.request)
				if err == nil {
					t.Fatalf("boundary %q succeeded", boundary)
				}
				if strings.HasPrefix(boundary, "journal-terminal-cleanup-") {
					var application *Error
					if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
						t.Fatalf("terminal boundary error=%v", err)
					}
					return
				}
				if HasCleanRollback(err) {
					fixture.assertRestored(t)
					return
				}
				var application *Error
				journalPath, _ := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
				if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
					t.Fatalf("publication boundary %q was neither clean nor durable: %v", boundary, err)
				}
				if _, statErr := os.Lstat(journalPath); statErr != nil {
					t.Fatalf("incomplete boundary %q lost journal: %v", boundary, statErr)
				}
				baseline := fixture.request.Plan.executionBaseline()
				if current, readErr := os.ReadFile(baseline.project.ConfigPath); readErr != nil || !bytes.Equal(current, baseline.localConfig) {
					t.Fatalf("incomplete boundary %q did not restore config bytes: %v", boundary, readErr)
				}
			})
		}
	}
}

// updatePublicationMetadataFixture is the smallest truthful M03-to-M04
// handoff for metadata-only publication tests. It uses a real local Git
// directory only because verifyPublishedUpdate intentionally constructs the
// public resolver, which validates the persisted registry against an actual
// Git identity. The completed M03 fast-forward itself is represented by its
// strict receipt and test Git facts; no remote, clone, fetch, or checkout is
// relevant to these metadata boundaries.
type updatePublicationMetadataFixture struct {
	request  UpdateExecutionRequest
	git      *updatePublicationMetadataGit
	original map[string][]byte
}

func newUpdatePublicationMetadataFixture(t *testing.T) updatePublicationMetadataFixture {
	t.Helper()
	repository := testutil.NewGitRepository(t)
	actualGit := gitadapter.NewAdapter("git")
	common, err := actualGit.CommonGitDir(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	candidatePath := filepath.Join(t.TempDir(), "candidate.wtree.yml")
	trackedPath := filepath.Join(repository.Path, "project.wtree.yml")
	configPath := filepath.Join(repository.Path, ".wtree.yml")
	oldHead, newHead := driftOID('0'), driftOID('1')

	manifest := func(name string) []byte {
		data, marshalErr := config.MarshalPortableManifest(config.PortableManifest{
			Version:      config.PortableManifestVersion,
			Project:      config.PortableProject{ID: "project", Name: name, BaseRepository: "root"},
			Repositories: map[string]config.PortableRepository{"root": driftRepository("", ".")},
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return data
	}
	current, candidate := manifest("current publication fixture"), manifest("candidate publication fixture")
	project := domain.Project{Version: domain.CurrentVersion, ID: "project", Name: "current publication fixture", ConfigPath: configPath, BaseRepository: "root", LogicalRoot: repository.Path, Repositories: []domain.Repository{{ID: "root", CommonGitDir: common, SourcePath: repository.Path, DefaultMount: ".", DefaultBranch: "main"}}}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: repository.Path, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: oldHead, Mount: ".", ResolvedPath: repository.Path}}}
	local := driftLocalConfig(project)
	local.Manifest = config.ManifestMetadata{Path: "project.wtree.yml", Source: candidatePath}
	localBytes, err := config.MarshalProject(local)
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(dataDir, project.ID, "default")
	stateBytes, err := store.WorkspaceBytes(driftWorkspaceState(workspace))
	if err != nil {
		t.Fatal(err)
	}
	registry := driftRegistry(project)
	registryBytes, err := store.RegistryBytes(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, localBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trackedPath, current, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, candidate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(statePath, driftWorkspaceState(workspace)); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(dataDir, "registry.json")
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}

	observation := DriftRepositoryObservation{RepositoryID: "root", Path: repository.Path, CommonGitDir: common, Branch: "main", Head: oldHead, Clean: true, IdentityKnown: true, IdentityMatches: true, TrackedManifestKnown: true, TrackedManifestExact: true, AdvertisedCommit: newHead, AdvertisedKnown: true, CanFastForward: true, UpstreamKnown: true, Upstream: gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: "https://example.test/project.git"}}
	snapshot, err := driftBuild(t, DriftSnapshotInput{DataDir: dataDir, Project: project, DefaultWorkspace: workspace, CurrentManifest: current, CurrentManifestPath: trackedPath, CurrentManifestSource: candidatePath, CandidateManifest: candidate, LocalConfig: &local, LocalConfigBytes: localBytes, Registry: &registry, RegistryBytes: registryBytes, DefaultState: PersistedWorkspaceGeneration{Path: statePath, Bytes: stateBytes}, Observations: []DriftRepositoryObservation{observation}})
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("metadata handoff snapshot=%#v err=%v", snapshot.Failures(), err)
	}
	plan, err := BuildUpdatePlan(snapshot, LoadedManifestSource{Kind: ManifestSourceLocal, Source: candidatePath, data: candidate})
	if err != nil {
		t.Fatal(err)
	}
	request := UpdateExecutionRequest{DataDir: dataDir, ProjectID: project.ID, OperationID: "update-metadata-fixture", Plan: plan}
	sources, err := prepareUpdateBackupSources([]updateBackupSource{
		{kind: "default-state", path: statePath},
		{kind: "local-config", path: configPath},
		{kind: "reconciliation", path: filepath.Join(dataDir, "projects", project.ID, "reconciliation.json")},
		{kind: "registry", path: registryPath},
		{kind: "tracked-manifest", path: trackedPath},
	}, map[string][]byte{"default-state": stateBytes, "local-config": localBytes, "reconciliation": nil, "registry": registryBytes, "tracked-manifest": current})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUpdateBackups(request, sources); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trackedPath, candidate, 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := encodeUpdateFastForwardReceipt(updateFastForwardReceipt{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, RepositoryID: "root", Branch: "main", OldCommit: oldHead, NewCommit: newHead, Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: newHead})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := updatePlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	journal := UpdateJournal{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, PlanDigest: digest, Generations: plan.Generations, Backups: backupMetadata(sources), Progress: []UpdateJournalEffect{{Sequence: 1, Name: "repository-root-fast-forward", Repository: "root", Receipt: receipt, State: "completed"}}, RollbackState: "active"}
	fake := &updatePublicationMetadataGit{path: repository.Path, common: common, head: newHead}
	journalPath, err := UpdateJournalPath(dataDir, project.ID, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewUpdateJournal(NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake}), journalPath, journal); err != nil {
		t.Fatal(err)
	}
	return updatePublicationMetadataFixture{request: request, git: fake, original: map[string][]byte{configPath: localBytes, statePath: stateBytes, registryPath: registryBytes, trackedPath: current}}
}

func (fixture updatePublicationMetadataFixture) assertRestored(t *testing.T) {
	t.Helper()
	for path, want := range fixture.original {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored metadata %q=%q err=%v", path, got, err)
		}
	}
	journalPath, err := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("clean rollback retained journal: %v", err)
	}
}

type updatePublicationMetadataGit struct {
	gitadapter.Git
	path, common, head string
	headErr, commonErr error
}

func (git *updatePublicationMetadataGit) Head(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path != git.path {
		return "", errors.New("unexpected metadata fixture path")
	}
	if git.headErr != nil {
		return "", git.headErr
	}
	return git.head, nil
}

func (git *updatePublicationMetadataGit) CommonGitDir(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path != git.path {
		return "", errors.New("unexpected metadata fixture path")
	}
	if git.commonErr != nil {
		return "", git.commonErr
	}
	return git.common, nil
}

func (git *updatePublicationMetadataGit) ObserveConfiguredRef(ctx context.Context, path, remote, remoteRef string) (gitadapter.ConfiguredRefObservation, error) {
	if err := ctx.Err(); err != nil {
		return gitadapter.ConfiguredRefObservation{}, err
	}
	if path != git.path || remote != "origin" || remoteRef != "refs/heads/main" {
		return gitadapter.ConfiguredRefObservation{}, errors.New("unexpected metadata fixture ref")
	}
	return gitadapter.ConfiguredRefObservation{Remote: remote, RemoteRef: remoteRef, Commit: git.head}, nil
}

func (git *updatePublicationMetadataGit) RestoreFastForward(_ context.Context, path string, receipt gitadapter.FastForwardReceipt) error {
	if path != git.path || receipt.NewCommit != git.head {
		return errors.New("unexpected metadata fixture rollback")
	}
	// The journal's effect is an injected M03 fact. Metadata rollback needs to
	// exercise the owned inverse call, but this fixture has no physical branch
	// to mutate; retaining the reported completed head keeps later recovery
	// checks deterministic.
	return nil
}

func (git *updatePublicationMetadataGit) RestoreConfiguredRef(_ context.Context, path string, receipt gitadapter.ConfiguredRefFetch) error {
	if path != git.path || receipt.Remote != "origin" || receipt.RemoteRef != "refs/heads/main" || receipt.ActualRemoteCommit != driftOID('1') {
		return errors.New("unexpected metadata fixture ref rollback")
	}
	return nil
}

func TestUpdatePublicationRejectsTamperedFastForwardReceiptAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*updateFastForwardReceipt)
	}{
		{name: "project", mutate: func(receipt *updateFastForwardReceipt) { receipt.ProjectID = "other" }},
		{name: "repository", mutate: func(receipt *updateFastForwardReceipt) { receipt.RepositoryID = "other" }},
		{name: "branch", mutate: func(receipt *updateFastForwardReceipt) { receipt.Branch = "other" }},
		{name: "remote", mutate: func(receipt *updateFastForwardReceipt) { receipt.Remote = "other" }},
		{name: "ref", mutate: func(receipt *updateFastForwardReceipt) { receipt.RemoteRef = "refs/heads/other" }},
		{name: "commit", mutate: func(receipt *updateFastForwardReceipt) { receipt.OldCommit = driftOID('f') }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request, journal := updatePublicationFastForwardReceiptFixture(t)
			receipt, err := decodeUpdateFastForwardReceipt(journal.Progress[0].Receipt)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&receipt)
			encoded, err := encodeUpdateFastForwardReceipt(receipt)
			if err != nil {
				t.Fatal(err)
			}
			journal.Progress[0].Receipt = encoded
			git := &updatePublicationReceiptGit{head: driftOID('1')}
			heads, err := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: git}).updateJournalHeads(context.Background(), journal, request)
			if err == nil || len(heads) != 0 || git.calls != 0 {
				t.Fatalf("tampered %s receipt adopted heads=%#v calls=%d err=%v", test.name, heads, git.calls, err)
			}
		})
	}
}

func TestUpdatePublicationRejectsTamperedEffectNamesBeforeGitObservation(t *testing.T) {
	request, journal := updatePublicationFastForwardReceiptFixture(t)
	journal.Progress[0].Name = "repository-other-fast-forward"
	git := &updatePublicationReceiptGit{head: driftOID('1')}
	if heads, err := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: git}).updateJournalHeads(context.Background(), journal, request); err == nil || len(heads) != 0 || git.calls != 0 {
		t.Fatalf("tampered fast-forward effect adopted heads=%#v calls=%d err=%v", heads, git.calls, err)
	}

	added := updateAddedReceipt{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, RepositoryID: "root", Head: driftOID('1'), CommonGitDirSHA256: strings.Repeat("a", 64), TreeSHA256: strings.Repeat("b", 64), TreeEntries: 1}
	data, err := json.Marshal(added)
	if err != nil {
		t.Fatal(err)
	}
	journal.Progress[0].Receipt = base64.RawURLEncoding.EncodeToString(data)
	journal.Progress[0].Name = "repository-other-add"
	if heads, err := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: git}).updateJournalHeads(context.Background(), journal, request); err == nil || len(heads) != 0 || git.calls != 0 {
		t.Fatalf("tampered add effect adopted heads=%#v calls=%d err=%v", heads, git.calls, err)
	}
}

func TestUpdatePublicationResultRefusesConcurrentValidHEADMovement(t *testing.T) {
	fixture := newUpdatePublicationMetadataFixture(t)
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fixture.git, Before: func(step string) error {
		if step == "publication-result-before" {
			fixture.git.head = driftOID('f')
		}
		return nil
	}})
	result, err := executor.CompleteUpdate(context.Background(), fixture.request)
	if err == nil || !reflect.DeepEqual(result, UpdatePublicationResult{}) {
		t.Fatalf("concurrent valid HEAD completed result=%#v err=%v", result, err)
	}
	journalPath, pathErr := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Lstat(journalPath); statErr != nil {
		t.Fatalf("concurrent valid HEAD lost recovery authority: %v", statErr)
	}
}

func TestUpdatePublicationResultObservationFailureAndCancellationNeverComplete(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*updatePublicationMetadataGit, context.CancelFunc)
	}{
		{name: "HEAD error", inject: func(git *updatePublicationMetadataGit, _ context.CancelFunc) {
			git.headErr = errors.New("injected HEAD observation error")
		}},
		{name: "common Git error", inject: func(git *updatePublicationMetadataGit, _ context.CancelFunc) {
			git.commonErr = errors.New("injected common Git observation error")
		}},
		{name: "cancellation", inject: func(_ *updatePublicationMetadataGit, cancel context.CancelFunc) { cancel() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdatePublicationMetadataFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fixture.git, Before: func(step string) error {
				if step == "publication-result-before" {
					test.inject(fixture.git, cancel)
				}
				return nil
			}})
			result, err := executor.CompleteUpdate(ctx, fixture.request)
			if err == nil || !reflect.DeepEqual(result, UpdatePublicationResult{}) {
				t.Fatalf("result observation completed result=%#v err=%v", result, err)
			}
			var application *Error
			if !HasCleanRollback(err) && (!errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete) {
				t.Fatalf("result observation was neither cleanly rolled back nor durably retained: %v", err)
			}
			if errors.As(err, &application) && application.Kind == ErrorRollbackIncomplete {
				journalPath, pathErr := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
				if pathErr != nil {
					t.Fatal(pathErr)
				}
				if _, statErr := os.Lstat(journalPath); statErr != nil {
					t.Fatalf("incomplete result observation lost recovery authority: %v", statErr)
				}
			}
		})
	}
}

// One real-Git end-to-end row retains the physical concurrent-HEAD contract;
// the six field-binding rows above are pure private-receipt grammar and do
// not need to spawn six independent repository topologies.
func TestUpdatePublicationRejectsConcurrentlyMovedFastForwardHEADBeforeMetadata(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	if _, err := NewUpdateExecutor().Execute(context.Background(), fixture.request); err != nil {
		t.Fatalf("M03 effects: %v", err)
	}
	baseline := fixture.request.Plan.executionBaseline()
	before := map[string][]byte{}
	for _, path := range []string{baseline.project.ConfigPath, baseline.defaultState.Path, filepath.Join(fixture.request.DataDir, "registry.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	repository := testutil.GitRepository{Path: fixture.paths["root"]}
	repository.Run(t, "config", "user.name", "wtree test")
	repository.Run(t, "config", "user.email", "wtree@example.invalid")
	repository.CommitFile("concurrent.txt", "moved\n", "concurrent move")
	result, err := NewUpdateExecutor().CompleteUpdate(context.Background(), fixture.request)
	if err == nil || result.Version != 0 {
		t.Fatalf("moved HEAD completed result=%#v err=%v", result, err)
	}
	for path, want := range before {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("moved HEAD adopted metadata %q=%q err=%v", path, got, readErr)
		}
	}
}

func updatePublicationFastForwardReceiptFixture(t *testing.T) (UpdateExecutionRequest, UpdateJournal) {
	t.Helper()
	plan := updateExecutorPlan(t)
	request := UpdateExecutionRequest{DataDir: "/data", ProjectID: "project", OperationID: "update-0123456789abcdef01234567", Plan: plan}
	receipt, err := encodeUpdateFastForwardReceipt(updateFastForwardReceipt{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, RepositoryID: "root", Branch: "main", OldCommit: driftOID('0'), NewCommit: driftOID('1'), Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: driftOID('1')})
	if err != nil {
		t.Fatal(err)
	}
	return request, UpdateJournal{Progress: []UpdateJournalEffect{{Sequence: 1, Name: "repository-root-fast-forward", Repository: "root", Receipt: receipt, State: "completed"}}}
}

type updatePublicationReceiptGit struct {
	gitadapter.Git
	head  string
	calls int
}

func (git *updatePublicationReceiptGit) Head(context.Context, string) (string, error) {
	git.calls++
	return git.head, nil
}

func (git *updatePublicationReceiptGit) CommonGitDir(context.Context, string) (string, error) {
	git.calls++
	return "/git/root", nil
}

func (git *updatePublicationReceiptGit) ObserveConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefObservation, error) {
	git.calls++
	return gitadapter.ConfiguredRefObservation{Remote: "origin", RemoteRef: "refs/heads/main", Commit: driftOID('1')}, nil
}

func assertDurablyPublishedUpdateRecovery(t *testing.T, fixture updateExecutionCrashFixture, err error) {
	t.Helper()
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("published cancellation error=%v, want durable incomplete recovery", err)
	}
	baseline := fixture.request.Plan.executionBaseline()
	candidate, candidateErr := config.LoadPortableManifest(fixture.request.Plan.CandidateManifestBytes())
	if candidateErr != nil {
		t.Fatal(candidateErr)
	}
	local, localErr := config.ReadProjectFile(baseline.project.ConfigPath)
	state, stateErr := store.ReadWorkspace(WorkspaceStatePath(fixture.request.DataDir, fixture.request.ProjectID, "default"))
	registry, registryErr := store.ReadRegistry(filepath.Join(fixture.request.DataDir, "registry.json"))
	if localErr != nil || stateErr != nil || registryErr != nil || local.Manifest.Source != fixture.request.Plan.Source.Value || len(local.Repositories) != len(candidate.Repositories) || len(state.Repositories) != len(candidate.Repositories) || len(registry.Projects[fixture.request.ProjectID].RepositoryIDs) != len(candidate.Repositories) {
		t.Fatalf("durably published generations config=%#v/%v state=%#v/%v registry=%#v/%v", local, localErr, state, stateErr, registry, registryErr)
	}
	journalPath, _ := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
	if _, journalErr := os.Lstat(journalPath); journalErr != nil {
		t.Fatalf("durably published cancellation lost journal: %v", journalErr)
	}
}

// This inventory is the M04 publication boundary contract. The companion
// observation test fails when CompleteUpdate emits a new publication-family
// boundary without adding it to the fault/cancellation matrix above.
func updatePublicationBoundaryInventory() []string {
	return []string{
		"update-publication-preflight-before",
		"journal-metadata-local-config-before", "journal-metadata-local-config-after",
		"journal-metadata-default-state-before", "journal-metadata-default-state-after",
		"journal-metadata-registry-before", "journal-metadata-registry-after",
		"journal-metadata-reconciliation-before", "journal-metadata-reconciliation-after",
		"publication-postcondition-before", "publication-postcondition-after",
		"publication-result-before",
		"journal-terminal-cleanup-start-before", "journal-terminal-cleanup-start-after",
		"journal-terminal-cleanup-remove-before", "journal-terminal-cleanup-remove-after",
	}
}

func TestUpdatePublicationBoundaryInventoryMatchesObservedPublicationBoundaries(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	complete := false
	seen := map[string]bool{}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(step string) error {
		if complete && updatePublicationBoundaryFamily(step) {
			seen[step] = true
		}
		return nil
	}})
	if _, err := executor.Execute(context.Background(), fixture.request); err != nil {
		t.Fatalf("M03 repository effects: %v", err)
	}
	complete = true
	if _, err := executor.CompleteUpdate(context.Background(), fixture.request); err != nil {
		t.Fatalf("M04 completion: %v", err)
	}
	for _, boundary := range updatePublicationBoundaryInventory() {
		if !seen[boundary] {
			t.Fatalf("publication boundary inventory omitted observed %q", boundary)
		}
		delete(seen, boundary)
	}
	for boundary := range seen {
		t.Fatalf("observed publication boundary %q is missing fault/cancellation coverage", boundary)
	}
}

func updatePublicationBoundaryFamily(step string) bool {
	return step == "update-publication-preflight-before" ||
		strings.HasPrefix(step, "journal-metadata-") ||
		strings.HasPrefix(step, "publication-") ||
		strings.HasPrefix(step, "journal-terminal-cleanup-")
}

func TestUpdateRepositorySetChangeWithNamedWorkspaceRefusesBeforeMutation(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, true)
	baseline := fixture.request.Plan.executionBaseline()
	defaultState, err := store.ReadWorkspace(WorkspaceStatePath(fixture.request.DataDir, fixture.request.ProjectID, "default"))
	if err != nil {
		t.Fatal(err)
	}
	named := defaultState
	named.ID, named.Name = "feature", "feature"
	if err := store.WriteWorkspace(WorkspaceStatePath(fixture.request.DataDir, fixture.request.ProjectID, "feature"), named); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(baseline.project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeHead, err := fixture.git.Head(context.Background(), fixture.paths["root"])
	if err != nil {
		t.Fatal(err)
	}
	candidate := LoadedManifestSource{Kind: fixture.request.Plan.Source.Kind, Source: fixture.request.Plan.Source.Value, data: fixture.request.Plan.CandidateManifestBytes()}
	input, err := NewUpdateSnapshotCollector(baseline.project, baseline.workspace, fixture.request.DataDir, candidate).CollectDriftSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.HasFailure("project", "non-default-workspace-repository-set-change") {
		t.Fatalf("named workspace set-change snapshot=%#v err=%v", snapshot.Failures(), err)
	}
	if _, err := BuildUpdatePlan(snapshot, candidate); err == nil {
		t.Fatal("repository-set change with named workspace planned")
	}
	afterConfig, _ := os.ReadFile(baseline.project.ConfigPath)
	afterHead, _ := fixture.git.Head(context.Background(), fixture.paths["root"])
	if !bytes.Equal(beforeConfig, afterConfig) || beforeHead != afterHead {
		t.Fatal("named-workspace refusal mutated project")
	}
}

func TestUpdatePublicationPreservesConcurrentRegistryGeneration(t *testing.T) {
	fixture := newUpdatePublicationMetadataFixture(t)
	registryPath := filepath.Join(fixture.request.DataDir, "registry.json")
	concurrent := []byte("concurrent registry generation\n")
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fixture.git, Before: func(step string) error {
		if step == "journal-metadata-registry-before" {
			return os.WriteFile(registryPath, concurrent, 0o600)
		}
		return nil
	}})
	if _, err := executor.CompleteUpdate(context.Background(), fixture.request); err == nil {
		t.Fatal("concurrent registry generation succeeded")
	} else {
		var application *Error
		if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
			t.Fatalf("concurrent registry generation error=%v, want durable incomplete recovery", err)
		}
	}
	if got, err := os.ReadFile(registryPath); err != nil || !bytes.Equal(got, concurrent) {
		t.Fatalf("concurrent registry generation overwritten=%q err=%v", got, err)
	}
	journalPath, _ := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
	if _, err := os.Lstat(journalPath); err != nil {
		t.Fatalf("concurrent registry generation lost recovery journal: %v", err)
	}
}

func TestUpdatePublicationPureFastForwardPreservesNamedWorkspaceListLookupAndStatus(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	// Replace the fixture's added-repository candidate with an equivalent
	// manifest carried by a newer base commit: this is a genuine pure FF.
	if err := os.WriteFile(fixture.request.Plan.Source.Value, fixture.current, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.rootSource.CommitFile("project.wtree.yml", string(fixture.current), "pure fast-forward manifest")
	if err := fixture.git.FetchTrackingBranch(context.Background(), fixture.paths["root"], "origin", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	baseline := fixture.request.Plan.executionBaseline()
	namedRoot := filepath.Join(t.TempDir(), "feature")
	creator := NewWorkspaceCreator()
	if _, err := creator.Create(context.Background(), baseline.project, WorkspacePlanRequest{
		WorkspaceName: "feature",
		TargetPath:    namedRoot,
		DataDir:       fixture.request.DataDir,
	}, nil); err != nil {
		t.Fatalf("create named workspace before pure FF: %v", err)
	}
	candidate := LoadedManifestSource{Kind: ManifestSourceLocal, Source: fixture.request.Plan.Source.Value, data: fixture.current}
	input, err := NewUpdateSnapshotCollector(baseline.project, baseline.workspace, fixture.request.DataDir, candidate).CollectDriftSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("pure-FF snapshot=%#v err=%v", snapshot.Failures(), err)
	}
	plan, err := BuildUpdatePlan(snapshot, candidate)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.Plan = plan
	executor := NewUpdateExecutor()
	if _, err := executor.Execute(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.CompleteUpdate(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	workspaces, err := ListWorkspaces(baseline.project, fixture.request.DataDir)
	if err != nil || len(workspaces) != 2 {
		t.Fatalf("list after pure FF=%#v err=%v", workspaces, err)
	}
	named, found, err := FindWorkspace(baseline.project, fixture.request.DataDir, "feature")
	if err != nil || !found || named.Name != "feature" {
		t.Fatalf("lookup after pure FF=%#v found=%t err=%v", named, found, err)
	}
	if path, err := named.ResolveRepository("root"); err != nil || path != namedRoot {
		t.Fatalf("repository lookup after pure FF path=%q err=%v", path, err)
	}
	if status, err := NewStatusService().Status(context.Background(), baseline.project, named); err != nil || len(status.Repositories) != 1 {
		t.Fatalf("status after pure FF=%#v err=%v", status, err)
	}
	if _, err := NewWorkspaceRemover().PlanRemove(context.Background(), baseline.project, named, false); err != nil {
		t.Fatalf("remove preflight after pure FF: %v", err)
	}
	if _, err := NewWorkspaceDeleter().PlanDelete(context.Background(), baseline.project, named, false); err != nil {
		t.Fatalf("delete preflight after pure FF: %v", err)
	}
	if _, err := NewWorkspaceRemover().Remove(context.Background(), baseline.project, named, fixture.request.DataDir, false, nil); err != nil {
		t.Fatalf("remove after pure FF: %v", err)
	}
	if _, err := creator.PlanCheckout(context.Background(), baseline.project, WorkspaceCheckoutRequest{
		WorkspaceName: "feature",
		DataDir:       fixture.request.DataDir,
	}); err != nil {
		t.Fatalf("checkout preflight after pure FF: %v", err)
	}
	if _, err := creator.CheckoutWorkspace(context.Background(), baseline.project, WorkspaceCheckoutRequest{
		WorkspaceName: "feature",
		DataDir:       fixture.request.DataDir,
	}, nil); err != nil {
		t.Fatalf("checkout after pure FF: %v", err)
	}
}

func TestRefuseActiveUpdateJournalLeavesReadOnlyScopeAvailable(t *testing.T) {
	data := t.TempDir()
	projectID, operationID := "project", "update-0123456789abcdef01234567"
	path, err := UpdateJournalPath(data, projectID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A malformed journal is still an active mutator refusal; read-only callers
	// intentionally do not use this guard.
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RefuseActiveUpdateJournal(data, projectID); err == nil {
		t.Fatal("active journal was accepted")
	}
}

func TestReconcileProjectRefusesActiveUpdateJournalBeforeRegistryMutation(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	if _, err := NewUpdateExecutor().Execute(context.Background(), fixture.request); err != nil {
		t.Fatalf("prepare active update journal: %v", err)
	}
	baseline := fixture.request.Plan.executionBaseline()
	registryPath := filepath.Join(fixture.request.DataDir, "registry.json")
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	err = NewResolver().ReconcileProject(context.Background(), fixture.request.DataDir, baseline.project)
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorConflict {
		t.Fatalf("ReconcileProject active update journal error=%v", err)
	}
	after, readErr := os.ReadFile(registryPath)
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("active journal reconciliation changed registry before=%q after=%q err=%v", before, after, readErr)
	}
	if _, err := NewResolver().ResolveReadOnly(context.Background(), ResolveRequest{Path: baseline.project.LogicalRoot, ProjectPath: baseline.project.ConfigPath, DataDir: fixture.request.DataDir}); err != nil {
		t.Fatalf("read-only resolution lost interrupted update visibility: %v", err)
	}
}

func TestUpdatePublicationRejectsBetweenPhaseIdentityChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		nested bool
		mutate func(t *testing.T, fixture updateExecutionCrashFixture)
	}{
		{name: "unchanged-head-moved", mutate: func(t *testing.T, fixture updateExecutionCrashFixture) {
			fixture.rootSource.CommitFile("after-m03.txt", "moved\n", "move unchanged root after M03")
			ctx := context.Background()
			old, err := fixture.git.Head(ctx, fixture.paths["root"])
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.git.FetchTrackingBranch(ctx, fixture.paths["root"], "origin", "refs/heads/main"); err != nil {
				t.Fatal(err)
			}
			next, err := fixture.git.ResolveRef(ctx, fixture.paths["root"], "refs/remotes/origin/main")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.git.FastForward(ctx, fixture.paths["root"], "main", old, next); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained-checkout-removed", nested: true, mutate: func(t *testing.T, fixture updateExecutionCrashFixture) {
			if err := os.Rename(fixture.paths["removed"], filepath.Join(t.TempDir(), "removed-replaced")); err != nil {
				t.Fatal(err)
			}
		}},
		// The replacement has the same path and commit as the locked checkout,
		// but a different Git common directory. A commit comparison alone would
		// silently adopt a checkout which this transaction never owned.
		{name: "unchanged-checkout-replaced-at-same-commit", mutate: func(t *testing.T, fixture updateExecutionCrashFixture) {
			ctx := context.Background()
			originalHead, err := fixture.git.Head(ctx, fixture.paths["root"])
			if err != nil {
				t.Fatal(err)
			}
			originalCommon, err := fixture.git.CommonGitDir(ctx, fixture.paths["root"])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(fixture.paths["root"], filepath.Join(t.TempDir(), "original-root")); err != nil {
				t.Fatal(err)
			}
			// A worktree of a different repository has a .git file at the same
			// checkout path but points to the source repository's common Git dir.
			fixture.rootSource.Run(t, "worktree", "add", "--detach", fixture.paths["root"], originalHead)
			replacementHead, err := fixture.git.Head(ctx, fixture.paths["root"])
			if err != nil || replacementHead != originalHead {
				t.Fatalf("replacement HEAD=%q err=%v, want %q", replacementHead, err, originalHead)
			}
			replacementCommon, err := fixture.git.CommonGitDir(ctx, fixture.paths["root"])
			if err != nil || replacementCommon == originalCommon {
				t.Fatalf("replacement common=%q err=%v, want a different common Git directory from %q", replacementCommon, err, originalCommon)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newUpdateExecutionCrashFixture(t, test.nested)
			if test.nested {
				fixture = prepareUpdatePublicationBaseCandidate(t, fixture)
			}
			executor := NewUpdateExecutor()
			if _, err := executor.Execute(context.Background(), fixture.request); err != nil {
				t.Fatalf("M03 repository effects: %v", err)
			}
			test.mutate(t, fixture)
			if _, err := executor.CompleteUpdate(context.Background(), fixture.request); err == nil {
				t.Fatal("between-phase replacement was adopted")
			}
		})
	}
}

func TestUpdatePublicationProjectRenameKeepsAllGenerationsCoherent(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	// This real-Git fixture is deliberately assembled from temporary paths.
	// Normalize its registry receipt before capturing the new plan so the
	// subsequent resolver assertion exercises the same canonical-path contract
	// as an initialized project.
	initial := fixture.request.Plan.executionBaseline()
	registryPath := filepath.Join(fixture.request.DataDir, "registry.json")
	registry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalConfig, err := filepath.EvalSymlinks(initial.project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Projects[fixture.request.ProjectID]
	entry.ConfigPath = canonicalConfig
	registry.Projects[fixture.request.ProjectID] = entry
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	baseline := initial
	baseline.project.ConfigPath = canonicalConfig
	canonicalRoot, err := filepath.EvalSymlinks(initial.project.LogicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	baseline.project.LogicalRoot = canonicalRoot
	for index := range baseline.project.Repositories {
		canonicalPath, pathErr := filepath.EvalSymlinks(baseline.project.Repositories[index].SourcePath)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		baseline.project.Repositories[index].SourcePath = canonicalPath
	}
	baseline.workspace.RootPath = canonicalRoot
	for index := range baseline.workspace.Checkouts {
		canonicalPath, pathErr := filepath.EvalSymlinks(baseline.workspace.Checkouts[index].ResolvedPath)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		baseline.workspace.Checkouts[index].ResolvedPath = canonicalPath
	}
	if err := store.WriteWorkspace(WorkspaceStatePath(fixture.request.DataDir, fixture.request.ProjectID, "default"), driftWorkspaceState(baseline.workspace)); err != nil {
		t.Fatal(err)
	}
	candidate, err := config.LoadPortableManifest(fixture.request.Plan.CandidateManifestBytes())
	if err != nil {
		t.Fatal(err)
	}
	candidate.Project.Name = "renamed crash fixture"
	candidateBytes, err := config.MarshalPortableManifest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.request.Plan.Source.Value, candidateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.rootSource.CommitFile("project.wtree.yml", string(candidateBytes), "rename update candidate")
	if err := fixture.git.FetchTrackingBranch(context.Background(), fixture.paths["root"], "origin", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	source := LoadedManifestSource{Kind: fixture.request.Plan.Source.Kind, Source: fixture.request.Plan.Source.Value, data: candidateBytes}
	input, err := NewUpdateSnapshotCollector(baseline.project, baseline.workspace, fixture.request.DataDir, source).CollectDriftSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("rename snapshot=%#v err=%v", snapshot.Failures(), err)
	}
	plan, err := BuildUpdatePlan(snapshot, source)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.Plan = plan
	baseline = plan.executionBaseline()
	executor := NewUpdateExecutor()
	if _, err := executor.Execute(context.Background(), fixture.request); err != nil {
		t.Fatalf("M03 rename effects: %v", err)
	}
	if _, err := executor.CompleteUpdate(context.Background(), fixture.request); err != nil {
		t.Fatalf("M04 rename completion: %v", err)
	}
	local, err := config.ReadProjectFile(baseline.project.ConfigPath)
	if err != nil || local.Project.Name != candidate.Project.Name {
		t.Fatalf("published local name=%q err=%v, want %q", local.Project.Name, err, candidate.Project.Name)
	}
	registry, err = store.ReadRegistry(registryPath)
	if err != nil || registry.Projects[fixture.request.ProjectID].Name != candidate.Project.Name {
		t.Fatalf("published registry=%#v err=%v", registry.Projects[fixture.request.ProjectID], err)
	}
	resolved, err := NewResolver().ResolveReadOnly(context.Background(), ResolveRequest{Path: fixture.paths["root"], ProjectPath: baseline.project.ConfigPath, DataDir: fixture.request.DataDir})
	if err != nil || resolved.Project.Name != candidate.Project.Name {
		t.Fatalf("resolver name=%q err=%v, want %q", resolved.Project.Name, err, candidate.Project.Name)
	}
	postInput, err := NewUpdateSnapshotCollector(resolved.Project, resolved.Workspace, fixture.request.DataDir, source).CollectDriftSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	post, err := BuildDriftSnapshot(postInput)
	if err != nil || post.HasFailure("project", "registry-generation") {
		t.Fatalf("post-rename snapshot=%#v err=%v", post.Failures(), err)
	}
}

func TestUpdatePublicationFailureReturnsNoCompletionResult(t *testing.T) {
	for _, test := range []struct {
		name        string
		wantDurable bool
		realGit     bool
		before      func(string) error
	}{
		{name: "clean rollback", before: func(step string) error {
			if step == "update-publication-preflight-before" {
				return errors.New("injected clean publication failure")
			}
			return nil
		}},
		{name: "incomplete rollback", wantDurable: true, before: func(step string) error {
			if step == "journal-terminal-cleanup-start-before" {
				return errors.New("injected retained cleanup failure")
			}
			return nil
		}},
		{name: "result observation failure", wantDurable: true, realGit: true, before: func(step string) error {
			if step == "publication-result-before" {
				return errors.New("injected result observation failure")
			}
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// Result observation uses the full M03 topology: it proves that an
			// inability to observe a completed real checkout retains durable
			// recovery rather than reporting a clean rollback. The other rows
			// cover metadata-only boundaries through the shared handoff fixture.
			if test.realGit {
				fixture := newUpdateExecutionCrashFixture(t, false)
				executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: test.before})
				if _, err := executor.Execute(context.Background(), fixture.request); err != nil {
					t.Fatalf("M03 effects: %v", err)
				}
				result, err := executor.CompleteUpdate(context.Background(), fixture.request)
				assertUpdatePublicationFailureResult(t, fixture.request, result, err, test.wantDurable, fixture.assertRestored)
				return
			}
			fixture := newUpdatePublicationMetadataFixture(t)
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fixture.git, Before: test.before})
			result, err := executor.CompleteUpdate(context.Background(), fixture.request)
			assertUpdatePublicationFailureResult(t, fixture.request, result, err, test.wantDurable, fixture.assertRestored)
		})
	}
}

func assertUpdatePublicationFailureResult(t *testing.T, request UpdateExecutionRequest, result UpdatePublicationResult, err error, wantDurable bool, assertRestored func(*testing.T)) {
	t.Helper()
	if err == nil || result.Version != 0 || result.Operation != "" || result.Status != "" || len(result.Repositories) != 0 || result.OperationID != "" {
		t.Fatalf("failure completion result=%#v err=%v", result, err)
	}
	if wantDurable {
		var application *Error
		if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
			t.Fatalf("incomplete error=%v", err)
		}
		journalPath, pathErr := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		if _, statErr := os.Lstat(journalPath); statErr != nil {
			t.Fatalf("incomplete result lost journal: %v", statErr)
		}
		return
	}
	if !HasCleanRollback(err) {
		t.Fatalf("clean rollback error=%v", err)
	}
	assertRestored(t)
}

func TestUpdateReconciliationRejectsCredentialsAndUnknownFields(t *testing.T) {
	for _, value := range []string{
		`{"version":1,"retained":[{"repositoryId":"api","path":"/work/api","commonGitDir":"/git/api"}],"url":"https://example.invalid"}`,
		`{"version":1,"retained":[{"repositoryId":"api","path":"https://user:secret@example.invalid/api","commonGitDir":"/git/api"}]}`,
	} {
		if _, err := DecodeUpdateReconciliation([]byte(value)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("DecodeUpdateReconciliation(%q) = %v", value, err)
		}
	}
}
