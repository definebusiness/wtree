package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/fsutil"
)

func TestHookRunRecordRoundTripAndPrivacy(t *testing.T) {
	now := time.Now().UTC().Round(0)
	record := HookRunRecord{Version: HookRunRecordVersion, ProjectID: "project", WorkspaceID: "default", WorkspaceName: "Default", Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "running", CreatedAt: now, UpdatedAt: now}
	data, err := HookRunRecordBytes(record)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeHookRunRecord(data); err != nil || got.ProjectID != record.ProjectID || got.HookIDs == nil || strings.Contains(string(data), "command") {
		t.Fatalf("DecodeHookRunRecord()=%#v %v", got, err)
	}
}

func TestHookRunRecordPathAndPrivateWrite(t *testing.T) {
	path, err := HookRunRecordPath(t.TempDir(), "project", "default", "post-create")
	if err != nil || filepath.Base(path) != "post-create.json" {
		t.Fatalf("HookRunRecordPath()=%q %v", path, err)
	}
	now := time.Now().UTC().Round(0)
	record := HookRunRecord{Version: HookRunRecordVersion, ProjectID: "project", WorkspaceID: "default", WorkspaceName: "Default", Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"one"}, CompletedHookIDs: []string{}, State: "running", CreatedAt: now, UpdatedAt: now}
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	assertPrivateHookRecord(t, path)
}

func TestHookRunRecordReadAndWriteRejectIntermediateAncestorChange(t *testing.T) {
	for _, scenario := range []string{"missing", "replacement"} {
		for _, operation := range []string{"read", "write"} {
			t.Run(scenario+"/"+operation, func(t *testing.T) {
				path, original := hookRecordTestPathAndValue(t)
				if err := WriteHookRunRecord(path, original); err != nil {
					t.Fatal(err)
				}
				replacement := original
				replacement.WorkspaceName = "Replacement"
				step := "after-open-" + operation
				hookRunAuthorityStepHook = func(got string) error {
					if got != step {
						return nil
					}
					hookRunAuthorityStepHook = nil
					dataDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path)))))
					if err := os.Rename(filepath.Join(dataDir, "projects"), filepath.Join(dataDir, "old-projects")); err != nil {
						if os.IsPermission(err) {
							t.Skipf("directory replacement fixture unavailable: %v", err)
						}
						return err
					}
					if scenario == "replacement" {
						return WriteHookRunRecord(path, replacement)
					}
					return nil
				}
				defer func() { hookRunAuthorityStepHook = nil }()
				if operation == "read" {
					if _, err := ReadHookRunRecord(path); err == nil {
						t.Fatal("ReadHookRunRecord accepted a detached intermediate ancestor")
					}
				} else {
					updated := original
					updated.WorkspaceName = "Detached"
					if err := WriteHookRunRecord(path, updated); err == nil {
						t.Fatal("WriteHookRunRecord published beneath a detached intermediate ancestor")
					}
				}
				if scenario == "replacement" {
					got, err := ReadHookRunRecord(path)
					if err != nil || got.WorkspaceName != replacement.WorkspaceName {
						t.Fatalf("authoritative replacement=%#v %v", got, err)
					}
				} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing authoritative path=%v", err)
				}
			})
		}
	}
}

func TestHookRunRecordRejectsUnknownAndInconsistentState(t *testing.T) {
	data := []byte(`{"version":1,"projectId":"p","workspaceId":"w","workspaceName":"W","operation":"create","event":"post-create","source":"local","sourceSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","planSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","workspaceStateSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","hookIds":["one"],"completedHookIds":["one"],"nextIndex":0,"state":"running","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","unknown":true}`)
	if _, err := DecodeHookRunRecord(data); err == nil {
		t.Fatal("DecodeHookRunRecord accepted unknown/inconsistent record")
	}
}

