package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestRegistrationConflictIDsCanonicalizePathsAndKeepIdentityCaseExact(t *testing.T) {
	directory := t.TempDir()
	realConfig := filepath.Join(directory, "real", ".wtree.yml")
	if err := os.MkdirAll(filepath.Dir(realConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realConfig, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(filepath.Dir(realConfig), alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ids := service.RegistrationConflictIDs(filepath.Join(alias, ".wtree.yml"), []string{"/Identity/Case"}, []service.RegistrationConflictCandidate{
		{ID: "path", ConfigPath: realConfig},
		{ID: "exact-identity", ConfigPath: filepath.Join(directory, "other", ".wtree.yml"), RepositoryIdentities: []string{"/Identity/Case"}},
		{ID: "different-case", ConfigPath: filepath.Join(directory, "case", ".wtree.yml"), RepositoryIdentities: []string{"/identity/case"}},
	})
	if want := []string{"exact-identity", "path"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("conflicts = %v, want %v", ids, want)
	}
}

func TestRegistrationConflictIDsCompareCanonicalLogicalRootsAndTopLevelPaths(t *testing.T) {
	directory := t.TempDir()
	target := service.RegistrationConflictCandidate{
		ConfigPath:           filepath.Join(directory, "target", ".wtree.yml"),
		RepositoryIdentities: []string{"target-identity"},
		LogicalRoot:          filepath.Join(directory, "workspace"),
		TopLevelPaths:        []string{filepath.Join(directory, "workspace", "api"), filepath.Join(directory, "workspace", "web")},
	}
	candidates := []service.RegistrationConflictCandidate{
		{ID: "logical-root", ConfigPath: filepath.Join(directory, "logical", ".wtree.yml"), RepositoryIdentities: []string{"logical-identity"}, LogicalRoot: filepath.Join(directory, "WORKSPACE"), TopLevelPaths: []string{filepath.Join(directory, "WORKSPACE", "other")}},
		{ID: "top-level", ConfigPath: filepath.Join(directory, "top", ".wtree.yml"), RepositoryIdentities: []string{"top-identity"}, LogicalRoot: filepath.Join(directory, "other-root"), TopLevelPaths: []string{filepath.Join(directory, "workspace", "API")}},
		{ID: "stale-without-topology", ConfigPath: filepath.Join(directory, "stale", ".wtree.yml"), RepositoryIdentities: []string{"stale-identity"}},
		{ID: "unrelated", ConfigPath: filepath.Join(directory, "unrelated", ".wtree.yml"), RepositoryIdentities: []string{"unrelated-identity"}, LogicalRoot: filepath.Join(directory, "elsewhere"), TopLevelPaths: []string{filepath.Join(directory, "elsewhere", "api")}},
	}
	if got, want := service.RegistrationConflictIDsForTarget(target, candidates), []string{"logical-root", "top-level"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("topology conflicts = %v, want %v", got, want)
	}
}

func TestInitializerRejectsExistingRegistrationBeforePublishing(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	first, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(data, "state", first.ProjectID, "default.json")
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first.ConfigPath); err != nil {
		t.Fatal(err)
	}

	for _, dryRun := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry-run", false: "real"}[dryRun], func(t *testing.T) {
			result, initErr := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, DryRun: dryRun})
			if result.ProjectID != "" || initErr == nil || !hasServiceErrorKind(initErr, service.ErrorConflict) || !strings.Contains(initErr.Error(), first.ProjectID) || !strings.Contains(initErr.Error(), "wtree project list") {
				t.Fatalf("init result=%#v error=%v, want conflict naming existing registration", result, initErr)
			}
			assertInitConflictLeavesNoPublication(t, first.ConfigPath, registryPath, registryBytes, statePath, stateBytes, data, first.ProjectID)
		})
	}
}

