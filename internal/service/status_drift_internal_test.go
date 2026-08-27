package service

import (
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
)

func TestApplyLocalStatusDriftProjectsParentFirstSetAndIdentityEvidence(t *testing.T) {
	project := domain.Project{Repositories: []domain.Repository{
		{ID: "root"},
		{ID: "child", ParentID: "root"},
	}}
	value := WorkspaceStatus{Repositories: []RepositoryStatus{
		{ID: "root", ExpectedIdentity: "/git/root", HeadMismatch: true},
		{ID: "child", ParentID: "root", ExpectedIdentity: "/git/child"},
	}}
	snapshot := DriftSnapshot{
		defaultWorkspace: domain.Workspace{ID: "default", Name: "default", RootPath: "/tree"},
		differences: []DriftSetDifference{
			{ID: "child", Origin: "disk", Check: "disk-only"},
			{ID: "root", Origin: "manifest", Check: "checkout"},
		},
		retained:     []RetainedUnmanagedFact{{RepositoryID: "child"}},
		observations: []DriftRepositoryObservation{{RepositoryID: "root", CommonGitDir: "/git/other", IdentityKnown: true, IdentityMatches: false}},
	}
	applyLocalStatusDrift(&value, project, snapshot.defaultWorkspace, snapshot)
	if !value.Repositories[0].IdentityMismatch || value.Repositories[0].ActualIdentity != "/git/other" {
		t.Fatalf("root identity facts = %#v", value.Repositories[0])
	}
	want := []StatusDrift{
		{ID: "root", Origin: "manifest", Check: "checkout", Status: "declared-absent"},
		{ID: "root", Origin: "checkout", Check: "head", Status: "mismatch"},
		{ID: "root", Origin: "checkout", Check: "identity", Status: "mismatch"},
		{ID: "child", ParentID: "root", Origin: "disk", Check: "disk-only", Status: "state-or-disk-not-manifest"},
		{ID: "child", ParentID: "root", Origin: "retained", Check: "retained-unmanaged", Status: "retained-unmanaged"},
	}
	if len(value.Drift) != len(want) {
		t.Fatalf("drift = %#v, want %#v", value.Drift, want)
	}
	for index := range want {
		if value.Drift[index] != want[index] {
			t.Fatalf("drift[%d] = %#v, want %#v", index, value.Drift[index], want[index])
		}
	}
}

func TestApplyStatusFallbackDriftKeepsOnlyIndependentDurableEvidence(t *testing.T) {
	value := WorkspaceStatus{}
	project := domain.Project{Repositories: []domain.Repository{{ID: "root"}, {ID: "child", ParentID: "root"}}}
	applyStatusFallbackDrift(&value, project, []DoctorFinding{
		{Code: "manifest-configuration-mismatch"},
		{Code: "retained-unmanaged-repository", RepositoryID: "child"},
		{Code: "update-recovery-record"},
	})
	want := []StatusDrift{
		{ID: "child", ParentID: "root", Origin: "retained", Check: "retained-unmanaged", Status: "retained-unmanaged"},
		{Origin: "operation", Check: "update-recovery-record", Status: "incomplete-operation"},
	}
	if len(value.Drift) != len(want) {
		t.Fatalf("fallback drift = %#v, want %#v", value.Drift, want)
	}
	for index := range want {
		if value.Drift[index] != want[index] {
			t.Fatalf("fallback drift[%d] = %#v, want %#v", index, value.Drift[index], want[index])
		}
	}
}

func TestApplyLocalStatusDriftProjectsProjectAuthoritiesAndOperationsOnce(t *testing.T) {
	project := domain.Project{Repositories: []domain.Repository{{ID: "root"}}}
	value := WorkspaceStatus{Repositories: []RepositoryStatus{{ID: "root", Missing: true, Status: "missing"}}}
	defaultWorkspace := domain.Workspace{ID: "default", Name: "default", RootPath: "/tree"}
	snapshot := DriftSnapshot{
		defaultWorkspace: defaultWorkspace,
		failures: []DriftFailure{
			{RepositoryID: "root", Check: "checkout"},
			{RepositoryID: "root", Check: "identity"},
			{RepositoryID: "project", Check: "local-config"},
			{RepositoryID: "project", Check: "registry-generation"},
			{RepositoryID: "project", Check: "default-state"},
			{RepositoryID: "project", Check: "workspace-state"},
			{RepositoryID: "project", Check: "unresolved-operation"},
		},
		operations: []DriftOperationRecord{
			{Path: "/data/projects/project/recovery/default.json", Operation: "remove"},
			{Path: "/data/projects/project/update/active", Operation: "update"},
		},
	}
	applyLocalStatusDrift(&value, project, defaultWorkspace, snapshot)
	want := []StatusDrift{
		{ID: "root", Origin: "manifest", Check: "checkout", Status: "declared-absent"},
		{Origin: "authority", Check: "default-state", Status: "inconsistent"},
		{Origin: "authority", Check: "local-config", Status: "inconsistent"},
		{Origin: "authority", Check: "registry-generation", Status: "inconsistent"},
		{Origin: "authority", Check: "unresolved-operation", Status: "inconsistent"},
		{Path: "/data/projects/project/update/active", Origin: "operation", Check: "update-in-progress", Status: "incomplete-operation"},
		{Path: "/data/projects/project/recovery/default.json", Origin: "operation", Check: "update-recovery-record", Status: "incomplete-operation"},
		{Origin: "authority", Check: "workspace-state", Status: "inconsistent"},
	}
	if len(value.Drift) != len(want) {
		t.Fatalf("drift = %#v, want %#v", value.Drift, want)
	}
	for index := range want {
		if value.Drift[index] != want[index] {
			t.Fatalf("drift[%d] = %#v, want %#v", index, value.Drift[index], want[index])
		}
	}
}

