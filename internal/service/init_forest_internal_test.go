package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/discovery"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

type initForestFixture struct {
	logicalRoot string
	paths       map[string]string
}

func newInitForestFixture(t *testing.T) initForestFixture {
	t.Helper()
	logicalRoot := t.TempDir()
	paths := map[string]string{
		"api":   filepath.Join(logicalRoot, "api"),
		"alpha": filepath.Join(logicalRoot, "api", "alpha"),
		"beta":  filepath.Join(logicalRoot, "api", "alpha", "beta"),
		"gamma": filepath.Join(logicalRoot, "api", "alpha", "beta", "gamma"),
		"web":   filepath.Join(logicalRoot, "web"),
	}
	repositories := map[string]testutil.PushedGitRepository{}
	for _, id := range []string{"api", "alpha", "beta", "gamma", "web"} {
		repository := testutil.NewPushedGitRepository(t)
		repository.CommitFile("readme", id+"\n", "initial")
		repositories[id] = repository
	}
	for _, id := range []string{"api", "alpha", "beta", "gamma", "web"} {
		if err := os.MkdirAll(filepath.Dir(paths[id]), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(repositories[id].Path, paths[id]); err != nil {
			t.Fatal(err)
		}
	}
	return initForestFixture{logicalRoot: logicalRoot, paths: paths}
}

func TestInitAuthorsPlainForestOnlyInSelectedNonDotBase(t *testing.T) {
	logicalRoot := t.TempDir()
	api, web := testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t)
	api.CommitFile("api.txt", "api\n", "api")
	web.CommitFile("web.txt", "web\n", "web")
	apiPath, webPath := filepath.Join(logicalRoot, "services", "api"), filepath.Join(logicalRoot, "web")
	if err := os.MkdirAll(filepath.Dir(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(api.Path, apiPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(web.Path, webPath); err != nil {
		t.Fatal(err)
	}

	data := t.TempDir()
	result, err := NewInitializer().Init(context.Background(), InitRequest{Path: logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(logicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalAPI, err := filepath.EvalSymlinks(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.LogicalRoot != canonicalRoot || result.BaseRepository != "api" || result.ConfigPath != filepath.Join(canonicalAPI, ".wtree.yml") || result.ManifestPath != filepath.Join(canonicalAPI, "project.wtree.yml") {
		t.Fatalf("init result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(webPath, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("sibling local config stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(logicalRoot, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("logical-root local config stat error = %v", err)
	}
	localBytes, err := os.ReadFile(filepath.Join(apiPath, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.LoadProject(localBytes)
	if err != nil {
		t.Fatal(err)
	}
	if local.Project.BaseRepository != "api" || local.LogicalRoot != "../.." || local.Repositories["api"].Source != "services/api" || local.Repositories["web"].Source != "web" {
		t.Fatalf("local config = %#v", local)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(data, result.ProjectID, "default"))
	if err != nil || state.Path != canonicalRoot || len(state.Repositories) != 2 {
		t.Fatalf("workspace state = %#v, %v", state, err)
	}
}

func TestInitRequiresExplicitTopLevelBaseForMultipleRoots(t *testing.T) {
	logicalRoot := t.TempDir()
	for _, name := range []string{"api", "web"} {
		repository := testutil.NewPushedGitRepository(t)
		repository.CommitFile(name+".txt", name+"\n", name)
		if err := os.Rename(repository.Path, filepath.Join(logicalRoot, name)); err != nil {
			t.Fatal(err)
		}
	}
	data := t.TempDir()
	if _, err := NewInitializer().Init(context.Background(), InitRequest{Path: logicalRoot, DataDir: data}); err == nil {
		t.Fatal("init accepted ambiguous top-level forest without --base-repository")
	}
	for _, path := range []string{filepath.Join(logicalRoot, "api", ".wtree.yml"), filepath.Join(logicalRoot, "api", "project.wtree.yml"), filepath.Join(data, "registry.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("ambiguous init wrote %q: %v", path, err)
		}
	}
}

func TestInitRejectsUnknownAndNestedBaseSelectionsWithoutMutation(t *testing.T) {
	logicalRoot := t.TempDir()
	for _, relative := range []string{"api", "api/shared", "web"} {
		repository := testutil.NewPushedGitRepository(t)
		repository.CommitFile(filepath.Base(relative)+".txt", relative+"\n", relative)
		path := filepath.Join(logicalRoot, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(repository.Path, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, base := range []string{"missing", "shared"} {
		t.Run(base, func(t *testing.T) {
			data := t.TempDir()
			_, err := NewInitializer().Init(context.Background(), InitRequest{Path: logicalRoot, DataDir: data, BaseRepository: base})
			if err == nil {
				t.Fatal("init accepted invalid base selection")
			}
			for _, path := range []string{filepath.Join(logicalRoot, "api", ".wtree.yml"), filepath.Join(logicalRoot, "api", "project.wtree.yml"), filepath.Join(data, "registry.json")} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("invalid base selection wrote %q: %v", path, statErr)
				}
			}
		})
	}
}

func TestInitForestWritesOnlyBaseMetadataAndImmediateParentIgnores(t *testing.T) {
	logicalRoot := t.TempDir()
	api, shared, web := testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t)
	api.CommitFile("readme", "x\n", "initial")
	shared.CommitFile("readme", "x\n", "initial")
	web.CommitFile("readme", "x\n", "initial")
	apiPath, sharedPath, webPath := filepath.Join(logicalRoot, "api"), filepath.Join(logicalRoot, "api", "shared"), filepath.Join(logicalRoot, "web")
	if err := os.Rename(api.Path, apiPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(shared.Path, sharedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(web.Path, webPath); err != nil {
		t.Fatal(err)
	}

	if _, err := NewInitializer().Init(context.Background(), InitRequest{Path: logicalRoot, DataDir: t.TempDir(), BaseRepository: "api"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(apiPath, ".gitignore")); err != nil || string(data) != "/.wtree.yml\n/shared/\n" {
		t.Fatalf("base ignore = %q, %v", data, err)
	}
	for _, path := range []string{filepath.Join(webPath, ".gitignore"), filepath.Join(logicalRoot, ".gitignore"), filepath.Join(sharedPath, ".gitignore")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected ignore %q: %v", path, err)
		}
	}
}

func TestInitForestProjectIDIsStableAcrossLogicalRootRelocationAndSensitiveToBase(t *testing.T) {
	apiSource, webSource := testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t)
	apiSource.CommitFile("api.txt", "api\n", "api")
	webSource.CommitFile("web.txt", "web\n", "web")
	cloneForest := func(root string, order []string) {
		t.Helper()
		checkouts := map[string]struct{ source, relative string }{
			"api": {apiSource.Path, "services/api"},
			"web": {webSource.Path, "web"},
		}
		for _, id := range order {
			checkout := checkouts[id]
			target := filepath.Join(root, checkout.relative)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("git", "clone", checkout.source, target).CombinedOutput(); err != nil {
				t.Fatalf("clone %s: %v\n%s", checkout.relative, err, output)
			}
		}
	}
	firstRoot, secondRoot := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	cloneForest(firstRoot, []string{"api", "web"})
	cloneForest(secondRoot, []string{"web", "api"})

	first, err := NewInitializer().Init(context.Background(), InitRequest{Path: firstRoot, DataDir: t.TempDir(), BaseRepository: "api", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewInitializer().Init(context.Background(), InitRequest{Path: secondRoot, DataDir: t.TempDir(), BaseRepository: "api", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectID != second.ProjectID {
		t.Fatalf("relocated forest project IDs differ: %q != %q", first.ProjectID, second.ProjectID)
	}
	if got, want := initForestTopology(first.Repositories), initForestTopology(second.Repositories); got != want {
		t.Fatalf("creation order changed discovered topology:\nfirst:  %s\nsecond: %s", got, want)
	}
	otherBase, err := NewInitializer().Init(context.Background(), InitRequest{Path: firstRoot, DataDir: t.TempDir(), BaseRepository: "web", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if otherBase.ProjectID == first.ProjectID {
		t.Fatalf("changing selected base did not change project identity: %q", first.ProjectID)
	}
}

func initForestTopology(repositories []discovery.Repository) string {
	values := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		values = append(values, repository.ID+"|"+repository.ParentID+"|"+repository.Mount)
	}
	return strings.Join(values, "\n")
}

func TestInitForestRetainsCompletedParentIgnoresAfterWorkspaceFailure(t *testing.T) {
	logicalRoot := t.TempDir()
	api, shared, leaf, web := testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t)
	for _, repository := range []testutil.PushedGitRepository{api, shared, leaf, web} {
		repository.CommitFile("readme", "x\n", "initial")
	}
	apiPath := filepath.Join(logicalRoot, "api")
	if err := os.Rename(api.Path, apiPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(shared.Path, filepath.Join(apiPath, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(leaf.Path, filepath.Join(apiPath, "shared", "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(web.Path, filepath.Join(logicalRoot, "web")); err != nil {
		t.Fatal(err)
	}

	data := t.TempDir()
	initializer := NewInitializerWithWriters(store.WriteRegistry, func(string, store.WorkspaceState) error {
		return errors.New("injected workspace failure")
	})
	_, err := initializer.Init(context.Background(), InitRequest{Path: logicalRoot, DataDir: data, BaseRepository: "api"})
	if err == nil || !strings.Contains(err.Error(), "source ignore progress") {
		t.Fatalf("Init() error = %v, want retained source ignore progress", err)
	}
	for path, want := range map[string]string{
		filepath.Join(apiPath, ".gitignore"):           "/.wtree.yml\n/shared/\n",
		filepath.Join(apiPath, "shared", ".gitignore"): "/leaf/\n",
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("retained ignore %s = %q, %v; want %q", path, got, readErr, want)
		}
	}
	for _, path := range []string{
		filepath.Join(apiPath, ".wtree.yml"),
		filepath.Join(apiPath, "project.wtree.yml"),
		filepath.Join(data, "registry.json"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("rollback-owned target %s survived: %v", path, statErr)
		}
	}
}

func TestInitForestSelectionRejectionsPreserveAllCandidateTargets(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	targets := []string{
		filepath.Join(fixture.paths["api"], ".wtree.yml"), filepath.Join(fixture.paths["api"], "project.wtree.yml"), filepath.Join(fixture.paths["api"], ".gitignore"),
		filepath.Join(fixture.paths["alpha"], ".gitignore"), filepath.Join(fixture.paths["beta"], ".gitignore"),
		filepath.Join(fixture.paths["web"], ".wtree.yml"), filepath.Join(fixture.paths["web"], "project.wtree.yml"), filepath.Join(fixture.paths["web"], ".gitignore"),
		filepath.Join(fixture.logicalRoot, ".gitignore"), filepath.Join(data, "registry.json"), filepath.Join(data, "state", "retained", "default.json"),
	}
	for index, path := range targets {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("preserve-"+string(rune('a'+index))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotInitTargets(t, targets)
	for _, test := range []struct {
		name, base, phrase string
	}{
		{"missing", "", "multiple top-level repositories require --base-repository"},
		{"unknown", "missing", "base repository \"missing\" is not discovered"},
		{"nested", "alpha", "base repository \"alpha\" is nested"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewInitializer().Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: test.base})
			if err == nil || !strings.Contains(err.Error(), test.phrase) || !strings.Contains(err.Error(), "candidates: api (api), web (web)") {
				t.Fatalf("Init() error = %v, want deterministic candidates", err)
			}
			assertInitTargetSnapshot(t, before)
		})
	}
}

func TestInitForestFailureBoundariesRetainOnlyCompletedIgnoreProgress(t *testing.T) {
	for _, boundary := range []string{"ignore-first", "ignore-middle", "ignore-last", "local", "portable", "registry", "workspace"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newInitForestFixture(t)
			data := t.TempDir()
			registryPath := filepath.Join(data, "registry.json")
			originalRegistry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{}}
			if err := store.WriteRegistry(registryPath, originalRegistry); err != nil {
				t.Fatal(err)
			}
			registryBytes, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			initializer := newInitializer()
			switch boundary {
			case "ignore-first", "ignore-middle", "ignore-last":
				failAt := map[string]int{"ignore-first": 1, "ignore-middle": 2, "ignore-last": 3}[boundary]
				writes := 0
				initializer.applyIgnores = NewIgnoreApplierWith(func(file IgnoreFilePlan, beforeReplace func() error) (bool, error) {
					writes++
					if writes == failAt {
						return false, errors.New("injected " + boundary)
					}
					if err := beforeReplace(); err != nil {
						return false, err
					}
					if err := os.WriteFile(file.Path, file.NewBytes, file.Snapshot.Mode); err != nil {
						return false, err
					}
					return true, nil
				}).Apply
			case "local", "portable":
				initializer.writeFile = func(path string, contents []byte, _ os.FileMode) error {
					if (boundary == "local" && filepath.Base(path) == ".wtree.yml") || (boundary == "portable" && filepath.Base(path) == "project.wtree.yml") {
						return errors.New("injected " + boundary)
					}
					return store.WriteRawAtomic(path, contents)
				}
			case "registry":
				initializer.useStoreCAS = false
				initializer.writeRegistry = func(string, store.Registry) error { return errors.New("injected registry") }
			case "workspace":
				initializer.useStoreCAS = false
				initializer.writeWorkspace = func(string, store.WorkspaceState) error { return errors.New("injected workspace") }
			}

			_, err = initializer.Init(context.Background(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api"})
			if err == nil {
				t.Fatal("Init() error = nil")
			}
			assertForestIgnoreBoundary(t, fixture, boundary, err)
			for _, path := range []string{filepath.Join(fixture.paths["api"], ".wtree.yml"), filepath.Join(fixture.paths["api"], "project.wtree.yml")} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("rollback-owned metadata %s survived: %v", path, statErr)
				}
			}
			gotRegistry, readErr := os.ReadFile(registryPath)
			if readErr != nil || !bytes.Equal(gotRegistry, registryBytes) {
				t.Fatalf("registry after %s = %q, %v; want original bytes", boundary, gotRegistry, readErr)
			}
			entries, readErr := os.ReadDir(filepath.Join(data, "state"))
			if readErr == nil && len(entries) != 0 {
				t.Fatalf("workspace state survived %s: %v", boundary, entries)
			} else if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
		})
	}
}

type initTargetSnapshot map[string][]byte

func snapshotInitTargets(t *testing.T, paths []string) initTargetSnapshot {
	t.Helper()
	result := make(initTargetSnapshot, len(paths))
	for _, path := range paths {
		bytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = bytes
	}
	return result
}

func assertInitTargetSnapshot(t *testing.T, want initTargetSnapshot) {
	t.Helper()
	for path, wantBytes := range want {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, wantBytes) {
			t.Fatalf("target %s = %q, %v; want %q", path, got, err, wantBytes)
		}
	}
}

func assertForestIgnoreBoundary(t *testing.T, fixture initForestFixture, boundary string, err error) {
	t.Helper()
	paths := []string{filepath.Join(fixture.paths["api"], ".gitignore"), filepath.Join(fixture.paths["alpha"], ".gitignore"), filepath.Join(fixture.paths["beta"], ".gitignore")}
	wants := []string{"/.wtree.yml\n/alpha/\n", "/beta/\n", "/gamma/\n"}
	completed := 3
	switch boundary {
	case "ignore-first":
		completed = 0
	case "ignore-middle":
		completed = 1
	case "ignore-last":
		completed = 2
	}
	for index, path := range paths {
		got, readErr := os.ReadFile(path)
		if index < completed {
			if readErr != nil || string(got) != wants[index] {
				t.Fatalf("completed ignore %s = %q, %v; want %q", path, got, readErr, wants[index])
			}
			continue
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("uncompleted ignore %s exists after %s: %v", path, boundary, statErr)
		}
	}
	if completed != 0 {
		updates := make([]IgnoreFileUpdate, 0, len(paths))
		for index, path := range paths {
			parent, canonicalErr := filepath.EvalSymlinks(filepath.Dir(path))
			if canonicalErr != nil {
				t.Fatal(canonicalErr)
			}
			updates = append(updates, IgnoreFileUpdate{ParentRepositoryID: []string{"api", "alpha", "beta"}[index], Path: filepath.Join(parent, ".gitignore"), AddedRules: []string{strings.TrimSpace(wants[index])}})
			if index == 0 {
				updates[index].AddedRules = []string{"/.wtree.yml", "/alpha/"}
			}
		}
		wantProgress := ignoreProgressDiagnostic(IgnoreApplyResult{Changed: updates[:completed], Remaining: updates[completed:]})
		if !strings.Contains(err.Error(), wantProgress) {
			t.Fatalf("retained progress diagnostic = %q; want exact %q", err, wantProgress)
		}
	}
}
