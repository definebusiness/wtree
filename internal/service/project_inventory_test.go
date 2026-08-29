package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
)

func TestProjectInventoryEmptyHealthyAndNoMutation(t *testing.T) {
	data := t.TempDir()
	before := snapshotInventoryTree(t, data)
	report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil || report.Projects == nil || len(report.Projects) != 0 {
		t.Fatalf("empty inventory = %#v, %v", report, err)
	}
	if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(after, before) {
		t.Fatalf("inventory mutated data: before=%#v after=%#v", before, after)
	}

	configPath := writeInventoryConfig(t, data, "healthy", "healthy-name")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"healthy": {Name: "healthy-name", ConfigPath: configPath, RepositoryIDs: map[string]string{"git-a": "root"}},
	})
	if err := store.WriteWorkspace(service.WorkspaceStatePath(data, "healthy", "default"), store.WorkspaceState{ID: "default", Name: "default", Path: filepath.Dir(configPath), Repositories: map[string]store.CheckoutState{
		"root": {Branch: "main", Mount: ".", ResolvedPath: filepath.Dir(configPath), Head: "healthy-head"},
	}}); err != nil {
		t.Fatal(err)
	}
	report, err = service.NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil || len(report.Projects) != 1 || report.Projects[0].Status != "healthy" || report.Projects[0].Findings == nil {
		t.Fatalf("healthy inventory = %#v, %v", report, err)
	}
}

