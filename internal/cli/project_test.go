package cli_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestProjectListJSONEmptyAndOutsideProject(t *testing.T) {
	data := t.TempDir()
	result := testutil.RunCommand(t, cli.Execute, "project", "list", "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("project list = %#v", result)
	}
	var report struct {
		Projects []json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil || report.Projects == nil || len(report.Projects) != 0 {
		t.Fatalf("JSON = %q, %#v, %v", result.Stdout, report, err)
	}
}

func TestProjectListHumanJSONAndUnsupportedOptions(t *testing.T) {
	data := t.TempDir()
	configPath := filepath.Join(data, "path with spaces", ".wtree.yml")
	if err := config.WriteProjectFile(configPath, config.ProjectConfig{Version: 1, Project: config.Project{ID: "project-a", Name: "A"}, Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: "."}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), store.Registry{Projects: map[string]store.RegistryProject{"project-a": {Name: "A", ConfigPath: configPath}}}); err != nil {
		t.Fatal(err)
	}
	human := testutil.RunCommand(t, cli.Execute, "project", "list", "--data-dir", data)
	if human.Err != nil || human.Stderr != "" || !containsAll(human.Stdout, "project-a", "missing-default-state", configPath) {
		t.Fatalf("human = %#v", human)
	}
	for _, arguments := range [][]string{{"project", "list", "unexpected"}, {"project", "list", "--dry-run"}, {"project", "list", "--force"}, {"project", "list", "--force=false"}, {"project", "list", "--verbose"}, {"project", "list", "--verbose=false"}, {"project", "--project", ".", "list"}, {"project", "--project", "."}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Fatalf("%v = %#v", arguments, result)
		}
	}
	if result := testutil.RunCommand(t, cli.Execute, "project"); result.Err != nil || !containsAll(result.Stdout, "USAGE", "wtree project") {
		t.Fatalf("project help = %#v", result)
	}
}

