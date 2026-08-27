package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestDriftSnapshotClassifiesParentFirstAndIsImmutable(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{
		"root": driftRepository("", "."), "child": driftRepository("root", "child"),
	})
	candidate := driftManifest(t, map[string]config.PortableRepository{
		"root": driftRepository("", "."), "child": driftRepository("root", "child"), "added": driftRepository("root", "added"),
	})
	project := driftProject([]domain.Repository{
		{ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"},
		{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"},
	})
	workspace := driftWorkspace(project)
	snapshot, err := driftBuild(t, DriftSnapshotInput{
		Project: project, DefaultWorkspace: workspace, CurrentManifest: current, CandidateManifest: candidate,
		Observations: []DriftRepositoryObservation{
			{RepositoryID: "child", Path: "/tree/child", CommonGitDir: "/git/child", Branch: "main", Head: driftOID('1'), Clean: true, AdvertisedCommit: driftOID('1'), IgnoreVerified: true, TrackedManifestExact: true},
			{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('2'), CanFastForward: true, IgnoreVerified: true, TrackedManifestExact: true},
			{RepositoryID: "added", Path: "/tree/added", TargetAbsent: true, IgnoreVerified: true, AdvertisedCommit: driftOID('3')},
		},
	})
	if err != nil {
		t.Fatalf("BuildDriftSnapshot: %v", err)
	}
	if !snapshot.MayUpdate() {
		t.Fatalf("snapshot rejected: %#v", snapshot.Failures())
	}
	repositories := snapshot.Repositories()
	if got, want := []string{repositories[0].ID, repositories[1].ID, repositories[2].ID}, []string{"root", "added", "child"}; !sameStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if repositories[0].Classification != UpdateClassificationFastForwardable {
		t.Fatalf("root = %s", repositories[0].Classification)
	}
	if repositories[1].Classification != UpdateClassificationAdded {
		t.Fatalf("added = %s", repositories[1].Classification)
	}
	if repositories[2].Classification != UpdateClassificationUnchanged {
		t.Fatalf("child = %s", repositories[2].Classification)
	}
	repositories[0].Path = "mutated"
	if snapshot.Repositories()[0].Path == "mutated" {
		t.Fatal("repository accessor leaked mutable state")
	}
	current[0] ^= 1
	if bytes.Equal(current, snapshot.CurrentManifestBytes()) {
		t.Fatal("input bytes leaked into snapshot")
	}
	defaultWorkspace := snapshot.DefaultWorkspace()
	defaultWorkspace.Checkouts[0].Branch = "mutated"
	if snapshot.DefaultWorkspace().Checkouts[0].Branch == "mutated" {
		t.Fatal("default workspace accessor leaked mutable state")
	}
}

func TestDriftSnapshotRejectsStructuralAndWorkspaceStateChangesWithoutMutation(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	candidate := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "added": driftRepository("root", "added")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	workspace := driftWorkspace(project)
	raw, err := store.WorkspaceBytes(store.WorkspaceState{ID: "feature", Name: "feature", Path: "/feature", Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Mount: ".", ResolvedPath: "/feature", Head: driftOID('0')}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := driftBuild(t, DriftSnapshotInput{
		Project: project, DefaultWorkspace: workspace, CurrentManifest: current, CandidateManifest: candidate,
		PersistedWorkspaces: []PersistedWorkspaceGeneration{{Path: "/data/state/project/feature.json", Bytes: raw}},
		Observations:        []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true, IgnoreVerified: true}, {RepositoryID: "added", Path: "/tree/added", TargetAbsent: false, IgnoreVerified: true}},
	})
	if err != nil {
		t.Fatalf("BuildDriftSnapshot: %v", err)
	}
	if snapshot.MayUpdate() {
		t.Fatal("repository-set change with named workspace was accepted")
	}
	if !snapshot.HasFailure("project", "non-default-workspace-repository-set-change") {
		t.Fatalf("failures = %#v", snapshot.Failures())
	}
	if !bytes.Equal(raw, snapshot.PersistedWorkspaces()[0].Bytes()) {
		t.Fatal("snapshot changed persisted workspace bytes")
	}
}

func TestDriftSnapshotRecordsObservationEvidenceWithoutExecutionTarget(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('1'), TrackedManifestExact: true, IgnoreVerified: true}}})
	if err != nil {
		t.Fatal(err)
	}
	fact := snapshot.Repositories()[0]
	if fact.ObservedCommit != driftOID('1') || fact.ExecutionCommit != "" {
		t.Fatalf("observation = %#v", fact)
	}
}

func TestUpdateClassificationRejectsObservedCheckoutDrift(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	base := DriftRepositoryObservation{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}
	cases := []struct {
		name, check string
		change      func(*DriftRepositoryObservation)
		want        UpdateClassification
	}{
		{"dirty", "cleanliness", func(value *DriftRepositoryObservation) { value.Clean = false }, UpdateClassificationDirty},
		{"detached", "branch", func(value *DriftRepositoryObservation) { value.Detached = true }, UpdateClassificationDivergent},
		{"missing", "checkout", func(value *DriftRepositoryObservation) { value.TargetAbsent, value.Path = true, "" }, UpdateClassificationMissing},
		{"identity", "identity", func(value *DriftRepositoryObservation) { value.IdentityKnown, value.IdentityMatches = true, false }, UpdateClassificationStructurallyInconsistent},
		{"tracked manifest", "tracked-manifest", func(value *DriftRepositoryObservation) {
			value.TrackedManifestKnown, value.TrackedManifestExact = true, false
		}, UpdateClassificationStructurallyInconsistent},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			observation := base
			test.change(&observation)
			snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: current, Observations: []DriftRepositoryObservation{observation}})
			if err != nil {
				t.Fatal(err)
			}
			if got := snapshot.Repositories()[0].Classification; got != test.want {
				t.Fatalf("classification = %q, want %q", got, test.want)
			}
			if !snapshot.HasFailure("root", test.check) {
				t.Fatalf("failures = %#v", snapshot.Failures())
			}
		})
	}
}

func TestUpdateClassificationRejectsCandidateContractAndIgnoreDrift(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "child": driftRepository("root", "child")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}, {ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"}})
	observations := []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}, {RepositoryID: "child", Path: "/tree/child", CommonGitDir: "/git/child", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), IgnoreKnown: true}}
	candidateMount := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "child": driftRepository("root", "moved")})
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: candidateMount, Observations: observations})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Repositories()[1].Classification; got != UpdateClassificationMountChangeBlocked {
		t.Fatalf("mount classification = %q", got)
	}
	if !snapshot.HasFailure("child", "mount-change") {
		t.Fatalf("failures = %#v", snapshot.Failures())
	}
	observations[1].IgnoreVerified = false
	snapshot, err = driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: current, Observations: observations})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Repositories()[1].Classification; got != UpdateClassificationStructurallyInconsistent {
		t.Fatalf("ignore classification = %q", got)
	}
	if !snapshot.HasFailure("child", "parent-ignore") {
		t.Fatalf("failures = %#v", snapshot.Failures())
	}
}

func TestDriftSnapshotRejectsImportedPartialWorkspaceAndRedactsOperationDiagnostics(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	partial, err := store.WorkspaceBytes(store.WorkspaceState{ID: "imported", Name: "imported", Path: "/imported", Partial: true, MissingRepositoryIDs: []string{"root"}, Repositories: map[string]store.CheckoutState{}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: current, PersistedWorkspaces: []PersistedWorkspaceGeneration{{Path: "/data/state/project/imported.json", Bytes: partial}}, Operations: []DriftOperationRecord{{Path: "/data/projects/project/update/one", Operation: "update", Diagnostic: "https://user:secret@example.test/project"}}, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.HasFailure("project", "unresolved-operation") {
		t.Fatalf("failures = %#v", snapshot.Failures())
	}
	if bytes.Contains([]byte(snapshot.Operations()[0].Diagnostic), []byte("secret")) {
		t.Fatalf("unredacted operation = %#v", snapshot.Operations()[0])
	}
	for _, failure := range snapshot.Failures() {
		if bytes.Contains([]byte(failure.Message), []byte("secret")) {
			t.Fatalf("unredacted failure %#v", failure)
		}
	}
	if !bytes.Equal(partial, snapshot.PersistedWorkspaces()[0].Bytes()) {
		t.Fatal("partial workspace bytes changed")
	}
	if !snapshot.HasImportedPartialWorkspace() || snapshot.HasNonDefaultCompleteWorkspace() {
		t.Fatalf("workspace generation flags = partial:%t complete:%t", snapshot.HasImportedPartialWorkspace(), snapshot.HasNonDefaultCompleteWorkspace())
	}
}

func TestDriftSnapshotRejectsInconsistentRegistryGeneration(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	registry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"project": {ConfigPath: "/tree/.wtree.yml", RepositoryIDs: map[string]string{"different": "root"}}}}
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Registry: &registry, RegistryKnown: true, RegistryConsistent: false, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.HasFailure("project", "registry-generation") {
		t.Fatalf("failures = %#v", snapshot.Failures())
	}
}