func TestInitializerReportsSortedPathAndIdentityConflictsWithoutArtifacts(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	common, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(canonicalRoot, ".wtree.yml")
	unrelatedConfig := filepath.Join(data, "unrelated", ".wtree.yml")
	if err := config.WriteProjectFile(unrelatedConfig, config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "unrelated", Name: "healthy", BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: ".", DefaultBranch: "main"}}, Worktrees: config.Worktrees{}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: "/manifests/project.wtree.yml"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(filepath.Join(data, "state", "unrelated", "default.json"), store.WorkspaceState{Version: store.Version, ID: "default", Name: "default", Repositories: map[string]store.CheckoutState{}}); err != nil {
		t.Fatal(err)
	}
	registry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{
		"z-identity": {Name: "z", ConfigPath: filepath.Join(t.TempDir(), "missing-z", ".wtree.yml"), RepositoryIDs: map[string]string{common: "root"}},
		"a-path":     {Name: "a", ConfigPath: configPath, RepositoryIDs: map[string]string{"/unrelated/.git": "root"}},
		"unrelated":  {Name: "healthy", ConfigPath: unrelatedConfig, RepositoryIDs: map[string]string{"/unrelated-healthy/.git": "root"}},
	}}
	registryPath := filepath.Join(data, "registry.json")
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry-run", false: "real"}[dryRun], func(t *testing.T) {
			_, initErr := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, DryRun: dryRun})
			if initErr == nil || !hasServiceErrorKind(initErr, service.ErrorConflict) || !strings.Contains(initErr.Error(), "a-path, z-identity") || strings.Contains(initErr.Error(), "unrelated") {
				t.Fatalf("error = %v, want deterministic exact conflicts", initErr)
			}
			assertInitConflictLeavesNoPublication(t, configPath, registryPath, before, "", nil, data)
		})
	}
}

func TestInitializerRejectsIdentityOnlyRegistrationConflict(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	common, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	registry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{
		"identity-only": {Name: "other checkout", ConfigPath: filepath.Join(t.TempDir(), "other", ".wtree.yml"), RepositoryIDs: map[string]string{common: "root"}},
	}}
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry-run", false: "real"}[dryRun], func(t *testing.T) {
			_, initErr := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, DryRun: dryRun})
			if !hasServiceErrorKind(initErr, service.ErrorConflict) || !strings.Contains(initErr.Error(), "identity-only") {
				t.Fatalf("identity-only init error = %v", initErr)
			}
			canonicalRoot, err := filepath.EvalSymlinks(root.Path)
			if err != nil {
				t.Fatal(err)
			}
			assertInitConflictLeavesNoPublication(t, filepath.Join(canonicalRoot, ".wtree.yml"), registryPath, before, "", nil, data)
		})
	}
}

func hasServiceErrorKind(err error, want service.ErrorKind) bool {
	var application *service.Error
	return errors.As(err, &application) && application.Kind == want
}

func assertInitConflictLeavesNoPublication(t *testing.T, configPath, registryPath string, registryBytes []byte, statePath string, stateBytes []byte, data string, allowedProjectIDs ...string) {
	t.Helper()
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("duplicate refusal wrote config: %v", err)
	}
	after, err := os.ReadFile(registryPath)
	if err != nil || string(after) != string(registryBytes) {
		t.Fatalf("registry changed: after=%q error=%v", after, err)
	}
	if statePath != "" {
		stateAfter, err := os.ReadFile(statePath)
		if err != nil || string(stateAfter) != string(stateBytes) {
			t.Fatalf("default state changed: after=%q error=%v", stateAfter, err)
		}
	}
	projectDirectories, err := filepath.Glob(filepath.Join(data, "projects", "*"))
	allowed := make(map[string]bool, len(allowedProjectIDs))
	for _, id := range allowedProjectIDs {
		allowed[filepath.Join(data, "projects", id)] = true
	}
	for _, directory := range projectDirectories {
		if !allowed[directory] {
			t.Fatalf("duplicate refusal created project lock/artifact directory %q", directory)
		}
	}
	if err != nil || len(projectDirectories) != len(allowed) {
		t.Fatalf("duplicate refusal created project lock/artifact directories: %v error=%v", projectDirectories, err)
	}
}
