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
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestUpdatePlanSourcePrecedenceAndCancellation(t *testing.T) {
	stored := t.TempDir() + "/stored.wtree.yml"
	override := t.TempDir() + "/override.wtree.yml"
	if err := os.WriteFile(stored, []byte("stored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("override"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := NewUpdatePlanner()
	loaded, err := planner.LoadCandidate(context.Background(), stored, override)
	if err != nil || string(loaded.Bytes()) != "override" {
		t.Fatalf("override = %q, %v", loaded.Bytes(), err)
	}
	loaded, err = planner.LoadCandidate(context.Background(), stored, "")
	if err != nil || string(loaded.Bytes()) != "stored" {
		t.Fatalf("stored = %q, %v", loaded.Bytes(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := planner.Plan(ctx, DriftSnapshot{}, stored, ""); err == nil {
		t.Fatal("cancelled planning succeeded")
	}
}

func TestUpdatePlanLoadsHermeticHTTPOverrideWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" || request.URL.User != nil {
			t.Fatalf("unexpected request %q", request.URL)
		}
		_, _ = writer.Write([]byte("http candidate"))
	}))
	defer server.Close()
	planner := NewUpdatePlannerWithLoader(NewManifestSourceLoaderWithClient(server.Client()))
	loaded, err := planner.LoadCandidate(context.Background(), "/stored/project.wtree.yml", server.URL+"/project.wtree.yml")
	if err != nil || loaded.Kind != ManifestSourceHTTP || string(loaded.Bytes()) != "http candidate" {
		t.Fatalf("HTTP override = %#v, %v", loaded, err)
	}
	if _, err := planner.LoadCandidate(context.Background(), "/stored/project.wtree.yml", "https://user:secret@example.invalid/project.wtree.yml"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("credential source error = %v", err)
	}
	localDirectory := t.TempDir() + "/releases@2"
	if err := os.Mkdir(localDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	localPath := localDirectory + "/project.wtree.yml"
	if err := os.WriteFile(localPath, []byte("local @ candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := planner.LoadCandidate(context.Background(), localPath, ""); err != nil || loaded.Source != localPath {
		t.Fatalf("local @ source = %#v, %v", loaded, err)
	}
	if loaded, err := planner.LoadCandidate(context.Background(), "/stored/project.wtree.yml", server.URL+"/releases@2/project.wtree.yml"); err != nil || loaded.Source != server.URL+"/releases@2/project.wtree.yml" {
		t.Fatalf("HTTP @ source = %#v, %v", loaded, err)
	}
}

func TestUpdatePlanBuildsStablePrivateParentFirstPlan(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{
		"root": driftRepository("", "."), "child": driftRepository("root", "child"),
	})
	candidate := driftManifest(t, map[string]config.PortableRepository{
		"root": driftRepository("", "."), "added": driftRepository("root", "added"),
	})
	project := driftProject([]domain.Repository{{ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"}, {ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: candidate, Observations: []DriftRepositoryObservation{
		{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('1'), CanFastForward: true, TrackedManifestExact: true},
		{RepositoryID: "child", Path: "/tree/child", CommonGitDir: "/git/child", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), IgnoreVerified: true},
		{RepositoryID: "added", Path: "/tree/added", TargetAbsent: true, IgnoreVerified: true, AdvertisedCommit: driftOID('2')},
	}})
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("snapshot = %#v, %v", snapshot.Failures(), err)
	}
	source := LoadedManifestSource{Kind: ManifestSourceLocal, Source: "/candidate/releases@2/project.wtree.yml", data: append([]byte(nil), candidate...)}
	first, err := BuildUpdatePlan(snapshot, source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildUpdatePlan(snapshot, source)
	if err != nil {
		t.Fatal(err)
	}
	left, err := first.JSON()
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatalf("plan JSON is not stable:\n%s\n%s", left, right)
	}
	if bytes.Contains(left, candidate) || strings.Contains(string(left), "candidateData") {
		t.Fatalf("plan JSON exposed candidate bytes: %s", left)
	}
	actions := first.Actions()
	if got := []string{actions[0].Action, actions[1].Action, actions[2].Action}; strings.Join(got, ",") != "fast-forward,add,retain-unmanaged" {
		t.Fatalf("actions = %v", got)
	}
	var decoded map[string]any
	if err := json.Unmarshal(left, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["candidateData"]; ok {
		t.Fatal("JSON contains private candidate data")
	}
	copy := first.CandidateManifestBytes()
	copy[0] ^= 1
	if bytes.Equal(copy, first.CandidateManifestBytes()) {
		t.Fatal("candidate byte accessor leaked plan state")
	}
	originalJSON := append([]byte(nil), left...)
	copyOfPlan := first
	repositories := copyOfPlan.Repositories()
	copyActions := copyOfPlan.Actions()
	publication := copyOfPlan.Publication()
	rollback := copyOfPlan.Rollback()
	repositories[0].ID = "corrupt"
	copyActions[0].Action = "delete"
	publication[0] = "corrupt"
	rollback[0] = "corrupt"
	if err := first.Validate(); err != nil {
		t.Fatalf("accessor mutations corrupted original plan: %v", err)
	}
	after, err := first.JSON()
	if err != nil || !bytes.Equal(after, originalJSON) {
		t.Fatalf("accessor mutations changed original plan: %v\n%s", err, after)
	}
	if err := copyOfPlan.Validate(); err != nil {
		t.Fatalf("ordinary value copy was corrupted by accessor mutation: %v", err)
	}
	if _, err := BuildUpdatePlan(snapshot, LoadedManifestSource{Kind: ManifestSourceHTTP, Source: "https://example.test/releases@2/project.wtree.yml", data: candidate}); err != nil {
		t.Fatalf("credential-free HTTP @ path was rejected: %v", err)
	}
}