func TestDriftSnapshotNamedWorkspaceAllowsFastForwardButRejectsSetChange(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	withAdded := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "added": driftRepository("root", "added")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	named, err := store.WorkspaceBytes(store.WorkspaceState{ID: "feature", Name: "feature", Path: "/feature", Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Mount: ".", ResolvedPath: "/feature", Head: driftOID('0')}}})
	if err != nil {
		t.Fatal(err)
	}
	base := DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, PersistedWorkspaces: []PersistedWorkspaceGeneration{{Path: "/data/state/project/feature.json", Bytes: named}}, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('1'), CanFastForward: true, TrackedManifestExact: true}}}
	base.CandidateManifest = current
	snapshot, err := driftBuild(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.MayUpdate() || snapshot.Repositories()[0].Classification != UpdateClassificationFastForwardable {
		t.Fatalf("pure fast-forward = %#v / %#v", snapshot.Repositories(), snapshot.Failures())
	}
	if !snapshot.HasNonDefaultCompleteWorkspace() || snapshot.HasImportedPartialWorkspace() {
		t.Fatalf("workspace generation flags = complete:%t partial:%t", snapshot.HasNonDefaultCompleteWorkspace(), snapshot.HasImportedPartialWorkspace())
	}
	base.CandidateManifest = withAdded
	base.Observations = append(base.Observations, DriftRepositoryObservation{RepositoryID: "added", Path: "/tree/added", TargetAbsent: true, IgnoreVerified: true, AdvertisedCommit: driftOID('1')})
	snapshot, err = driftBuild(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MayUpdate() || !snapshot.HasFailure("project", "non-default-workspace-repository-set-change") {
		t.Fatalf("set change = %#v", snapshot.Failures())
	}
	if !bytes.Equal(named, snapshot.PersistedWorkspaces()[0].Bytes()) {
		t.Fatal("named workspace bytes changed")
	}
}

func TestDriftSnapshotClassifiesRemovedRetainedInParentFirstOrder(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "child": driftRepository("root", "child")})
	candidate := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"}, {ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: candidate, Observations: []DriftRepositoryObservation{{RepositoryID: "child", Path: "/tree/child", CommonGitDir: "/git/child", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), IgnoreVerified: true}, {RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	if err != nil {
		t.Fatal(err)
	}
	repositories := snapshot.Repositories()
	if got, want := []string{repositories[0].ID, repositories[1].ID}, []string{"root", "child"}; !sameStrings(got, want) {
		t.Fatalf("order = %v", got)
	}
	if repositories[1].Classification != UpdateClassificationRemovedRetained {
		t.Fatalf("removed classification = %q", repositories[1].Classification)
	}
	retained := snapshot.RetainedUnmanaged()
	if len(retained) != 1 || retained[0].RepositoryID != "child" || retained[0].Path != "/tree/child" {
		t.Fatalf("prospective retained fact = %#v", retained)
	}
}