func TestProjectInventoryDoesNotMutatePopulatedTree(t *testing.T) {
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "project", "project")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{"project": {ConfigPath: configPath}})
	if err := store.WriteWorkspace(service.WorkspaceStatePath(data, "project", "default"), store.WorkspaceState{ID: "default", Name: "default", Repositories: map[string]store.CheckoutState{}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "projects", "project", "recovery"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "projects", "project", "recovery", "record.json"), []byte(`{"nested":"content"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	before := snapshotInventoryTree(t, data)
	if _, err := service.NewProjectInventoryService().Inventory(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(after, before) {
		t.Fatalf("inventory mutated populated tree:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestProjectInventoryDiagnosesDuplicatesAndStaleEntriesDeterministically(t *testing.T) {
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "keeper", "keeper-name")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"superseded": {Name: "old", ConfigPath: configPath, RepositoryIDs: map[string]string{"shared": "root"}},
		"keeper":     {Name: "keeper-name", ConfigPath: configPath, RepositoryIDs: map[string]string{"shared": "root"}},
		"missing":    {Name: "gone", ConfigPath: filepath.Join(data, "missing", ".wtree.yml")},
	})
	report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{report.Projects[0].ID, report.Projects[1].ID, report.Projects[2].ID}; got[0] != "keeper" || got[1] != "missing" || got[2] != "superseded" {
		t.Fatalf("order = %v", got)
	}
	var superseded, missing service.ProjectInventoryEntry
	for _, project := range report.Projects {
		switch project.ID {
		case "superseded":
			superseded = project
		case "missing":
			missing = project
		}
	}
	if !superseded.Prunable || !hasProjectFinding(superseded, "duplicate-config-path") || !hasProjectFinding(superseded, "duplicate-repository-identity") {
		t.Fatalf("superseded = %#v", superseded)
	}
	if !missing.Prunable || !hasProjectFinding(missing, "missing-config") {
		t.Fatalf("missing = %#v", missing)
	}
}

func TestProjectInventoryAmbiguityStateAndRecovery(t *testing.T) {
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "declared", "name")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"first": {Name: "one", ConfigPath: configPath}, "second": {Name: "two", ConfigPath: configPath},
	})
	if err := os.MkdirAll(filepath.Join(data, "projects", "first", "recovery"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "projects", "first", "recovery", "record.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range report.Projects {
		if project.ID == "first" && project.Prunable {
			t.Fatalf("ambiguous/recovery project prunable: %#v", project)
		}
		if !hasProjectFinding(project, "duplicate-config-path") || !hasProjectFinding(project, "missing-default-state") {
			t.Fatalf("findings = %#v", project)
		}
	}
	if !hasProjectFinding(report.Projects[0], "recovery-record") {
		t.Fatalf("recovery finding missing: %#v", report.Projects[0])
	}
}

func TestProjectInventoryRejectsInvalidRegistryWithoutPartialReport(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "registry.json"), []byte(`{"version":2,"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
	if err == nil || report.Projects != nil {
		t.Fatalf("new registry = %#v, %v", report, err)
	}
}

func TestProjectInventoryRegistryErrorTaxonomyWithoutPartialReport(t *testing.T) {
	for _, test := range []struct {
		name, contents string
		directory      bool
		kind           service.ErrorKind
	}{
		{name: "malformed", contents: `{`, kind: service.ErrorValidation},
		{name: "newer", contents: `{"version":2,"projects":{}}`, kind: service.ErrorValidation},
		{name: "I/O", directory: true, kind: service.ErrorInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := t.TempDir()
			path := filepath.Join(data, "registry.json")
			var err error
			if test.directory {
				err = os.Mkdir(path, 0o700)
			} else {
				err = os.WriteFile(path, []byte(test.contents), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
			var application *service.Error
			if !errors.As(err, &application) || application.Kind != test.kind || report.Projects != nil {
				t.Fatalf("report=%#v error=%v", report, err)
			}
		})
	}
}

func TestProjectInventoryFindingCodesAreIndependentlyReported(t *testing.T) {
	for _, test := range []struct {
		code, severity, status string
		prunable               bool
		related                []string
		setup                  func(t *testing.T, data string)
	}{
		{code: "missing-config", severity: "error", status: "error", prunable: true, related: []string{}, setup: func(t *testing.T, data string) {
			writeValidDefaultState(t, data, "target")
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: filepath.Join(data, "missing", ".wtree.yml")}})
		}},
		{code: "unreadable-config", severity: "error", status: "error", prunable: true, related: []string{}, setup: func(t *testing.T, data string) {
			writeValidDefaultState(t, data, "target")
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: data}})
		}},
		{code: "invalid-config", severity: "error", status: "error", prunable: true, related: []string{}, setup: func(t *testing.T, data string) {
			writeValidDefaultState(t, data, "target")
			path := filepath.Join(data, "bad.yml")
			if err := os.WriteFile(path, []byte("version: ["), 0o600); err != nil {
				t.Fatal(err)
			}
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: path}})
		}},
		{code: "missing-manifest", severity: "error", status: "error", prunable: true, related: []string{}, setup: func(t *testing.T, data string) {
			writeValidDefaultState(t, data, "target")
			path := writeInventoryConfig(t, data, "target", "target")
			if err := os.Remove(filepath.Join(filepath.Dir(path), "project.wtree.yml")); err != nil {
				t.Fatal(err)
			}
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: path}})
		}},
		{code: "config-id-mismatch", severity: "error", status: "error", prunable: true, related: []string{"other"}, setup: func(t *testing.T, data string) {
			writeValidDefaultState(t, data, "target")
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: writeInventoryConfig(t, data, "other", "other")}})
		}},
		{code: "duplicate-config-path", severity: "warning", status: "warning", prunable: false, related: []string{"other"}, setup: func(t *testing.T, data string) {
			path := writeInventoryConfig(t, data, "target", "target")
			writeValidDefaultState(t, data, "target")
			writeValidDefaultState(t, data, "other")
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: path}, "other": {ConfigPath: path}})
		}},
		{code: "duplicate-repository-identity", severity: "warning", status: "warning", prunable: false, related: []string{"other"}, setup: func(t *testing.T, data string) {
			writeValidDefaultState(t, data, "target")
			writeValidDefaultState(t, data, "other")
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: writeInventoryConfig(t, data, "target", "target"), RepositoryIDs: map[string]string{"same": "root"}}, "other": {ConfigPath: writeInventoryConfig(t, data, "other", "other"), RepositoryIDs: map[string]string{"same": "root"}}})
		}},
		{code: "missing-default-state", severity: "warning", status: "warning", prunable: false, related: []string{}, setup: func(t *testing.T, data string) {
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: writeInventoryConfig(t, data, "target", "target")}})
		}},
		{code: "invalid-default-state", severity: "warning", status: "warning", prunable: false, related: []string{}, setup: func(t *testing.T, data string) {
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: writeInventoryConfig(t, data, "target", "target")}})
			if err := os.MkdirAll(service.WorkspaceStatePath(data, "target", "default"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{code: "recovery-record", severity: "error", status: "error", prunable: false, related: []string{}, setup: func(t *testing.T, data string) {
			writeValidDefaultState(t, data, "target")
			writeInventoryRegistry(t, data, map[string]store.RegistryProject{"target": {ConfigPath: writeInventoryConfig(t, data, "target", "target")}})
			path := filepath.Join(data, "projects", "target", "recovery")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "record.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.code, func(t *testing.T) {
			data := t.TempDir()
			test.setup(t, data)
			report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
			entry, found := inventoryEntry(report, "target")
			finding, findingFound := inventoryFinding(entry, test.code)
			if err != nil || !found || !findingFound || finding.Severity != test.severity || entry.Status != test.status || entry.Prunable != test.prunable || !reflect.DeepEqual(finding.RelatedProjectIDs, test.related) {
				t.Fatalf("report=%#v error=%v", report, err)
			}
		})
	}
}