func TestProjectListHumanFindingsFollowTheirProjectRows(t *testing.T) {
	data := t.TempDir()
	alphaConfig := filepath.Join(data, "alpha", ".wtree.yml")
	bravoConfig := filepath.Join(data, "bravo", ".wtree.yml")
	if err := config.WriteProjectFile(bravoConfig, config.ProjectConfig{Version: 1, Project: config.Project{ID: "bravo", Name: "Bravo"}, Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: "."}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(filepath.Join(data, "state", "alpha", "default.json"), store.WorkspaceState{ID: "default", Name: "default", Repositories: map[string]store.CheckoutState{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), store.Registry{Projects: map[string]store.RegistryProject{
		"alpha": {Name: "Alpha", ConfigPath: alphaConfig},
		"bravo": {Name: "Bravo", ConfigPath: bravoConfig},
	}}); err != nil {
		t.Fatal(err)
	}
	result := testutil.RunCommand(t, cli.Execute, "project", "list", "--data-dir", data)
	if result.Err != nil {
		t.Fatalf("project list = %#v", result)
	}
	alpha := strings.Index(result.Stdout, "alpha")
	alphaFinding := strings.Index(result.Stdout, "missing-config")
	bravo := strings.Index(result.Stdout, "bravo")
	bravoFinding := strings.Index(result.Stdout, "missing-default-state")
	if alpha == -1 || alphaFinding == -1 || bravo == -1 || bravoFinding == -1 || !(alpha < alphaFinding && alphaFinding < bravo && bravo < bravoFinding) {
		t.Fatalf("findings are not attached to their rows:\n%s", result.Stdout)
	}
}

func TestProjectHelpHidesUnsupportedProjectSelector(t *testing.T) {
	for _, arguments := range [][]string{{"project", "--help"}, {"project", "list", "--help"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil || strings.Contains(result.Stdout, "--project") {
			t.Fatalf("%v help = %#v", arguments, result)
		}
	}
}

func TestProjectListBrokenWriter(t *testing.T) {
	want := errors.New("broken pipe")
	err := cli.Execute([]string{"project", "list", "--data-dir", t.TempDir()}, brokenPipeWriter{err: want}, io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestProjectPruneHumanJSONAndDryRunRetention(t *testing.T) {
	// The global command must not resolve the current directory as a project.
	t.Chdir(t.TempDir())
	data := projectPruneCLIData(t)
	registryPath := filepath.Join(data, "registry.json")
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	dry := testutil.RunCommand(t, cli.Execute, "project", "prune", "stale", "--data-dir", data, "--dry-run")
	if dry.Err != nil || dry.Stderr != "" || !containsAll(dry.Stdout, "stale", "duplicate-config-path", "project configuration", "workspace state", "recovery data", "lock file", "No changes made.") {
		t.Fatalf("dry prune = %#v", dry)
	}
	after, _ := os.ReadFile(registryPath)
	if string(before) != string(after) {
		t.Fatal("dry prune rewrote registry")
	}
	jsonResult := testutil.RunCommand(t, cli.Execute, "project", "prune", "stale", "--data-dir", data, "--json")
	var plan struct {
		Operation string                                                               `json:"operation"`
		ProjectID string                                                               `json:"projectId"`
		Reasons   []string                                                             `json:"reasons"`
		Retained  struct{ ProjectConfig, WorkspaceState, RecoveryData, LockFile bool } `json:"retained"`
	}
	if jsonResult.Err != nil || json.Unmarshal([]byte(jsonResult.Stdout), &plan) != nil || plan.Operation != "prune" || plan.ProjectID != "stale" || len(plan.Reasons) == 0 || !plan.Retained.ProjectConfig || !plan.Retained.WorkspaceState || !plan.Retained.RecoveryData || !plan.Retained.LockFile {
		t.Fatalf("JSON prune = %#v plan=%#v", jsonResult, plan)
	}
	registry, err := store.ReadRegistry(registryPath)
	if err != nil || len(registry.Projects) != 1 || registry.Projects["keeper"].ConfigPath == "" {
		t.Fatalf("registry after prune=%#v %v", registry, err)
	}
	if _, err := os.Stat(filepath.Join(data, "state", "stale", "default.json")); err != nil {
		t.Fatalf("stale state removed: %v", err)
	}
}

func TestProjectPruneErrorsFlagsHelpAndBrokenWriter(t *testing.T) {
	data := projectPruneCLIData(t)
	for _, arguments := range [][]string{
		{"project", "prune"}, {"project", "prune", "stale", "extra"}, {"project", "prune", "../stale", "--data-dir", data},
		{"project", "prune", "stale", "--force", "--data-dir", data}, {"project", "prune", "stale", "--verbose", "--data-dir", data},
		{"project", "--project", ".", "prune", "stale", "--data-dir", data},
	} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Fatalf("%v = %#v", arguments, result)
		}
	}
	result := testutil.RunCommand(t, cli.Execute, "project", "prune", "keeper", "--data-dir", data, "--json")
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if result.Err == nil || cli.ExitCode(result.Err) != 5 || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Error.Code != "validation" {
		t.Fatalf("keeper = %#v envelope=%#v", result, envelope)
	}
	help := testutil.RunCommand(t, cli.Execute, "project", "prune", "--help")
	if help.Err != nil || !containsAll(help.Stdout, "wtree project prune", "--dry-run", "--json") || strings.Contains(help.Stdout, "--project") {
		t.Fatalf("prune help=%#v", help)
	}
	want := errors.New("broken pipe")
	if err := cli.Execute([]string{"project", "prune", "stale", "--data-dir", data, "--dry-run"}, brokenPipeWriter{err: want}, io.Discard); !errors.Is(err, want) {
		t.Fatalf("broken writer=%v", err)
	}
}

func TestProjectPruneRejectsDotDotIDAsInvalidArguments(t *testing.T) {
	data := projectPruneCLIData(t)
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry.Projects[".."] = registry.Projects["stale"]
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), registry); err != nil {
		t.Fatal(err)
	}
	before := snapshotCLIData(t, data)
	result := testutil.RunCommand(t, cli.Execute, "project", "prune", "..", "--data-dir", data, "--dry-run", "--json")
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if result.Err == nil || cli.ExitCode(result.Err) != 2 || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Error.Code != "invalid_arguments" {
		t.Fatalf("dot-dot=%#v envelope=%#v", result, envelope)
	}
	if after := snapshotCLIData(t, data); !reflect.DeepEqual(before, after) {
		t.Fatal("dot-dot CLI mutated data")
	}
}

func TestProjectPruneHumanSuccessAndBrokenSuccessWriter(t *testing.T) {
	data := projectPruneCLIData(t)
	human := testutil.RunCommand(t, cli.Execute, "project", "prune", "stale", "--data-dir", data)
	if human.Err != nil || human.Stderr != "" || human.Stdout != "Removed registry registration \"stale\" only; project data was retained.\n" {
		t.Fatalf("human success=%#v", human)
	}
	if _, err := os.Stat(filepath.Join(data, "state", "stale", "default.json")); err != nil {
		t.Fatalf("success removed retained state: %v", err)
	}

	data = projectPruneCLIData(t)
	want := errors.New("broken success pipe")
	err := cli.Execute([]string{"project", "prune", "stale", "--data-dir", data}, brokenPipeWriter{err: want}, io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("successful-result writer error=%v", err)
	}
	registry, readErr := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if readErr != nil || len(registry.Projects) != 1 {
		t.Fatalf("writer failure did not preserve successful mutation: %#v %v", registry, readErr)
	}
	if _, err := os.Stat(filepath.Join(data, "state", "stale", "default.json")); err != nil {
		t.Fatalf("writer failure removed retained state: %v", err)
	}
}

func TestProjectPruneRegistryErrorTaxonomyAndJSONEnvelope(t *testing.T) {
	for _, test := range []struct {
		name, contents, code string
		directory            bool
	}{
		{name: "absent target", code: "project_not_found"},
		{name: "malformed", contents: `{`, code: "validation"},
		{name: "newer", contents: `{"version":2,"projects":{}}`, code: "validation"},
		{name: "io", directory: true, code: "internal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := t.TempDir()
			if test.directory {
				if err := os.Mkdir(filepath.Join(data, "registry.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else if test.contents != "" {
				if err := os.WriteFile(filepath.Join(data, "registry.json"), []byte(test.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotCLIData(t, data)
			result := testutil.RunCommand(t, cli.Execute, "project", "prune", "missing", "--data-dir", data, "--json")
			var envelope struct {
				Success bool `json:"success"`
				Error   struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if result.Err == nil || result.Stderr != "" || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Success || envelope.Error.Code != test.code || strings.Contains(result.Stdout, `"operation"`) {
				t.Fatalf("result=%#v envelope=%#v", result, envelope)
			}
			if after := snapshotCLIData(t, data); !reflect.DeepEqual(before, after) {
				t.Fatal("error path mutated data")
			}
		})
	}
}

func TestProjectUnregisterHumanJSONRetentionAndReregistrationWarning(t *testing.T) {
	t.Chdir(t.TempDir())
	data := projectPruneCLIData(t)
	dry := testutil.RunCommand(t, cli.Execute, "project", "unregister", "keeper", "--data-dir", data, "--dry-run")
	if dry.Err != nil || dry.Stderr != "" || !containsAll(dry.Stdout, "keeper", "project configuration", "workspace state", "recovery data", "lock file", "register it again", "No changes made.") {
		t.Fatalf("dry unregister=%#v", dry)
	}
	jsonResult := testutil.RunCommand(t, cli.Execute, "project", "unregister", "keeper", "--data-dir", data, "--json")
	var plan struct {
		Operation                string                                                               `json:"operation"`
		ProjectID                string                                                               `json:"projectId"`
		Reasons                  []string                                                             `json:"reasons"`
		Retained                 struct{ ProjectConfig, WorkspaceState, RecoveryData, LockFile bool } `json:"retained"`
		LocalConfigMayReregister bool                                                                 `json:"localConfigMayReregister"`
	}
	if jsonResult.Err != nil || json.Unmarshal([]byte(jsonResult.Stdout), &plan) != nil || plan.Operation != "unregister" || plan.ProjectID != "keeper" || !reflect.DeepEqual(plan.Reasons, []string{"intentional-unregister"}) || !plan.Retained.ProjectConfig || !plan.Retained.WorkspaceState || !plan.Retained.RecoveryData || !plan.Retained.LockFile || !plan.LocalConfigMayReregister {
		t.Fatalf("JSON unregister=%#v plan=%#v", jsonResult, plan)
	}
	if _, err := os.Stat(filepath.Join(data, "state", "keeper", "default.json")); err != nil {
		t.Fatalf("unregister removed retained state: %v", err)
	}

	data = projectPruneCLIData(t)
	human := testutil.RunCommand(t, cli.Execute, "project", "unregister", "keeper", "--data-dir", data)
	if human.Err != nil || human.Stderr != "" || !containsAll(human.Stdout, "Removed registry registration", "only", "all project data was retained", "register it again") {
		t.Fatalf("human unregister=%#v", human)
	}
}

func TestProjectUnregisterErrorsFlagsHelpAndBrokenWriters(t *testing.T) {
	data := projectPruneCLIData(t)
	for _, arguments := range [][]string{
		{"project", "unregister"}, {"project", "unregister", "keeper", "extra"}, {"project", "unregister", "../keeper", "--data-dir", data},
		{"project", "unregister", "keeper", "--force", "--data-dir", data}, {"project", "unregister", "keeper", "--verbose", "--data-dir", data},
		{"project", "--project", ".", "unregister", "keeper", "--data-dir", data},
	} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Fatalf("%v = %#v", arguments, result)
		}
	}
	missing := testutil.RunCommand(t, cli.Execute, "project", "unregister", "missing", "--data-dir", data, "--json")
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if missing.Err == nil || cli.ExitCode(missing.Err) != 3 || json.Unmarshal([]byte(missing.Stdout), &envelope) != nil || envelope.Error.Code != "project_not_found" {
		t.Fatalf("missing=%#v envelope=%#v", missing, envelope)
	}
	help := testutil.RunCommand(t, cli.Execute, "project", "unregister", "--help")
	if help.Err != nil || !containsAll(help.Stdout, "wtree project unregister", "--dry-run", "--json", "register it again") || strings.Contains(help.Stdout, "--project") {
		t.Fatalf("help=%#v", help)
	}
	want := errors.New("broken unregister pipe")
	if err := cli.Execute([]string{"project", "unregister", "keeper", "--data-dir", data, "--dry-run"}, brokenPipeWriter{err: want}, io.Discard); !errors.Is(err, want) {
		t.Fatalf("dry writer=%v", err)
	}
	data = projectPruneCLIData(t)
	if err := cli.Execute([]string{"project", "unregister", "keeper", "--data-dir", data}, brokenPipeWriter{err: want}, io.Discard); !errors.Is(err, want) {
		t.Fatalf("success writer=%v", err)
	}
}

func TestProjectPruneAndUnregisterRejectExplicitFalseUnsupportedFlagsBeforePlanning(t *testing.T) {
	for _, operation := range []string{"prune", "unregister"} {
		for _, flag := range []string{"--force=false", "--verbose=false"} {
			t.Run(operation+"/"+flag, func(t *testing.T) {
				data := projectPruneCLIData(t)
				registryPath := filepath.Join(data, "registry.json")
				beforeBytes, err := os.ReadFile(registryPath)
				if err != nil {
					t.Fatal(err)
				}
				beforeTree := snapshotCLIData(t, data)
				result := testutil.RunCommand(t, cli.Execute, "project", operation, "keeper", flag, "--data-dir", data, "--json")
				var envelope struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if result.Err == nil || cli.ExitCode(result.Err) != 2 || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Error.Code != "invalid_arguments" {
					t.Fatalf("result=%#v envelope=%#v", result, envelope)
				}
				afterBytes, err := os.ReadFile(registryPath)
				if err != nil || !reflect.DeepEqual(beforeBytes, afterBytes) || !reflect.DeepEqual(beforeTree, snapshotCLIData(t, data)) {
					t.Fatal("explicit false unsupported flag mutated registry or data tree")
				}
				for _, lockPath := range []string{filepath.Join(data, "registry.lock"), filepath.Join(data, "projects", "keeper", "project.lock")} {
					if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
						t.Fatalf("explicit false unsupported flag created lock %q: %v", lockPath, err)
					}
				}
			})
		}
	}
}

func TestProjectUnregisterRegistryErrorsUseJSONEnvelopesWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name, contents, code string
		directory            bool
	}{
		{name: "malformed", contents: `{`, code: "validation"},
		{name: "newer", contents: `{"version":2,"projects":{}}`, code: "validation"},
		{name: "io", directory: true, code: "internal"},
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
			before := snapshotCLIData(t, data)
			result := testutil.RunCommand(t, cli.Execute, "project", "unregister", "missing", "--data-dir", data, "--json")
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if result.Err == nil || result.Stderr != "" || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Error.Code != test.code || strings.Contains(result.Stdout, `"operation"`) {
				t.Fatalf("result=%#v envelope=%#v", result, envelope)
			}
			if after := snapshotCLIData(t, data); !reflect.DeepEqual(before, after) {
				t.Fatal("error path mutated data")
			}
		})
	}
}

