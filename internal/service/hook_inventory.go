package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
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

var hookRunInventoryStepHook fsutil.AtomicStepHook

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
	if _, err := store.HookRunRecordPath(q.DataDir, q.Project.ID, q.Workspace.ID, "post-create"); err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	authority, err := fsutil.OpenPrivatePath(q.DataDir, []string{"projects", q.Project.ID, "hooks", q.Workspace.ID}, "post-create.json", false)
	if fsutil.PrivatePathNotExist(err) {
		return HookRunInventoryResult{Classification: HookRunAbsent, Setup: []HookSetupStatus{}}, nil
	}
	if err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	defer authority.Close()
	if hookRunInventoryStepHook != nil {
		if err := hookRunInventoryStepHook("after-open"); err != nil {
			return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
		}
	}
	entries, err := authority.ReadDirectory()
	if err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	if len(entries) == 0 {
		return HookRunInventoryResult{Classification: HookRunAbsent, Setup: []HookSetupStatus{}}, nil
	}
	sort.Strings(entries)
	allowed := map[string]bool{"post-create.json": true, "post-clone.json": true, "post-create.lock": true, "post-clone.lock": true}
	evidence := map[string]int{}
	files := make([]string, 0, 2)
	for _, name := range entries {
		if err := ctx.Err(); err != nil {
			return HookRunInventoryResult{}, err
		}
		if event, ok := hookRunRemovalEvidenceEvent(name); ok {
			evidence[event]++
			if evidence[event] > 1 {
				return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
			}
			continue
		}
		if !allowed[name] {
			return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
		}
		if len(name) > len(".json") && name[len(name)-len(".json"):] == ".json" {
			files = append(files, name)
		}
	}
	if len(files) == 0 {
		return HookRunInventoryResult{Classification: HookRunAbsent, Setup: []HookSetupStatus{}}, nil
	}
	if len(files) != 1 {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	sort.Strings(files)
	event := files[0][:len(files[0])-len(".json")]
	data, err := authority.ReadFileNamed(files[0])
	if err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	entriesAfter, err := authority.ReadDirectory()
	if err != nil {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	sort.Strings(entriesAfter)
	if len(entriesAfter) != len(entries) {
		return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
	}
	for index := range entries {
		if entries[index] != entriesAfter[index] {
			return HookRunInventoryResult{Classification: HookRunInvalid, Setup: []HookSetupStatus{}}, nil
		}
	}
	record, err := store.DecodeHookRunRecord(data)
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

func hookRunRemovalEvidenceEvent(name string) (string, bool) {
	for _, event := range []string{"post-create", "post-clone"} {
		prefix := "." + event + ".json.remove-"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(name, prefix), "-")
		if len(parts) != 2 {
			return "", false
		}
		pid, pidErr := strconv.ParseUint(parts[0], 10, 64)
		sequence, sequenceErr := strconv.ParseUint(parts[1], 10, 64)
		return event, pidErr == nil && sequenceErr == nil && pid > 0 && sequence > 0
	}
	return "", false
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