func TestDriftSnapshotRejectsInvalidRemovedRetentionAndRecordsUnionFacts(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "child": driftRepository("root", "child")})
	candidate := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}, {ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"}})
	state, err := store.WorkspaceBytes(store.WorkspaceState{ID: "ghost", Name: "ghost", Path: "/ghost", Repositories: map[string]store.CheckoutState{"state-only": {Branch: "main", Mount: ".", ResolvedPath: "/ghost", Head: driftOID('0')}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: candidate, PersistedWorkspaces: []PersistedWorkspaceGeneration{{Path: "/data/state/project/ghost.json", Bytes: state}}, RetainedUnmanaged: []RetainedUnmanagedFact{{RepositoryID: "retained-only", Path: "/retained", CommonGitDir: "/git/retained"}}, Observations: []DriftRepositoryObservation{
		{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true},
		{RepositoryID: "child", Path: "/tree/child", CommonGitDir: "/git/child", Branch: "main", Head: driftOID('0'), Clean: false, AdvertisedCommit: driftOID('0'), IgnoreVerified: true},
		{RepositoryID: "disk-only", Path: "/disk"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.HasFailure("child", "removed-retained") || !snapshot.HasFailure("disk-only", "disk-only") || !snapshot.HasFailure("state-only", "state-only") || !snapshot.HasFailure("retained-only", "retained-unmanaged") {
		t.Fatalf("union failures = %#v", snapshot.Failures())
	}
	got := snapshot.SetDifferences()
	if len(got) != 4 || got[0].ID != "child" || got[0].Check != "current-only" || got[1].ID != "disk-only" || got[2].ID != "retained-only" || got[3].ID != "state-only" {
		t.Fatalf("set differences = %#v", got)
	}
}

func TestDriftSnapshotAcceptsOneValidPriorRetainedRepositoryWithoutBlocking(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	retained := RetainedUnmanagedFact{RepositoryID: "old", Path: "/retained/old", CommonGitDir: "/git/old"}
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, RetainedUnmanaged: []RetainedUnmanagedFact{retained}, Observations: []DriftRepositoryObservation{
		{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('1'), CanFastForward: true, TrackedManifestExact: true},
		{RepositoryID: "old", Path: retained.Path, CommonGitDir: retained.CommonGitDir, Branch: "changed", Head: driftOID('0'), Clean: false, IdentityKnown: true, IdentityMatches: true},
	}})
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("prior retained snapshot = %#v, %v", snapshot.Failures(), err)
	}
	rows := snapshot.Repositories()
	if len(rows) != 2 || rows[0].ID != "old" || rows[0].Classification != UpdateClassificationRemovedRetained {
		t.Fatalf("prior retained rows = %#v", rows)
	}
	differences := snapshot.SetDifferences()
	if len(differences) != 1 || differences[0] != (DriftSetDifference{ID: "old", Origin: "retained", Check: "retained-only"}) {
		t.Fatalf("prior retained provenance = %#v", differences)
	}
}

func TestDriftSnapshotHistoricalRetentionDoesNotBlockLaterFastForward(t *testing.T) {
	manifestA := driftManifest(t, map[string]config.PortableRepository{
		"root": driftRepository("", "."),
		"old":  driftRepository("root", "old"),
	})
	manifestB := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	projectA := driftProject([]domain.Repository{
		{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"},
		{ID: "old", ParentID: "root", DefaultMount: "old", DefaultBranch: "main", CommonGitDir: "/git/old", SourcePath: "/tree/old"},
	})
	projectB := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})

	// The first A -> B update establishes the durable retained fact. A later
	// B -> B fast-forward must treat that fact as diagnostic history, not as a
	// new repository-set change.
	first, err := driftBuild(t, DriftSnapshotInput{
		Project: projectA, DefaultWorkspace: driftWorkspace(projectA), CurrentManifest: manifestA, CandidateManifest: manifestB,
		Observations: []DriftRepositoryObservation{
			{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true},
			{RepositoryID: "old", Path: "/tree/old", CommonGitDir: "/git/old", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), IgnoreVerified: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	retained := first.RetainedUnmanaged()
	if len(retained) != 1 || retained[0] != (RetainedUnmanagedFact{RepositoryID: "old", Path: "/tree/old", CommonGitDir: "/git/old"}) {
		t.Fatalf("A -> B retained evidence = %#v", retained)
	}

	workspaceBytes := func(t *testing.T, partial bool) []byte {
		t.Helper()
		state := store.WorkspaceState{ID: "feature", Name: "feature", Path: "/feature", Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Mount: ".", ResolvedPath: "/feature", Head: driftOID('0')}}}
		if partial {
			state = store.WorkspaceState{ID: "imported", Name: "imported", Path: "/imported", Partial: true, MissingRepositoryIDs: []string{"root"}, Repositories: map[string]store.CheckoutState{}}
		}
		data, marshalErr := store.WorkspaceBytes(state)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return data
	}
	for _, test := range []struct {
		name    string
		partial bool
	}{{name: "complete"}, {name: "imported-partial", partial: true}} {
		t.Run(test.name, func(t *testing.T) {
			persisted := workspaceBytes(t, test.partial)
			base := driftCompleteInput(t, DriftSnapshotInput{
				Project: projectB, DefaultWorkspace: driftWorkspace(projectB), CurrentManifest: manifestB, CandidateManifest: manifestB,
				PersistedWorkspaces: []PersistedWorkspaceGeneration{{Path: "/data/state/project/" + test.name + ".json", Bytes: persisted}},
				RetainedUnmanaged:   retained,
			})
			root := DriftRepositoryObservation{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, IdentityKnown: true, IdentityMatches: true, UpstreamKnown: true, Upstream: gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: "https://example.test/project.git"}, AdvertisedCommit: driftOID('1'), CanFastForward: true, TrackedManifestExact: true}
			old := DriftRepositoryObservation{RepositoryID: "old", Path: "/tree/old", CommonGitDir: "/git/old", Branch: "changed", Head: driftOID('9'), Clean: false, Detached: true, IdentityKnown: true, IdentityMatches: true}
			var wantRepositories []DriftRepository
			var wantDifferences []DriftSetDifference
			for index, observations := range [][]DriftRepositoryObservation{{root, old}, {old, root}} {
				input := base
				input.Observations = observations
				snapshot, buildErr := BuildDriftSnapshot(input)
				if buildErr != nil || !snapshot.MayUpdate() || snapshot.HasFailure("project", "non-default-workspace-repository-set-change") {
					t.Fatalf("B -> B fast-forward = %#v, %v", snapshot.Failures(), buildErr)
				}
				if !bytes.Equal(persisted, snapshot.PersistedWorkspaces()[0].Bytes()) || !bytes.Equal(base.DefaultState.Bytes, snapshot.DefaultState().Bytes) {
					t.Fatal("persisted workspace generation bytes changed")
				}
				rows, differences := snapshot.Repositories(), snapshot.SetDifferences()
				rowCount, differenceCount := 0, 0
				for _, row := range rows {
					if row.ID == "old" {
						rowCount++
						if row.Classification != UpdateClassificationRemovedRetained || len(row.Failures) != 0 {
							t.Fatalf("historical retained row = %#v", row)
						}
					}
				}
				for _, difference := range differences {
					if difference == (DriftSetDifference{ID: "old", Origin: "retained", Check: "retained-only"}) {
						differenceCount++
					}
				}
				if rowCount != 1 || differenceCount != 1 {
					t.Fatalf("historical retained provenance rows=%#v differences=%#v", rows, differences)
				}
				if index == 0 {
					wantRepositories, wantDifferences = rows, differences
				} else if !reflect.DeepEqual(rows, wantRepositories) || !reflect.DeepEqual(differences, wantDifferences) {
					t.Fatalf("observation permutation changed output: rows=%#v want=%#v differences=%#v want=%#v", rows, wantRepositories, differences, wantDifferences)
				}
			}
		})
	}
}

func TestDriftSnapshotRejectsActualRepositorySetChangesForAllNamedWorkspaceKinds(t *testing.T) {
	manifestA := driftManifest(t, map[string]config.PortableRepository{
		"root": driftRepository("", "."),
		"old":  driftRepository("root", "old"),
	})
	manifestB := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	manifestWithAdded := driftManifest(t, map[string]config.PortableRepository{
		"root":  driftRepository("", "."),
		"added": driftRepository("root", "added"),
	})
	projectA := driftProject([]domain.Repository{
		{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"},
		{ID: "old", ParentID: "root", DefaultMount: "old", DefaultBranch: "main", CommonGitDir: "/git/old", SourcePath: "/tree/old"},
	})
	projectB := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	for _, test := range []struct {
		name               string
		current, candidate []byte
		project            domain.Project
		partial, addition  bool
	}{
		{name: "addition-complete", current: manifestB, candidate: manifestWithAdded, project: projectB, addition: true},
		{name: "addition-imported-partial", current: manifestB, candidate: manifestWithAdded, project: projectB, partial: true, addition: true},
		{name: "removal-complete", current: manifestA, candidate: manifestB, project: projectA},
		{name: "removal-imported-partial", current: manifestA, candidate: manifestB, project: projectA, partial: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := store.WorkspaceState{ID: "feature", Name: "feature", Path: "/feature", Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Mount: ".", ResolvedPath: "/feature", Head: driftOID('0')}}}
			if test.partial {
				state = store.WorkspaceState{ID: "imported", Name: "imported", Path: "/imported", Partial: true, MissingRepositoryIDs: []string{"root"}, Repositories: map[string]store.CheckoutState{}}
			}
			persisted, err := store.WorkspaceBytes(state)
			if err != nil {
				t.Fatal(err)
			}
			observations := []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}
			if test.addition {
				observations = append(observations, DriftRepositoryObservation{RepositoryID: "added", Path: "/tree/added", TargetAbsent: true, IgnoreVerified: true, AdvertisedCommit: driftOID('1')})
			}
			if !test.addition {
				observations = append(observations, DriftRepositoryObservation{RepositoryID: "old", Path: "/tree/old", CommonGitDir: "/git/old", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), IgnoreVerified: true})
			}
			snapshot, buildErr := driftBuild(t, DriftSnapshotInput{Project: test.project, DefaultWorkspace: driftWorkspace(test.project), CurrentManifest: test.current, CandidateManifest: test.candidate, PersistedWorkspaces: []PersistedWorkspaceGeneration{{Path: "/data/state/project/" + test.name + ".json", Bytes: persisted}}, Observations: observations})
			if buildErr != nil || snapshot.MayUpdate() || !snapshot.HasFailure("project", "non-default-workspace-repository-set-change") {
				t.Fatalf("actual set change was accepted: failures=%#v err=%v", snapshot.Failures(), buildErr)
			}
			if !bytes.Equal(persisted, snapshot.PersistedWorkspaces()[0].Bytes()) {
				t.Fatal("persisted workspace bytes changed")
			}
		})
	}
}

