package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/store"
)

func TestRegistryAndRecoveryBytesExactlyMatchAtomicWriters(t *testing.T) {
	registry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"project": {Name: "project", ConfigPath: "/project/.wtree.yml", RepositoryIDs: map[string]string{"identity": "root"}}}}
	recovery := store.RecoveryRecord{Version: store.Version, ProjectID: "project", WorkspaceID: "default", Operation: "clone", FailedStep: "registry-write", UnrevertedSteps: []string{"registry"}}
	tests := []struct {
		name  string
		bytes func() ([]byte, error)
		write func(string) error
	}{
		{"registry", func() ([]byte, error) { return store.RegistryBytes(registry) }, func(path string) error { return store.WriteRegistry(path, registry) }},
		{"recovery", func() ([]byte, error) { return store.RecoveryBytes(recovery) }, func(path string) error { return store.WriteRecovery(path, recovery) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want, err := test.bytes()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), test.name+".json")
			if err := test.write(path); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("writer bytes = %q, want %q", got, want)
			}
		})
	}
}

func TestCASWritersNeverReplaceGenerationInstalledAtFinalBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	planned := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"planned": {Name: "planned", RepositoryIDs: map[string]string{}}}}
	attacker := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{"attacker": {Name: "attacker", RepositoryIDs: map[string]string{}}}}
	err := store.WriteRegistryCAS(path, planned, func() error {
		if err := store.WriteRegistry(path, attacker); err != nil {
			return err
		}
		return errors.New("generation changed")
	})
	if err == nil {
		t.Fatal("CAS writer replaced an attacker generation")
	}
	got, readErr := store.ReadRegistry(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if _, exists := got.Projects["attacker"]; !exists {
		t.Fatalf("registry = %#v", got)
	}
}
