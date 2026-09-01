package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/store"
)

type HookRunClassification string

const (
	HookRunAbsent    HookRunClassification = "absent"
	HookRunResumable HookRunClassification = "resumable"
	HookRunStale     HookRunClassification = "stale"
	HookRunInvalid   HookRunClassification = "invalid"
)

type HookSetupStatus struct {
	Event          string `json:"event"`
	State          string `json:"state"`
	NextHookID     string `json:"nextHookId,omitempty"`
	CompletedCount int    `json:"completedCount"`
	FailureKind    string `json:"failureKind,omitempty"`
}

type HookRunInventoryRequest struct {
	Project     domain.Project
	Workspace   domain.Workspace
	DataDir     string
	Environment []string
	Windows     bool
}

type HookRunInventoryResult struct {
	Classification HookRunClassification
	Setup          []HookSetupStatus
	record         *store.HookRunRecord
}

// HookRunInventoryService observes only one workspace's private hook
// directory. It never creates directories, locks, records, or workspace data.
type HookRunInventoryService struct{ builder hookRetryPlanBuilder }

func NewHookRunInventoryService() *HookRunInventoryService { return &HookRunInventoryService{} }
func NewHookRunInventoryServiceWith(builder hookRetryPlanBuilder) *HookRunInventoryService {
	return &HookRunInventoryService{builder: builder}
}

func (s *HookRunInventoryService) Inspect(ctx context.Context, q HookRunInventoryRequest) (HookRunInventoryResult, error) {
	if err := ctx.Err(); err != nil {
		return HookRunInventoryResult{}, err
	}
	if err := q.Project.Validate(); err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid}, nil
	}
	directory, present := hookRunInventoryDirectory(q.DataDir, q.Project.ID, q.Workspace.ID)
	if !present {
		return HookRunInventoryResult{Classification: HookRunAbsent, Setup: []HookSetupStatus{}}, nil
	}
	if directory == "" {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	if len(entries) == 0 {
		return HookRunInventoryResult{Classification: HookRunAbsent, Setup: []HookSetupStatus{}}, nil
	}
	allowed := map[string]bool{"post-create.json": true, "post-clone.json": true, "post-create.lock": true, "post-clone.lock": true}
	files := make([]string, 0, 2)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return HookRunInventoryResult{}, err
		}
		entryPath := filepath.Join(directory, entry.Name())
		info, statErr := os.Lstat(entryPath)
		if !allowed[entry.Name()] || statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
		}
		if filepath.Ext(entry.Name()) == ".json" {
			files = append(files, entry.Name())
		}
	}
	if len(files) != 1 {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	sort.Strings(files)
	event := files[0][:len(files[0])-len(".json")]
	path, err := store.HookRunRecordPath(q.DataDir, q.Project.ID, q.Workspace.ID, event)
	if err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	record, err := store.ReadHookRunRecord(path)
	if err != nil || record.ProjectID != q.Project.ID || record.WorkspaceID != q.Workspace.ID || record.WorkspaceName != q.Workspace.Name || record.Event != event {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	if !hookRunRecordLifecycleCompatible(record) {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	if s != nil && s.builder != nil {
		plan, verifier, rebuildErr := s.builder.Rebuild(ctx, HookRetryPlanRequest{Project: q.Project, Workspace: q.Workspace, Record: record, DataDir: q.DataDir, Environment: append([]string(nil), q.Environment...), Windows: q.Windows})
		if errors.Is(rebuildErr, context.Canceled) || errors.Is(rebuildErr, context.DeadlineExceeded) {
			return HookRunInventoryResult{}, rebuildErr
		}
		if rebuildErr != nil || verifier == nil || !matchesHookRunRecord(record, plan) {
			return HookRunInventoryResult{Classification: HookRunStale, Setup: []HookSetupStatus{hookSetupStatus(record)}}, nil
		}
		snapshot, verifyErr := verifier(ctx)
		if errors.Is(verifyErr, context.Canceled) || errors.Is(verifyErr, context.DeadlineExceeded) {
			return HookRunInventoryResult{}, verifyErr
		}
		if verifyErr != nil || digest(snapshot.SourceBytes) != record.SourceSHA256 || digest(snapshot.WorkspaceStateBytes) != record.WorkspaceStateSHA256 {
			return HookRunInventoryResult{Classification: HookRunStale, Setup: []HookSetupStatus{hookSetupStatus(record)}}, nil
		}
	}
	setup := hookSetupStatus(record)
	return HookRunInventoryResult{Classification: HookRunResumable, Setup: []HookSetupStatus{setup}, record: &record}, nil
}

// hookRunInventoryDirectory walks only the private record hierarchy with
// Lstat. A missing owned component means there is no record; every other
// type, symlink, or non-private owned directory is invalid rather than an
// invitation to follow a user-controlled path.
func hookRunInventoryDirectory(dataDir, projectID, workspaceID string) (string, bool) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir {
		return "", true
	}
	info, err := os.Lstat(dataDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", true
	}
	current := dataDir
	for _, name := range []string{"projects", projectID, "hooks", workspaceID} {
		current = filepath.Join(current, name)
		info, err = os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return "", true
		}
	}
	return current, true
}

// hookRunRecordLifecycleCompatible is intentionally limited to immutable
// source/event pairing. Current source, plan, workspace, and executable facts
// are rebuilt under the event lock by retry; this read-only inventory must not
// acquire that authority or mutate a record while rendering status/doctor.
func hookRunRecordLifecycleCompatible(record store.HookRunRecord) bool {
	return (record.Source == "local" && record.Operation == "create" && record.Event == "post-create") ||
		(record.Source == "portable" && record.Operation == "clone" && record.Event == "post-clone")
}

func hookSetupStatus(record store.HookRunRecord) HookSetupStatus {
	value := HookSetupStatus{Event: record.Event, State: record.State, CompletedCount: len(record.CompletedHookIDs)}
	if record.NextIndex < len(record.HookIDs) {
		value.NextHookID = record.HookIDs[record.NextIndex]
	}
	if record.Failure != nil {
		value.FailureKind = record.Failure.Kind
	}
	return value
}
