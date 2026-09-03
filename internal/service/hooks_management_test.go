package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestHookListGroupsDefinitionsWithoutMutation(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "project.wtree.yml")
	configPath := filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "local", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.Hooks = config.HookEvents{"post-clone": {{ID: "clone", Command: []string{"setup"}}}}
	manifest.SharedHooks = config.HookEvents{"post-create": {{ID: "local", Command: []string{"setup"}}}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := NewHookManagementService().List(context.Background(), HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != HookManagementResultVersion || len(result.Groups) != 3 || result.Groups[1].Events[0].Comparison.State != "identical" {
		t.Fatalf("list result = %#v", result)
	}
}

func TestHookManagementUsesColocatedWorkingManifestNotProvenanceSource(t *testing.T) {
	root, data, external := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "candidate.wtree.yml")
	working, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(external)
	local.Manifest.Path = "project.wtree.yml"
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	workingManifest := hookManagementManifest()
	workingBytes, err := config.MarshalPortableManifest(workingManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(working, workingBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	externalManifest := hookManagementManifest()
	externalManifest.Project.Name = "External"
	externalBytes, err := config.MarshalPortableManifest(externalManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, externalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	request := HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}
	if _, err := NewHookManagementService().List(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result, err := NewHookManagementService().Share(context.Background(), HookShareRequest{HookManagementRequest: request, Event: "post-create"})
	if err != nil || !result.Changed {
		t.Fatalf("Share=%#v %v", result, err)
	}
	if after := mustReadHookManagement(t, external); string(after) != string(externalBytes) {
		t.Fatal("share mutated provenance source")
	}
}

func TestHookManagementRejectsStaleSameIdentityTopology(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Repositories["root"] = config.Repository{Source: "other", Parent: "", DefaultMount: ".", DefaultBranch: "main"}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeLocal, beforeManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifestPath)
	request := HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}
	for _, operation := range []string{"list", "share", "install"} {
		var err error
		switch operation {
		case "list":
			_, err = NewHookManagementService().List(context.Background(), request)
		case "share":
			_, err = NewHookManagementService().Share(context.Background(), HookShareRequest{HookManagementRequest: request, Event: "post-create"})
		case "install":
			_, err = NewHookManagementService().Install(context.Background(), HookInstallRequest{HookManagementRequest: request})
		}
		var serviceError *Error
		if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorConflict {
			t.Fatalf("%s stale topology=%v", operation, err)
		}
	}
	if string(beforeLocal) != string(mustReadHookManagement(t, configPath)) || string(beforeManifest) != string(mustReadHookManagement(t, manifestPath)) {
		t.Fatal("stale topology mutated bytes")
	}
}

func TestHookShareAndInstallMutateOnlyTheirTargetGeneration(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.Version = config.PortableManifestVersion
	manifestData, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewHookManagementService()
	request := HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}
	shared, err := service.Share(context.Background(), HookShareRequest{HookManagementRequest: request, Event: "post-create"})
	if err != nil || !shared.Changed || len(shared.Added) != 1 {
		t.Fatalf("Share() = %#v, %v", shared, err)
	}
	if current, err := config.LoadProject(mustReadHookManagement(t, configPath)); err != nil || len(current.Hooks) != 1 {
		t.Fatalf("share changed local config: %#v, %v", current, err)
	}
	sharedManifest, err := config.LoadPortableManifest(mustReadHookManagement(t, manifestPath))
	if err != nil || sharedManifest.Version != config.PortableManifestVersion3 || len(sharedManifest.SharedHooks) != 1 {
		t.Fatalf("shared manifest = %#v, %v", sharedManifest, err)
	}

	local = hookManagementLocal(manifestPath)
	local.Version = config.ProjectConfigVersion
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	installed, err := service.Install(context.Background(), HookInstallRequest{HookManagementRequest: request})
	if err != nil || !installed.Changed || len(installed.Added) != 1 {
		t.Fatalf("Install() = %#v, %v", installed, err)
	}
	installedLocal, err := config.LoadProject(mustReadHookManagement(t, configPath))
	if err != nil || installedLocal.Version != config.ProjectConfigVersion3 || len(installedLocal.Hooks) != 1 {
		t.Fatalf("installed local = %#v, %v", installedLocal, err)
	}
}

type hookTrackingFact bool

func (fact hookTrackingFact) WorkingFileTracked(context.Context, string, string) (bool, error) {
	return bool(fact), nil
}

func TestHookShareRejectsUntrackedRelativeExecutableWithoutMutation(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".wtree-hooks", "setup"), []byte("setup\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{".wtree-hooks/setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifestData, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustReadHookManagement(t, manifestPath)
	service := NewHookManagementService()
	service.tracked = hookTrackingFact(false)
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	if err == nil {
		t.Fatal("Share() accepted an untracked executable")
	}
	if after := mustReadHookManagement(t, manifestPath); string(after) != string(before) {
		t.Fatal("Share() mutated manifest on validation failure")
	}
}

