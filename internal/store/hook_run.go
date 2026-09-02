package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/fsutil"
)

const HookRunRecordVersion = 1

var ErrHookRunDurabilityUnconfirmed = errors.New("hook run durability unconfirmed")
var ErrHookRunRemovalDurabilityUnconfirmed = errors.New("hook run removal durability unconfirmed")

var hookRunRemoveSync = func(path *fsutil.PrivatePath) error { return path.SyncDirectory() }
var hookRunRemoveStepHook fsutil.AtomicStepHook
var hookRunAuthorityStepHook fsutil.AtomicStepHook

type HookRunFailure struct {
	Kind         string `json:"kind"`
	HookID       string `json:"hookId"`
	RepositoryID string `json:"repositoryId"`
	ExitCode     *int   `json:"exitCode,omitempty"`
	Timeout      bool   `json:"timeout,omitempty"`
}
type HookRunRecord struct {
	Version              int             `json:"version"`
	ProjectID            string          `json:"projectId"`
	WorkspaceID          string          `json:"workspaceId"`
	WorkspaceName        string          `json:"workspaceName"`
	Operation            string          `json:"operation"`
	Event                string          `json:"event"`
	Source               string          `json:"source"`
	SourceSHA256         string          `json:"sourceSha256"`
	PlanSHA256           string          `json:"planSha256"`
	WorkspaceStateSHA256 string          `json:"workspaceStateSha256"`
	HookIDs              []string        `json:"hookIds"`
	CompletedHookIDs     []string        `json:"completedHookIds"`
	NextIndex            int             `json:"nextIndex"`
	State                string          `json:"state"`
	Failure              *HookRunFailure `json:"failure,omitempty"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
}

func HookRunRecordBytes(value HookRunRecord) ([]byte, error) {
	if err := validateHookRunRecord(value); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func DecodeHookRunRecord(data []byte) (HookRunRecord, error) {
	var value HookRunRecord
	if err := decode(data, &value); err != nil {
		return HookRunRecord{}, err
	}
	if err := validateHookRunRecord(value); err != nil {
		return HookRunRecord{}, err
	}
	return cloneHookRunRecord(value), nil
}
func ReadHookRunRecord(path string) (HookRunRecord, error) {
	authority, err := openHookRecordPath(path, false)
	if err != nil {
		return HookRunRecord{}, err
	}
	defer authority.Close()
	if hookRunAuthorityStepHook != nil {
		if err := hookRunAuthorityStepHook("after-open-read"); err != nil {
			return HookRunRecord{}, err
		}
	}
	data, err := authority.ReadFile()
	if err != nil {
		return HookRunRecord{}, err
	}
	return DecodeHookRunRecord(data)
}
func WriteHookRunRecord(path string, value HookRunRecord) error {
	authority, err := openHookRecordPath(path, true)
	if err != nil {
		return err
	}
	defer authority.Close()
	if hookRunAuthorityStepHook != nil {
		if err := hookRunAuthorityStepHook("after-open-write"); err != nil {
			return err
		}
	}
	data, err := HookRunRecordBytes(value)
	if err != nil {
		return err
	}
	if err := authority.WriteFileAtomicModeWithHook(data, 0o600, atomicHook); err != nil {
		if fsutil.ReplacementCompleted(err) {
			got, readErr := authority.ReadFile()
			if readErr == nil && bytes.Equal(got, data) {
				if _, decodeErr := DecodeHookRunRecord(got); decodeErr == nil {
					return ErrHookRunDurabilityUnconfirmed
				}
			}
		}
		return err
	}
	return nil
}
func RemoveHookRunRecord(path string) error {
	authority, err := openHookRecordPath(path, false)
	if err != nil {
		return err
	}
	defer authority.Close()
	if err := authority.RemoveWithHook(hookRunRemoveStepHook); err != nil {
		if errors.Is(err, fsutil.ErrPrivateRemovalQuarantined) {
			// On Unix, fsutil has already synced and revalidated both the
			// authoritative absence and the retained exact quarantine. A second
			// store sync cannot improve that completed logical removal and, if it
			// fails, would incorrectly make a runner restore finalizing.
			return nil
		}
		return err
	}
	if err := hookRunRemoveSync(authority); err != nil {
		return ErrHookRunRemovalDurabilityUnconfirmed
	}
	return nil
}
func HookRunRecordPath(dataDir, project, workspace, event string) (string, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir || !safeHookID(project) || !safeHookID(workspace) || !safeHookID(event) {
		return "", errors.New("unsafe hook run record path")
	}
	return filepath.Join(dataDir, "projects", project, "hooks", workspace, event+".json"), nil
}

// openHookRecordPath accepts only the exact private path layout emitted by
// HookRunRecordPath and returns retained authority over its owned suffix.
func openHookRecordPath(path string, create bool) (*fsutil.PrivatePath, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Ext(path) != ".json" {
		return nil, errors.New("unsafe hook run record path")
	}
	workspaceDirectory := filepath.Dir(path)
	hooksDirectory := filepath.Dir(workspaceDirectory)
	projectDirectory := filepath.Dir(hooksDirectory)
	projectsDirectory := filepath.Dir(projectDirectory)
	dataDirectory := filepath.Dir(projectsDirectory)
	project, workspace := filepath.Base(projectDirectory), filepath.Base(workspaceDirectory)
	event := strings.TrimSuffix(filepath.Base(path), ".json")
	if filepath.Base(hooksDirectory) != "hooks" || filepath.Base(projectsDirectory) != "projects" || !safeHookID(project) || !safeHookID(workspace) || !safeHookID(event) || path != filepath.Join(dataDirectory, "projects", project, "hooks", workspace, event+".json") {
		return nil, errors.New("unsafe hook run record path")
	}
	authority, err := fsutil.OpenPrivatePath(dataDirectory, []string{"projects", project, "hooks", workspace}, event+".json", create)
	if err != nil {
		return nil, errors.Join(errors.New("unsafe hook run record path authority"), err)
	}
	return authority, nil
}

func validateHookRunRecord(v HookRunRecord) error {
	if v.Version != HookRunRecordVersion || !safeHookID(v.ProjectID) || !safeHookID(v.WorkspaceID) || !safeHookID(v.Event) || !validHookName(v.WorkspaceName, 256) || !oneOf(v.Operation, "create", "clone") || !oneOf(v.Source, "local", "portable") || !oneOf(v.Event, "post-create", "post-clone") || !hash(v.SourceSHA256) || !hash(v.PlanSHA256) || !hash(v.WorkspaceStateSHA256) || len(v.HookIDs) == 0 || len(v.HookIDs) > 1024 || v.HookIDs == nil || v.CompletedHookIDs == nil || v.NextIndex < 0 || v.NextIndex > len(v.HookIDs) || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() || v.CreatedAt.Location() != time.UTC || v.UpdatedAt.Location() != time.UTC || v.CreatedAt.After(v.UpdatedAt) {
		return errors.New("invalid hook run record")
	}
	if len(v.CompletedHookIDs) != v.NextIndex {
		return errors.New("invalid hook run record")
	}
	seen := map[string]bool{}
	for i, id := range v.HookIDs {
		if !safeHookID(id) || seen[id] || i < v.NextIndex && v.CompletedHookIDs[i] != id {
			return errors.New("invalid hook run record")
		}
		seen[id] = true
	}
	switch v.State {
	case "running":
		if v.Failure != nil || v.NextIndex >= len(v.HookIDs) {
			return errors.New("invalid hook run record")
		}
	case "finalizing":
		if v.Failure != nil || v.NextIndex != len(v.HookIDs) {
			return errors.New("invalid hook run record")
		}
	case "failed":
		if v.NextIndex >= len(v.HookIDs) || v.Failure == nil || v.Failure.HookID != v.HookIDs[v.NextIndex] || !safeHookID(v.Failure.RepositoryID) || !oneOf(v.Failure.Kind, "non-zero-exit", "missing-executable", "launch", "timeout", "canceled", "output-writer", "generation-changed") {
			return errors.New("invalid hook run record")
		}
		if v.Failure.Kind == "non-zero-exit" && (v.Failure.ExitCode == nil || v.Failure.Timeout) {
			return errors.New("invalid hook run record")
		}
		if v.Failure.Kind == "timeout" && (v.Failure.ExitCode != nil || !v.Failure.Timeout) {
			return errors.New("invalid hook run record")
		}
		if v.Failure.Kind != "non-zero-exit" && v.Failure.Kind != "timeout" && (v.Failure.ExitCode != nil || v.Failure.Timeout) {
			return errors.New("invalid hook run record")
		}
	default:
		return errors.New("invalid hook run record")
	}
	return nil
}
func safeHookID(s string) bool {
	return utf8.ValidString(s) && config.ValidatePortableID(s) == nil
}
func validHookName(s string, max int) bool {
	if len(s) == 0 || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func hash(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && len(s) == sha256.Size*2 && s == strings.ToLower(s)
}
func cloneHookRunRecord(v HookRunRecord) HookRunRecord {
	v.HookIDs = append([]string{}, v.HookIDs...)
	// Keep schema-valid empty prefixes observable to callers. A nil clone here
	// loses the record's required empty-array shape and can break a later
	// failed-to-running retry rewrite.
	v.CompletedHookIDs = append([]string{}, v.CompletedHookIDs...)
	if v.Failure != nil {
		x := *v.Failure
		v.Failure = &x
	}
	return v
}
func (v HookRunRecord) String() string {
	return fmt.Sprintf("hook run %s/%s", v.ProjectID, v.WorkspaceID)
}
