package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
)

func TestDoctorReportsAndRepairsVerifiedMovedMountOnlyWithFix(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "feature")
	root.Run(t, "branch", "feature/doctor")
	root.Run(t, "worktree", "add", target, "feature/doctor")
	backend.Run(t, "branch", "feature/doctor")
	backend.Run(t, "worktree", "add", filepath.Join(target, "api"), "feature/doctor")
	git := gitadapter.NewAdapter("git")
	rootHead, err := git.Head(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	backendHead, err := git.Head(context.Background(), filepath.Join(target, "api"))
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, "doctor")
	if err := store.WriteWorkspace(statePath, store.WorkspaceState{
		ID: "doctor", Name: "doctor", Path: target,
		Repositories: map[string]store.CheckoutState{
			"root":    {Branch: "feature/doctor", Head: rootHead, Mount: ".", ResolvedPath: target},
			"backend": {Branch: "feature/doctor", Head: backendHead, Mount: "backend", ResolvedPath: filepath.Join(target, "backend")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "doctor")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	doctor := service.NewDoctorService()
	report, err := doctor.Doctor(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	if !doctorHasFinding(report, "mount-mismatch", "backend", true) {
		t.Fatalf("report missing fixable mount finding: %#v", report)
	}
	afterRead, err := os.ReadFile(statePath)
	if err != nil || string(afterRead) != string(before) {
		t.Fatalf("read-only doctor changed state: %v\nbefore=%s\nafter=%s", err, before, afterRead)
	}
	if _, err := doctor.Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	afterDryRun, _ := os.ReadFile(statePath)
	if string(afterDryRun) != string(before) {
		t.Fatal("dry-run changed state")
	}
	if _, err := doctor.Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data}); err != nil {
		t.Fatal(err)
	}
	fixed, err := service.RequireWorkspace(project, data, "doctor")
	if err != nil {
		t.Fatal(err)
	}
	path, err := fixed.ResolveRepository("backend")
	if err != nil || path != filepath.Join(target, "api") {
		t.Fatalf("fixed backend path = %q, %v", path, err)
	}
}

func TestDoctorReportsCombinedFindingsWithoutAutoFixingUnsafeState(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "missing")
	statePath := service.WorkspaceStatePath(data, project.ID, "missing")
	if err := store.WriteWorkspace(statePath, store.WorkspaceState{ID: "missing", Name: "missing", Path: target, Repositories: map[string]store.CheckoutState{
		"root": {Branch: "missing", Head: "deadbeef", Mount: ".", ResolvedPath: target}, "backend": {Branch: "wrong", Head: "deadbeef", Mount: "backend", ResolvedPath: filepath.Join(target, "backend")},
	}}); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "missing")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.NewDoctorService().Doctor(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	if !doctorHasFinding(report, "missing-checkout", "root", false) || !doctorHasFinding(report, "missing-checkout", "backend", false) || doctorHasRepair(report, "remove-stale-state") {
		t.Fatalf("combined report = %#v", report)
	}
	if _, err := service.NewDoctorService().Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("dry-run removed state: %v", err)
	}
	if _, err := service.NewDoctorService().Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("doctor removed retained state: %v", err)
	}
	_ = root
	_ = backend
}

func TestDoctorFixPreservesRetainedRemovalStateForCheckout(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "retained")
	creator := service.NewWorkspaceCreator()
	if _, err := createFixtureWorkspace(t, project, "feature/retained", target, data); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/retained")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewDoctorService().Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequireWorkspace(project, data, "feature/retained"); err != nil {
		t.Fatalf("doctor removed retained state: %v", err)
	}
	if _, err := creator.CheckoutWorkspace(context.Background(), project, service.WorkspaceCheckoutRequest{WorkspaceName: "feature/retained", DataDir: data}, nil); err != nil {
		t.Fatalf("checkout after doctor fix: %v", err)
	}
}

func TestDoctorFixPreservesStateWithRecoveryRecord(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "recovery")
	if _, err := createFixtureWorkspace(t, project, "feature/recovery", target, data); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/recovery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, false, nil); err != nil {
		t.Fatal(err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspace.ID+".json")
	if err := store.WriteRecovery(recoveryPath, store.RecoveryRecord{ProjectID: project.ID, WorkspaceID: workspace.ID, Operation: "remove", FailedStep: "remove root"}); err != nil {
		t.Fatal(err)
	}
	report, err := service.NewDoctorService().Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if !doctorHasFinding(report, "recovery-record", "", false) {
		t.Fatalf("report = %#v", report)
	}
	if _, err := service.RequireWorkspace(project, data, "feature/recovery"); err != nil {
		t.Fatalf("doctor removed recovery state: %v", err)
	}
	if _, err := os.Stat(recoveryPath); err != nil {
		t.Fatalf("doctor removed recovery record: %v", err)
	}
}

func TestDoctorReportsBranchAndHeadDriftWithoutFix(t *testing.T) {
	project, root, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "branch-drift")
	root.Run(t, "branch", "feature/expected")
	root.Run(t, "worktree", "add", target, "feature/expected")
	git := gitadapter.NewAdapter("git")
	head, err := git.Head(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	root.Run(t, "branch", "feature/actual")
	root.Run(t, "-C", target, "checkout", "feature/actual")
	statePath := service.WorkspaceStatePath(data, project.ID, "branch-drift")
	if err := store.WriteWorkspace(statePath, store.WorkspaceState{ID: "branch-drift", Name: "branch-drift", Path: target, Partial: true, MissingRepositoryIDs: []string{"backend"}, Repositories: map[string]store.CheckoutState{"root": {Branch: "feature/expected", Head: head, Mount: ".", ResolvedPath: target}}}); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "branch-drift")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.NewDoctorService().Doctor(context.Background(), project, workspace, data)
	if err != nil || !doctorHasFinding(report, "branch-mismatch", "root", false) {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestDoctorReportsStaleRegistryWithoutAutoFix(t *testing.T) {
	project, root, _, data := createFixture(t)
	workspace, err := service.RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	registry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Projects[project.ID]
	entry.ConfigPath = filepath.Join(t.TempDir(), "moved", ".wtree.yml")
	registry.Projects[project.ID] = entry
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	report, err := service.NewDoctorService().Doctor(context.Background(), project, workspace, data)
	if err != nil || !doctorHasFinding(report, "stale-registry", "", false) {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	after, err := store.ReadRegistry(registryPath)
	if err != nil || after.Projects[project.ID].ConfigPath != entry.ConfigPath {
		t.Fatalf("doctor rewrote registry: %#v %v", after, err)
	}
	_ = root
}

func doctorHasFinding(report service.DoctorReport, code, repository string, fixable bool) bool {
	for _, finding := range report.Findings {
		if finding.Code == code && finding.RepositoryID == repository && finding.Fixable == fixable {
			return true
		}
	}
	return false
}

func doctorHasRepair(report service.DoctorReport, code string) bool {
	for _, repair := range report.Repairs {
		if repair.Code == code {
			return true
		}
	}
	return false
}

var _ = domain.Workspace{}