func projectPruneCLIData(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	configPath := filepath.Join(data, "path with spaces", ".wtree.yml")
	if err := config.WriteProjectFile(configPath, config.ProjectConfig{Version: 1, Project: config.Project{ID: "keeper", Name: "Keeper"}, Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: "."}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"keeper", "stale"} {
		if err := store.WriteWorkspace(filepath.Join(data, "state", id, "default.json"), store.WorkspaceState{ID: "default", Name: "default", Repositories: map[string]store.CheckoutState{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), store.Registry{Projects: map[string]store.RegistryProject{"keeper": {Name: "Keeper", ConfigPath: configPath}, "stale": {Name: "Stale", ConfigPath: configPath}}}); err != nil {
		t.Fatal(err)
	}
	return data
}

type cliTreeEntry struct {
	mode     os.FileMode
	contents []byte
}

func snapshotCLIData(t *testing.T, root string) map[string]cliTreeEntry {
	t.Helper()
	entries := map[string]cliTreeEntry{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := cliTreeEntry{mode: info.Mode()}
		if !entry.IsDir() {
			value.contents, err = os.ReadFile(path)
		}
		entries[relative] = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestProjectListInvalidRegistryUsesJSONErrorWithoutPartialSuccess(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "registry.json"), []byte(`{"version":2,"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := testutil.RunCommand(t, cli.Execute, "project", "list", "--data-dir", data, "--json")
	if result.Err == nil || result.Stderr != "" || contains(result.Stdout, `"projects"`) {
		t.Fatalf("invalid registry = %#v", result)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &envelope); err != nil || envelope.Error.Code != "validation" {
		t.Fatalf("error envelope = %q, %#v, %v", result.Stdout, envelope, err)
	}
}

func TestProjectListRegistryIOFailureUsesInternalJSONErrorWithoutPartialSuccess(t *testing.T) {
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, "registry.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := testutil.RunCommand(t, cli.Execute, "project", "list", "--data-dir", data, "--json")
	if result.Err == nil || result.Stderr != "" || contains(result.Stdout, `"projects"`) {
		t.Fatalf("registry I/O = %#v", result)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &envelope); err != nil || envelope.Error.Code != "internal" {
		t.Fatalf("error envelope = %q, %#v, %v", result.Stdout, envelope, err)
	}
}

func TestProjectListRegistryErrorTaxonomyAndJSONCollections(t *testing.T) {
	for _, test := range []struct {
		name, contents, code string
		directory            bool
	}{
		{name: "malformed", contents: `{`, code: "validation"},
		{name: "newer", contents: `{"version":2,"projects":{}}`, code: "validation"},
		{name: "I/O", directory: true, code: "internal"},
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
			result := testutil.RunCommand(t, cli.Execute, "project", "list", "--data-dir", data, "--json")
			if result.Err == nil || contains(result.Stdout, `"projects"`) {
				t.Fatalf("result = %#v", result)
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &envelope); err != nil || envelope.Error.Code != test.code {
				t.Fatalf("envelope = %q, %#v, %v", result.Stdout, envelope, err)
			}
		})
	}

	data := t.TempDir()
	configPath := filepath.Join(data, "space path", ".wtree.yml")
	if err := config.WriteProjectFile(configPath, config.ProjectConfig{Version: 1, Project: config.Project{ID: "keeper"}, Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: "."}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRegistry(filepath.Join(data, "registry.json"), store.Registry{Projects: map[string]store.RegistryProject{"keeper": {ConfigPath: configPath}, "stale": {ConfigPath: configPath}}}); err != nil {
		t.Fatal(err)
	}
	result := testutil.RunCommand(t, cli.Execute, "project", "list", "--data-dir", data, "--json")
	var report struct {
		Projects []struct {
			ID       string `json:"id"`
			Findings []struct {
				Code    string   `json:"code"`
				Related []string `json:"relatedProjectIds"`
			} `json:"findings"`
		} `json:"projects"`
	}
	if result.Err != nil || json.Unmarshal([]byte(result.Stdout), &report) != nil {
		t.Fatalf("duplicate JSON = %#v", result)
	}
	for _, project := range report.Projects {
		for _, finding := range project.Findings {
			if finding.Related == nil {
				t.Fatalf("%s %s has null relatedProjectIds", project.ID, finding.Code)
			}
		}
	}
}

func containsAll(value string, values ...string) bool {
	for _, item := range values {
		if !contains(value, item) {
			return false
		}
	}
	return true
}
func contains(value, item string) bool {
	for index := 0; index+len(item) <= len(value); index++ {
		if value[index:index+len(item)] == item {
			return true
		}
	}
	return false
}