func TestUpdatePlanRejectsTamperingAndCredentialBearingSource(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildUpdatePlan(snapshot, LoadedManifestSource{Kind: ManifestSourceLocal, Source: "/candidate/project.wtree.yml", data: manifest})
	if err != nil {
		t.Fatal(err)
	}
	for name, tamper := range map[string]func(*UpdatePlan){
		"version":           func(value *UpdatePlan) { value.Version++ },
		"digest":            func(value *UpdatePlan) { value.Source.SHA256 = strings.Repeat("0", 64) },
		"order":             func(value *UpdatePlan) { value.private.actions[0].Sequence = 2 },
		"facts":             func(value *UpdatePlan) { value.private.repositories[0].ID = "other" },
		"observed-commit":   func(value *UpdatePlan) { value.private.repositories[0].ObservedCommit = driftOID('f') },
		"action-value":      func(value *UpdatePlan) { value.private.actions[0].Action = "delete" },
		"action-repository": func(value *UpdatePlan) { value.private.actions[0].RepositoryID = "other" },
		"verification":      func(value *UpdatePlan) { value.Verification.NoMutation = false },
		"publication":       func(value *UpdatePlan) { value.private.publication = nil },
		"rollback":          func(value *UpdatePlan) { value.private.rollback = nil },
		"credentials":       func(value *UpdatePlan) { value.Source.Value = "https://user:secret@example.invalid/project.wtree.yml" },
		"query":             func(value *UpdatePlan) { value.Source.Value = "https://example.invalid/project.wtree.yml?secret=value" },
		"candidate-bytes":   func(value *UpdatePlan) { value.private.candidateData[0] ^= 1 },
		"candidate-nil":     func(value *UpdatePlan) { value.private.candidateData = nil },
		"candidate-empty":   func(value *UpdatePlan) { value.private.candidateData = []byte{} },
	} {
		t.Run(name, func(t *testing.T) {
			broken := cloneUpdatePlanForTamper(plan)
			tamper(&broken)
			if err := broken.Validate(); err == nil {
				t.Fatal("Validate() accepted tampering")
			}
		})
	}
}

func cloneUpdatePlanForTamper(plan UpdatePlan) UpdatePlan {
	copy := plan
	copy.private = &updatePlanPrivate{
		repositories:  append([]UpdatePlanRepository(nil), plan.private.repositories...),
		actions:       append([]UpdatePlanAction(nil), plan.private.actions...),
		publication:   append([]string(nil), plan.private.publication...),
		rollback:      append([]string(nil), plan.private.rollback...),
		candidateData: append([]byte(nil), plan.private.candidateData...),
		factsDigest:   plan.private.factsDigest,
	}
	return copy
}

