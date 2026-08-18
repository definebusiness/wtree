package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
	"github.com/google/uuid"
)

func TestDeterministicInitProjectIDUsesCanonicalV2BaseFact(t *testing.T) {
	repositories := map[string]config.PortableRepository{
		"root": {
			Clone:    config.CloneSource{Remote: "origin", URL: "https://example.invalid/root.git"},
			Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"},
			Identity: config.RepositoryIdentity{InitialCommits: []string{"0123456789abcdef0123456789abcdef01234567"}},
			Mount:    ".", DefaultBranch: "main",
		},
		"child": {
			Clone:    config.CloneSource{Remote: "origin", URL: "https://example.invalid/child.git"},
			Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"},
			Identity: config.RepositoryIdentity{InitialCommits: []string{"89abcdef0123456789abcdef0123456789abcdef"}},
			Parent:   "root", Mount: "child", DefaultBranch: "main",
		},
	}
	identity, err := config.MarshalPortableManifest(config.PortableManifest{
		Version:      config.PortableManifestVersion,
		Project:      config.PortableProject{ID: "identity", Name: "identity", BaseRepository: "root"},
		Repositories: repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(identity, []byte("base_repository: root\n")) {
		t.Fatalf("canonical identity omitted v2 base fact:\n%s", identity)
	}
	want := uuid.NewSHA1(uuid.NameSpaceURL, append([]byte("wtree:init:v2\n"), identity...)).String()
	if got := deterministicInitProjectID("root", repositories); got != want {
		t.Fatalf("deterministic project ID = %q, want canonical v2 identity %q", got, want)
	}
	if got := deterministicInitProjectID("root", map[string]config.PortableRepository{
		"child": repositories["child"], "root": repositories["root"],
	}); got != want {
		t.Fatalf("deterministic project ID changed with map iteration order: %q != %q", got, want)
	}
}

func TestInitRejectsPostPlanManifestChangeWithoutOtherPublication(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	initializer := NewInitializer()
	manifest := filepath.Join(repository.Path, "project.wtree.yml")
	initializer.beforePublish = func() {
		if err := os.WriteFile(manifest, []byte("user-owned\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	_, err := initializer.Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data})
	var conflict *Error
	if !errors.As(err, &conflict) || conflict.Kind != ErrorConflict {
		t.Fatalf("Init() error = %v, want conflict", err)
	}
	dataAfter, readErr := os.ReadFile(manifest)
	if readErr != nil || string(dataAfter) != "user-owned\n" {
		t.Fatalf("manifest after conflict = %q, %v", dataAfter, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(repository.Path, ".wtree.yml")); !os.IsNotExist(statErr) {
		t.Fatalf("local config was published: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(statErr) {
		t.Fatalf("registry was published: %v", statErr)
	}
}

func TestInitRollbackPreservesConcurrentPublishedFileReplacements(t *testing.T) {
	for _, name := range []string{".gitignore", ".wtree.yml", "project.wtree.yml"} {
		t.Run(name, func(t *testing.T) {
			repository := testutil.NewPushedGitRepository(t)
			repository.CommitFile("readme", "x\n", "initial")
			data := t.TempDir()
			attacker := []byte("concurrent user generation\n")
			initializer := NewInitializerWithWriters(store.WriteRegistry, func(string, store.WorkspaceState) error {
				if err := os.WriteFile(filepath.Join(repository.Path, name), attacker, 0o600); err != nil {
					return err
				}
				return errors.New("injected workspace failure after concurrent replacement")
			})

			_, err := initializer.Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data})
			var application *Error
			if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
				t.Fatalf("Init() error = %v, want rollback incomplete", err)
			}
			got, readErr := os.ReadFile(filepath.Join(repository.Path, name))
			if readErr != nil || string(got) != string(attacker) {
				t.Fatalf("%s after rollback = %q, %v; want attacker bytes", name, got, readErr)
			}
		})
	}
}

func TestInitRollbackDoesNotUnlinkReplacementOfOriginallyAbsentTarget(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(canonicalRoot, ".wtree.yml")
	attacker := []byte("concurrent replacement in remove window\n")
	initializer := NewInitializerWithWriters(store.WriteRegistry, func(string, store.WorkspaceState) error {
		return errors.New("injected workspace failure")
	})
	initializer.remove = func(path string) error {
		if path == configPath {
			t.Fatalf("rollback passed the public target to unlink: %s", path)
		}
		return os.Remove(path)
	}
	boundaryReached := false
	initializer.captureOwnedFile = func(path, capturedPath string) error {
		if path != configPath {
			return os.Rename(path, capturedPath)
		}
		boundaryReached = true
		replacement := filepath.Join(filepath.Dir(path), ".attacker-replacement")
		if err := os.WriteFile(replacement, attacker, 0o600); err != nil {
			t.Fatalf("replace owned target in removal window: %v", err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatalf("publish replacement in removal window: %v", err)
		}
		return os.Rename(path, capturedPath)
	}

	_, err = initializer.Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("Init() error = %v, want rollback incomplete", err)
	}
	if !boundaryReached {
		t.Fatal("rollback did not reach the atomic removal boundary")
	}
	got, readErr := os.ReadFile(configPath)
	if readErr != nil || string(got) != string(attacker) {
		t.Fatalf("replacement after rollback = %q, %v; want attacker bytes", got, readErr)
	}
}

func TestInitRollbackRejectsReplacementAfterOwnedFileCapture(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(canonicalRoot, ".wtree.yml")
	attacker := []byte("concurrent replacement after capture\n")
	initializer := NewInitializerWithWriters(store.WriteRegistry, func(string, store.WorkspaceState) error {
		return errors.New("injected workspace failure")
	})
	initializer.remove = func(path string) error {
		if path == configPath {
			t.Fatalf("rollback passed the public target to unlink: %s", path)
		}
		return os.Remove(path)
	}
	boundaryReached := false
	initializer.captureOwnedFile = func(path, capturedPath string) error {
		if err := os.Rename(path, capturedPath); err != nil {
			return err
		}
		if path != configPath {
			return nil
		}
		boundaryReached = true
		replacement := filepath.Join(filepath.Dir(path), ".attacker-replacement")
		if err := os.WriteFile(replacement, attacker, 0o600); err != nil {
			t.Fatalf("create replacement after capture: %v", err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatalf("publish replacement after capture: %v", err)
		}
		return nil
	}

	_, err = initializer.Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data})
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("Init() error = %v, want rollback incomplete", err)
	}
	if !boundaryReached {
		t.Fatal("rollback did not reach the atomic capture boundary")
	}
	got, readErr := os.ReadFile(configPath)
	if readErr != nil || string(got) != string(attacker) {
		t.Fatalf("replacement after rollback = %q, %v; want attacker bytes", got, readErr)
	}
	residue, globErr := filepath.Glob(filepath.Join(filepath.Dir(configPath), ".wtree-init-rollback-*"))
	if globErr != nil || len(residue) != 0 {
		t.Fatalf("rollback quarantine residue = %v, %v; want none", residue, globErr)
	}
}

func TestInitRollbackPreservesConcurrentStoreReplacements(t *testing.T) {
	for _, target := range []string{"registry", "workspace"} {
		t.Run(target, func(t *testing.T) {
			repository := testutil.NewPushedGitRepository(t)
			repository.CommitFile("readme", "x\n", "initial")
			data := t.TempDir()
			attacker := []byte("concurrent store generation\n")
			var replaced string
			initializer := NewInitializerWithWriters(store.WriteRegistry, func(path string, state store.WorkspaceState) error {
				if target == "workspace" {
					if err := store.WriteWorkspace(path, state); err != nil {
						return err
					}
					replaced = path
				} else {
					replaced = filepath.Join(data, "registry.json")
				}
				if err := os.WriteFile(replaced, attacker, 0o600); err != nil {
					return err
				}
				return errors.New("injected workspace failure after concurrent store replacement")
			})

			_, err := initializer.Init(context.Background(), InitRequest{Path: repository.Path, DataDir: data})
			var application *Error
			if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
				t.Fatalf("Init() error = %v, want rollback incomplete", err)
			}
			got, readErr := os.ReadFile(replaced)
			if readErr != nil || string(got) != string(attacker) {
				t.Fatalf("%s after rollback = %q, %v; want attacker bytes", target, got, readErr)
			}
		})
	}
}
