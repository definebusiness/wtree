package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
)

// This first slice establishes that pruning is a service-owned, read-only
// plan. The fixture helpers deliberately use a temporary data directory.
func TestProjectPrunePlansOnlySupersededDuplicate(t *testing.T) {
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "keeper", "keeper")
	writeValidDefaultState(t, data, "keeper")
	writeValidDefaultState(t, data, "stale")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"keeper": {ConfigPath: configPath},
		"stale":  {ConfigPath: configPath},
	})

	plan, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, "stale")
	if err != nil || plan.ProjectID != "stale" || len(plan.Reasons) != 2 || plan.Reasons[0] != "config-id-mismatch" || plan.Reasons[1] != "duplicate-config-path" {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	if !plan.Retained.ProjectConfig || !plan.Retained.WorkspaceState || !plan.Retained.RecoveryData || !plan.Retained.LockFile {
		t.Fatalf("retained = %#v", plan.Retained)
	}
}

func TestProjectPruneRejectsUnsafeUnknownAndNonPrunableWithoutMutation(t *testing.T) {
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "healthy", "healthy")
	writeValidDefaultState(t, data, "healthy")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{"healthy": {ConfigPath: configPath}})
	before := snapshotInventoryTree(t, data)
	for _, id := range []string{"", ".", "..", "../healthy", "a/b", `a\\b`, filepath.Join(data, "absolute"), "unknown", "healthy"} {
		_, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, id)
		if err == nil {
			t.Fatalf("PlanPrune(%q) succeeded", id)
		}
	}
	if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected plan mutated data: before=%#v after=%#v", before, after)
	}
}

func TestProjectPruneRejectsDotDotIDWithoutCreatingEscapedLock(t *testing.T) {
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "keeper", "keeper")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"keeper": {ConfigPath: configPath}, "..": {ConfigPath: configPath},
	})
	before := snapshotInventoryTree(t, data)
	_, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, "..")
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorInvalidArguments {
		t.Fatalf("dot-dot error=%v", err)
	}
	if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(before, after) {
		t.Fatal("dot-dot validation mutated registry data")
	}
	for _, path := range []string{filepath.Join(data, "registry.lock"), filepath.Join(data, "project.lock"), filepath.Join(data, "projects", "..", "project.lock")} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("dot-dot validation created lock %q: %v", path, statErr)
		}
	}
}

func TestProjectPruneRejectsAmbiguousDuplicateEvenWithStaleDiagnostic(t *testing.T) {
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "declared-other", "other")
	for _, id := range []string{"first", "second"} {
		writeValidDefaultState(t, data, id)
	}
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"first":  {ConfigPath: configPath},
		"second": {ConfigPath: configPath},
	})
	before := snapshotInventoryTree(t, data)
	for _, id := range []string{"first", "second"} {
		if _, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, id); err == nil {
			report, _ := service.NewProjectInventoryService().Inventory(context.Background(), data)
			t.Fatalf("ambiguous duplicate %q was selected: %#v", id, report)
		}
	}
	if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(before, after) {
		t.Fatal("ambiguous planning mutated data")
	}
}

func TestProjectPruneDryPlanDoesNotCreateLocksOrChangeArtifacts(t *testing.T) {
	data, plan := projectPruneFixture(t)
	registryPath := filepath.Join(data, "registry.json")
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	tree := snapshotInventoryTree(t, data)
	got, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, "stale")
	if err != nil || !reflect.DeepEqual(got, plan) {
		t.Fatalf("dry plan=%#v %v", got, err)
	}
	after, err := os.ReadFile(registryPath)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("registry bytes changed: %q %v", after, err)
	}
	afterInfo, err := os.Stat(registryPath)
	if err != nil || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatalf("registry timestamp changed: %v %v", afterInfo, err)
	}
	if afterTree := snapshotInventoryTree(t, data); !reflect.DeepEqual(tree, afterTree) {
		t.Fatalf("dry plan changed tree")
	}
	for _, path := range []string{filepath.Join(data, "registry.lock"), filepath.Join(data, "projects", "stale", "project.lock")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry plan created lock %q: %v", path, err)
		}
	}
}