func TestUpdatePlanUnchangedIsStableAcrossObservationPermutations(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "child": driftRepository("root", "child")})
	project := driftProject([]domain.Repository{{ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"}, {ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	observations := []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}, {RepositoryID: "child", Path: "/tree/child", CommonGitDir: "/git/child", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), IgnoreVerified: true}}
	build := func(value []DriftRepositoryObservation) []byte {
		snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: value})
		if err != nil || !snapshot.MayUpdate() {
			t.Fatalf("snapshot = %#v, %v", snapshot.Failures(), err)
		}
		plan, err := BuildUpdatePlan(snapshot, LoadedManifestSource{Kind: ManifestSourceLocal, Source: "/candidate/project.wtree.yml", data: manifest})
		if err != nil {
			t.Fatal(err)
		}
		for _, repository := range plan.Repositories() {
			if repository.Classification != UpdateClassificationUnchanged {
				t.Fatalf("classification = %s", repository.Classification)
			}
		}
		data, err := plan.JSON()
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first := build(observations)
	second := build([]DriftRepositoryObservation{observations[1], observations[0]})
	if !bytes.Equal(first, second) {
		t.Fatalf("permuted plan differs:\n%s\n%s", first, second)
	}
}

func TestUpdateSnapshotCollectorAggregatesInjectedObservationFailuresDeterministically(t *testing.T) {
	secret := "very-secret-token"
	manifest := driftManifest(t, map[string]config.PortableRepository{
		"root": driftRepository("", "."), "child": driftRepository("root", "child"),
	})
	project := driftProject([]domain.Repository{
		{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"},
		{ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"},
	})
	observations := []DriftRepositoryObservation{
		{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true},
		{RepositoryID: "child", Path: "/tree/child", CommonGitDir: "/git/child", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), IgnoreVerified: true},
	}
	rawCurrent, rawCandidate := append([]byte(nil), manifest...), append([]byte(nil), manifest...)
	build := func(reverse bool) (DriftSnapshot, error) {
		collector := &UpdateSnapshotCollector{observe: func(ctx context.Context, current, candidate []byte, path string) ([]DriftRepositoryObservation, []DriftFailure, error) {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if !bytes.Equal(current, rawCurrent) || !bytes.Equal(candidate, rawCandidate) || path != "project.wtree.yml" {
				t.Fatal("collector seam changed its authoritative inputs")
			}
			failures := []DriftFailure{
				{RepositoryID: "root", Check: "advertised-ref-observation", Message: "remote https://user:" + secret + "@example.invalid/root.git unavailable"},
				{RepositoryID: "child", Check: "upstream-observation", Message: "child upstream unavailable"},
			}
			if reverse {
				failures[0], failures[1] = failures[1], failures[0]
			}
			return observations, failures, nil
		}}
		_, failures, err := collector.collectObservations(context.Background(), rawCurrent, rawCandidate, "project.wtree.yml")
		if err != nil {
			t.Fatal(err)
		}
		input := driftCompleteInput(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: rawCurrent, CandidateManifest: rawCandidate, Observations: observations})
		input.Collection.Errors = failures
		input.CollectionFailureOrder = updateObservationRepositoryOrder(rawCurrent, rawCandidate)
		return BuildDriftSnapshot(input)
	}
	first, firstErr := build(false)
	second, secondErr := build(true)
	var firstTyped, secondTyped *DriftPreflightError
	if !errors.As(firstErr, &firstTyped) || !errors.As(secondErr, &secondTyped) {
		t.Fatalf("aggregate failures were not typed: %v / %v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(first.Failures(), second.Failures()) || !reflect.DeepEqual(firstTyped.Snapshot.Failures(), secondTyped.Snapshot.Failures()) {
		t.Fatalf("aggregate failures are order-dependent: %#v / %#v", first.Failures(), second.Failures())
	}
	if got := first.Failures(); len(got) != 2 || got[0].RepositoryID != "root" || got[1].RepositoryID != "child" || bytes.Contains([]byte(fmt.Sprintf("%#v %v", firstTyped, got)), []byte(secret)) {
		t.Fatalf("aggregate failures lost parent/check evidence or leaked a credential: %#v", got)
	}
	if !bytes.Equal(rawCurrent, manifest) || !bytes.Equal(rawCandidate, manifest) {
		t.Fatal("read-only collector seam mutated manifest input")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collector := &UpdateSnapshotCollector{observe: func(context.Context, []byte, []byte, string) ([]DriftRepositoryObservation, []DriftFailure, error) {
		t.Fatal("cancelled collection invoked observation seam")
		return nil, nil, nil
	}}
	if _, _, err := collector.collectObservations(ctx, rawCurrent, rawCandidate, "project.wtree.yml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collection = %v", err)
	}
}

func TestUpdateSnapshotCollectorProductionObservationsAggregateRemoteFailures(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	candidateRepositories := map[string]config.PortableRepository{
		"root":  driftRepository("", "."),
		"alpha": driftRepository("root", "alpha"),
		"beta":  driftRepository("root", "beta"),
	}
	for _, id := range []string{"alpha", "beta"} {
		repository := candidateRepositories[id]
		repository.Clone.URL = server.URL + "/" + id + ".git"
		candidateRepositories[id] = repository
	}
	candidate := driftManifest(t, candidateRepositories)
	collector := &UpdateSnapshotCollector{DefaultWorkspace: domain.Workspace{RootPath: t.TempDir()}, git: gitadapter.NewAdapter("git")}
	firstObservations, firstFailures, firstErr := collector.productionObservations(context.Background(), current, candidate, "project.wtree.yml")
	secondObservations, secondFailures, secondErr := collector.productionObservations(context.Background(), current, candidate, "project.wtree.yml")
	if firstErr != nil || secondErr != nil {
		t.Fatalf("production observations = %v / %v", firstErr, secondErr)
	}
	if got := []string{firstObservations[0].RepositoryID, firstObservations[1].RepositoryID, firstObservations[2].RepositoryID}; !reflect.DeepEqual(got, []string{"root", "alpha", "beta"}) {
		t.Fatalf("production observations are not parent-first: %v", got)
	}
	if !reflect.DeepEqual(firstObservations, secondObservations) || !reflect.DeepEqual(firstFailures, secondFailures) {
		t.Fatalf("production observation result is unstable:\n%#v\n%#v", firstFailures, secondFailures)
	}
	for _, id := range []string{"alpha", "beta"} {
		found := false
		for _, failure := range firstFailures {
			if failure.RepositoryID == id && failure.Check == "advertised-ref-observation" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing aggregated remote failure for %q: %#v", id, firstFailures)
		}
	}
	if !bytes.Equal(current, driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})) || !bytes.Equal(candidate, driftManifest(t, candidateRepositories)) {
		t.Fatal("production observation mutated manifest bytes")
	}
}

