package store_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/store"
)

func TestStoreRoundTripsVersionedStateAtomicallyWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	want := store.WorkspaceState{Version: 1, ID: "feature-login", Name: "feature/login"}
	if err := store.WriteWorkspace(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadWorkspace(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadWorkspace() = %#v, %v", got, err)
	}
	assertPrivateStoreFile(t, path)
	if _, err := store.ReadWorkspace(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing state read succeeded")
	}
}

func TestStoreRejectsCorruptionUnknownFieldsAndNewerVersions(t *testing.T) {
	for _, contents := range []string{
		`{"version":2,"id":"id","name":"name"}`,
		`{"version":1,"id":"id","name":"name","unknown":true}`,
		`{`,
	} {
		path := filepath.Join(t.TempDir(), "workspace.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadWorkspace(path); err == nil {
			t.Fatalf("ReadWorkspace(%q) succeeded", contents)
		}
	}
}

func TestRegistryAndRecoveryRejectMalformedUnknownAndNewer(t *testing.T) {
	for _, test := range []struct {
		name, contents string
		read           func(string) error
	}{
		{"registry malformed", "{", func(path string) error { _, err := store.ReadRegistry(path); return err }},
		{"registry newer", `{"version":2,"projects":{}}`, func(path string) error { _, err := store.ReadRegistry(path); return err }},
		{"recovery unknown", `{"version":1,"unknown":true}`, func(path string) error { _, err := store.ReadRecovery(path); return err }},
		{"recovery newer", `{"version":2}`, func(path string) error { _, err := store.ReadRecovery(path); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := test.read(path); err == nil {
				t.Fatal("invalid state accepted")
			}
		})
	}
}

func TestMigrateWorkspaceVersionOneIsNoOp(t *testing.T) {
	state := store.WorkspaceState{Version: 1, ID: "id", Name: "name"}
	got, err := store.MigrateWorkspace(state)
	if err != nil || !reflect.DeepEqual(got, state) {
		t.Fatalf("MigrateWorkspace() = %#v, %v", got, err)
	}
}

func TestMigrateRegistryAndRecoveryVersionOneAreNoOps(t *testing.T) {
	registry := store.Registry{Version: 1}
	if got, err := store.MigrateRegistry(registry); err != nil || !reflect.DeepEqual(got, registry) {
		t.Fatal(got, err)
	}
	recovery := store.RecoveryRecord{Version: 1}
	if got, err := store.MigrateRecovery(recovery); err != nil || !reflect.DeepEqual(got, recovery) {
		t.Fatal(got, err)
	}
}

func TestStoreRejectsNonEOFTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"id":"id","name":"name"} garbage`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadWorkspace(path); err == nil {
		t.Fatal("garbage trailing JSON accepted")
	}
}

func TestFailedWritePreservesOldAuthoritativeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	old := store.WorkspaceState{Version: 1, ID: "old", Name: "old"}
	if err := store.WriteWorkspace(path, old); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(path, store.WorkspaceState{Version: 2, ID: "new"}); err == nil {
		t.Fatal("unsupported write succeeded")
	}
	if got, err := store.ReadWorkspace(path); err != nil || !reflect.DeepEqual(got, old) {
		t.Fatalf("old state not preserved %#v %v", got, err)
	}
}

func TestRegistryWorkspaceAndRecoveryRoundTripFullModels(t *testing.T) {
	directory := t.TempDir()
	registry := store.Registry{Version: 1, Projects: map[string]store.RegistryProject{"p": {Name: "project", ConfigPath: "/project/.wtree.yml", RepositoryIDs: map[string]string{"/git/root": "root"}}}}
	if err := store.WriteRegistry(filepath.Join(directory, "registry.json"), registry); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadRegistry(filepath.Join(directory, "registry.json")); err != nil || !reflect.DeepEqual(got, registry) {
		t.Fatalf("registry=%#v %v", got, err)
	}
	workspace := store.WorkspaceState{Version: 1, ID: "id", Name: "name", Path: "/workspace", Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Mount: ".", ResolvedPath: "/workspace"}}}
	if err := store.WriteWorkspace(filepath.Join(directory, "workspace.json"), workspace); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadWorkspace(filepath.Join(directory, "workspace.json")); err != nil || !reflect.DeepEqual(got, workspace) {
		t.Fatalf("workspace=%#v %v", got, err)
	}
	recovery := store.RecoveryRecord{Version: 1, ProjectID: "p", WorkspaceID: "id", Operation: "create", FailedStep: "add", CompletedSteps: []string{"branch"}}
	if err := store.WriteRecovery(filepath.Join(directory, "recovery.json"), recovery); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadRecovery(filepath.Join(directory, "recovery.json")); err != nil || !reflect.DeepEqual(got, recovery) {
		t.Fatalf("recovery=%#v %v", got, err)
	}
}