func TestProjectPruneExecutionDeletesOnlyTargetAndRetainsArtifacts(t *testing.T) {
	data, plan := projectPruneFixture(t)
	beforeTree := snapshotInventoryTree(t, data)
	otherRecovery := filepath.Join(data, "projects", "other", "recovery", "record.json")
	if err := os.MkdirAll(filepath.Dir(otherRecovery), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherRecovery, []byte("retain recovery"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeTree = snapshotInventoryTree(t, data)
	beforeRegistry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.NewProjectRegistryRemovalService().Prune(context.Background(), data, plan)
	if err != nil || !reflect.DeepEqual(got, plan) {
		t.Fatalf("Prune=%#v %v", got, err)
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil || len(registry.Projects) != 2 {
		t.Fatalf("registry=%#v %v", registry, err)
	}
	if _, found := registry.Projects["stale"]; found {
		t.Fatal("target registration retained")
	}
	delete(beforeRegistry.Projects, "stale")
	if registry.Version != store.Version || !reflect.DeepEqual(registry, beforeRegistry) {
		t.Fatalf("non-target registry data changed: got=%#v want=%#v", registry, beforeRegistry)
	}
	for _, path := range []string{filepath.Join(data, "registry.lock"), filepath.Join(data, "projects", "stale", "project.lock")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("removal did not retain lock path %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "projects", "stale", "recovery")); !os.IsNotExist(err) {
		t.Fatalf("prune created recovery metadata: %v", err)
	}
	for path, before := range beforeTree {
		if path == "registry.json" || path == "." || path == "projects/stale" {
			continue
		}
		after, found := snapshotInventoryTree(t, data)[path]
		if !found || !reflect.DeepEqual(after, before) {
			t.Fatalf("artifact changed %q: before=%#v after=%#v", path, before, after)
		}
	}
}

func TestProjectPruneRevalidatesAndWriterFailurePreservesRegistry(t *testing.T) {
	data, plan := projectPruneFixture(t)
	registryPath := filepath.Join(data, "registry.json")
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	failing := service.NewProjectRegistryRemovalServiceWith(lock.Manager{}, func(string, store.Registry) error { return errors.New("writer failed") })
	if _, err := failing.Prune(context.Background(), data, plan); err == nil {
		t.Fatal("writer failure succeeded")
	}
	after, _ := os.ReadFile(registryPath)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("writer failure changed registry")
	}
	registry, _ := store.ReadRegistry(registryPath)
	registry.Projects["stale"] = store.RegistryProject{ConfigPath: filepath.Join(data, "changed.yml")}
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewProjectRegistryRemovalService().Prune(context.Background(), data, plan); err == nil {
		t.Fatal("changed entry accepted")
	}
}

func TestProjectPruneRecoveryBlocksBeforeWrite(t *testing.T) {
	data, _ := projectPruneFixture(t)
	path := filepath.Join(data, "projects", "stale", "recovery", "record.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(data, "registry.json"))
	if _, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, "stale"); err == nil {
		t.Fatal("recovery accepted")
	}
	after, _ := os.ReadFile(filepath.Join(data, "registry.json"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("recovery planning rewrote registry")
	}
}

func TestProjectPruneRefusesRegistryAndProjectLockContention(t *testing.T) {
	data, plan := projectPruneFixture(t)
	manager := lock.Manager{}
	registryLock, err := manager.RegistryLock(context.Background(), data, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewProjectRegistryRemovalService().Prune(context.Background(), data, plan); err == nil {
		t.Fatal("registry lock contention accepted")
	}
	if err := registryLock.Unlock(); err != nil {
		t.Fatal(err)
	}
	projectLock, err := manager.ProjectLock(context.Background(), data, "stale", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer projectLock.Unlock()
	if _, err := service.NewProjectRegistryRemovalService().Prune(context.Background(), data, plan); err == nil {
		t.Fatal("project lock contention accepted")
	}
}

func TestProjectPruneAcquiresRegistryThenProjectAndWritesOnlyAfterBoth(t *testing.T) {
	data, plan := projectPruneFixture(t)
	for _, test := range []struct {
		name     string
		failAt   string
		wantCall []string
	}{
		{name: "registry", failAt: "registry", wantCall: []string{"registry"}},
		{name: "project", failAt: "project", wantCall: []string{"registry", "project"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			locker := &removalObservingLocker{failAt: test.failAt}
			writes := 0
			remover := service.NewProjectRegistryRemovalServiceWith(locker, func(string, store.Registry) error { writes++; return nil })
			if _, err := remover.Prune(context.Background(), data, plan); err == nil {
				t.Fatal("lock failure accepted")
			}
			if !reflect.DeepEqual(locker.calls, test.wantCall) || writes != 0 {
				t.Fatalf("calls=%v writes=%d", locker.calls, writes)
			}
		})
	}
}

func TestProjectPruneLockedRacesFailBeforeWriter(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, data string)
	}{
		{name: "missing target", mutate: func(t *testing.T, data string) {
			registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
			if err != nil {
				t.Fatal(err)
			}
			delete(registry.Projects, "stale")
			if err := store.WriteRegistry(filepath.Join(data, "registry.json"), registry); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "eligibility changed", mutate: func(t *testing.T, data string) {
			path := filepath.Join(data, "projects with spaces", "keeper", ".wtree.yml")
			if err := config.WriteProjectFile(path, config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "stale", Name: "stale", BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: ".", DefaultBranch: "main"}}, Worktrees: config.Worktrees{}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: "/manifests/project.wtree.yml"}}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "recovery appeared", mutate: func(t *testing.T, data string) {
			path := filepath.Join(data, "projects", "stale", "recovery", "record.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, plan := projectPruneFixture(t)
			var afterMutation map[string]inventoryTreeEntry
			locker := &removalObservingLocker{onProject: func() { test.mutate(t, data); afterMutation = snapshotInventoryTree(t, data) }}
			writes := 0
			remover := service.NewProjectRegistryRemovalServiceWith(locker, func(string, store.Registry) error { writes++; return nil })
			if _, err := remover.Prune(context.Background(), data, plan); err == nil {
				t.Fatal("race was accepted")
			}
			if writes != 0 || !reflect.DeepEqual(afterMutation, snapshotInventoryTree(t, data)) {
				t.Fatalf("writes=%d or artifacts changed", writes)
			}
		})
	}
}

func TestProjectPruneRegistryErrorTaxonomy(t *testing.T) {
	for _, test := range []struct {
		name, contents string
		directory      bool
		kind           service.ErrorKind
	}{
		{name: "missing target", kind: service.ErrorProjectNotFound},
		{name: "malformed", contents: `{`, kind: service.ErrorValidation},
		{name: "newer", contents: `{"version":2,"projects":{}}`, kind: service.ErrorValidation},
		{name: "io", directory: true, kind: service.ErrorInternal},
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
			before := snapshotInventoryTree(t, data)
			_, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, "missing")
			var application *service.Error
			if !errors.As(err, &application) || application.Kind != test.kind || !reflect.DeepEqual(before, snapshotInventoryTree(t, data)) {
				t.Fatalf("error=%v kind=%v", err, application)
			}
		})
	}
}

func TestProjectPrunePostReplacementWriterErrorLeavesReadableRegistry(t *testing.T) {
	data, plan := projectPruneFixture(t)
	retained := snapshotInventoryTree(t, data)
	remover := service.NewProjectRegistryRemovalServiceWith(lock.Manager{}, func(path string, registry store.Registry) error {
		if err := store.WriteRegistry(path, registry); err != nil {
			return err
		}
		return errors.New("durability status unavailable after replacement")
	})
	if _, err := remover.Prune(context.Background(), data, plan); err == nil {
		t.Fatal("post-replacement error succeeded")
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil || registry.Version != store.Version || len(registry.Projects) != 2 {
		t.Fatalf("registry after replacement error = %#v, %v", registry, err)
	}
	for path, before := range retained {
		if path == "registry.json" || path == "." || path == "projects/stale" {
			continue
		}
		after, found := snapshotInventoryTree(t, data)[path]
		if !found || !reflect.DeepEqual(before, after) {
			t.Fatalf("retained artifact changed %q", path)
		}
	}
}

func TestProjectUnregisterPlansHealthyAndInconsistentEntriesButRecoveryBlocks(t *testing.T) {
	data := t.TempDir()
	healthyPath := writeInventoryConfig(t, data, "healthy", "healthy")
	writeValidDefaultState(t, data, "healthy")
	writeValidDefaultState(t, data, "inconsistent")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"healthy":      {Name: "Healthy", ConfigPath: healthyPath},
		"inconsistent": {Name: "Inconsistent", ConfigPath: filepath.Join(data, "missing", ".wtree.yml")},
	})
	remover := service.NewProjectRegistryRemovalService()
	for _, id := range []string{"healthy", "inconsistent"} {
		plan, err := remover.PlanUnregister(context.Background(), data, id)
		if err != nil || plan.Operation != "unregister" || plan.ProjectID != id || plan.Reasons == nil || !reflect.DeepEqual(plan.Reasons, []string{"intentional-unregister"}) || !plan.Retained.ProjectConfig || !plan.Retained.WorkspaceState || !plan.Retained.RecoveryData || !plan.Retained.LockFile || !plan.LocalConfigMayReregister {
			t.Fatalf("PlanUnregister(%q) = %#v, %v", id, plan, err)
		}
	}
	if _, err := remover.PlanPrune(context.Background(), data, "healthy"); err == nil {
		t.Fatal("prune accepted healthy registration")
	}
	before := snapshotInventoryTree(t, data)
	for _, id := range []string{"", ".", "..", "../healthy", "missing"} {
		if _, err := remover.PlanUnregister(context.Background(), data, id); err == nil {
			t.Fatalf("PlanUnregister(%q) succeeded", id)
		}
	}
	if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected unregister planning mutated data")
	}
	recovery := filepath.Join(data, "projects", "healthy", "recovery", "record.json")
	if err := os.MkdirAll(filepath.Dir(recovery), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recovery, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	withRecovery := snapshotInventoryTree(t, data)
	if _, err := remover.PlanUnregister(context.Background(), data, "healthy"); err == nil {
		t.Fatal("unregister accepted recovery-bearing registration")
	}
	if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(withRecovery, after) {
		t.Fatal("unregister planning mutated data beyond test recovery fixture")
	}
}

