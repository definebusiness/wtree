//go:build windows

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/store"
	"golang.org/x/sys/windows"
)

func TestWindowsHookRunInventoryClassificationsAndDescriptorNonMutation(t *testing.T) {
	root, dataDir := t.TempDir(), t.TempDir()
	project := hookManagementProject(filepath.Join(root, ".wtree.yml"), root)
	workspace := domain.Workspace{ID: "workspace", Name: "Workspace"}
	path, err := store.HookRunRecordPath(dataDir, project.ID, workspace.ID, "post-create")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: project.ID, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "failed", Failure: &store.HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: project.BaseRepository}, CreatedAt: now, UpdatedAt: now}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	request := HookRunInventoryRequest{Project: project, Workspace: workspace, DataDir: dataDir}
	tracked := []string{filepath.Join(dataDir, "projects"), filepath.Join(dataDir, "projects", project.ID), filepath.Join(dataDir, "projects", project.ID, "hooks"), filepath.Dir(path), path}
	before := windowsInventoryDescriptors(t, tracked)
	result, err := NewHookRunInventoryService().Inspect(context.Background(), request)
	if err != nil || result.Classification != HookRunResumable {
		t.Fatalf("resumable=%#v %v", result, err)
	}
	result, err = NewHookRunInventoryServiceWith(&hookRetryBuilderFake{}).Inspect(context.Background(), request)
	if err != nil || result.Classification != HookRunStale {
		t.Fatalf("stale=%#v %v", result, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewHookRunInventoryService().Inspect(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled=%v", err)
	}
	after := windowsInventoryDescriptors(t, tracked)
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("inventory mutated descriptor for %q: before=%q after=%q", tracked[index], before[index], after[index])
		}
	}
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = NewHookRunInventoryService().Inspect(context.Background(), request)
	if err != nil || result.Classification != HookRunInvalid {
		t.Fatalf("invalid=%#v %v", result, err)
	}
}

func windowsInventoryDescriptors(t *testing.T, paths []string) []string {
	t.Helper()
	values := make([]string, len(paths))
	for index, path := range paths {
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		values[index] = descriptor.String()
	}
	return values
}