func TestHookRunRecordStrictValidationAndCopies(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	valid := HookRunRecord{Version: HookRunRecordVersion, ProjectID: "project", WorkspaceID: "workspace", WorkspaceName: "Workspace", Operation: "clone", Event: "post-clone", Source: "portable", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"one", "two"}, CompletedHookIDs: []string{"one"}, NextIndex: 1, State: "running", CreatedAt: now, UpdatedAt: now}
	data, err := HookRunRecordBytes(valid)
	if err != nil || !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("HookRunRecordBytes()=%q %v", data, err)
	}
	decoded, err := DecodeHookRunRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	decoded.HookIDs[0] = "changed"
	if again, err := DecodeHookRunRecord(data); err != nil || again.HookIDs[0] != "one" {
		t.Fatalf("copy=%#v %v", again, err)
	}

	tests := []struct {
		name   string
		mutate func(*HookRunRecord)
	}{
		{"unsafe-id", func(v *HookRunRecord) { v.ProjectID = "../project" }},
		{"bad-sha", func(v *HookRunRecord) { v.PlanSHA256 = "A" + strings.Repeat("a", 63) }},
		{"duplicate", func(v *HookRunRecord) { v.HookIDs[1] = "one" }},
		{"prefix", func(v *HookRunRecord) { v.CompletedHookIDs[0] = "two" }},
		{"index", func(v *HookRunRecord) { v.NextIndex = 2 }},
		{"failure-state", func(v *HookRunRecord) {
			v.State = "failed"
			v.Failure = &HookRunFailure{Kind: "timeout", HookID: "two", RepositoryID: "repo"}
		}},
		{"timestamp", func(v *HookRunRecord) { v.UpdatedAt = now.Add(-time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.HookIDs = append([]string(nil), valid.HookIDs...)
			value.CompletedHookIDs = append([]string(nil), valid.CompletedHookIDs...)
			test.mutate(&value)
			if _, err := HookRunRecordBytes(value); err == nil {
				t.Fatal("accepted invalid record")
			}
		})
	}
	trailing := append(append([]byte(nil), data...), []byte(`{}`)...)
	if _, err := DecodeHookRunRecord(trailing); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	var mapValue map[string]any
	if err := json.Unmarshal(data, &mapValue); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"command", "environment", "stdout", "stderr", "path"} {
		if _, ok := mapValue[forbidden]; ok {
			t.Fatalf("leaked %s", forbidden)
		}
	}
}

func TestHookRunRecordAtomicFailureAndDurabilityMatrix(t *testing.T) {
	for _, step := range []string{"create-temp", "write", "sync", "close", "before-rename", "dir-sync"} {
		t.Run(step, func(t *testing.T) {
			path, record := hookRecordTestPathAndValue(t)
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			atomicStepHook = func(got string) error {
				if got == step {
					return os.ErrPermission
				}
				return nil
			}
			defer func() { atomicStepHook = nil }()
			err := WriteHookRunRecord(path, record)
			if step == "dir-sync" {
				if err != ErrHookRunDurabilityUnconfirmed {
					t.Fatalf("WriteHookRunRecord()=%v", err)
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if _, decodeErr := DecodeHookRunRecord(data); decodeErr != nil {
					t.Fatalf("new record invalid: %v", decodeErr)
				}
			} else {
				if err == nil {
					t.Fatal("WriteHookRunRecord succeeded")
				}
				if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "old" {
					t.Fatalf("old generation=%q %v", data, readErr)
				}
			}
			entries, readErr := os.ReadDir(filepath.Dir(path))
			if readErr != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
				t.Fatalf("temporary entries=%v %v", entries, readErr)
			}
		})
	}
}

func TestHookRunRecordRejectsUnsafePrivatePathComponents(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	assertRejectsUnsafePrivateHookRecordComponents(t, path, record)
}

func assertRejectsUnsafePrivateHookRecordComponentsUnix(t *testing.T, path string, record HookRunRecord) {
	for _, directory := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path)), filepath.Dir(filepath.Dir(filepath.Dir(path))), filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))} {
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteHookRunRecord(path, record); err == nil {
			t.Fatalf("accepted non-private directory %s", directory)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "other"), path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteHookRunRecord(path, record); err == nil {
		t.Fatal("accepted record symlink")
	}
}

func testHookRunRecordRemovalNeverDeletesSubstitutedGeneration(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	opened := path + ".opened"
	hookRunRemoveStepHook = func(step string) error {
		if step != "before-quarantine" {
			return nil
		}
		hookRunRemoveStepHook = nil
		if err := os.Rename(path, opened); err != nil {
			t.Fatal(err)
		}
		return os.WriteFile(path, []byte("replacement"), 0o600)
	}
	defer func() { hookRunRemoveStepHook = nil }()
	if err := RemoveHookRunRecord(path); !errors.Is(err, fsutil.ErrPrivateRemovalAmbiguous) {
		t.Fatalf("RemoveHookRunRecord()=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement=%q %v", data, err)
	}
}