func TestProjectUnregisterUsesSharedBoundaryAndRetainsAllArtifacts(t *testing.T) {
	data, _ := projectPruneFixture(t)
	remover := service.NewProjectRegistryRemovalService()
	plan, err := remover.PlanUnregister(context.Background(), data, "keeper")
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	beforeRegistry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeTree := snapshotInventoryTree(t, data)
	dry, err := remover.PlanUnregister(context.Background(), data, "keeper")
	if err != nil || !reflect.DeepEqual(dry, plan) {
		t.Fatalf("dry plan=%#v %v", dry, err)
	}
	afterBytes, _ := os.ReadFile(registryPath)
	afterInfo, _ := os.Stat(registryPath)
	if !reflect.DeepEqual(beforeBytes, afterBytes) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) || !reflect.DeepEqual(beforeTree, snapshotInventoryTree(t, data)) {
		t.Fatal("unregister dry-run planning mutated data")
	}
	result, err := remover.Unregister(context.Background(), data, plan)
	if err != nil || !reflect.DeepEqual(result, plan) {
		t.Fatalf("Unregister=%#v %v", result, err)
	}
	registry, err := store.ReadRegistry(registryPath)
	if err != nil || registry.Version != store.Version || len(registry.Projects) != 2 {
		t.Fatalf("registry=%#v %v", registry, err)
	}
	if _, exists := registry.Projects["keeper"]; exists {
		t.Fatal("target registration retained")
	}
	delete(beforeRegistry.Projects, "keeper")
	if !reflect.DeepEqual(registry, beforeRegistry) {
		t.Fatalf("unregister changed non-target registry data: got=%#v want=%#v", registry, beforeRegistry)
	}
	for _, path := range []string{filepath.Join(data, "registry.lock"), filepath.Join(data, "projects", "keeper", "project.lock")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unregister did not retain lock path %q: %v", path, err)
		}
	}
	for path, before := range beforeTree {
		if path == "." || path == "registry.json" || path == "projects" || path == "projects/keeper" {
			continue
		}
		after, found := snapshotInventoryTree(t, data)[path]
		if !found || !reflect.DeepEqual(before, after) {
			t.Fatalf("retained artifact changed %q", path)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "projects", "keeper", "recovery")); !os.IsNotExist(err) {
		t.Fatalf("unregister created recovery metadata: %v", err)
	}
}