func TestHookShareUsesLiteralTrackedNestedExecutableWithoutGitMutation(t *testing.T) {
	repository, data := testutil.NewGitRepository(t), t.TempDir()
	relative := ".wtree hooks/ü setup"
	if runtime.GOOS == "windows" {
		relative += ".exe"
	}
	repository.CommitFile(relative, "setup\n", "hook")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(repository.Path, filepath.FromSlash(relative)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifestPath, configPath := filepath.Join(repository.Path, "project.wtree.yml"), filepath.Join(repository.Path, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{relative}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeStatus, err := gitadapter.NewAdapter("git").Status(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewHookManagementService().Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, repository.Path), DataDir: data}, Event: "post-create"})
	if err != nil || !result.Changed {
		t.Fatalf("Share tracked nested = %#v, %v", result, err)
	}
	afterStatus, err := gitadapter.NewAdapter("git").Status(context.Background(), repository.Path)
	if err != nil || !reflect.DeepEqual(afterStatus, beforeStatus) {
		t.Fatalf("Share changed Git state before=%#v after=%#v err=%v", beforeStatus, afterStatus, err)
	}
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{strings.ReplaceAll(relative, "/", `\`)}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := NewHookManagementService().Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, repository.Path), DataDir: data}, Event: "post-create"}); err != nil || !result.Changed {
		t.Fatalf("Share backslash separator = %#v, %v", result, err)
	}
}

func TestHookShareRejectsNonExecutableTrackedRelativeExecutableWithoutMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX executable mode bits")
	}
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".wtree-hooks", "setup"), []byte("setup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{".wtree-hooks/setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifestData, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustReadHookManagement(t, manifestPath)
	service := NewHookManagementService()
	service.tracked = hookTrackingFact(true)
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	if err == nil {
		t.Fatal("Share() accepted a non-executable tracked file")
	}
	if after := mustReadHookManagement(t, manifestPath); string(after) != string(before) {
		t.Fatal("Share() mutated manifest for a non-executable file")
	}
}

func TestHookShareRejectsChangedLocalGenerationAfterAcquiringAuthority(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifestData, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeManifest := mustReadHookManagement(t, manifestPath)
	service := NewHookManagementService()
	service.locker = hookMutatingLocker{mutate: func() {
		if err := os.WriteFile(configPath, append(mustReadHookManagement(t, configPath), '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	var serviceError *Error
	if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorConflict {
		t.Fatalf("Share() after local generation mutation = %v, want conflict", err)
	}
	if after := mustReadHookManagement(t, manifestPath); string(after) != string(beforeManifest) {
		t.Fatal("Share() replaced manifest despite a changed local generation")
	}
}

func TestHookShareRevalidatesExecutableAfterAuthority(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, ".wtree-hooks", "setup")
	if err := os.WriteFile(executable, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{".wtree-hooks/setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustReadHookManagement(t, manifestPath)
	service := NewHookManagementService()
	service.tracked = hookTrackingFact(true)
	service.locker = hookMutatingLocker{mutate: func() { _ = os.Remove(executable) }}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	var serviceError *Error
	if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorValidation || string(before) != string(mustReadHookManagement(t, manifestPath)) {
		t.Fatalf("under-lock executable removal=%v", err)
	}
}

func TestHookShareRevalidatesExecutableAtBeforeRename(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, ".wtree-hooks", "setup")
	if err := os.WriteFile(executable, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{".wtree-hooks/setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustReadHookManagement(t, manifestPath)
	service := NewHookManagementService()
	service.tracked = hookTrackingFact(true)
	service.writeAtomic = func(_ string, _ []byte, _ os.FileMode, hook fsutil.AtomicStepHook) error {
		_ = os.Remove(executable)
		return hook("before-rename")
	}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	var serviceError *Error
	if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorValidation || string(before) != string(mustReadHookManagement(t, manifestPath)) {
		t.Fatalf("before rename executable removal=%v", err)
	}
}

func TestHookShareRevalidatesTrackingAtBeforeRename(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".wtree-hooks", "setup"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{".wtree-hooks/setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustReadHookManagement(t, manifestPath)
	tracked := &hookMutableTracking{tracked: true}
	service := NewHookManagementService()
	service.tracked = tracked
	service.writeAtomic = func(_ string, _ []byte, _ os.FileMode, hook fsutil.AtomicStepHook) error {
		tracked.tracked = false
		return hook("before-rename")
	}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	var serviceError *Error
	if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorValidation || string(before) != string(mustReadHookManagement(t, manifestPath)) {
		t.Fatalf("before rename untracked=%v", err)
	}
}

func TestHookSharePropagatesTrackerFailuresAtBeforeRename(t *testing.T) {
	for _, failure := range []error{context.Canceled, errors.New("git failed")} {
		t.Run(failure.Error(), func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			executable := writeHookManagementPortableExecutable(t, root)
			local := hookManagementLocal(manifestPath)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{executable}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, manifestPath)
			tracked := &hookMutableTracking{tracked: true}
			service := NewHookManagementService()
			service.tracked = tracked
			service.writeAtomic = func(_ string, _ []byte, _ os.FileMode, hook fsutil.AtomicStepHook) error {
				tracked.err = failure
				return hook("before-rename")
			}
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			var serviceError *Error
			if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorGit || string(before) != string(mustReadHookManagement(t, manifestPath)) {
				t.Fatalf("tracker failure=%v", err)
			}
		})
	}
}

func TestHookShareRevalidatesPhysicalFactsAtBeforeRename(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) error
	}{
		{"mode loss", func(path string) error { return os.Chmod(path, 0o600) }},
		{"directory", func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Mkdir(path, 0o700)
		}},
		{"symlink escape", func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			outside := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(path))), "outside")
			if err := os.WriteFile(outside, []byte("x"), 0o700); err != nil {
				return err
			}
			return os.Symlink(outside, path)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.name == "symlink escape" {
				t.Skip("symlink privilege is host-dependent")
			}
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(root, ".wtree-hooks", "setup")
			if err := os.WriteFile(executable, []byte("x"), 0o700); err != nil {
				t.Fatal(err)
			}
			local := hookManagementLocal(manifestPath)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{".wtree-hooks/setup"}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, manifestPath)
			service := NewHookManagementService()
			service.tracked = hookTrackingFact(true)
			service.writeAtomic = func(_ string, _ []byte, _ os.FileMode, hook fsutil.AtomicStepHook) error {
				if err := test.mutate(executable); err != nil {
					return err
				}
				return hook("before-rename")
			}
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			var serviceError *Error
			if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorValidation || string(before) != string(mustReadHookManagement(t, manifestPath)) {
				t.Fatalf("%s=%v", test.name, err)
			}
		})
	}
}

type hookMutableTracking struct {
	tracked bool
	err     error
}

func (fact *hookMutableTracking) WorkingFileTracked(context.Context, string, string) (bool, error) {
	return fact.tracked, fact.err
}

func TestHookInstallMissingReportsConflictsWithoutReplacingThem(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "local", Command: []string{"local"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.SharedHooks = config.HookEvents{
		"post-create": {{ID: "shared", Command: []string{"shared"}}},
	}
	manifestData, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewHookManagementService().Install(context.Background(), HookInstallRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Missing: true})
	if err != nil || result.Changed || len(result.Added) != 0 || strings.Join(result.Skipped, ",") != "post-create" || strings.Join(result.Conflicting, ",") != "post-create" {
		t.Fatalf("Install(--missing) = %#v, %v", result, err)
	}
	installed, err := config.LoadProject(mustReadHookManagement(t, configPath))
	if err != nil || installed.Hooks["post-create"][0].ID != "local" {
		t.Fatalf("Install(--missing) local generation = %#v, %v", installed, err)
	}
}

func TestHookShareAtomicWriteFailurePreservesPriorGeneration(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifestData, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o640); err != nil {
		t.Fatal(err)
	}
	before := mustReadHookManagement(t, manifestPath)
	service := NewHookManagementService()
	service.writeAtomic = func(string, []byte, os.FileMode, fsutil.AtomicStepHook) error {
		return errors.New("injected write failure")
	}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	var serviceError *Error
	if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorInternal {
		t.Fatalf("Share() write failure = %v, want internal", err)
	}
	if after := mustReadHookManagement(t, manifestPath); string(after) != string(before) {
		t.Fatal("Share() changed target after atomic writer failure")
	}
}

func TestHookShareReportsInstalledGenerationWhenDurabilityConfirmationFails(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewHookManagementService()
	service.writeAtomic = func(path string, data []byte, mode os.FileMode, hook fsutil.AtomicStepHook) error {
		return fsutil.WriteFileAtomicModeWithHook(path, data, mode, func(step string) error {
			if step == "dir-sync" {
				return errors.New("dir sync")
			}
			return hook(step)
		})
	}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	if err == nil || !strings.Contains(err.Error(), "installed but durability confirmation failed") {
		t.Fatalf("durability result=%v", err)
	}
	installed, decodeErr := config.LoadPortableManifest(mustReadHookManagement(t, manifestPath))
	if decodeErr != nil || installed.Version != config.PortableManifestVersion3 || len(installed.SharedHooks) != 1 {
		t.Fatalf("installed generation=%#v %v", installed, decodeErr)
	}
}

func TestHookShareRejectsMissingInstalledGenerationAfterReplacement(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewHookManagementService()
	service.writeAtomic = func(path string, data []byte, mode os.FileMode, hook fsutil.AtomicStepHook) error {
		return fsutil.WriteFileAtomicModeWithHook(path, data, mode, func(step string) error {
			if step == "dir-sync" {
				_ = os.Remove(path)
				return errors.New("dir sync")
			}
			return hook(step)
		})
	}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	if err == nil || !strings.Contains(err.Error(), "validate installed hook definition generation") {
		t.Fatalf("missing installed generation=%v", err)
	}
}

func TestHookShareRejectsCorruptInstalledGenerationAfterReplacement(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewHookManagementService()
	service.writeAtomic = func(path string, data []byte, mode os.FileMode, hook fsutil.AtomicStepHook) error {
		return fsutil.WriteFileAtomicModeWithHook(path, data, mode, func(step string) error {
			if step == "dir-sync" {
				_ = os.WriteFile(path, []byte("invalid"), mode)
				return errors.New("dir sync")
			}
			return hook(step)
		})
	}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	if err == nil || !strings.Contains(err.Error(), "validate installed hook definition generation") {
		t.Fatalf("corrupt installed generation=%v", err)
	}
}

func TestHookListIsDeterministicAndReadOnly(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "z", Command: []string{"z"}}, {ID: "a", Command: []string{"a"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.Hooks = config.HookEvents{"post-clone": {{ID: "clone", Command: []string{"clone"}}}}
	manifest.SharedHooks = config.HookEvents{"post-create": {{ID: "shared", Command: []string{"shared"}}}}
	encoded, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o640); err != nil {
		t.Fatal(err)
	}
	beforeLocal, beforeManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifestPath)
	localInfo, _ := os.Stat(configPath)
	manifestInfo, _ := os.Stat(manifestPath)
	locker := &hookCountingLocker{}
	service := NewHookManagementService()
	service.locker = locker
	result, err := service.List(context.Background(), HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{result.Groups[0].Source, result.Groups[1].Source, result.Groups[2].Source}; strings.Join(got, ",") != "portable,shared,local" || result.Groups[1].Events[0].Comparison.State != "conflicting" || result.Groups[2].Events[0].Comparison.State != "conflicting" {
		t.Fatalf("unexpected groups: %#v", result.Groups)
	}
	if got := result.Groups[2].Events[0].Hooks; got[0].ID != "z" || got[1].ID != "a" || got[0].Repository != "root" || got[0].Timeout != "1m0s" || got[0].ExecutionPolicy != "automatic-post-create-unless-bypassed" {
		t.Fatalf("unexpected local hook presentation: %#v", got)
	}
	if locker.calls.Load() != 0 || string(beforeLocal) != string(mustReadHookManagement(t, configPath)) || string(beforeManifest) != string(mustReadHookManagement(t, manifestPath)) {
		t.Fatal("list acquired mutation authority or changed bytes")
	}
	afterLocal, _ := os.Stat(configPath)
	afterManifest, _ := os.Stat(manifestPath)
	if localInfo.Mode() != afterLocal.Mode() || manifestInfo.Mode() != afterManifest.Mode() {
		t.Fatal("list changed a file mode")
	}
	jsonResult, err := json.Marshal(result)
	if err != nil || strings.Contains(string(jsonResult), root) || strings.Contains(string(jsonResult), "digest") || strings.Contains(string(jsonResult), "environment") {
		t.Fatalf("list JSON leaks observation data: %s, %v", jsonResult, err)
	}
}

func TestHookListComparisonStatesIncludeMissingInBothDirections(t *testing.T) {
	base := "root"
	for _, test := range []struct {
		name   string
		local  config.HookEvents
		shared config.HookEvents
		group  int
	}{
		{name: "shared missing local", shared: config.HookEvents{"post-create": {{ID: "shared", Command: []string{"shared"}}}}, group: 1},
		{name: "local missing shared", local: config.HookEvents{"post-create": {{ID: "local", Command: []string{"local"}}}}, group: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := hookManagementLocal("unused")
			local.Hooks = test.local
			manifest := hookManagementManifest()
			manifest.SharedHooks = test.shared
			result, err := buildHookList(local, manifest)
			if err != nil || result.Groups[test.group].Events[0].Comparison == nil || result.Groups[test.group].Events[0].Comparison.State != "missing" || result.Groups[test.group].Events[0].Comparison.Source == "" || base != "root" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestHookShareNoOpConflictAndForcePreserveExpectedGenerations(t *testing.T) {
	for _, test := range []struct {
		name        string
		shared      []config.Hook
		force       bool
		wantChanged bool
		wantKind    ErrorKind
	}{
		{name: "identical", shared: []config.Hook{{ID: "setup", Command: []string{"setup"}}}},
		{name: "conflict", shared: []config.Hook{{ID: "other", Command: []string{"other"}}}, wantKind: ErrorConflict},
		{name: "force", shared: []config.Hook{{ID: "other", Command: []string{"other"}}}, force: true, wantChanged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			local := hookManagementLocal(manifestPath)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			manifest := hookManagementManifest()
			manifest.Hooks = config.HookEvents{"post-clone": {{ID: "portable", Command: []string{"clone"}}}}
			manifest.SharedHooks = config.HookEvents{"post-create": test.shared}
			encoded, err := config.MarshalPortableManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, manifestPath)
			result, err := NewHookManagementService().Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create", Force: test.force})
			if test.wantKind != "" {
				var serviceError *Error
				if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != test.wantKind || err.Error() != `conflict: hooks share event "post-create" conflicts with the shared definition; rerun with --force` {
					t.Fatalf("Share() = %#v, %v", result, err)
				}
				if string(before) != string(mustReadHookManagement(t, manifestPath)) {
					t.Fatal("conflict changed bytes")
				}
				return
			}
			if err != nil || result.Changed != test.wantChanged {
				t.Fatalf("Share() = %#v, %v", result, err)
			}
			if !test.wantChanged && string(before) != string(mustReadHookManagement(t, manifestPath)) {
				t.Fatal("no-op changed bytes")
			}
			if test.wantChanged {
				current, loadErr := config.LoadPortableManifest(mustReadHookManagement(t, manifestPath))
				if loadErr != nil || current.Hooks["post-clone"][0].ID != "portable" {
					t.Fatalf("force did not preserve portable definition: %#v, %v", current, loadErr)
				}
			}
		})
	}
}

func TestHookAtomicOutcomesLeaveOldOrCompleteNewGeneration(t *testing.T) {
	for _, step := range []string{"create-temp", "write", "sync", "close", "before-rename", "replace", "dir-sync"} {
		t.Run(step, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			local := hookManagementLocal(manifestPath)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, manifestPath)
			beforeInfo, err := os.Stat(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			service := NewHookManagementService()
			service.writeAtomic = func(path string, data []byte, mode os.FileMode, hook fsutil.AtomicStepHook) error {
				if step == "before-rename" {
					if err := os.WriteFile(configPath, append(mustReadHookManagement(t, configPath), '\n'), 0o600); err != nil {
						return err
					}
					return hook("before-rename")
				}
				if step != "replace" && step != "dir-sync" {
					return errors.New(step)
				}
				if err := hook("before-rename"); err != nil {
					return err
				}
				if err := os.WriteFile(path, data, mode); err != nil {
					return err
				}
				return errors.New(step)
			}
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			if err == nil {
				t.Fatal("expected atomic outcome error")
			}
			after := mustReadHookManagement(t, manifestPath)
			afterInfo, statErr := os.Stat(manifestPath)
			if statErr != nil || afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
				t.Fatalf("atomic mode before=%#o after=%v err=%v", beforeInfo.Mode().Perm(), afterInfo, statErr)
			}
			if step == "replace" || step == "dir-sync" {
				if _, decodeErr := config.LoadPortableManifest(after); decodeErr != nil || string(after) == string(before) {
					t.Fatalf("post-replacement generation = %q, %v", after, decodeErr)
				}
			} else if string(after) != string(before) {
				t.Fatalf("pre-replacement %s changed bytes", step)
			}
		})
	}
}

func TestHookInstallNoOpConflictAndForce(t *testing.T) {
	for _, test := range []struct {
		name        string
		local       []config.Hook
		force       bool
		wantChanged bool
		wantKind    ErrorKind
	}{
		{name: "no shared", local: nil},
		{name: "identical", local: []config.Hook{{ID: "setup", Command: []string{"setup"}}}},
		{name: "conflict", local: []config.Hook{{ID: "local", Command: []string{"local"}}}, wantKind: ErrorConflict},
		{name: "force", local: []config.Hook{{ID: "local", Command: []string{"local"}}}, force: true, wantChanged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			local := hookManagementLocal(manifestPath)
			if test.local != nil {
				local.Hooks = config.HookEvents{"post-create": test.local}
			}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			manifest := hookManagementManifest()
			if test.name != "no shared" {
				manifest.SharedHooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
			}
			encoded, err := config.MarshalPortableManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, configPath)
			beforeInfo, err := os.Stat(configPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := NewHookManagementService().Install(context.Background(), HookInstallRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Force: test.force})
			if test.wantKind != "" {
				var serviceError *Error
				if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorConflict || err.Error() != "conflict: hooks install has conflicting events: post-create" {
					t.Fatalf("Install() = %#v, %v", result, err)
				}
				if string(before) != string(mustReadHookManagement(t, configPath)) {
					t.Fatal("conflict changed local bytes")
				}
				return
			}
			if err != nil || result.Changed != test.wantChanged {
				t.Fatalf("Install() = %#v, %v", result, err)
			}
			if !test.wantChanged && string(before) != string(mustReadHookManagement(t, configPath)) {
				t.Fatal("no-op changed local bytes")
			}
			afterInfo, statErr := os.Stat(configPath)
			if statErr != nil || afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
				t.Fatalf("local mode changed: before=%#o after=%v err=%v", beforeInfo.Mode().Perm(), afterInfo, statErr)
			}
		})
	}
}

func TestHookManagementCancelledBeforeObservationDoesNotMutate(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeLocal, beforeManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifestPath)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}
	if _, err := NewHookManagementService().List(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("List cancellation = %v", err)
	}
	if _, err := NewHookManagementService().Share(ctx, HookShareRequest{HookManagementRequest: request, Event: "post-create"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Share cancellation = %v", err)
	}
	if _, err := NewHookManagementService().Install(ctx, HookInstallRequest{HookManagementRequest: request}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Install cancellation = %v", err)
	}
	if string(beforeLocal) != string(mustReadHookManagement(t, configPath)) || string(beforeManifest) != string(mustReadHookManagement(t, manifestPath)) {
		t.Fatal("cancelled operation mutated bytes")
	}
}

func TestHookSharePortableSyntaxErrorsAreSafe(t *testing.T) {
	root := t.TempDir()
	local := hookManagementLocal(filepath.Join(root, "project.wtree.yml"))
	for _, executable := range []string{"/absolute", "~owner/setup", "file:///secret", "https://token:secret@example.test/x", "bad\x1b"} {
		t.Run(strings.ReplaceAll(executable, "/", "_"), func(t *testing.T) {
			err := NewHookManagementService().validateSharePortability(context.Background(), hookManagementProject(filepath.Join(root, ".wtree.yml"), root), local, hookManagementManifest(), "post-create", []config.Hook{{ID: "safe", Command: []string{executable}}})
			var serviceError *Error
			if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorValidation {
				t.Fatalf("validate %q = %v", executable, err)
			}
			if strings.Contains(err.Error(), executable) {
				t.Fatalf("unsafe literal leaked in %q", err)
			}
		})
	}
}

func TestHookShareMissingBoundariesFailBeforeMutation(t *testing.T) {
	for _, boundary := range []string{"writer", "locker", "tracker"} {
		t.Run(boundary, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			local := hookManagementLocal(manifestPath)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, manifestPath)
			service := NewHookManagementService()
			switch boundary {
			case "writer":
				service.writeAtomic = nil
			case "locker":
				service.locker = nil
			case "tracker":
				service.tracked = nil
			}
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			var serviceError *Error
			if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorInternal || string(before) != string(mustReadHookManagement(t, manifestPath)) {
				t.Fatalf("%s boundary err=%v", boundary, err)
			}
		})
	}
}

func TestHookShareTrackerCancellationAfterObservationDoesNotMutate(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	executable := writeHookManagementPortableExecutable(t, root)
	local := hookManagementLocal(manifestPath)
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{executable}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustReadHookManagement(t, manifestPath)
	service := NewHookManagementService()
	service.tracked = hookTrackerError{err: context.Canceled}
	_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
	var serviceError *Error
	if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorGit || string(before) != string(mustReadHookManagement(t, manifestPath)) {
		t.Fatalf("tracker cancellation = %v", err)
	}
}

type hookTrackerError struct{ err error }

func (tracker hookTrackerError) WorkingFileTracked(context.Context, string, string) (bool, error) {
	return false, tracker.err
}

func writeHookManagementPortableExecutable(t *testing.T, root string) string {
	t.Helper()
	name, command, data := "setup", ".wtree-hooks/setup", []byte("#!/bin/sh\n")
	if runtime.GOOS == "windows" {
		name, command, data = "setup.cmd", ".wtree-hooks/setup.cmd", []byte("@echo off\r\n")
	}
	if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".wtree-hooks", name), data, 0o700); err != nil {
		t.Fatal(err)
	}
	return command
}

func TestHookManagementContainsNoExecutionBoundary(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate hook management source")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "hooks_management.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"os/exec", "exec.Command", ".Run("} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("hook management contains execution boundary %q", forbidden)
		}
	}
}

func TestHookExecutableAvailabilityCrossPlatformRules(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "hook.exe")
	text := filepath.Join(directory, "hook.txt")
	if err := os.WriteFile(executable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(text, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	exeInfo, _ := os.Stat(executable)
	textInfo, _ := os.Stat(text)
	if !hookExecutableAvailableWithEnvironment(executable, exeInfo, true, []string{"PATHEXT=.EXE"}) || hookExecutableAvailableWithEnvironment(text, textInfo, true, []string{"PATHEXT=.EXE"}) {
		t.Fatal("platform executable availability mismatch")
	}
	if runtime.GOOS == "windows" {
		if !hookExecutableAvailable(executable, exeInfo) {
			t.Fatal("real Windows .exe availability mismatch")
		}
		return
	}
	if hookExecutableAvailableForPlatform(executable, exeInfo, false) || !hookExecutableAvailableForPlatform(text, textInfo, false) {
		t.Fatal("POSIX executable availability mismatch")
	}
}

func TestHookExecutablePathUsesWindowsPATHEXTRulesWithoutExecuting(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"setup.EXE", "script.Py", "plain.txt", "exact.weird", "tool.bat.exe"} {
		if err := os.WriteFile(filepath.Join(root, "hooks", name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, command string
		environment   []string
		want          string
	}{
		{name: "extensionless default", command: `hooks\setup`, want: "hooks/setup.EXE"},
		{name: "case insensitive extension", command: "hooks/SETUP.exe", want: "hooks/setup.EXE"},
		{name: "custom extension", command: "hooks/script", environment: []string{"PATHEXT=.PY"}, want: "hooks/script.Py"},
		{name: "explicit suffix outside path extension", command: "hooks/EXACT.WEIRD", environment: []string{"PATHEXT=.PY"}, want: "hooks/exact.weird"},
		{name: "explicit missing path extends", command: "hooks/TOOL.BAT", environment: []string{"PATHEXT=.EXE"}, want: "hooks/tool.bat.exe"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			relative, _, info, err := hookExecutablePath(root, test.command, true, test.environment)
			if err != nil || relative != test.want || !hookResolvedExecutableAvailable(info, true) {
				t.Fatalf("hookExecutablePath() = %q %#v %v", relative, info, err)
			}
		})
	}
	if _, _, _, err := hookExecutablePath(root, "hooks/plain", true, nil); err == nil {
		t.Fatal("Windows resolver accepted non-launchable extension")
	}
	if err := os.Mkdir(filepath.Join(root, "hooks", "directory.weird"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := hookExecutablePath(root, "hooks/directory.weird", true, []string{"PATHEXT=.PY"}); err == nil {
		t.Fatal("Windows resolver accepted directory")
	}
	info, err := os.Stat(filepath.Join(root, "hooks", "setup.EXE"))
	if err != nil || hookExecutableAvailableWithEnvironment("hooks/setup.EXE", info, false, nil) {
		t.Fatalf("POSIX availability = %v, %v", info, err)
	}
}

func TestHookManagementTopologyUsesLogicalRootResolver(t *testing.T) {
	logicalRoot := t.TempDir()
	child := filepath.Join(logicalRoot, "nested", "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath, configPath := filepath.Join(logicalRoot, "project.wtree.yml"), filepath.Join(logicalRoot, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Repositories["child"] = config.Repository{Source: "nested/child", Parent: "root", DefaultMount: "nested/child", DefaultBranch: "main"}
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	project := hookManagementProject(configPath, logicalRoot)
	project.Repositories = append(project.Repositories, domain.Repository{ID: "child", SourcePath: child, ParentID: "root", DefaultMount: "nested/child", DefaultBranch: "main"})
	request := HookManagementRequest{Project: project, DataDir: t.TempDir()}
	service := NewHookManagementService()
	if _, err := service.List(context.Background(), request); err != nil {
		t.Fatalf("List() = %v", err)
	}
	if result, err := service.Share(context.Background(), HookShareRequest{HookManagementRequest: request, Event: "post-create"}); err != nil || !result.Changed {
		t.Fatalf("Share() = %#v, %v", result, err)
	}
	local.Hooks = nil
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Install(context.Background(), HookInstallRequest{HookManagementRequest: request}); err != nil || !result.Changed {
		t.Fatalf("Install() = %#v, %v", result, err)
	}
}

func TestHookManagementSupportsNestedBaseResolverTopology(t *testing.T) {
	logicalRoot := t.TempDir()
	base, sibling := filepath.Join(logicalRoot, "base"), filepath.Join(logicalRoot, "base", "sibling")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, configPath := filepath.Join(base, "project.wtree.yml"), filepath.Join(base, ".wtree.yml")
	local := hookManagementLocal(manifest)
	local.LogicalRoot = ".."
	local.Repositories["root"] = config.Repository{Source: "base", DefaultMount: "base", DefaultBranch: "main"}
	local.Repositories["sibling"] = config.Repository{Source: "base/sibling", Parent: "root", DefaultMount: "sibling", DefaultBranch: "main"}
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.MarshalPortableManifest(hookManagementManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, encoded, 0o640); err != nil {
		t.Fatal(err)
	}
	project := hookManagementProject(configPath, base)
	project.LogicalRoot = logicalRoot
	project.Repositories[0].DefaultMount = "base"
	project.Repositories = append(project.Repositories, domain.Repository{ID: "sibling", SourcePath: sibling, ParentID: "root", DefaultMount: "sibling", DefaultBranch: "main"})
	request := HookManagementRequest{Project: project, DataDir: t.TempDir()}
	service := NewHookManagementService()
	if _, err := service.List(context.Background(), request); err != nil {
		t.Fatalf("List() = %v", err)
	}
	if result, err := service.Share(context.Background(), HookShareRequest{HookManagementRequest: request, Event: "post-create"}); err != nil || !result.Changed {
		t.Fatalf("Share() = %#v, %v", result, err)
	}
	local.Hooks = nil
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Install(context.Background(), HookInstallRequest{HookManagementRequest: request}); err != nil || !result.Changed {
		t.Fatalf("Install() = %#v, %v", result, err)
	}

	local.Repositories["sibling"] = config.Repository{Source: "base/sibling", DefaultMount: "sibling", DefaultBranch: "main"}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	beforeConfig, beforeManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifest)
	if _, err := service.List(context.Background(), request); err == nil || !strings.Contains(err.Error(), "topology") {
		t.Fatalf("stale parent List() = %v", err)
	}
	if string(beforeConfig) != string(mustReadHookManagement(t, configPath)) || string(beforeManifest) != string(mustReadHookManagement(t, manifest)) {
		t.Fatal("stale parent mutated generations")
	}
}

func TestHookManagementRejectsStaleLogicalRootAndRepositoryAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config.ProjectConfig, string)
	}{
		{name: "logical root", mutate: func(local *config.ProjectConfig, root string) {
			local.LogicalRoot = ".."
			local.Repositories["root"] = config.Repository{Source: filepath.Base(root), DefaultMount: ".", DefaultBranch: "main"}
		}},
		{name: "source", mutate: func(local *config.ProjectConfig, root string) {
			_ = os.MkdirAll(filepath.Join(root, "other"), 0o700)
			local.Repositories["root"] = config.Repository{Source: "other", DefaultMount: ".", DefaultBranch: "main"}
		}},
		{name: "repository set", mutate: func(local *config.ProjectConfig, root string) {
			_ = os.MkdirAll(filepath.Join(root, "sibling"), 0o700)
			local.Repositories["sibling"] = config.Repository{Source: "sibling", Parent: "root", DefaultMount: "sibling", DefaultBranch: "main"}
		}},
		{name: "mount", mutate: func(local *config.ProjectConfig, root string) {
			local.Repositories["root"] = config.Repository{Source: ".", DefaultMount: "root", DefaultBranch: "main"}
		}},
		{name: "branch", mutate: func(local *config.ProjectConfig, root string) {
			local.Repositories["root"] = config.Repository{Source: ".", DefaultMount: ".", DefaultBranch: "next"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifest, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			local := hookManagementLocal(manifest)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
			test.mutate(&local, root)
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			beforeConfig, beforeManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifest)
			request := HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}
			for _, operation := range []string{"list", "share", "install"} {
				var err error
				switch operation {
				case "list":
					_, err = NewHookManagementService().List(context.Background(), request)
				case "share":
					_, err = NewHookManagementService().Share(context.Background(), HookShareRequest{HookManagementRequest: request, Event: "post-create"})
				case "install":
					_, err = NewHookManagementService().Install(context.Background(), HookInstallRequest{HookManagementRequest: request})
				}
				var serviceError *Error
				if err == nil || !errors.As(err, &serviceError) || (serviceError.Kind != ErrorConflict && serviceError.Kind != ErrorValidation) {
					t.Fatalf("%s = %v", operation, err)
				}
			}
			if string(beforeConfig) != string(mustReadHookManagement(t, configPath)) || string(beforeManifest) != string(mustReadHookManagement(t, manifest)) {
				t.Fatal("stale authority mutated generations")
			}
		})
	}
}

func TestHookManagementGenerationIdentityAndModeChangesAbortMutations(t *testing.T) {
	for _, operation := range []struct {
		name   string
		setup  func(string, string) error
		run    func(*HookManagementService, HookManagementRequest) error
		target func(string, string) string
	}{
		{
			name: "share manifest", target: func(manifest, _ string) string { return manifest },
			setup: func(manifest, configPath string) error {
				local := hookManagementLocal(manifest)
				local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
				return config.WriteProjectFile(configPath, local)
			},
			run: func(service *HookManagementService, request HookManagementRequest) error {
				_, err := service.Share(context.Background(), HookShareRequest{HookManagementRequest: request, Event: "post-create"})
				return err
			},
		},
		{
			name: "install local", target: func(_ string, configPath string) string { return configPath },
			setup: func(manifest, configPath string) error {
				if err := config.WriteProjectFile(configPath, hookManagementLocal(manifest)); err != nil {
					return err
				}
				value := hookManagementManifest()
				value.SharedHooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
				data, err := config.MarshalPortableManifest(value)
				if err != nil {
					return err
				}
				return os.WriteFile(manifest, data, 0o640)
			},
			run: func(service *HookManagementService, request HookManagementRequest) error {
				_, err := service.Install(context.Background(), HookInstallRequest{HookManagementRequest: request})
				return err
			},
		},
	} {
		for _, phase := range []string{"under-lock", "before-rename"} {
			t.Run(operation.name+"/"+phase, func(t *testing.T) {
				root, data := t.TempDir(), t.TempDir()
				manifest, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
				if err := operation.setup(manifest, configPath); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(manifest); os.IsNotExist(err) {
					data, marshalErr := config.MarshalPortableManifest(hookManagementManifest())
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if err := os.WriteFile(manifest, data, 0o640); err != nil {
						t.Fatal(err)
					}
				}
				target := operation.target(manifest, configPath)
				before := mustReadHookManagement(t, target)
				beforeInfo, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				changedMode := hookGenerationChangedMode(beforeInfo.Mode().Perm())
				mutate := func() {
					if err := os.Chmod(target, changedMode); err != nil {
						t.Fatal(err)
					}
				}
				service := NewHookManagementService()
				if phase == "under-lock" {
					service.locker = hookMutatingLocker{mutate: mutate}
				} else {
					service.writeAtomic = func(_ string, _ []byte, _ os.FileMode, hook fsutil.AtomicStepHook) error {
						mutate()
						return hook("before-rename")
					}
				}
				err = operation.run(service, HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data})
				var serviceError *Error
				afterInfo, statErr := os.Stat(target)
				if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorConflict || statErr != nil || string(before) != string(mustReadHookManagement(t, target)) || afterInfo.Mode().Perm() != changedMode || beforeInfo.Mode().Perm() == afterInfo.Mode().Perm() {
					t.Fatalf("%s %s = %v; mode %#o", operation.name, phase, err, afterInfo.Mode().Perm())
				}
			})
		}
	}
}

func hookGenerationChangedMode(before os.FileMode) os.FileMode {
	if runtime.GOOS == "windows" {
		if before&0o200 != 0 {
			return 0o444
		}
		return 0o666
	}
	if before == 0o600 {
		return 0o640
	}
	return 0o600
}

func TestHookShareRejectsSameByteIdentityAndTypeSwapsUnderAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, target string, data []byte)
	}{
		{name: "same bytes replacement", mutate: func(t *testing.T, target string, data []byte) {
			t.Helper()
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, data, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory", mutate: func(t *testing.T, target string, _ []byte) {
			t.Helper()
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, target string, data []byte) {
			t.Helper()
			outside := target + ".outside"
			if err := os.WriteFile(outside, data, 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.name == "symlink" {
				t.Skip("symlink privilege is host-dependent")
			}
			root, data := t.TempDir(), t.TempDir()
			manifest, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			local := hookManagementLocal(manifest)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, manifest)
			service := NewHookManagementService()
			service.locker = hookMutatingLocker{mutate: func() { test.mutate(t, manifest, before) }}
			writerCalled := false
			service.writeAtomic = func(string, []byte, os.FileMode, fsutil.AtomicStepHook) error {
				writerCalled = true
				return errors.New("writer called")
			}
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			var serviceError *Error
			if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorConflict || writerCalled {
				t.Fatalf("Share() = %v writer=%t", err, writerCalled)
			}
			if test.name == "same bytes replacement" && string(before) != string(mustReadHookManagement(t, manifest)) {
				t.Fatal("identity swap content changed")
			}
		})
	}
}

func TestHookFileGenerationRetainsIdentityAcrossSameByteReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generation.yml")
	data := []byte("same bytes\n")
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	generation, err := captureHookFileGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	held := generation.file
	if held == nil {
		t.Fatal("capture did not retain the generation descriptor")
	}
	defer generation.close()
	before, err := held.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("replacement reused an identity still retained by the capture")
	}
	if err := generation.verify(); !errors.Is(err, errHookDefinitionGenerationChanged) {
		t.Fatalf("verify() = %v, want changed generation", err)
	}
	generation.close()
	if _, err := held.Stat(); err == nil {
		t.Fatal("generation descriptor remains open after close")
	}
}

func TestHookFileGenerationAllowsAtomicReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "generation.yml")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	generation, err := captureHookFileGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	if generation.file == nil {
		t.Fatal("capture did not retain the generation descriptor")
	}
	defer generation.close()
	if err := fsutil.WriteFileAtomicMode(path, []byte("new\n"), 0o640); err != nil {
		t.Fatalf("atomic replacement with captured generation = %v", err)
	}
	if err := generation.verify(); !errors.Is(err, errHookDefinitionGenerationChanged) {
		t.Fatalf("verify() after replacement = %v, want changed generation", err)
	}
}

func TestHookSharePublicRejectionsPreserveBothGenerations(t *testing.T) {
	for _, test := range []struct {
		name, command string
		makeSource    func(string) error
	}{
		{name: "missing", command: ".wtree-hooks/missing"},
		{name: "directory", command: "bin/dir", makeSource: func(root string) error { return os.MkdirAll(filepath.Join(root, "bin", "dir"), 0o700) }},
		{name: "symlink escape", command: ".wtree-hooks/out", makeSource: func(root string) error {
			if err := os.MkdirAll(filepath.Join(root, ".wtree-hooks"), 0o700); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, ".wtree-hooks", "out"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.name == "symlink escape" {
				t.Skip("symlink privilege is host-dependent")
			}
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			if test.makeSource != nil {
				if err := test.makeSource(root); err != nil {
					t.Fatal(err)
				}
			}
			local := hookManagementLocal(manifestPath)
			local.Hooks = config.HookEvents{"post-create": {{ID: "safe", Command: []string{test.command}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			beforeLocal, beforeManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifestPath)
			localInfo, _ := os.Stat(configPath)
			manifestInfo, _ := os.Stat(manifestPath)
			locker := &hookCountingLocker{}
			service := NewHookManagementService()
			service.locker = locker
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			var serviceError *Error
			if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorValidation || strings.Contains(err.Error(), test.command) {
				t.Fatalf("Share %s = %v", test.name, err)
			}
			afterLocal, afterManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifestPath)
			localAfter, _ := os.Stat(configPath)
			manifestAfter, _ := os.Stat(manifestPath)
			if locker.calls.Load() != 0 || string(beforeLocal) != string(afterLocal) || string(beforeManifest) != string(afterManifest) || localInfo.Mode() != localAfter.Mode() || manifestInfo.Mode() != manifestAfter.Mode() {
				t.Fatal("rejected share changed state")
			}
		})
	}
}

func TestHookShareRealAtomicPreReplacementFailuresLeaveNoTemporaryArtifacts(t *testing.T) {
	for _, step := range []string{"create-temp", "write", "sync", "close", "before-rename"} {
		t.Run(step, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			local := hookManagementLocal(manifestPath)
			local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
			if err := config.WriteProjectFile(configPath, local); err != nil {
				t.Fatal(err)
			}
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			before := mustReadHookManagement(t, manifestPath)
			beforeInfo, _ := os.Stat(manifestPath)
			beforeEntries, _ := os.ReadDir(root)
			service := NewHookManagementService()
			service.writeAtomic = func(path string, data []byte, mode os.FileMode, hook fsutil.AtomicStepHook) error {
				return fsutil.WriteFileAtomicModeWithHook(path, data, mode, func(got string) error {
					if got == step {
						return errors.New(step)
					}
					return hook(got)
				})
			}
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			if err == nil {
				t.Fatal("expected failure")
			}
			after := mustReadHookManagement(t, manifestPath)
			afterInfo, _ := os.Stat(manifestPath)
			afterEntries, _ := os.ReadDir(root)
			if string(before) != string(after) || beforeInfo.Mode() != afterInfo.Mode() || len(beforeEntries) != len(afterEntries) {
				t.Fatalf("atomic %s changed target or left temporary", step)
			}
		})
	}
}

func TestHookSharePublicUnsafeDeclarationsAreSafeAndReadOnly(t *testing.T) {
	for _, test := range []struct{ name, command, repository string }{
		{"absolute", "/private/secret", ""}, {"home", "~/secret", ""}, {"file", "file:///private/secret", ""}, {"userinfo", "https://token:secret@example.test/x", ""}, {"control", `"bad\nnext"`, ""}, {"unknown repository", "setup", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
			encoded, err := config.MarshalPortableManifest(hookManagementManifest())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			repository := ""
			if test.repository != "" {
				repository = "      repository: " + test.repository + "\n"
			}
			local := "version: 3\nproject:\n  id: hooks-project\n  name: Hooks\n  base_repository: root\nlogical_root: .\nrepositories:\n  root:\n    source: .\n    parent: \"\"\n    mount: .\n    default_branch: main\nworktrees: {}\nmanifest:\n  path: project.wtree.yml\n  source: " + manifestPath + "\nhooks:\n  post-create:\n    - id: safe\n" + repository + "      command: [" + test.command + "]\n"
			if err := os.WriteFile(configPath, []byte(local), 0o600); err != nil {
				t.Fatal(err)
			}
			beforeLocal, beforeManifest := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifestPath)
			localInfo, _ := os.Stat(configPath)
			manifestInfo, _ := os.Stat(manifestPath)
			locker := &hookCountingLocker{}
			service := NewHookManagementService()
			service.locker = locker
			_, err = service.Share(context.Background(), HookShareRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Event: "post-create"})
			var serviceError *Error
			if err == nil || !errors.As(err, &serviceError) || serviceError.Kind != ErrorValidation || strings.Contains(err.Error(), test.command) {
				t.Fatalf("Share %s = %v", test.name, err)
			}
			localAfter, manifestAfter := mustReadHookManagement(t, configPath), mustReadHookManagement(t, manifestPath)
			localMode, _ := os.Stat(configPath)
			manifestMode, _ := os.Stat(manifestPath)
			if locker.calls.Load() != 0 || string(beforeLocal) != string(localAfter) || string(beforeManifest) != string(manifestAfter) || localInfo.Mode() != localMode.Mode() || manifestInfo.Mode() != manifestMode.Mode() {
				t.Fatal("unsafe declaration mutated state")
			}
		})
	}
}

func TestHookInstallPreservesUnrelatedLocalAndManifestGenerations(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	manifestPath, configPath := filepath.Join(root, "project.wtree.yml"), filepath.Join(root, ".wtree.yml")
	local := hookManagementLocal(manifestPath)
	local.Worktrees.Root = filepath.Join(root, "worktrees")
	local.Discovery.Ignore = []string{"vendor", "generated"}
	local.Hooks = config.HookEvents{"post-create": {{ID: "local", Command: []string{"local"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifest := hookManagementManifest()
	manifest.Hooks = config.HookEvents{"post-clone": {{ID: "portable", Command: []string{"clone"}}}}
	manifest.SharedHooks = config.HookEvents{"post-create": {{ID: "shared", Command: []string{"shared"}}}}
	encoded, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o640); err != nil {
		t.Fatal(err)
	}
	beforeManifest := mustReadHookManagement(t, manifestPath)
	manifestInfo, _ := os.Stat(manifestPath)
	result, err := NewHookManagementService().Install(context.Background(), HookInstallRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}, Force: true})
	if err != nil || !result.Changed || strings.Join(result.Replaced, ",") != "post-create" {
		t.Fatalf("force install = %#v, %v", result, err)
	}
	installed, err := config.LoadProject(mustReadHookManagement(t, configPath))
	if err != nil || installed.Version != config.ProjectConfigVersion3 || installed.Worktrees.Root != local.Worktrees.Root || !reflect.DeepEqual(installed.Discovery.Ignore, local.Discovery.Ignore) || installed.Hooks["post-create"][0].ID != "shared" {
		t.Fatalf("installed preservation=%#v err=%v", installed, err)
	}
	afterManifest := mustReadHookManagement(t, manifestPath)
	manifestAfter, _ := os.Stat(manifestPath)
	if string(beforeManifest) != string(afterManifest) || manifestInfo.Mode() != manifestAfter.Mode() {
		t.Fatal("install changed non-target manifest")
	}
	before := mustReadHookManagement(t, configPath)
	beforeInfo, _ := os.Stat(configPath)
	noOp, err := NewHookManagementService().Install(context.Background(), HookInstallRequest{HookManagementRequest: HookManagementRequest{Project: hookManagementProject(configPath, root), DataDir: data}})
	after := mustReadHookManagement(t, configPath)
	afterInfo, _ := os.Stat(configPath)
	if err != nil || noOp.Changed || strings.Join(noOp.Unchanged, ",") != "post-create" || string(before) != string(after) || beforeInfo.Mode() != afterInfo.Mode() || installed.Version != config.ProjectConfigVersion3 {
		t.Fatalf("install no-op=%#v err=%v", noOp, err)
	}
}

type hookCountingLocker struct{ calls atomic.Int32 }

func (locker *hookCountingLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	locker.calls.Add(1)
	return hookNoopLock{}, nil
}

type hookMutatingLocker struct{ mutate func() }

func (locker hookMutatingLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	locker.mutate()
	return hookNoopLock{}, nil
}

type hookNoopLock struct{}

func (hookNoopLock) Unlock() error { return nil }

func mustReadHookManagement(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func hookManagementProject(configPath, root string) domain.Project {
	return domain.Project{Version: domain.CurrentVersion, ID: "hooks-project", Name: "Hooks", ConfigPath: configPath, LogicalRoot: root, BaseRepository: "root", Repositories: []domain.Repository{{ID: "root", SourcePath: root, DefaultMount: ".", DefaultBranch: "main"}}}
}

func hookManagementLocal(manifestPath string) config.ProjectConfig {
	return config.ProjectConfig{Version: config.ProjectConfigVersion3, Project: config.Project{ID: "hooks-project", Name: "Hooks", BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{"root": {Source: ".", Parent: "", DefaultMount: ".", DefaultBranch: "main"}}, Worktrees: config.Worktrees{}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: manifestPath}}
}

func hookManagementManifest() config.PortableManifest {
	return config.PortableManifest{Version: config.PortableManifestVersion3, Project: config.PortableProject{ID: "hooks-project", Name: "Hooks", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: "https://example.test/project.git"}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{"0123456789abcdef0123456789abcdef01234567"}}, Mount: ".", DefaultBranch: "main"}}}
}