func testHookRunRecordRemovalNeverOverwritesConcurrentRestoreTarget(t *testing.T) {
	path, record := hookRecordTestPathAndValue(t)
	if err := WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	opened := path + ".opened"
	hookRunRemoveStepHook = func(step string) error {
		switch step {
		case "before-quarantine":
			if err := os.Rename(path, opened); err != nil {
				t.Fatal(err)
			}
			return os.WriteFile(path, []byte("replacement"), 0o600)
		case "after-quarantine":
			return os.WriteFile(path, []byte("concurrent"), 0o600)
		default:
			return nil
		}
	}
	defer func() { hookRunRemoveStepHook = nil }()
	if err := RemoveHookRunRecord(path); !errors.Is(err, fsutil.ErrPrivateRemovalAmbiguous) {
		t.Fatalf("RemoveHookRunRecord()=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "concurrent" {
		t.Fatalf("concurrent target=%q %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	foundReplacement := false
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".remove-") {
			quarantined, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), entry.Name()))
			if readErr != nil || string(quarantined) != "replacement" {
				t.Fatalf("quarantine=%q %v", quarantined, readErr)
			}
			foundReplacement = true
		}
	}
	if !foundReplacement {
		t.Fatal("substituted generation was not preserved in quarantine")
	}
}

func TestHookRunRecordReplacementCompletedRequiresExactReadableGeneration(t *testing.T) {
	for _, test := range []struct {
		name  string
		after func(string) error
	}{
		// A generic atomic replacement remains valid while the private writer
		// retains the published handle. os.WriteFile opens without delete-share
		// on Windows and therefore cannot model a competing generation.
		{"corrupt", func(path string) error { return fsutil.WriteFileAtomicMode(path, []byte("not-json"), 0o600) }},
		{"missing", func(path string) error { return os.Remove(path) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, record := hookRecordTestPathAndValue(t)
			atomicStepHook = func(step string) error {
				if step != "dir-sync" {
					return nil
				}
				if err := test.after(path); err != nil {
					t.Fatal(err)
				}
				return os.ErrPermission
			}
			defer func() { atomicStepHook = nil }()
			err := WriteHookRunRecord(path, record)
			if err == nil || errors.Is(err, ErrHookRunDurabilityUnconfirmed) {
				t.Fatalf("WriteHookRunRecord()=%v", err)
			}
		})
	}
}

func TestHookRunRecordFailedDecodingIsPanicFreeAndExact(t *testing.T) {
	_, valid := hookRecordTestPathAndValue(t)
	valid.State, valid.NextIndex = "failed", 0
	valid.Failure = &HookRunFailure{Kind: "missing-executable", HookID: "setup", RepositoryID: "repo"}
	cases := []func(*HookRunRecord){
		func(v *HookRunRecord) { v.NextIndex = len(v.HookIDs) },
		func(v *HookRunRecord) { v.HookIDs = []string{} },
		func(v *HookRunRecord) { v.Failure.HookID = "other" },
		func(v *HookRunRecord) { v.Failure.Kind, v.Failure.Timeout = "timeout", false },
		func(v *HookRunRecord) { code := 1; v.Failure.Kind, v.Failure.ExitCode = "timeout", &code },
		func(v *HookRunRecord) { v.Failure.Kind, v.Failure.Timeout = "non-zero-exit", true },
		func(v *HookRunRecord) { v.WorkspaceName = "bad\u0085control" },
	}
	for _, mutate := range cases {
		value := valid
		value.HookIDs = append([]string(nil), valid.HookIDs...)
		value.CompletedHookIDs = append([]string(nil), valid.CompletedHookIDs...)
		failure := *valid.Failure
		value.Failure = &failure
		mutate(&value)
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("panic: %v", recovered)
				}
			}()
			if _, err := HookRunRecordBytes(value); err == nil {
				t.Fatal("accepted invalid failed record")
			}
		}()
	}
}

func hookRecordTestPathAndValue(t *testing.T) (string, HookRunRecord) {
	t.Helper()
	dataDir := t.TempDir()
	path, err := HookRunRecordPath(dataDir, "project", "workspace", "post-create")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := openHookRecordPath(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return path, HookRunRecord{Version: HookRunRecordVersion, ProjectID: "project", WorkspaceID: "workspace", WorkspaceName: "Workspace", Operation: "create", Event: "post-create", Source: "local", SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), WorkspaceStateSHA256: strings.Repeat("c", 64), HookIDs: []string{"setup"}, CompletedHookIDs: []string{}, NextIndex: 0, State: "running", CreatedAt: now, UpdatedAt: now}
}