func TestProjectUnregisterRevalidatesLocksAndWriterFailures(t *testing.T) {
	data, _ := projectPruneFixture(t)
	plan, err := service.NewProjectRegistryRemovalService().PlanUnregister(context.Background(), data, "keeper")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, failAt string }{{"registry", "registry"}, {"project", "project"}} {
		t.Run(test.name, func(t *testing.T) {
			locker := &removalObservingLocker{failAt: test.failAt}
			writes := 0
			remover := service.NewProjectRegistryRemovalServiceWith(locker, func(string, store.Registry) error { writes++; return nil })
			if _, err := remover.Unregister(context.Background(), data, plan); err == nil || writes != 0 {
				t.Fatalf("lock failure=%v writes=%d", err, writes)
			}
			if !reflect.DeepEqual(locker.calls, map[string][]string{"registry": {"registry"}, "project": {"registry", "project"}}[test.failAt]) {
				t.Fatalf("lock order=%v", locker.calls)
			}
		})
	}
	registryPath := filepath.Join(data, "registry.json")
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	failing := service.NewProjectRegistryRemovalServiceWith(lock.Manager{}, func(string, store.Registry) error { return errors.New("writer failed") })
	if _, err := failing.Unregister(context.Background(), data, plan); err == nil {
		t.Fatal("writer failure succeeded")
	}
	after, _ := os.ReadFile(registryPath)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("writer failure changed registry")
	}
	for _, test := range []struct {
		name   string
		mutate func(string)
	}{
		{"missing", func(data string) {
			registryPath := filepath.Join(data, "registry.json")
			registry, _ := store.ReadRegistry(registryPath)
			delete(registry.Projects, "keeper")
			_ = store.WriteRegistry(registryPath, registry)
		}},
		{"changed", func(data string) {
			registryPath := filepath.Join(data, "registry.json")
			registry, _ := store.ReadRegistry(registryPath)
			registry.Projects["keeper"] = store.RegistryProject{ConfigPath: filepath.Join(data, "changed.yml")}
			_ = store.WriteRegistry(registryPath, registry)
		}},
		{"recovery", func(data string) {
			path := filepath.Join(data, "projects", "keeper", "recovery", "record.json")
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			_ = os.WriteFile(path, []byte("{}"), 0o600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _ := projectPruneFixture(t)
			plan, planErr := service.NewProjectRegistryRemovalService().PlanUnregister(context.Background(), data, "keeper")
			if planErr != nil {
				t.Fatal(planErr)
			}
			writes := 0
			var afterMutation map[string]inventoryTreeEntry
			locker := &removalObservingLocker{onProject: func() { test.mutate(data); afterMutation = snapshotInventoryTree(t, data) }}
			remover := service.NewProjectRegistryRemovalServiceWith(locker, func(string, store.Registry) error { writes++; return nil })
			if _, err := remover.Unregister(context.Background(), data, plan); err == nil || writes != 0 {
				t.Fatalf("race=%v writes=%d", err, writes)
			}
			if after := snapshotInventoryTree(t, data); !reflect.DeepEqual(afterMutation, after) {
				t.Fatal("locked revalidation changed data after the injected race")
			}
		})
	}
}