func TestDriftSnapshotRequiresPositiveIdentityAndCopiesCapturedEvidence(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	input := driftCompleteInput(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	input.Observations[0].IdentityKnown, input.Observations[0].IdentityMatches = false, false
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.HasFailure("root", "identity") {
		t.Fatalf("zero identity was accepted: %#v, %v", snapshot.Failures(), err)
	}
	input = driftCompleteInput(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	input.Reconciliation = DriftFileGeneration{Path: "/data/projects/project/reconciliation.json", Bytes: []byte("captured")}
	snapshot, err = BuildDriftSnapshot(input)
	if err != nil {
		t.Fatal(err)
	}
	input.DefaultState.Bytes[0] ^= 1
	input.Reconciliation.Bytes[0] ^= 1
	input.Observations[0].Path = "/mutated"
	reconciliation := snapshot.ReconciliationGeneration()
	reconciliation.Bytes[0] ^= 1
	if bytes.Equal(input.DefaultState.Bytes, snapshot.DefaultState().Bytes) || bytes.Equal(input.Reconciliation.Bytes, snapshot.ReconciliationGeneration().Bytes) || bytes.Equal(reconciliation.Bytes, snapshot.ReconciliationGeneration().Bytes) || snapshot.Observations()[0].Path == "/mutated" {
		t.Fatal("snapshot accessor leaked captured evidence")
	}
}

func TestDriftSnapshotBindsLocalConfigLogicalRootAndSourcePath(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	project.ConfigPath, project.LogicalRoot = "/tree/config/.wtree.yml", "/tree"
	input := driftCompleteInput(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	local := driftLocalConfig(project)
	local.LogicalRoot = ".."
	local.Repositories["root"] = config.Repository{Source: "wrong", Parent: "", DefaultMount: ".", DefaultBranch: "main"}
	data, err := config.MarshalProject(local)
	if err != nil {
		t.Fatal(err)
	}
	input.LocalConfig, input.LocalConfigBytes = &local, data
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.HasFailure("root", "local-config") {
		t.Fatalf("source mismatch = %#v, %v", snapshot.Failures(), err)
	}
}

func TestDriftSnapshotRequiresAuthoritativeConfigPathAndManifestBinding(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	base := DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}}
	input := driftCompleteInput(t, base)
	input.Project.ConfigPath = ""
	_, err := BuildDriftSnapshot(input)
	var typed *DriftPreflightError
	if !errors.As(err, &typed) || typed.Failure.Check != "local-config" {
		t.Fatalf("empty config path = %#v", err)
	}
	input = driftCompleteInput(t, base)
	input.CurrentManifestPath = "/other/project.wtree.yml"
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.HasFailure("project", "local-config") {
		t.Fatalf("manifest path mismatch = %#v, %v", snapshot.Failures(), err)
	}
	input = driftCompleteInput(t, base)
	input.CurrentManifestSource = "https://example.test/other.wtree.yml"
	snapshot, err = BuildDriftSnapshot(input)
	if err != nil || !snapshot.HasFailure("project", "local-config") {
		t.Fatalf("manifest source mismatch = %#v, %v", snapshot.Failures(), err)
	}
	input = driftCompleteInput(t, base)
	local := *input.LocalConfig
	local.Manifest = config.ManifestMetadata{}
	data, marshalErr := config.MarshalProject(local)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	input.LocalConfig, input.LocalConfigBytes = &local, data
	snapshot, err = BuildDriftSnapshot(input)
	if err != nil || !snapshot.HasFailure("project", "local-config") {
		t.Fatalf("empty manifest binding = %#v, %v", snapshot.Failures(), err)
	}
}

func TestDriftSnapshotSortsAndRedactsCompleteCollectionFailures(t *testing.T) {
	input := DriftSnapshotInput{Collection: DriftCollectionEvidence{CurrentManifestKnown: true, CandidateManifestKnown: true, ConfigKnown: true, RegistryKnown: true, DefaultStateKnown: true, WorkspaceInventoryKnown: true, RetainedKnown: true, OperationInventoryKnown: true, ObservationInventoryKnown: true, Errors: []DriftFailure{{RepositoryID: "https://user:secret@example.invalid", Check: "z", Message: "https://user:secret@example.invalid"}, {RepositoryID: "project", Check: "a", Message: "first"}}}}
	snapshot, err := BuildDriftSnapshot(input)
	var typed *DriftPreflightError
	if !errors.As(err, &typed) || len(snapshot.Failures()) != 2 || snapshot.Failures()[0].Check != "a" || bytes.Contains([]byte(fmt.Sprint(snapshot.Failures())), []byte("secret")) {
		t.Fatalf("collection provenance = %#v, %v", snapshot.Failures(), err)
	}
	want := snapshot.Failures()
	encoded, marshalErr := json.Marshal(typed)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if !reflect.DeepEqual(snapshot.CollectionEvidence().Errors, want) || !reflect.DeepEqual(typed.Snapshot.Failures(), want) || !reflect.DeepEqual(typed.Snapshot.CollectionEvidence().Errors, want) || typed.Failure != want[0] || typed.Error() != want[0].Check+": "+want[0].Message || bytes.Contains([]byte(fmt.Sprintf("%#v %#v %s %s", typed, snapshot.CollectionEvidence(), typed.Error(), encoded)), []byte("secret")) {
		t.Fatalf("collection failure views disagree or leak: snapshot=%#v evidence=%#v typed=%#v", want, snapshot.CollectionEvidence().Errors, typed)
	}
	reversed := input
	reversed.Collection.Errors = []DriftFailure{input.Collection.Errors[1], input.Collection.Errors[0]}
	reversedSnapshot, reversedErr := BuildDriftSnapshot(reversed)
	var reversedTyped *DriftPreflightError
	if !errors.As(reversedErr, &reversedTyped) || !reflect.DeepEqual(reversedSnapshot.Failures(), want) || !reflect.DeepEqual(reversedSnapshot.CollectionEvidence().Errors, want) || !reflect.DeepEqual(reversedTyped.Snapshot.Failures(), want) {
		t.Fatalf("reversed collection provenance = %#v %#v %v", reversedSnapshot.Failures(), reversedSnapshot.CollectionEvidence().Errors, reversedErr)
	}
}

func TestDriftSnapshotPriorRetainedMismatchAlwaysKeepsOneRowAndDifference(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	retained := RetainedUnmanagedFact{RepositoryID: "old", Path: "/retained/old", CommonGitDir: "/git/old"}
	root := DriftRepositoryObservation{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}
	tests := []struct {
		name, check string
		observation *DriftRepositoryObservation
	}{
		{name: "missing", check: "retained-unmanaged"},
		{name: "path", check: "path", observation: &DriftRepositoryObservation{RepositoryID: "old", Path: "/other", CommonGitDir: retained.CommonGitDir, IdentityKnown: true, IdentityMatches: true}},
		{name: "identity", check: "identity", observation: &DriftRepositoryObservation{RepositoryID: "old", Path: retained.Path, CommonGitDir: "/git/other", IdentityKnown: true, IdentityMatches: false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observations := []DriftRepositoryObservation{root}
			if test.observation != nil {
				observations = append(observations, *test.observation)
			}
			snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, RetainedUnmanaged: []RetainedUnmanagedFact{retained}, Observations: observations})
			if err != nil {
				t.Fatal(err)
			}
			rows, differences := snapshot.Repositories(), snapshot.SetDifferences()
			rowCount, differenceCount := 0, 0
			for _, row := range rows {
				if row.ID == retained.RepositoryID {
					rowCount++
				}
			}
			for _, difference := range differences {
				if difference == (DriftSetDifference{ID: retained.RepositoryID, Origin: "retained", Check: "retained-only"}) {
					differenceCount++
				}
			}
			if rowCount != 1 || differenceCount != 1 || !snapshot.HasFailure(retained.RepositoryID, test.check) {
				t.Fatalf("rows=%#v differences=%#v failures=%#v", rows, differences, snapshot.Failures())
			}
		})
	}
}

func TestDriftSnapshotRetainedUnionIsStableAcrossPermutationsAndMixedState(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	stateBytes, err := store.WorkspaceBytes(store.WorkspaceState{Version: store.Version, ID: "named", Name: "named", Path: "/named", Repositories: map[string]store.CheckoutState{"old": {Branch: "changed", Mount: ".", ResolvedPath: "/retained/old", Head: driftOID('9')}}})
	if err != nil {
		t.Fatal(err)
	}
	retained := RetainedUnmanagedFact{RepositoryID: "old", Path: "/retained/old", CommonGitDir: "/git/old"}
	root := DriftRepositoryObservation{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}
	old := DriftRepositoryObservation{RepositoryID: "old", Path: retained.Path, CommonGitDir: retained.CommonGitDir, Branch: "changed", Head: driftOID('9'), Clean: false, Detached: true, IdentityKnown: true, IdentityMatches: true}
	var wantRepositories []DriftRepository
	var wantDifferences []DriftSetDifference
	for index, observations := range [][]DriftRepositoryObservation{{root, old}, {old, root}} {
		snapshot, buildErr := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, PersistedWorkspaces: []PersistedWorkspaceGeneration{{Path: "/data/state/project/named.json", Bytes: stateBytes}}, RetainedUnmanaged: []RetainedUnmanagedFact{retained}, Observations: observations})
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		rows, differences := snapshot.Repositories(), snapshot.SetDifferences()
		oldRows := 0
		for _, row := range rows {
			if row.ID == "old" {
				oldRows++
				if row.Classification != UpdateClassificationRemovedRetained || len(row.Failures) != 0 {
					t.Fatalf("modified unmanaged row = %#v", row)
				}
			}
		}
		retainedDifferences, stateDifferences := 0, 0
		for _, difference := range differences {
			if difference.ID == "old" && difference.Origin == "retained" {
				retainedDifferences++
			}
			if difference.ID == "old" && difference.Origin == "state" {
				stateDifferences++
			}
		}
		if oldRows != 1 || retainedDifferences != 1 || stateDifferences != 1 {
			t.Fatalf("rows=%#v differences=%#v", rows, differences)
		}
		if index == 0 {
			wantRepositories, wantDifferences = rows, differences
		} else if !reflect.DeepEqual(rows, wantRepositories) || !reflect.DeepEqual(differences, wantDifferences) {
			t.Fatalf("permutation changed output: rows=%#v want=%#v differences=%#v want=%#v", rows, wantRepositories, differences, wantDifferences)
		}
	}
}