func TestUpdateSnapshotCollectorAncestryErrorsAndCancellationRemainObservationBoundaries(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("initial.txt", "initial\n", "initial")
	remoteURL, err := exec.Command("git", "-C", repository.Path, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatal(err)
	}
	writer := filepath.Join(t.TempDir(), "writer")
	if output, cloneErr := exec.Command("git", "clone", string(bytes.TrimSpace(remoteURL)), writer).CombinedOutput(); cloneErr != nil {
		t.Fatalf("clone writer: %v: %s", cloneErr, output)
	}
	if output, checkoutErr := exec.Command("git", "-C", writer, "checkout", "-b", "main", "origin/main").CombinedOutput(); checkoutErr != nil {
		t.Fatalf("checkout writer main: %v: %s", checkoutErr, output)
	}
	for _, arguments := range [][]string{{"config", "user.name", "wtree test"}, {"config", "user.email", "wtree@example.invalid"}} {
		if output, configErr := exec.Command("git", append([]string{"-C", writer}, arguments...)...).CombinedOutput(); configErr != nil {
			t.Fatalf("configure writer: %v: %s", configErr, output)
		}
	}
	if err := os.WriteFile(filepath.Join(writer, "advanced.txt"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "--", "advanced.txt"}, {"commit", "-m", "advance remote"}, {"push", "origin", "main"}} {
		if output, commandErr := exec.Command("git", append([]string{"-C", writer}, arguments...)...).CombinedOutput(); commandErr != nil {
			t.Fatalf("advance remote: %v: %s", commandErr, output)
		}
	}

	adapter := gitadapter.NewAdapter("git")
	head, err := adapter.Head(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	common, err := adapter.CommonGitDir(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	portable := driftRepository("", ".")
	portable.Identity.InitialCommits = []string{head}
	collector := &UpdateSnapshotCollector{
		Project: domain.Project{Version: domain.CurrentVersion, ID: "project", Name: "Project", ConfigPath: filepath.Join(repository.Path, ".wtree.yml"), BaseRepository: "other", LogicalRoot: repository.Path, Repositories: []domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: common, SourcePath: repository.Path}}},
		git:     adapter,
	}
	before := updateObservationGitFacts(t, repository.Path)
	secret := "ancestry-secret"
	collector.isAncestor = func(context.Context, string, string, string) (bool, error) {
		return false, fmt.Errorf("remote https://user:%s@example.invalid/root.git ancestry failed", secret)
	}
	observation, failures, observeErr := collector.observeExisting(context.Background(), "root", repository.Path, portable, nil, "project.wtree.yml")
	if observeErr != nil || observation.CanFastForward || !observation.AdvertisedKnown {
		t.Fatalf("ancestry operational error became a classification: %#v %#v %v", observation, failures, observeErr)
	}
	if len(failures) != 1 || failures[0].RepositoryID != "root" || failures[0].Check != "ancestry-observation" || strings.Contains(fmt.Sprintf("%#v", failures), secret) {
		t.Fatalf("ancestry failure lacks bounded redacted provenance: %#v", failures)
	}
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": portable})
	snapshotInput := driftCompleteInput(t, DriftSnapshotInput{Project: collector.Project, DefaultWorkspace: domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: repository.Path, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: head, Mount: ".", ResolvedPath: repository.Path}}}, CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{observation}})
	snapshotInput.Collection.Errors = failures
	snapshot, snapshotErr := BuildDriftSnapshot(snapshotInput)
	var typed *DriftPreflightError
	if !errors.As(snapshotErr, &typed) || len(snapshot.Failures()) == 0 || strings.Contains(fmt.Sprintf("%#v %v", typed, snapshot.Failures()), secret) {
		t.Fatalf("ancestry failure did not remain typed/redacted snapshot evidence: %#v %v", snapshot.Failures(), snapshotErr)
	}
	if after := updateObservationGitFacts(t, repository.Path); !reflect.DeepEqual(before, after) {
		t.Fatalf("ancestry observation mutated Git state: before=%#v after=%#v", before, after)
	}

	ctx, cancel := context.WithCancel(context.Background())
	collector.isAncestor = func(context.Context, string, string, string) (bool, error) {
		cancel()
		return false, errors.New("late ancestry failure")
	}
	if _, _, observeErr := collector.observeExisting(ctx, "root", repository.Path, portable, nil, "project.wtree.yml"); !errors.Is(observeErr, context.Canceled) {
		t.Fatalf("final-repository ancestry cancellation = %v", observeErr)
	}
	if after := updateObservationGitFacts(t, repository.Path); !reflect.DeepEqual(before, after) {
		t.Fatalf("cancelled ancestry observation mutated Git state: before=%#v after=%#v", before, after)
	}
}

func updateObservationGitFacts(t *testing.T, repository string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for name, arguments := range map[string][]string{
		"head":   {"rev-parse", "HEAD"},
		"refs":   {"show-ref", "--head"},
		"status": {"status", "--porcelain=v1", "--untracked-files=all"},
	} {
		output, err := exec.Command("git", append([]string{"-C", repository}, arguments...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("capture Git %s: %v: %s", name, err, output)
		}
		result[name] = string(output)
	}
	return result
}