func TestProjectUnregisterRegistryErrorTaxonomy(t *testing.T) {
	for _, test := range []struct {
		name, contents string
		directory      bool
		kind           service.ErrorKind
	}{
		{name: "missing target", kind: service.ErrorProjectNotFound},
		{name: "malformed", contents: `{`, kind: service.ErrorValidation},
		{name: "newer", contents: `{"version":2,"projects":{}}`, kind: service.ErrorValidation},
		{name: "io", directory: true, kind: service.ErrorInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := t.TempDir()
			path := filepath.Join(data, "registry.json")
			var err error
			if test.directory {
				err = os.Mkdir(path, 0o700)
			} else if test.contents != "" {
				err = os.WriteFile(path, []byte(test.contents), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotInventoryTree(t, data)
			_, err = service.NewProjectRegistryRemovalService().PlanUnregister(context.Background(), data, "missing")
			var application *service.Error
			if !errors.As(err, &application) || application.Kind != test.kind || !reflect.DeepEqual(before, snapshotInventoryTree(t, data)) {
				t.Fatalf("error=%v kind=%v", err, application)
			}
		})
	}
}

func projectPruneFixture(t *testing.T) (string, service.ProjectRegistryRemovalPlan) {
	t.Helper()
	data := t.TempDir()
	configPath := writeInventoryConfig(t, data, "keeper", "keeper")
	writeValidDefaultState(t, data, "keeper")
	writeValidDefaultState(t, data, "stale")
	writeInventoryRegistry(t, data, map[string]store.RegistryProject{
		"keeper": {Name: "keeper", ConfigPath: configPath, RepositoryIDs: map[string]string{"/git/keeper": "root", "/git/nested": "nested"}},
		"other":  {Name: "other", ConfigPath: filepath.Join(data, "other", ".wtree.yml"), RepositoryIDs: map[string]string{"/git/other": "root"}},
		"stale":  {Name: "stale", ConfigPath: configPath, RepositoryIDs: map[string]string{"/git/stale": "root"}},
	})
	// Project artifacts are intentionally populated and must survive pruning.
	artifact := filepath.Join(data, "projects", "stale", "data", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := service.NewProjectRegistryRemovalService().PlanPrune(context.Background(), data, "stale")
	if err != nil {
		t.Fatal(err)
	}
	return data, plan
}

type removalObservingLocker struct {
	calls     []string
	failAt    string
	onProject func()
}

func (l *removalObservingLocker) RegistryLock(context.Context, string, time.Duration) (lock.Handle, error) {
	l.calls = append(l.calls, "registry")
	if l.failAt == "registry" {
		return nil, errors.New("registry lock unavailable")
	}
	return removalNoopLock{}, nil
}

func (l *removalObservingLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	l.calls = append(l.calls, "project")
	if l.onProject != nil {
		l.onProject()
	}
	if l.failAt == "project" {
		return nil, errors.New("project lock unavailable")
	}
	return removalNoopLock{}, nil
}

type removalNoopLock struct{}

func (removalNoopLock) Unlock() error { return nil }