func TestDriftSnapshotRetainedReAddMergesIntoCandidateRow(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	candidate := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "old": driftRepository("root", "old")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	retained := RetainedUnmanagedFact{RepositoryID: "old", Path: "/retained/old", CommonGitDir: "/git/old"}
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: candidate, RetainedUnmanaged: []RetainedUnmanagedFact{retained}, Observations: []DriftRepositoryObservation{
		{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true},
		{RepositoryID: "old", Path: retained.Path, CommonGitDir: retained.CommonGitDir, TargetAbsent: true, AdvertisedCommit: driftOID('1'), IgnoreVerified: true, IdentityKnown: true, IdentityMatches: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rows := snapshot.Repositories()
	count := 0
	for _, row := range rows {
		if row.ID == "old" {
			count++
		}
	}
	if count != 1 || !snapshot.HasFailure("old", "retained-unmanaged") {
		t.Fatalf("re-add rows=%#v failures=%#v", rows, snapshot.Failures())
	}
}

func TestDriftSnapshotReadersCollectEachAuthorityOnceAndDeepCopy(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	base := driftCompleteInput(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	calls := 0
	count := func() { calls++ }
	readers := DriftSnapshotReaders{
		ReadCurrentManifest: func(context.Context) (DriftManifestGeneration, error) {
			count()
			return DriftManifestGeneration{Path: base.CurrentManifestPath, Source: base.CurrentManifestSource, Bytes: base.CurrentManifest}, nil
		}, ReadCandidateManifest: func(context.Context) ([]byte, error) { count(); return base.CandidateManifest, nil },
		ReadLocalConfig: func(context.Context) ([]byte, *config.ProjectConfig, error) {
			count()
			return base.LocalConfigBytes, base.LocalConfig, nil
		}, ReadRegistry: func(context.Context) ([]byte, *store.Registry, error) {
			count()
			return base.RegistryBytes, base.Registry, nil
		},
		ReadDefaultState: func(context.Context) (PersistedWorkspaceGeneration, error) { count(); return base.DefaultState, nil },
		ReadObservations: func(context.Context) (domain.Project, domain.Workspace, []DriftRepositoryObservation, error) {
			count()
			return base.Project, base.DefaultWorkspace, base.Observations, nil
		},
		Inventory: DriftInventoryReader{DataDir: "/data", ReadDir: func(_ context.Context, path string) ([]DriftDirectoryEntry, error) {
			if path == WorkspaceStateDirectory("/data", project.ID) {
				return []DriftDirectoryEntry{{Name: "default.json", Regular: true}}, nil
			}
			return nil, os.ErrNotExist
		}, Lstat: func(context.Context, string) (DriftDirectoryEntry, error) {
			return DriftDirectoryEntry{}, os.ErrNotExist
		}, ReadFile: func(_ context.Context, path string) ([]byte, error) {
			if path == WorkspaceStatePath("/data", project.ID, "default") {
				return append([]byte(nil), base.DefaultState.Bytes...), nil
			}
			return nil, os.ErrNotExist
		}, DecodeReconciliation: func(string, []byte) ([]RetainedUnmanagedFact, error) { return nil, nil }, DecodeOperation: func(string, []byte) (DriftOperationRecord, error) { return DriftOperationRecord{}, nil }},
	}
	input, err := readers.CollectDriftSnapshot(context.Background())
	if err != nil || calls != 6 || !input.Collection.Complete() {
		t.Fatalf("collection = %#v, %v, calls=%d", input.Collection, err, calls)
	}
	base.CurrentManifest[0] ^= 1
	if bytes.Equal(base.CurrentManifest, input.CurrentManifest) {
		t.Fatal("collector leaked mutable authority bytes")
	}
	if _, err := BuildDriftSnapshot(input); err != nil {
		t.Fatalf("collected snapshot: %v", err)
	}
}

func TestDriftSnapshotReadersUseFixedAuthoritiesAndOneDefaultGeneration(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	base := driftCompleteInput(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	workspaceDirectory := WorkspaceStateDirectory("/data", project.ID)
	defaultPath := WorkspaceStatePath("/data", project.ID, "default")
	reconciliationPath := filepath.Join("/data", "projects", project.ID, "reconciliation.json")
	recoveryDirectory := filepath.Join("/data", "projects", project.ID, "recovery")
	recoveryPath := filepath.Join(recoveryDirectory, "workspace.json")
	updateDirectory := filepath.Join("/data", "projects", project.ID, "update")
	oldRetainedDirectory := filepath.Join("/data", "projects", project.ID, "retained")
	readDirs := []string{}
	files := map[string][]byte{defaultPath: base.DefaultState.Bytes, reconciliationPath: []byte("reconciliation"), recoveryPath: []byte("recovery")}
	directories := map[string][]DriftDirectoryEntry{
		workspaceDirectory: {{Name: "default.json", Regular: true}},
		recoveryDirectory:  {{Name: "workspace.json", Regular: true}},
		updateDirectory:    {{Name: "operation-1", Directory: true}},
	}
	readers := DriftSnapshotReaders{
		ReadCurrentManifest: func(context.Context) (DriftManifestGeneration, error) {
			return DriftManifestGeneration{Path: base.CurrentManifestPath, Source: base.CurrentManifestSource, Bytes: base.CurrentManifest}, nil
		},
		ReadCandidateManifest: func(context.Context) ([]byte, error) { return base.CandidateManifest, nil },
		ReadLocalConfig: func(context.Context) ([]byte, *config.ProjectConfig, error) {
			return base.LocalConfigBytes, base.LocalConfig, nil
		},
		ReadRegistry: func(context.Context) ([]byte, *store.Registry, error) { return base.RegistryBytes, base.Registry, nil },
		ReadObservations: func(context.Context) (domain.Project, domain.Workspace, []DriftRepositoryObservation, error) {
			return base.Project, base.DefaultWorkspace, base.Observations, nil
		},
		Inventory: DriftInventoryReader{
			DataDir: "/data",
			ReadDir: func(_ context.Context, path string) ([]DriftDirectoryEntry, error) {
				readDirs = append(readDirs, path)
				entries, ok := directories[path]
				if !ok {
					return nil, fmt.Errorf("optional: %w", os.ErrNotExist)
				}
				return append([]DriftDirectoryEntry(nil), entries...), nil
			},
			Lstat: func(_ context.Context, path string) (DriftDirectoryEntry, error) {
				if path != reconciliationPath {
					return DriftDirectoryEntry{}, os.ErrNotExist
				}
				return DriftDirectoryEntry{Name: "reconciliation.json", Regular: true}, nil
			},
			ReadFile: func(_ context.Context, path string) ([]byte, error) { return append([]byte(nil), files[path]...), nil },
			DecodeReconciliation: func(path string, data []byte) ([]RetainedUnmanagedFact, error) {
				if path != reconciliationPath || string(data) != "reconciliation" {
					t.Fatalf("decoder received %q %q", path, data)
				}
				return []RetainedUnmanagedFact{{RepositoryID: "old", Path: "/old", CommonGitDir: "/git/old"}}, nil
			},
			DecodeOperation: func(path string, data []byte) (DriftOperationRecord, error) {
				if path != recoveryPath || string(data) != "recovery" {
					t.Fatalf("recovery decoder received %q %q", path, data)
				}
				return DriftOperationRecord{Operation: "create"}, nil
			},
		},
	}
	input, err := readers.CollectDriftSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if input.DefaultState.Path != defaultPath || !bytes.Equal(input.DefaultState.Bytes, base.DefaultState.Bytes) || len(input.PersistedWorkspaces) != 0 {
		t.Fatalf("default generation = %#v, workspaces=%#v", input.DefaultState, input.PersistedWorkspaces)
	}
	if input.Reconciliation.Path != reconciliationPath || !bytes.Equal(input.Reconciliation.Bytes, []byte("reconciliation")) || len(input.RetainedUnmanaged) != 1 {
		t.Fatalf("reconciliation = %#v retained=%#v", input.Reconciliation, input.RetainedUnmanaged)
	}
	if len(input.Operations) != 2 || input.Operations[0].Path != recoveryPath || input.Operations[0].Operation != "create" || input.Operations[1].Path != filepath.Join(updateDirectory, "operation-1") || input.Operations[1].Operation != "update" {
		t.Fatalf("operations = %#v", input.Operations)
	}
	if strings.Contains(strings.Join(readDirs, "\n"), oldRetainedDirectory) {
		t.Fatalf("read obsolete retained authority: %v", readDirs)
	}
	for _, path := range []string{workspaceDirectory, recoveryDirectory, updateDirectory} {
		count := 0
		for _, actual := range readDirs {
			if actual == path {
				count++
			}
		}
		if count != 2 {
			t.Fatalf("ReadDir(%q) calls=%d, want stable double capture; all=%v", path, count, readDirs)
		}
	}
}

func TestCollectDriftSnapshotTurnsReaderFailureIntoRedactedTypedFailure(t *testing.T) {
	_, err := CollectDriftSnapshot(context.Background(), DriftSnapshotReaders{
		ReadCurrentManifest: func(context.Context) (DriftManifestGeneration, error) {
			return DriftManifestGeneration{}, errors.New("https://user:secret@example.invalid/manifest")
		},
		ReadCandidateManifest: func(context.Context) ([]byte, error) { return nil, nil },
		ReadLocalConfig:       func(context.Context) ([]byte, *config.ProjectConfig, error) { return nil, nil, nil },
		ReadRegistry:          func(context.Context) ([]byte, *store.Registry, error) { return nil, nil, nil },
		ReadDefaultState: func(context.Context) (PersistedWorkspaceGeneration, error) {
			return PersistedWorkspaceGeneration{}, nil
		},
		ReadObservations: func(context.Context) (domain.Project, domain.Workspace, []DriftRepositoryObservation, error) {
			return domain.Project{}, domain.Workspace{}, nil, nil
		},
		Inventory: DriftInventoryReader{DataDir: "/data", ReadDir: func(context.Context, string) ([]DriftDirectoryEntry, error) { return nil, nil }, Lstat: func(context.Context, string) (DriftDirectoryEntry, error) {
			return DriftDirectoryEntry{}, os.ErrNotExist
		}, ReadFile: func(context.Context, string) ([]byte, error) { return nil, nil }, DecodeReconciliation: func(string, []byte) ([]RetainedUnmanagedFact, error) { return nil, nil }, DecodeOperation: func(string, []byte) (DriftOperationRecord, error) { return DriftOperationRecord{}, nil }},
	})
	var typed *DriftPreflightError
	if !errors.As(err, &typed) || typed.Failure.Check != "current-manifest-collection" || bytes.Contains([]byte(typed.Error()), []byte("secret")) {
		t.Fatalf("reader error = %#v", err)
	}
}

func TestDriftSnapshotInventoryRejectsUnexpectedOrUnreadableEntriesBeforeClassification(t *testing.T) {
	reader := DriftInventoryReader{DataDir: "/data", ReadDir: func(context.Context, string) ([]DriftDirectoryEntry, error) {
		return []DriftDirectoryEntry{{Name: "hidden", Regular: true}}, nil
	}, Lstat: func(context.Context, string) (DriftDirectoryEntry, error) {
		return DriftDirectoryEntry{}, os.ErrNotExist
	}, ReadFile: func(context.Context, string) ([]byte, error) { return nil, errors.New("reader race") }, DecodeReconciliation: func(string, []byte) ([]RetainedUnmanagedFact, error) { return nil, nil }, DecodeOperation: func(string, []byte) (DriftOperationRecord, error) { return DriftOperationRecord{}, nil }}
	if _, err := reader.workspaceInventory(context.Background(), "project"); err == nil || bytes.Contains([]byte(err.Error()), []byte("hidden")) {
		t.Fatalf("unexpected inventory entry = %v", err)
	}
	reader.ReadDir = func(context.Context, string) ([]DriftDirectoryEntry, error) {
		return []DriftDirectoryEntry{{Name: "one.json", Regular: true}}, nil
	}
	if _, err := reader.workspaceInventory(context.Background(), "project"); err == nil || !bytes.Contains([]byte(err.Error()), []byte("workspace-inventory")) {
		t.Fatalf("read failure provenance = %v", err)
	}
}

func TestDriftSnapshotInventoryRejectsMembershipAndTypeRaces(t *testing.T) {
	ctx := context.Background()
	authorities := []struct {
		name  string
		entry DriftDirectoryEntry
		run   func(DriftInventoryReader) error
	}{
		{name: "workspace", entry: DriftDirectoryEntry{Name: "default.json", Regular: true}, run: func(reader DriftInventoryReader) error {
			_, err := reader.workspaceInventory(ctx, "project")
			return err
		}},
		{name: "recovery", entry: DriftDirectoryEntry{Name: "one.json", Regular: true}, run: func(reader DriftInventoryReader) error {
			_, err := reader.recoveryInventory(ctx, "project")
			return err
		}},
		{name: "update", entry: DriftDirectoryEntry{Name: "one", Directory: true}, run: func(reader DriftInventoryReader) error { _, err := reader.updateInventory(ctx, "project"); return err }},
	}
	mutations := []struct {
		name   string
		second func(DriftDirectoryEntry) []DriftDirectoryEntry
	}{
		{name: "add", second: func(entry DriftDirectoryEntry) []DriftDirectoryEntry {
			extra := entry
			extra.Name = "added" + filepath.Ext(entry.Name)
			return []DriftDirectoryEntry{entry, extra}
		}},
		{name: "remove", second: func(DriftDirectoryEntry) []DriftDirectoryEntry { return nil }},
		{name: "type", second: func(entry DriftDirectoryEntry) []DriftDirectoryEntry {
			entry.Regular, entry.Directory = entry.Directory, entry.Regular
			return []DriftDirectoryEntry{entry}
		}},
		{name: "symlink", second: func(entry DriftDirectoryEntry) []DriftDirectoryEntry {
			entry.Regular, entry.Directory, entry.Symlink = false, false, true
			return []DriftDirectoryEntry{entry}
		}},
	}
	for _, authority := range authorities {
		for _, mutation := range mutations {
			t.Run(authority.name+" "+mutation.name, func(t *testing.T) {
				calls := 0
				reader := DriftInventoryReader{
					DataDir: "/data",
					ReadDir: func(context.Context, string) ([]DriftDirectoryEntry, error) {
						calls++
						if calls == 1 {
							return []DriftDirectoryEntry{authority.entry}, nil
						}
						return mutation.second(authority.entry), nil
					},
					ReadFile: func(context.Context, string) ([]byte, error) { return []byte(`{"version":1}`), nil },
					DecodeOperation: func(path string, _ []byte) (DriftOperationRecord, error) {
						return DriftOperationRecord{Path: path, Operation: "recovery"}, nil
					},
				}
				if err := authority.run(reader); err == nil || calls != 2 {
					t.Fatalf("race was accepted: calls=%d err=%v", calls, err)
				}
			})
		}
	}
}

func TestDriftSnapshotReconciliationRejectsTypeReadDecodeAndReplacementRaces(t *testing.T) {
	path := filepath.Join("/data", "projects", "project", "reconciliation.json")
	regular := DriftDirectoryEntry{Name: "reconciliation.json", Regular: true}
	tests := []struct {
		name        string
		stats       []DriftDirectoryEntry
		statErrors  []error
		reads       [][]byte
		readErrors  []error
		decodeError error
	}{
		{name: "symlink", stats: []DriftDirectoryEntry{{Name: "reconciliation.json", Symlink: true}}},
		{name: "directory", stats: []DriftDirectoryEntry{{Name: "reconciliation.json", Directory: true}}},
		{name: "read", stats: []DriftDirectoryEntry{regular}, readErrors: []error{errors.New("read race")}},
		{name: "decode", stats: []DriftDirectoryEntry{regular, regular}, reads: [][]byte{[]byte("same"), []byte("same")}, decodeError: errors.New("decode race")},
		{name: "bytes replaced", stats: []DriftDirectoryEntry{regular, regular}, reads: [][]byte{[]byte("first"), []byte("second")}},
		{name: "type replaced", stats: []DriftDirectoryEntry{regular, {Name: "reconciliation.json", Directory: true}}, reads: [][]byte{[]byte("same"), []byte("same")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statIndex, readIndex := 0, 0
			reader := DriftInventoryReader{
				DataDir: "/data",
				Lstat: func(context.Context, string) (DriftDirectoryEntry, error) {
					index := statIndex
					statIndex++
					if index < len(test.statErrors) && test.statErrors[index] != nil {
						return DriftDirectoryEntry{}, test.statErrors[index]
					}
					if index >= len(test.stats) {
						return regular, nil
					}
					return test.stats[index], nil
				},
				ReadFile: func(context.Context, string) ([]byte, error) {
					index := readIndex
					readIndex++
					if index < len(test.readErrors) && test.readErrors[index] != nil {
						return nil, test.readErrors[index]
					}
					if index >= len(test.reads) {
						return []byte("same"), nil
					}
					return test.reads[index], nil
				},
				DecodeReconciliation: func(string, []byte) ([]RetainedUnmanagedFact, error) { return nil, test.decodeError },
			}
			if _, _, err := reader.reconciliationInventory(context.Background(), "project"); err == nil || !strings.Contains(err.Error(), "retained-inventory") {
				t.Fatalf("reconciliation race accepted at %q: %v", path, err)
			}
		})
	}
	reader := DriftInventoryReader{DataDir: "/data", Lstat: func(context.Context, string) (DriftDirectoryEntry, error) {
		return DriftDirectoryEntry{}, fmt.Errorf("optional: %w", os.ErrNotExist)
	}}
	generation, facts, err := reader.reconciliationInventory(context.Background(), "project")
	if err != nil || generation.Path != "" || len(facts) != 0 {
		t.Fatalf("optional reconciliation absence = %#v %#v %v", generation, facts, err)
	}
}

func TestDriftSnapshotReadersRejectDefaultReplacementBetweenCaptures(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	base := driftCompleteInput(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}}})
	readers := DriftSnapshotReaders{
		ReadCurrentManifest: func(context.Context) (DriftManifestGeneration, error) {
			return DriftManifestGeneration{Path: base.CurrentManifestPath, Source: base.CurrentManifestSource, Bytes: base.CurrentManifest}, nil
		},
		ReadCandidateManifest: func(context.Context) ([]byte, error) { return base.CandidateManifest, nil },
		ReadLocalConfig: func(context.Context) ([]byte, *config.ProjectConfig, error) {
			return base.LocalConfigBytes, base.LocalConfig, nil
		},
		ReadRegistry: func(context.Context) ([]byte, *store.Registry, error) { return base.RegistryBytes, base.Registry, nil },
		ReadDefaultState: func(context.Context) (PersistedWorkspaceGeneration, error) {
			return PersistedWorkspaceGeneration{Path: base.DefaultState.Path, Bytes: []byte("replacement")}, nil
		},
		ReadObservations: func(context.Context) (domain.Project, domain.Workspace, []DriftRepositoryObservation, error) {
			return base.Project, base.DefaultWorkspace, base.Observations, nil
		},
		Inventory: DriftInventoryReader{
			DataDir: "/data",
			ReadDir: func(_ context.Context, path string) ([]DriftDirectoryEntry, error) {
				if path == WorkspaceStateDirectory("/data", project.ID) {
					return []DriftDirectoryEntry{{Name: "default.json", Regular: true}}, nil
				}
				return nil, os.ErrNotExist
			},
			Lstat: func(context.Context, string) (DriftDirectoryEntry, error) {
				return DriftDirectoryEntry{}, os.ErrNotExist
			},
			ReadFile:             func(context.Context, string) ([]byte, error) { return base.DefaultState.Bytes, nil },
			DecodeReconciliation: func(string, []byte) ([]RetainedUnmanagedFact, error) { return nil, nil },
			DecodeOperation:      func(string, []byte) (DriftOperationRecord, error) { return DriftOperationRecord{}, nil },
		},
	}
	if _, err := readers.CollectDriftSnapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "default-state-collection") {
		t.Fatalf("default replacement accepted: %v", err)
	}
}