func TestApplyLocalStatusDriftKeepsDefaultIdentitySeparateFromSelectedWorkspace(t *testing.T) {
	project := domain.Project{Repositories: []domain.Repository{{ID: "root"}}}
	defaultWorkspace := domain.Workspace{ID: "default", Name: "default", RootPath: "/default"}
	selected := domain.Workspace{ID: "feature", Name: "feature", RootPath: "/feature"}
	wantStatus := RepositoryStatus{
		ID:               "root",
		Branch:           "feature",
		ExpectedBranch:   "feature",
		Head:             "selected-head",
		ExpectedHead:     "selected-head",
		Mount:            ".",
		ActualMount:      ".",
		ExpectedIdentity: "/git/root",
		ActualIdentity:   "/git/root",
		Clean:            true,
		Upstream:         true,
		Status:           "clean",
	}
	value := WorkspaceStatus{Repositories: []RepositoryStatus{wantStatus}}
	snapshot := DriftSnapshot{
		defaultWorkspace: defaultWorkspace,
		observations: []DriftRepositoryObservation{{
			RepositoryID:    "root",
			Path:            "/default",
			CommonGitDir:    "/git/replacement",
			IdentityKnown:   true,
			IdentityMatches: false,
		}},
		failures: []DriftFailure{{RepositoryID: "root", Check: "identity"}},
	}

	applyLocalStatusDrift(&value, project, selected, snapshot)
	if value.Repositories[0] != wantStatus {
		t.Fatalf("selected repository status = %#v, want %#v", value.Repositories[0], wantStatus)
	}
	wantDrift := []StatusDrift{{ID: "root", Path: "/default", Origin: "default-checkout", Check: "identity", Status: "mismatch"}}
	if len(value.Drift) != len(wantDrift) || value.Drift[0] != wantDrift[0] {
		t.Fatalf("default-only drift = %#v, want %#v", value.Drift, wantDrift)
	}
}

func TestApplyLocalStatusDriftRequiresExactDefaultWorkspaceBinding(t *testing.T) {
	project := domain.Project{Repositories: []domain.Repository{{ID: "root"}}}
	defaultWorkspace := domain.Workspace{ID: "default", Name: "default", RootPath: "/default"}
	snapshot := DriftSnapshot{
		defaultWorkspace: defaultWorkspace,
		observations:     []DriftRepositoryObservation{{RepositoryID: "root", CommonGitDir: "/git/default", IdentityKnown: true, IdentityMatches: false}},
	}
	for _, selected := range []domain.Workspace{
		{ID: "feature", Name: "default", RootPath: "/default"},
		{ID: "default", Name: "feature", RootPath: "/default"},
		{ID: "default", Name: "default", RootPath: "/alias"},
	} {
		value := WorkspaceStatus{Repositories: []RepositoryStatus{{ID: "root", ActualIdentity: "/git/selected"}}}
		applyLocalStatusDrift(&value, project, selected, snapshot)
		if value.Repositories[0].ActualIdentity != "/git/selected" || value.Repositories[0].IdentityMismatch {
			t.Fatalf("alias selected workspace %#v received default facts: %#v", selected, value.Repositories[0])
		}
	}

	value := WorkspaceStatus{Repositories: []RepositoryStatus{{ID: "root"}}}
	applyLocalStatusDrift(&value, project, defaultWorkspace, snapshot)
	if value.Repositories[0].ActualIdentity != "/git/default" || !value.Repositories[0].IdentityMismatch {
		t.Fatalf("exact default workspace did not receive snapshot facts: %#v", value.Repositories[0])
	}
}
