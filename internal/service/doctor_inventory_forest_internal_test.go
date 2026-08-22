package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/store"
)

func assertDoctorAndInventoryReportForestTopologyWithoutMutation(t *testing.T, project domain.Project, data string) {
	t.Helper()
	workspace, err := RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, "default")
	registryPath := data + "/registry.json"
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := NewDoctorService().Doctor(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	if report.LogicalRoot != workspace.RootPath || report.BaseRepository != project.BaseRepository {
		t.Fatalf("doctor topology = %#v", report)
	}
	if len(report.Repositories) != len(project.Repositories) {
		t.Fatalf("doctor repositories = %#v", report.Repositories)
	}
	for index, repository := range project.ParentFirst() {
		checkout := workspaceCheckout(t, workspace, repository.ID)
		got := report.Repositories[index]
		if got.ID != repository.ID || got.ParentID != repository.ParentID || got.Mount != checkout.Mount || got.ResolvedPath != checkout.ResolvedPath || got.Status != "known" || got.IdentityMismatch || got.Missing || got.MountMismatch || got.BranchMismatch || got.HeadMismatch {
			t.Fatalf("doctor repository %d = %#v", index, got)
		}
	}
	if stateAfter, err := os.ReadFile(statePath); err != nil || !reflect.DeepEqual(stateBefore, stateAfter) {
		t.Fatalf("doctor mutated state: %v", err)
	}
	if registryAfter, err := os.ReadFile(registryPath); err != nil || !reflect.DeepEqual(registryBefore, registryAfter) {
		t.Fatalf("doctor mutated registry: %v", err)
	}

	inventory, err := NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil || len(inventory.Projects) != 1 {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	entry := inventory.Projects[0]
	if entry.LogicalRoot != workspace.RootPath || entry.BaseRepository != project.BaseRepository || len(entry.Repositories) != len(project.Repositories) {
		t.Fatalf("inventory topology = %#v", entry)
	}
	for index, repository := range project.ParentFirst() {
		checkout := workspaceCheckout(t, workspace, repository.ID)
		got := entry.Repositories[index]
		if got.ID != repository.ID || got.ParentID != repository.ParentID || got.Mount != checkout.Mount || got.ResolvedPath != checkout.ResolvedPath {
			t.Fatalf("inventory repository %d = %#v", index, got)
		}
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	stale, err := NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil || len(stale.Projects) != 1 {
		t.Fatalf("stale inventory = %#v, %v", stale, err)
	}
	entry = stale.Projects[0]
	if entry.LogicalRoot != "" || entry.BaseRepository != "" || len(entry.Repositories) != 0 || !inventoryHasFinding(entry, "missing-default-state") {
		t.Fatalf("stale topology/diagnostic = %#v", entry)
	}
	invalidState, err := store.DecodeWorkspace(stateBefore)
	if err != nil {
		t.Fatal(err)
	}
	web := invalidState.Repositories["web"]
	web.ResolvedPath = filepath.Join(invalidState.Path, "unexpected-web")
	invalidState.Repositories["web"] = web
	if err := store.WriteWorkspace(statePath, invalidState); err != nil {
		t.Fatal(err)
	}
	stale, err = NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil || len(stale.Projects) != 1 {
		t.Fatalf("invalid-state inventory = %#v, %v", stale, err)
	}
	entry = stale.Projects[0]
	if entry.LogicalRoot != "" || entry.BaseRepository != "" || len(entry.Repositories) != 0 || !inventoryHasFinding(entry, "invalid-default-state") {
		t.Fatalf("invalid-state topology/diagnostic = %#v", entry)
	}
}

func inventoryHasFinding(entry ProjectInventoryEntry, code string) bool {
	for _, finding := range entry.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