func TestDriftSnapshotTypedCollectionAndInventoryFailures(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	_, err := BuildDriftSnapshot(DriftSnapshotInput{Collection: DriftCollectionEvidence{ConfigKnown: true}, Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest})
	var typed *DriftPreflightError
	if !errors.As(err, &typed) || typed.Failure.Check != "collection-completeness" {
		t.Fatalf("collection error = %#v", err)
	}
	_, err = driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: []byte("https://user:secret@example.invalid"), CandidateManifest: manifest})
	if !errors.As(err, &typed) || typed.Failure.Check != "current-manifest" || bytes.Contains([]byte(typed.Error()), []byte("secret")) {
		t.Fatalf("manifest error = %#v", err)
	}
}

func TestDriftSnapshotClassifiesUnexpectedDiskAndDuplicateObservation(t *testing.T) {
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	root := DriftRepositoryObservation{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true}
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{root, {RepositoryID: "ghost", Path: "/ghost"}}})
	if err != nil || !snapshot.HasFailure("ghost", "disk-only") {
		t.Fatalf("disk union = %#v, %v", snapshot.Failures(), err)
	}
	_, err = driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{root, root}})
	var typed *DriftPreflightError
	if !errors.As(err, &typed) || typed.Failure.Check != "observation-inventory" {
		t.Fatalf("duplicate error = %#v", err)
	}
}