func writeValidDefaultState(t *testing.T, data, id string) {
	t.Helper()
	if err := store.WriteWorkspace(service.WorkspaceStatePath(data, id, "default"), store.WorkspaceState{ID: "default", Name: "default", Repositories: map[string]store.CheckoutState{}}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectInventoryRegistryIOFailureIsInternalWithoutPartialReport(t *testing.T) {
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, "registry.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorInternal || report.Projects != nil {
		t.Fatalf("registry I/O = %#v, %v", report, err)
	}
}

func TestProjectInventoryRepeatedFindingOrderIsDeterministic(t *testing.T) {
	data := t.TempDir()
	projects := map[string]store.RegistryProject{}
	for _, id := range []string{"first", "second"} {
		configPath := writeInventoryConfig(t, data, id, id)
		if err := store.WriteWorkspace(service.WorkspaceStatePath(data, id, "default"), store.WorkspaceState{ID: "default", Name: "default", Repositories: map[string]store.CheckoutState{}}); err != nil {
			t.Fatal(err)
		}
		projects[id] = store.RegistryProject{ConfigPath: configPath, RepositoryIDs: map[string]string{"identity-z": "root", "identity-a": "nested"}}
	}
	writeInventoryRegistry(t, data, projects)
	want := []string{
		"repository identity is registered by multiple projects: identity-a",
		"repository identity is registered by multiple projects: identity-z",
	}
	for run := 0; run < 100; run++ {
		report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, finding := range report.Projects[0].Findings {
			if finding.Code == "duplicate-repository-identity" {
				got = append(got, finding.Message)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d finding order = %v, want %v", run, got, want)
		}
	}
}

func TestProjectInventoryDiagnosesConfigAndStateFailuresAndCanonicalAliases(t *testing.T) {
	data := t.TempDir()
	valid := writeInventoryConfig(t, data, "valid", "valid")
	alias := filepath.Join(data, "config-alias.yml")
	if err := os.Symlink(valid, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	invalid := filepath.Join(data, "invalid.yml")
	if err := os.WriteFile(invalid, []byte("version: 1\nproject: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"valid": {ConfigPath: valid}, "alias": {ConfigPath: alias}, "invalid": {ConfigPath: invalid}, "unreadable": {ConfigPath: data},
	})
	if err := os.MkdirAll(service.WorkspaceStatePath(data, "valid", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := service.NewProjectInventoryService().Inventory(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]service.ProjectInventoryEntry{}
	for _, entry := range report.Projects {
		entries[entry.ID] = entry
	}
	if !hasProjectFinding(entries["invalid"], "invalid-config") || !entries["invalid"].Prunable {
		t.Fatalf("invalid = %#v", entries["invalid"])
	}
	if !hasProjectFinding(entries["unreadable"], "unreadable-config") || !entries["unreadable"].Prunable {
		t.Fatalf("unreadable = %#v", entries["unreadable"])
	}
	if !hasProjectFinding(entries["valid"], "invalid-default-state") {
		t.Fatalf("state = %#v", entries["valid"])
	}
	if !hasProjectFinding(entries["alias"], "duplicate-config-path") || hasProjectFinding(entries["alias"], "missing-config") {
		t.Fatalf("alias = %#v", entries["alias"])
	}
	if !hasProjectFinding(entries["alias"], "config-id-mismatch") {
		t.Fatalf("mismatch = %#v", entries["alias"])
	}
}

func writeInventoryConfig(t *testing.T, data, id, name string) string {
	t.Helper()
	path := filepath.Join(data, "projects with spaces", id, ".wtree.yml")
	if err := config.WriteProjectFile(path, config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: id, Name: name, BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: ".", DefaultBranch: "main"}}, Worktrees: config.Worktrees{}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: filepath.Join(data, "manifests", "project.wtree.yml")}}); err != nil {
		t.Fatal(err)
	}
	manifest, err := config.MarshalPortableManifest(config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: id, Name: name, BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: "https://example.test/project.git"}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{"0123456789012345678901234567890123456789"}}, Mount: ".", DefaultBranch: "main"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "project.wtree.yml"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func writeInventoryRegistry(t *testing.T, data string, projects map[string]store.RegistryProject) {
	t.Helper()
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), store.Registry{Projects: projects}); err != nil {
		t.Fatal(err)
	}
}
func hasProjectFinding(entry service.ProjectInventoryEntry, code string) bool {
	for _, finding := range entry.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func inventoryFinding(entry service.ProjectInventoryEntry, code string) (service.ProjectInventoryFinding, bool) {
	for _, finding := range entry.Findings {
		if finding.Code == code {
			return finding, true
		}
	}
	return service.ProjectInventoryFinding{}, false
}

func inventoryEntry(report service.ProjectInventoryReport, id string) (service.ProjectInventoryEntry, bool) {
	for _, entry := range report.Projects {
		if entry.ID == id {
			return entry, true
		}
	}
	return service.ProjectInventoryEntry{}, false
}

type inventoryTreeEntry struct {
	Mode     os.FileMode
	ModTime  int64
	Contents []byte
}

func snapshotInventoryTree(t *testing.T, root string) map[string]inventoryTreeEntry {
	t.Helper()
	entries := map[string]inventoryTreeEntry{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := inventoryTreeEntry{Mode: info.Mode(), ModTime: inventorySnapshotModTime(entry, info)}
		if !entry.IsDir() {
			value.Contents, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		entries[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
