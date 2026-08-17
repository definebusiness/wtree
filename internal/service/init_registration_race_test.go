package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestInitializerRepeatsRegistrationConflictCheckUnderRegistryLock(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		t.Fatal(err)
	}
	common, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	initializer := NewInitializer()
	initializer.beforeLockedRegistryPreflight = func() {
		if err := store.WriteRegistry(registryPath, store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{
			"racing-registration": {Name: "racing", ConfigPath: filepath.Join(canonicalRoot, ".wtree.yml"), RepositoryIDs: map[string]string{common: "root"}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = initializer.Init(context.Background(), InitRequest{Path: root.Path, DataDir: data})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorConflict {
		t.Fatalf("init = %v, want locked duplicate conflict", err)
	}
	if _, err := os.Stat(filepath.Join(canonicalRoot, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("locked conflict wrote config: %v", err)
	}
	if entries, err := filepath.Glob(filepath.Join(data, "projects", "*")); err != nil || len(entries) != 0 {
		t.Fatalf("locked conflict acquired project lock or published state: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.lock")); err != nil {
		t.Fatalf("locked conflict did not retain the established registry lock file: %v", err)
	}
	registry, err := store.ReadRegistry(registryPath)
	if err != nil || len(registry.Projects) != 1 || registry.Projects["racing-registration"].ConfigPath == "" {
		t.Fatalf("registry after locked conflict = %#v, %v", registry, err)
	}
}