func TestUpdateClassificationRejectsActualUpstreamMismatch(t *testing.T) {
	current := driftRepository("", ".")
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	fact := classifyExistingDriftRepository("root", current, current, DriftRepositoryObservation{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, IdentityKnown: true, IdentityMatches: true, AdvertisedCommit: driftOID('0'), TrackedManifestExact: true, UpstreamKnown: true, Upstream: gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: "https://other.test/project.git"}}, project, true)
	if fact.Classification != UpdateClassificationStructurallyInconsistent || len(fact.Failures) != 1 || fact.Failures[0].Check != "upstream" {
		t.Fatalf("upstream classification = %#v", fact)
	}
}

func TestDriftSnapshotUsesActualUpstreamBeforeAnyUpdateAndRefusesDeletion(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("tracked.txt", "one\n", "initial")
	adapter := gitadapter.NewAdapter("git")
	upstream, err := adapter.Upstream(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := adapter.Head(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	common, err := adapter.CommonGitDir(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	portable := driftRepository("", ".")
	portable.Clone.URL = upstream.FetchURL
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": portable})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: common, SourcePath: repository.Path}})
	workspace := driftWorkspace(project)
	workspace.RootPath = repository.Path
	observation := DriftRepositoryObservation{RepositoryID: "root", Path: repository.Path, CommonGitDir: common, Branch: "main", Head: head, Clean: true, IdentityKnown: true, IdentityMatches: true, AdvertisedCommit: head, AdvertisedKnown: true, TrackedManifestExact: true, UpstreamKnown: true, Upstream: upstream}
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: workspace, CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{observation}})
	if err != nil || !snapshot.MayUpdate() || snapshot.Repositories()[0].ExecutionCommit != "" {
		t.Fatalf("published upstream snapshot = %#v, %v", snapshot, err)
	}
	other := testutil.NewBareGitRemote(t)
	repository.Run(t, "remote", "set-url", "origin", other)
	changed, err := adapter.Upstream(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	observation.Upstream = changed
	snapshot, err = driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: workspace, CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{observation}})
	if err != nil || !snapshot.HasFailure("root", "upstream") {
		t.Fatalf("altered remote was not refused before fetch: %#v, %v", snapshot.Failures(), err)
	}
	observation.Upstream = upstream
	observation.AdvertisedCommit, observation.AdvertisedKnown = "", true
	snapshot, err = driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: workspace, CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{observation}})
	if err != nil || !snapshot.HasFailure("root", "advertised-ref") {
		t.Fatalf("deleted selected ref was not refused: %#v, %v", snapshot.Failures(), err)
	}
	observation.AdvertisedCommit = head
	observation.Upstream.FetchURL = "https://user:secret@example.invalid/repository.git"
	snapshot, err = driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: workspace, CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{observation}})
	if err != nil || !snapshot.HasFailure("root", "upstream") || bytes.Contains([]byte(snapshot.Failures()[0].Message), []byte("secret")) {
		t.Fatalf("credential-bearing upstream diagnostic = %#v, %v", snapshot.Failures(), err)
	}
}

func driftManifest(t *testing.T, repositories map[string]config.PortableRepository) []byte {
	t.Helper()
	data, err := config.MarshalPortableManifest(config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "project", Name: "Project", BaseRepository: "root"}, Repositories: repositories})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func driftRepository(parent, mount string) config.PortableRepository {
	return config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: "https://example.test/project.git"}, Upstream: config.Upstream{Remote: "origin", Branch: "main", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{driftOID('a')}}, Parent: parent, Mount: mount, DefaultBranch: "main"}
}
func driftProject(repositories []domain.Repository) domain.Project {
	logicalRoot := "/tree"
	for _, repository := range repositories {
		if repository.ID == "root" {
			logicalRoot = repository.SourcePath
			break
		}
	}
	return domain.Project{Version: domain.CurrentVersion, ID: "project", Name: "Project", ConfigPath: filepath.Join(logicalRoot, ".wtree.yml"), BaseRepository: "root", LogicalRoot: logicalRoot, Repositories: repositories}
}
func driftWorkspace(project domain.Project) domain.Workspace {
	checkouts := make([]domain.Checkout, 0, len(project.Repositories))
	for _, repository := range project.Repositories {
		checkouts = append(checkouts, domain.Checkout{RepositoryID: repository.ID, Branch: "main", Head: driftOID('0'), Mount: repository.DefaultMount, ResolvedPath: repository.SourcePath})
	}
	return domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: "/tree", Checkouts: checkouts}
}

// driftBuild supplies the complete authority capture required by production
// preflight. Individual tests override only the fact they are exercising;
// known-empty inventories remain explicit rather than being inferred.
func driftBuild(t *testing.T, input DriftSnapshotInput) (DriftSnapshot, error) {
	t.Helper()
	return BuildDriftSnapshot(driftCompleteInput(t, input))
}

func driftCompleteInput(t *testing.T, input DriftSnapshotInput) DriftSnapshotInput {
	t.Helper()
	if input.DataDir == "" {
		input.DataDir = "/data"
	}
	input.Collection = DriftCollectionEvidence{
		CurrentManifestKnown: true, CandidateManifestKnown: true,
		ConfigKnown: true, RegistryKnown: true, DefaultStateKnown: true,
		WorkspaceInventoryKnown: true, RetainedKnown: true,
		OperationInventoryKnown: true, ObservationInventoryKnown: true,
	}
	if input.LocalConfig == nil {
		local := driftLocalConfig(input.Project)
		input.LocalConfig = &local
	}
	if len(input.LocalConfigBytes) == 0 {
		data, err := config.MarshalProject(*input.LocalConfig)
		if err != nil {
			t.Fatal(err)
		}
		input.LocalConfigBytes = data
	}
	if input.CurrentManifestPath == "" {
		input.CurrentManifestPath = filepath.Join(filepath.Dir(input.Project.ConfigPath), input.LocalConfig.Manifest.Path)
	}
	if input.CurrentManifestSource == "" {
		input.CurrentManifestSource = input.LocalConfig.Manifest.Source
	}
	if input.Registry == nil {
		registry := driftRegistry(input.Project)
		input.Registry = &registry
	}
	if len(input.RegistryBytes) == 0 {
		data, err := store.RegistryBytes(*input.Registry)
		if err != nil {
			t.Fatal(err)
		}
		input.RegistryBytes = data
	}
	if len(input.DefaultState.Bytes) == 0 {
		for checkoutIndex := range input.DefaultWorkspace.Checkouts {
			for _, observation := range input.Observations {
				if observation.RepositoryID == input.DefaultWorkspace.Checkouts[checkoutIndex].RepositoryID && observation.Path != "" && !observation.TargetAbsent {
					input.DefaultWorkspace.Checkouts[checkoutIndex].Branch = observation.Branch
					input.DefaultWorkspace.Checkouts[checkoutIndex].Head = observation.Head
					input.DefaultWorkspace.Checkouts[checkoutIndex].Detached = observation.Detached
					if observation.Detached {
						input.DefaultWorkspace.Checkouts[checkoutIndex].Branch = ""
					}
				}
			}
		}
		data, err := store.WorkspaceBytes(driftWorkspaceState(input.DefaultWorkspace))
		if err != nil {
			t.Fatal(err)
		}
		input.DefaultState = PersistedWorkspaceGeneration{Path: WorkspaceStatePath(input.DataDir, input.Project.ID, "default"), Bytes: data}
	}
	if len(input.RetainedUnmanaged) != 0 && input.Reconciliation.Path == "" {
		input.Reconciliation = DriftFileGeneration{Path: filepath.Join(input.DataDir, "projects", input.Project.ID, "reconciliation.json"), Bytes: []byte("captured reconciliation generation")}
	}
	for index := range input.Observations {
		observation := &input.Observations[index]
		if !observation.IdentityKnown {
			observation.IdentityKnown, observation.IdentityMatches = true, true
		}
		if !observation.UpstreamKnown {
			observation.UpstreamKnown = true
			observation.Upstream = gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: "https://example.test/project.git"}
		}
	}
	return input
}

func driftLocalConfig(project domain.Project) config.ProjectConfig {
	repositories := make(map[string]config.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		source, err := filepath.Rel(project.LogicalRoot, repository.SourcePath)
		if err != nil {
			panic(err)
		}
		repositories[repository.ID] = config.Repository{Source: filepath.ToSlash(source), Parent: repository.ParentID, DefaultMount: repository.DefaultMount, DefaultBranch: repository.DefaultBranch}
	}
	logicalRoot, err := filepath.Rel(filepath.Dir(project.ConfigPath), project.LogicalRoot)
	if err != nil {
		panic(err)
	}
	return config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: project.ID, Name: project.Name, BaseRepository: project.BaseRepository}, LogicalRoot: filepath.ToSlash(logicalRoot), Repositories: repositories, Worktrees: config.Worktrees{Root: "/worktrees"}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: "https://example.test/project.wtree.yml"}}
}

func driftRegistry(project domain.Project) store.Registry {
	identities := make(map[string]string, len(project.Repositories))
	for _, repository := range project.Repositories {
		identities[repository.CommonGitDir] = repository.ID
	}
	return store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{project.ID: {Name: project.Name, ConfigPath: project.ConfigPath, RepositoryIDs: identities}}}
}

func driftWorkspaceState(workspace domain.Workspace) store.WorkspaceState {
	repositories := make(map[string]store.CheckoutState, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		repositories[checkout.RepositoryID] = store.CheckoutState{Branch: checkout.Branch, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath, Head: checkout.Head, Detached: checkout.Detached}
	}
	return store.WorkspaceState{Version: store.Version, ID: workspace.ID, Name: workspace.Name, Path: workspace.RootPath, Partial: workspace.Partial, MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...), Repositories: repositories}
}
func driftOID(character rune) string { return string(bytes.Repeat([]byte(string(character)), 40)) }
func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
