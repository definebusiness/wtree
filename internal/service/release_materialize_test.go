package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestReleaseMaterializeFetchesAdvertisedCommitAndPublishesDetachedChild(t *testing.T) {
	child := newCloneExecutionRemote(t, "local", "published", map[string]string{"child.txt": "exact\n"})
	base := testutil.NewGitRepository(t)
	base.CommitFile(".gitignore", "/.wtree.yml\n/backend/\n", "ignore local state and child")
	baseIdentity := cloneGitOutput(t, base.Path, "rev-parse", "HEAD")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "release-materialize", Name: "release materialize", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{
		"root":  {Clone: config.CloneSource{Remote: "root", URL: child.remote}, Upstream: config.Upstream{Branch: "main", Remote: "root", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{baseIdentity}}, Mount: ".", DefaultBranch: "main"},
		"child": {Clone: config.CloneSource{Remote: "child", URL: child.remote}, Upstream: config.Upstream{Branch: "main", Remote: "child", Merge: "refs/heads/published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{child.identity}}, Parent: "root", Mount: "backend", DefaultBranch: "main"},
	}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lockBytes, err := config.MarshalReleaseLock(config.ReleaseLock{Version: config.ReleaseLockVersion, Project: config.ReleaseLockProject{ID: manifest.Project.ID, ManifestSHA256: config.ReleaseManifestSHA256(manifestBytes)}, Release: config.ReleaseLockRelease{Name: "v1"}, Repositories: map[string]config.ReleaseLockRepository{"child": {Revision: child.identity}}})
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, base.Path, map[string]string{"project.wtree.yml": string(manifestBytes), ReleaseLockFilename: string(lockBytes)}, "release input")
	baseBefore := cloneGitOutput(t, base.Path, "rev-parse", "HEAD")
	data := filepath.Join(t.TempDir(), "data")
	result, err := NewReleaseMaterializeService().Materialize(context.Background(), ReleaseMaterializeRequest{LockPath: filepath.Join(base.Path, ReleaseLockFilename), DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	childResult := materializeRepository(result.Repositories, "child")
	baseResult := materializeRepository(result.Repositories, "root")
	if result.Status != "completed" || len(result.Repositories) != 2 || childResult.Expected != child.identity || childResult.Observed != child.identity || baseResult.Role != "caller-provided-base" || baseResult.Expected != baseBefore || baseResult.Observed != baseBefore {
		t.Fatalf("result = %#v", result)
	}
	adapter := gitadapter.NewAdapter("git")
	if head, err := adapter.Head(context.Background(), filepath.Join(base.Path, "backend")); err != nil || head != child.identity {
		t.Fatalf("child head = %q, %v", head, err)
	}
	if _, detached, err := adapter.CurrentBranch(context.Background(), filepath.Join(base.Path, "backend")); err != nil || !detached {
		t.Fatalf("child detached = %t, %v", detached, err)
	}
	if got := cloneGitOutput(t, base.Path, "rev-parse", "HEAD"); got != baseBefore {
		t.Fatalf("base changed from %s to %s", baseBefore, got)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(data, manifest.Project.ID, "default"))
	if err != nil || !state.Repositories["child"].Detached || state.Repositories["child"].Head != child.identity {
		t.Fatalf("state = %#v, %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(base.Path, ".wtree.yml")); err != nil {
		t.Fatal(err)
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil || len(registry.Projects[manifest.Project.ID].RepositoryIDs) != 2 {
		t.Fatalf("registry = %#v, %v", registry, err)
	}
	if resolved, resolveErr := NewResolver().ResolveReadOnly(context.Background(), ResolveRequest{Path: base.Path, DataDir: data}); resolveErr != nil || resolved.Project.ID != manifest.Project.ID || !releaseMaterializeDetached(resolved.Workspace.Checkouts, "child") {
		t.Fatalf("materialized workspace is not immediately resolvable: %#v, %v", resolved, resolveErr)
	}
	marker := filepath.Join(t.TempDir(), "exec-heads")
	moduleRoot, rootErr := filepath.Abs(filepath.Join("..", ".."))
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	binary := filepath.Join(t.TempDir(), "wtree")
	build := exec.Command("go", "build", "-o", binary, "./cmd/wtree")
	build.Dir = moduleRoot
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build wtree exec fixture: %v %s", buildErr, output)
	}
	command := exec.Command(binary, "exec", "--data-dir", data, "--", "/bin/sh", "-c", "git rev-parse HEAD >> '"+marker+"'")
	command.Dir = base.Path
	command.Env = os.Environ()
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		t.Fatalf("wtree exec: %v %s", commandErr, output)
	}
	execHeads, execErr := os.ReadFile(marker)
	if execErr != nil || !strings.Contains(string(execHeads), baseBefore) || !strings.Contains(string(execHeads), child.identity) {
		t.Fatalf("wtree exec heads=%q err=%v", execHeads, execErr)
	}
}

func releaseMaterializeDetached(checkouts []domain.Checkout, id string) bool {
	for _, checkout := range checkouts {
		if checkout.RepositoryID == id {
			return checkout.Detached
		}
	}
	return false
}

func TestReleaseMaterializeDryRunNeverContactsUnavailableRevision(t *testing.T) {
	child := newCloneExecutionRemote(t, "local", "published", map[string]string{"child.txt": "exact\n"})
	base := testutil.NewGitRepository(t)
	base.CommitFile(".gitignore", "/.wtree.yml\n/backend/\n", "ignore")
	baseIdentity := cloneGitOutput(t, base.Path, "rev-parse", "HEAD")
	missing := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "release-dry-run", Name: "release dry run", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{
		"root":  {Clone: config.CloneSource{Remote: "root", URL: child.remote}, Upstream: config.Upstream{Branch: "main", Remote: "root", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{baseIdentity}}, Mount: ".", DefaultBranch: "main"},
		"child": {Clone: config.CloneSource{Remote: "child", URL: child.remote}, Upstream: config.Upstream{Branch: "main", Remote: "child", Merge: "refs/heads/published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{child.identity}}, Parent: "root", Mount: "backend", DefaultBranch: "main"},
	}}
	manifestBytes, _ := config.MarshalPortableManifest(manifest)
	lockBytes, _ := config.MarshalReleaseLock(config.ReleaseLock{Version: 1, Project: config.ReleaseLockProject{ID: manifest.Project.ID, ManifestSHA256: config.ReleaseManifestSHA256(manifestBytes)}, Release: config.ReleaseLockRelease{Name: "v1"}, Repositories: map[string]config.ReleaseLockRepository{"child": {Revision: missing}}})
	writeAndCommitCloneFiles(t, base.Path, map[string]string{"project.wtree.yml": string(manifestBytes), ReleaseLockFilename: string(lockBytes)}, "release input")
	result, err := NewReleaseMaterializeService().Materialize(context.Background(), ReleaseMaterializeRequest{LockPath: filepath.Join(base.Path, ReleaseLockFilename), DataDir: filepath.Join(t.TempDir(), "data"), DryRun: true})
	if err != nil || result.Status != "planned" {
		t.Fatalf("dry run = %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(base.Path, "backend")); !os.IsNotExist(err) {
		t.Fatalf("dry run created child: %v", err)
	}
}

func TestReleaseMaterializeBaseOnlyRegistersCallerCheckout(t *testing.T) {
	base := testutil.NewGitRepository(t)
	base.CommitFile(".gitignore", "/.wtree.yml\n", "ignore")
	identity := cloneGitOutput(t, base.Path, "rev-parse", "HEAD")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "release-base-only", Name: "release base only", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "root", URL: testutil.NewBareGitRemote(t)}, Upstream: config.Upstream{Branch: "main", Remote: "root", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{identity}}, Mount: ".", DefaultBranch: "main"}}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lockBytes, err := config.MarshalReleaseLock(config.ReleaseLock{Version: 1, Project: config.ReleaseLockProject{ID: manifest.Project.ID, ManifestSHA256: config.ReleaseManifestSHA256(manifestBytes)}, Release: config.ReleaseLockRelease{Name: "v1"}, Repositories: map[string]config.ReleaseLockRepository{}})
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, base.Path, map[string]string{"project.wtree.yml": string(manifestBytes), ReleaseLockFilename: string(lockBytes)}, "release input")
	base.Run(t, "checkout", "--detach")
	data := filepath.Join(t.TempDir(), "data")
	result, err := NewReleaseMaterializeService().Materialize(context.Background(), ReleaseMaterializeRequest{LockPath: filepath.Join(base.Path, ReleaseLockFilename), DataDir: data})
	if err != nil || result.Status != "completed" || len(result.Repositories) != 1 || result.Repositories[0].Role != "caller-provided-base" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(data, manifest.Project.ID, "default"))
	if err != nil || !state.Repositories["root"].Detached || state.Repositories["root"].Head != cloneGitOutput(t, base.Path, "rev-parse", "HEAD") {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestReleaseMaterializeStagesNestedAndSiblingBeforePublication(t *testing.T) {
	api := newCloneExecutionRemote(t, "api", "published", map[string]string{".gitignore": "/nested/\n", "api.txt": "api\n"})
	nested := newCloneExecutionRemote(t, "nested", "published", map[string]string{"nested.txt": "nested\n"})
	web := newCloneExecutionRemote(t, "web", "published", map[string]string{"web.txt": "web\n"})
	base := testutil.NewGitRepository(t)
	base.CommitFile(".gitignore", "/.wtree.yml\n/api/\n/web/\n", "ignore")
	baseIdentity := cloneGitOutput(t, base.Path, "rev-parse", "HEAD")
	hookCanary := filepath.Join(base.Path, "post-materialize-ran")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion3, Project: config.PortableProject{ID: "release-forest", Name: "release forest", BaseRepository: "root"}, Hooks: config.HookEvents{config.HookEventPostClone: {{ID: "must-not-run", Repository: "root", Command: []string{"sh", "-c", "touch " + hookCanary}}}}, Repositories: map[string]config.PortableRepository{
		"root":   {Clone: config.CloneSource{Remote: "root", URL: api.remote}, Upstream: config.Upstream{Branch: "main", Remote: "root", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{baseIdentity}}, Mount: ".", DefaultBranch: "main"},
		"api":    {Clone: config.CloneSource{Remote: "api", URL: api.remote}, Upstream: config.Upstream{Branch: "main", Remote: "api", Merge: "refs/heads/published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{api.identity}}, Parent: "root", Mount: "api", DefaultBranch: "main"},
		"nested": {Clone: config.CloneSource{Remote: "nested", URL: nested.remote}, Upstream: config.Upstream{Branch: "main", Remote: "nested", Merge: "refs/heads/published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{nested.identity}}, Parent: "api", Mount: "nested", DefaultBranch: "main"},
		"web":    {Clone: config.CloneSource{Remote: "web", URL: web.remote}, Upstream: config.Upstream{Branch: "main", Remote: "web", Merge: "refs/heads/published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{web.identity}}, Parent: "root", Mount: "web", DefaultBranch: "main"},
	}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lockBytes, err := config.MarshalReleaseLock(config.ReleaseLock{Version: 1, Project: config.ReleaseLockProject{ID: manifest.Project.ID, ManifestSHA256: config.ReleaseManifestSHA256(manifestBytes)}, Release: config.ReleaseLockRelease{Name: "v1"}, Repositories: map[string]config.ReleaseLockRepository{"api": {Revision: api.identity}, "nested": {Revision: nested.identity}, "web": {Revision: web.identity}}})
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, base.Path, map[string]string{"project.wtree.yml": string(manifestBytes), ReleaseLockFilename: string(lockBytes)}, "release input")
	result, err := NewReleaseMaterializeService().Materialize(context.Background(), ReleaseMaterializeRequest{LockPath: filepath.Join(base.Path, ReleaseLockFilename), DataDir: filepath.Join(t.TempDir(), "data")})
	if err != nil || result.Status != "completed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(hookCanary); !os.IsNotExist(err) {
		t.Fatalf("materialization ran portable lifecycle hook: %v", err)
	}
	for path, want := range map[string]string{filepath.Join(base.Path, "api"): api.identity, filepath.Join(base.Path, "api", "nested"): nested.identity, filepath.Join(base.Path, "web"): web.identity} {
		head, headErr := gitadapter.NewAdapter("git").Head(context.Background(), path)
		if headErr != nil || head != want {
			t.Fatalf("%s head=%s want=%s err=%v", path, head, want, headErr)
		}
		if _, detached, branchErr := gitadapter.NewAdapter("git").CurrentBranch(context.Background(), path); branchErr != nil || !detached {
			t.Fatalf("%s detached=%t err=%v", path, detached, branchErr)
		}
	}
}

func TestReleaseMaterializeCanceledBeforeObservationCreatesNoState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := filepath.Join(t.TempDir(), "data")
	_, err := NewReleaseMaterializeService().Materialize(ctx, ReleaseMaterializeRequest{LockPath: filepath.Join(t.TempDir(), ReleaseLockFilename), DataDir: data})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled materialize error = %v", err)
	}
	if _, statErr := os.Stat(data); !os.IsNotExist(statErr) {
		t.Fatalf("cancellation created data state: %v", statErr)
	}
}

func TestReleaseMaterializeFinalBoundaryCancellationAndDestinationReplacementDoNotPublish(t *testing.T) {
	base, _, request := releaseMaterializeChildFixture(t, "release-boundary")
	service := NewReleaseMaterializeService()
	service.beforePublish = func() error { return context.Canceled }
	if _, err := service.Materialize(context.Background(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("before-publication cancellation = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base.Path, "backend")); !os.IsNotExist(err) {
		t.Fatalf("cancellation published child: %v", err)
	}
	service = NewReleaseMaterializeService()
	service.beforePublish = func() error { return os.Mkdir(filepath.Join(base.Path, "backend"), 0o700) }
	if _, err := service.Materialize(context.Background(), request); err == nil {
		t.Fatal("final destination replacement error = nil")
	}
	if info, err := os.Lstat(filepath.Join(base.Path, "backend")); err != nil || !info.IsDir() {
		t.Fatalf("intervening destination was not retained: %v", err)
	}
}

func TestReleaseMaterializeRevalidatesBaseAuthorityAfterStaging(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(testutil.GitRepository)
	}{
		{"head", func(base testutil.GitRepository) { base.CommitFile("moved", "moved\n", "move base") }},
		{"manifest", func(base testutil.GitRepository) {
			if err := os.WriteFile(filepath.Join(base.Path, "project.wtree.yml"), []byte("version: 2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"lock", func(base testutil.GitRepository) {
			if err := os.WriteFile(filepath.Join(base.Path, ReleaseLockFilename), []byte("version: 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, _, request := releaseMaterializeChildFixture(t, "release-authority-"+test.name)
			service := NewReleaseMaterializeService()
			service.beforePublish = func() error { test.mutate(base); return nil }
			if _, err := service.Materialize(context.Background(), request); err == nil {
				t.Fatal("authority drift error = nil")
			}
			if _, err := os.Lstat(filepath.Join(base.Path, "backend")); !os.IsNotExist(err) {
				t.Fatalf("authority drift published child: %v", err)
			}
			if _, err := os.Lstat(WorkspaceStatePath(request.DataDir, "release-authority-"+test.name, "default")); !os.IsNotExist(err) {
				t.Fatalf("authority drift published state: %v", err)
			}
		})
	}
}

func TestReleaseMaterializeStopsMetadataAfterChildMakesBaseDirty(t *testing.T) {
	base, _, request := releaseMaterializeChildFixture(t, "release-base-dirty-after-child")
	base.CommitFile("tracked.txt", "before\n", "tracked control")
	service := NewReleaseMaterializeService()
	service.afterPublish = func(string) error {
		return os.WriteFile(filepath.Join(base.Path, "tracked.txt"), []byte("foreign mutation\n"), 0o600)
	}
	if _, err := service.Materialize(context.Background(), request); err == nil || !hasCloneErrorKind(err, ErrorConflict) {
		t.Fatalf("dirty-base publication error = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(base.Path, "tracked.txt")); err != nil || string(data) != "foreign mutation\n" {
		t.Fatalf("tracked mutation = %q, %v", data, err)
	}
	for _, path := range []string{filepath.Join(base.Path, ".wtree.yml"), WorkspaceStatePath(request.DataDir, "release-base-dirty-after-child", "default")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dirty base published %s: %v", path, err)
		}
	}
}

func TestReleaseMaterializeRejectsWrongCallerBaseIdentityAndMountBeforeStaging(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config.PortableManifest)
	}{
		{name: "identity", mutate: func(manifest *config.PortableManifest) {
			manifest.Repositories["root"] = config.PortableRepository{Clone: manifest.Repositories["root"].Clone, Upstream: manifest.Repositories["root"].Upstream, Identity: config.RepositoryIdentity{InitialCommits: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, Mount: ".", DefaultBranch: manifest.Repositories["root"].DefaultBranch}
		}},
		{name: "mount", mutate: func(manifest *config.PortableManifest) {
			root := manifest.Repositories["root"]
			root.Mount = "caller-base"
			manifest.Repositories["root"] = root
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, child, request := releaseMaterializeChildFixture(t, "release-wrong-base-"+test.name)
			bytes, err := os.ReadFile(filepath.Join(base.Path, "project.wtree.yml"))
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := config.LoadPortableManifest(bytes)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&manifest)
			manifestBytes, err := config.MarshalPortableManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			lockBytes, err := config.MarshalReleaseLock(config.ReleaseLock{Version: config.ReleaseLockVersion, Project: config.ReleaseLockProject{ID: manifest.Project.ID, ManifestSHA256: config.ReleaseManifestSHA256(manifestBytes)}, Release: config.ReleaseLockRelease{Name: "v1"}, Repositories: map[string]config.ReleaseLockRepository{"child": {Revision: child.identity}}})
			if err != nil {
				t.Fatal(err)
			}
			writeAndCommitCloneFiles(t, base.Path, map[string]string{"project.wtree.yml": string(manifestBytes), ReleaseLockFilename: string(lockBytes)}, "wrong caller authority")
			if _, err := NewReleaseMaterializeService().Materialize(context.Background(), request); err == nil || !hasCloneErrorKind(err, ErrorConflict) {
				t.Fatalf("wrong caller %s error = %v", test.name, err)
			}
			if _, err := os.Lstat(filepath.Join(base.Path, "backend")); !os.IsNotExist(err) {
				t.Fatalf("wrong caller %s reached staging/publication: %v", test.name, err)
			}
		})
	}
}

func TestReleaseMaterializeOwnedPublicationRollsBackAndRecordsRecoveryOnFailure(t *testing.T) {
	base, _, request := releaseMaterializeChildFixture(t, "release-rollback")
	service := NewReleaseMaterializeService()
	service.afterPublish = func(string) error { return errors.New("injected publication failure") }
	if _, err := service.Materialize(context.Background(), request); err == nil {
		t.Fatal("publication failure error = nil")
	}
	if _, err := os.Lstat(filepath.Join(base.Path, "backend")); !os.IsNotExist(err) {
		t.Fatalf("owned mount was not rolled back: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("config published after failed mount: %v", err)
	}
	base, _, request = releaseMaterializeChildFixture(t, "release-rollback-recovery")
	service = NewReleaseMaterializeService()
	service.afterPublish = func(string) error { return errors.New("injected publication failure") }
	service.removeAll = func(string) error { return errors.New("injected rollback failure") }
	_, materializeErr := service.Materialize(context.Background(), request)
	if materializeErr == nil {
		t.Fatal("rollback failure error = nil")
	}
	if _, err := store.ReadRecovery(filepath.Join(request.DataDir, "projects", "release-rollback-recovery", "recovery", "default.json")); err != nil {
		t.Fatalf("recovery record = %v; materialize = %v", err, materializeErr)
	}
}

func TestReleaseMaterializeChildQuarantinePreservesFinalBoundaryReplacement(t *testing.T) {
	base, _, request := releaseMaterializeChildFixture(t, "release-child-quarantine")
	service := NewReleaseMaterializeService()
	service.afterPublish = func(string) error { return errors.New("trigger rollback") }
	service.beforeChildQuarantine = func(path string) error {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "foreign"), []byte("foreign\n"), 0o600)
	}
	if _, err := service.Materialize(context.Background(), request); err == nil {
		t.Fatal("rollback replacement error = nil")
	}
	data, err := os.ReadFile(filepath.Join(base.Path, "backend", "foreign"))
	if err != nil || string(data) != "foreign\n" {
		t.Fatalf("foreign child was not preserved: %q %v", data, err)
	}
	if _, err := store.ReadRecovery(filepath.Join(request.DataDir, "projects", "release-child-quarantine", "recovery", "default.json")); err != nil {
		t.Fatalf("recovery = %v", err)
	}
}

func TestReleaseMaterializeFileRemovalPreservesFinalBoundaryReplacement(t *testing.T) {
	base, _, request := releaseMaterializeChildFixture(t, "release-file-quarantine")
	service := NewReleaseMaterializeService()
	service.writeCAS = func(original cloneFileSnapshot, data []byte, compare func() error) (ClonePublicationReceipt, error) {
		receipt, err := defaultMaterializeCAS(original, data, compare, nil)
		if original.path == filepath.Join(base.Path, ".wtree.yml") && err == nil {
			return receipt, errors.New("post-replacement config failure")
		}
		return receipt, err
	}
	service.beforeFileRemoval = func(path string) error {
		if path != filepath.Join(base.Path, ".wtree.yml") {
			return nil
		}
		return os.WriteFile(path, []byte("foreign\n"), 0o600)
	}
	if _, err := service.Materialize(context.Background(), request); err == nil {
		t.Fatal("file removal replacement error = nil")
	}
	data, err := os.ReadFile(filepath.Join(base.Path, ".wtree.yml"))
	if err != nil || string(data) != "foreign\n" {
		t.Fatalf("foreign file was not preserved: %q %v", data, err)
	}
	if _, err := store.ReadRecovery(filepath.Join(request.DataDir, "projects", "release-file-quarantine", "recovery", "default.json")); err != nil {
		t.Fatalf("recovery = %v", err)
	}
}

func TestReleaseMaterializeGroupingQuarantinePreservesFinalBoundaryReplacement(t *testing.T) {
	base, _, request := releaseMaterializeChildFixture(t, "release-group-quarantine")
	base.CommitFile(".gitignore", "/.wtree.yml\n/groups/\n", "ignore grouped child")
	manifestBytes, err := os.ReadFile(filepath.Join(base.Path, "project.wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	child := manifest.Repositories["child"]
	child.Mount = "groups/backend"
	manifest.Repositories["child"] = child
	manifestBytes, err = config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lockBytes, err := os.ReadFile(filepath.Join(base.Path, ReleaseLockFilename))
	if err != nil {
		t.Fatal(err)
	}
	locked, err := config.LoadReleaseLock(lockBytes)
	if err != nil {
		t.Fatal(err)
	}
	locked.Project.ManifestSHA256 = config.ReleaseManifestSHA256(manifestBytes)
	lockBytes, err = config.MarshalReleaseLock(locked)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, base.Path, map[string]string{"project.wtree.yml": string(manifestBytes), ReleaseLockFilename: string(lockBytes)}, "group child")
	service := NewReleaseMaterializeService()
	service.afterPublish = func(string) error { return errors.New("trigger grouping rollback") }
	service.beforeGroupingQuarantine = func(path string) error {
		if filepath.Base(path) != "groups" {
			return nil
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "foreign"), []byte("foreign\n"), 0o600)
	}
	if _, err := service.Materialize(context.Background(), request); err == nil {
		t.Fatal("grouping replacement error = nil")
	}
	data, err := os.ReadFile(filepath.Join(base.Path, "groups", "foreign"))
	if err != nil || string(data) != "foreign\n" {
		t.Fatalf("foreign grouping was not preserved: %q %v", data, err)
	}
	if _, err := store.ReadRecovery(filepath.Join(request.DataDir, "projects", "release-group-quarantine", "recovery", "default.json")); err != nil {
		t.Fatalf("recovery = %v", err)
	}
}

func TestReleaseMaterializeStagingQuarantinePreservesFinalBoundaryReplacement(t *testing.T) {
	parent := t.TempDir()
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	staging, owned, retainedParent, lease, err := createCloneStaging(parent, ".wtree-release-", parentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "owned"), []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = releaseMaterializeCleanupStaging(staging, owned, retainedParent, lease, func(path string) error {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "foreign"), []byte("foreign\n"), 0o600)
	}, nil)
	if err == nil {
		t.Fatal("staging replacement cleanup error = nil")
	}
	data, readErr := os.ReadFile(filepath.Join(staging, "foreign"))
	if readErr != nil || string(data) != "foreign\n" {
		t.Fatalf("foreign staging was not preserved: %q %v", data, readErr)
	}
}

type releaseMaterializeObservedLease struct {
	cloneStagingLease
	closeCalls *int
	closeErr   error
}

func (lease releaseMaterializeObservedLease) closeAll() error {
	*lease.closeCalls++
	return errors.Join(lease.cloneStagingLease.closeAll(), lease.closeErr)
}

func TestReleaseMaterializePropagatesStagingCleanupAndRecordsRecovery(t *testing.T) {
	for _, test := range []struct {
		name           string
		operationErr   error
		substitute     bool
		closeErr       error
		quarantineErr  error
		wantUnreverted string
	}{
		{name: "after-successful-publication", substitute: true, wantUnreverted: "private-staging"},
		{name: "joins-earlier-operation-error", operationErr: errors.New("injected publication boundary failure"), substitute: true, wantUnreverted: "private-staging"},
		{name: "close-only-does-not-claim-retained-tree", closeErr: errors.New("injected lease close failure"), wantUnreverted: "staging-authority"},
		{name: "quarantine-only-does-not-claim-retained-tree", quarantineErr: errors.New("injected quarantine removal failure"), wantUnreverted: "staging-quarantine"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, request := releaseMaterializeChildFixture(t, "release-staging-propagation-"+test.name)
			service := NewReleaseMaterializeService()
			closeCalls := 0
			service.wrapStagingLease = func(lease cloneStagingLease) cloneStagingLease {
				return releaseMaterializeObservedLease{cloneStagingLease: lease, closeCalls: &closeCalls, closeErr: test.closeErr}
			}
			if test.quarantineErr != nil {
				service.removeStagingQuarantine = func(string) error { return test.quarantineErr }
			}
			if test.operationErr != nil {
				service.beforePublish = func() error { return test.operationErr }
			}

			const secretCanary = "materialize-staging-secret-canary"
			var retainedStaging string
			service.beforeStagingQuarantine = func(path string) error {
				retainedStaging = path
				if !test.substitute {
					return nil
				}
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(path, "foreign"), []byte(secretCanary+"\n"), 0o600)
			}

			result, err := service.Materialize(context.Background(), request)
			if err == nil || !hasCloneErrorKind(err, ErrorRollbackIncomplete) {
				t.Fatalf("Materialize error = %v, want rollback-incomplete", err)
			}
			if result.Status == "completed" {
				t.Fatalf("Materialize status = %q after cleanup failure, want non-success", result.Status)
			}
			if test.operationErr != nil && !errors.Is(err, test.operationErr) {
				t.Fatalf("Materialize error = %v, want original %v", err, test.operationErr)
			}
			if closeCalls != 1 {
				t.Fatalf("staging lease close calls = %d, want 1", closeCalls)
			}
			if test.substitute {
				foreign, readErr := os.ReadFile(filepath.Join(retainedStaging, "foreign"))
				if readErr != nil || string(foreign) != secretCanary+"\n" {
					t.Fatalf("foreign staging was not preserved: %q %v", foreign, readErr)
				}
			}

			recoveryPath := filepath.Join(request.DataDir, "projects", "release-staging-propagation-"+test.name, "recovery", "default.json")
			recovery, recoveryErr := store.ReadRecovery(recoveryPath)
			if recoveryErr != nil {
				t.Fatalf("recovery = %v; Materialize = %v", recoveryErr, err)
			}
			if recovery.FailedStep != "staging-cleanup" || len(recovery.UnrevertedSteps) != 1 || recovery.UnrevertedSteps[0] != test.wantUnreverted || len(recovery.RollbackFailures) != 1 || recovery.RollbackFailures[0].Error == "" {
				t.Fatalf("recovery is not actionable for retained staging: %+v", recovery)
			}
			if test.wantUnreverted == "staging-quarantine" {
				if !strings.Contains(recovery.RollbackFailures[0].Error, "retained staging quarantine") {
					t.Fatalf("recovery does not identify retained quarantine: %+v", recovery)
				}
			} else if !strings.Contains(recovery.RollbackFailures[0].Error, retainedStaging) {
				t.Fatalf("recovery does not identify staging location: %+v", recovery)
			}
			if !test.substitute && strings.Contains(strings.Join(recovery.UnrevertedSteps, ","), "private-staging") {
				t.Fatalf("recovery falsely claims retained private staging: %+v", recovery)
			}
			recoveryBytes, readRecoveryErr := os.ReadFile(recoveryPath)
			if readRecoveryErr != nil || strings.Contains(string(recoveryBytes), secretCanary) {
				t.Fatalf("recovery persisted secret canary: %q %v", recoveryBytes, readRecoveryErr)
			}
		})
	}
}

func TestReleaseMaterializeStagingCleanupMakesRollbackIncompleteAuthoritative(t *testing.T) {
	_, _, request := releaseMaterializeChildFixture(t, "release-staging-authoritative")
	sentinel := errors.New("classified publication conflict")
	original := NewError(ErrorConflict, sentinel)
	service := NewReleaseMaterializeService()
	closeCalls := 0
	service.wrapStagingLease = func(lease cloneStagingLease) cloneStagingLease {
		return releaseMaterializeObservedLease{cloneStagingLease: lease, closeCalls: &closeCalls}
	}
	service.beforePublish = func() error { return original }
	var staging string
	service.beforeStagingQuarantine = func(path string) error {
		staging = path
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "foreign"), []byte("foreign\n"), 0o600)
	}

	result, err := service.Materialize(context.Background(), request)
	var outer *Error
	if !errors.As(err, &outer) || outer.Kind != ErrorRollbackIncomplete {
		t.Fatalf("outer Materialize error = %#v, want rollback-incomplete", outer)
	}
	if !hasCloneErrorKind(err, ErrorConflict) || !errors.Is(err, sentinel) {
		t.Fatalf("Materialize error lost classified original cause: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("Materialize status = %q, want failed", result.Status)
	}
	if closeCalls != 1 {
		t.Fatalf("staging lease close calls = %d, want 1", closeCalls)
	}
	data, readErr := os.ReadFile(filepath.Join(staging, "foreign"))
	if readErr != nil || string(data) != "foreign\n" {
		t.Fatalf("foreign staging was not preserved: %q %v", data, readErr)
	}
	if _, recoveryErr := store.ReadRecovery(filepath.Join(request.DataDir, "projects", "release-staging-authoritative", "recovery", "default.json")); recoveryErr != nil {
		t.Fatalf("recovery = %v; Materialize = %v", recoveryErr, err)
	}
}

func TestReleaseMaterializeCanceledStagingCleanupStillRecordsBoundedRecovery(t *testing.T) {
	base, _, request := releaseMaterializeChildFixture(t, "release-staging-canceled")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service := NewReleaseMaterializeService()
	closeCalls := 0
	service.wrapStagingLease = func(lease cloneStagingLease) cloneStagingLease {
		return releaseMaterializeObservedLease{cloneStagingLease: lease, closeCalls: &closeCalls}
	}
	service.beforePublish = func() error {
		cancel()
		return ctx.Err()
	}
	var staging string
	service.beforeStagingQuarantine = func(path string) error {
		staging = path
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "foreign"), []byte("foreign\n"), 0o600)
	}

	result, err := service.Materialize(ctx, request)
	var outer *Error
	if !errors.As(err, &outer) || outer.Kind != ErrorRollbackIncomplete {
		t.Fatalf("outer Materialize error = %#v, want rollback-incomplete", outer)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Materialize error lost cancellation: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("Materialize status = %q, want failed", result.Status)
	}
	if closeCalls != 1 {
		t.Fatalf("staging lease close calls = %d, want 1", closeCalls)
	}
	data, readErr := os.ReadFile(filepath.Join(staging, "foreign"))
	if readErr != nil || string(data) != "foreign\n" {
		t.Fatalf("foreign staging was not preserved: %q %v", data, readErr)
	}
	if _, recoveryErr := store.ReadRecovery(filepath.Join(request.DataDir, "projects", "release-staging-canceled", "recovery", "default.json")); recoveryErr != nil {
		t.Fatalf("bounded recovery = %v; Materialize = %v", recoveryErr, err)
	}
	_ = base
}

func TestReleaseMaterializePostReplacementMetadataErrorsRollbackOnlyOwnedGenerations(t *testing.T) {
	for _, target := range []string{"config", "state", "registry"} {
		t.Run(target, func(t *testing.T) {
			base, _, request := releaseMaterializeChildFixture(t, "release-metadata-"+target)
			service := NewReleaseMaterializeService()
			service.writeCAS = func(original cloneFileSnapshot, data []byte, compare func() error) (ClonePublicationReceipt, error) {
				receipt, err := defaultMaterializeCAS(original, data, compare, nil)
				if err != nil {
					return receipt, err
				}
				matches := (target == "config" && original.path == filepath.Join(base.Path, ".wtree.yml")) ||
					(target == "state" && original.path == WorkspaceStatePath(request.DataDir, "release-metadata-"+target, "default")) ||
					(target == "registry" && original.path == filepath.Join(request.DataDir, "registry.json"))
				if matches {
					return receipt, errors.New("injected post-replacement writer error")
				}
				return receipt, nil
			}
			if _, err := service.Materialize(context.Background(), request); err == nil {
				t.Fatal("post-replacement error = nil")
			}
			for _, path := range []string{filepath.Join(base.Path, ".wtree.yml"), WorkspaceStatePath(request.DataDir, "release-metadata-"+target, "default"), filepath.Join(request.DataDir, "registry.json"), filepath.Join(base.Path, "backend")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("owned failed publication remained at %s: %v", path, err)
				}
			}
		})
	}
}

func TestReleaseMaterializePreservesForeignPublicationMutationsAndRecordsRecovery(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ReleaseMaterializeService, testutil.GitRepository, ReleaseMaterializeRequest)
		path   func(testutil.GitRepository, ReleaseMaterializeRequest) string
	}{
		{
			name: "child-tree",
			mutate: func(service *ReleaseMaterializeService, base testutil.GitRepository, _ ReleaseMaterializeRequest) {
				service.afterPublish = func(string) error {
					return os.WriteFile(filepath.Join(base.Path, "backend", "foreign"), []byte("foreign\n"), 0o600)
				}
			},
			path: func(base testutil.GitRepository, _ ReleaseMaterializeRequest) string {
				return filepath.Join(base.Path, "backend", "foreign")
			},
		},
		{
			name: "config-file",
			mutate: func(service *ReleaseMaterializeService, base testutil.GitRepository, _ ReleaseMaterializeRequest) {
				service.writeCAS = func(original cloneFileSnapshot, data []byte, compare func() error) (ClonePublicationReceipt, error) {
					receipt, err := defaultMaterializeCAS(original, data, compare, nil)
					if err == nil && original.path == filepath.Join(base.Path, ".wtree.yml") {
						err = os.WriteFile(original.path, []byte("foreign\n"), 0o600)
					}
					return receipt, err
				}
			},
			path: func(base testutil.GitRepository, _ ReleaseMaterializeRequest) string {
				return filepath.Join(base.Path, ".wtree.yml")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, _, request := releaseMaterializeChildFixture(t, "release-foreign-"+test.name)
			service := NewReleaseMaterializeService()
			test.mutate(service, base, request)
			if _, err := service.Materialize(context.Background(), request); err == nil {
				t.Fatal("foreign mutation error = nil")
			}
			data, err := os.ReadFile(test.path(base, request))
			if err != nil || string(data) != "foreign\n" {
				t.Fatalf("foreign generation was not preserved: %q %v", data, err)
			}
			if _, err := store.ReadRecovery(filepath.Join(request.DataDir, "projects", "release-foreign-"+test.name, "recovery", "default.json")); err != nil {
				t.Fatalf("recovery = %v", err)
			}
		})
	}
}

func TestReleaseMaterializeRejectsDifferentRegisteredProjectBeforePublication(t *testing.T) {
	for _, test := range []struct {
		name      string
		candidate func(testutil.GitRepository, ReleaseMaterializeRequest) RegistrationConflictCandidate
	}{
		{
			name: "canonical-config-and-logical-root",
			candidate: func(base testutil.GitRepository, _ ReleaseMaterializeRequest) RegistrationConflictCandidate {
				return RegistrationConflictCandidate{ID: "existing-config-root", ConfigPath: filepath.Join(base.Path, ".wtree.yml"), LogicalRoot: base.Path}
			},
		},
		{
			name: "shared-base-git-identity",
			candidate: func(base testutil.GitRepository, _ ReleaseMaterializeRequest) RegistrationConflictCandidate {
				identity, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), base.Path)
				if err != nil {
					t.Fatal(err)
				}
				return RegistrationConflictCandidate{ID: "existing-git-identity", ConfigPath: filepath.Join(t.TempDir(), "existing", ".wtree.yml"), RepositoryIdentities: []string{identity}}
			},
		},
		{
			name: "top-level-destination-path",
			candidate: func(base testutil.GitRepository, _ ReleaseMaterializeRequest) RegistrationConflictCandidate {
				return RegistrationConflictCandidate{ID: "existing-top-level", ConfigPath: filepath.Join(t.TempDir(), "existing", ".wtree.yml"), TopLevelPaths: []string{base.Path}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, _, request := releaseMaterializeChildFixture(t, "release-r3-"+test.name)
			candidate := test.candidate(base, request)
			registryPath := filepath.Join(request.DataDir, "registry.json")
			if err := store.WriteRegistry(registryPath, store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{candidate.ID: {Name: candidate.ID, ConfigPath: candidate.ConfigPath, RepositoryIDs: map[string]string{}}}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			service := NewReleaseMaterializeService()
			service.registrationCandidates = func(_ context.Context, _ string, registry store.Registry) []RegistrationConflictCandidate {
				if _, exists := registry.Projects[candidate.ID]; !exists {
					t.Fatal("existing registration was not visible under publication locks")
				}
				return []RegistrationConflictCandidate{candidate}
			}
			if _, err := service.Materialize(context.Background(), request); err == nil || !hasCloneErrorKind(err, ErrorConflict) {
				t.Fatalf("registered conflict = %v", err)
			}
			for _, path := range []string{filepath.Join(base.Path, "backend"), filepath.Join(base.Path, ".wtree.yml"), WorkspaceStatePath(request.DataDir, "release-r3-"+test.name, "default")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("conflict published %s: %v", path, err)
				}
			}
			after, err := os.ReadFile(registryPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("existing registry changed: %q %v", after, err)
			}
		})
	}
}

func TestReleaseMaterializeAuthenticationFailureLeaksNoCanaryToArtifacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	base, _, request := releaseMaterializeChildFixture(t, "release-auth-artifacts")
	canary := "release-auth-secret-canary"
	capture := filepath.Join(t.TempDir(), "git-environment")
	binary := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$*\" in *fetch*) env > \"$WTREE_CAPTURE\"; echo helper-"+canary+" >&2; exit 9;; esac\nexec /usr/bin/git \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewReleaseMaterializeService()
	service.beforeStaging = func() error {
		service.git = gitadapter.NewAdapterWithEnv(binary, []string{"PATH=" + os.Getenv("PATH"), "WTREE_CAPTURE=" + capture, "SSH_AUTH_SOCK=/tmp/agent", "GIT_ASKPASS=/tmp/askpass", "HELPER_SECRET=" + canary, "HOME=/tmp/helper-home", "GIT_TERMINAL_PROMPT=1"})
		return nil
	}
	_, err := service.Materialize(context.Background(), request)
	if err == nil || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "helper-") || len(err.Error()) > 2048 {
		t.Fatalf("unsafe or unbounded authentication error: %v", err)
	}
	captured, readErr := os.ReadFile(capture)
	if readErr != nil || !strings.Contains(string(captured), "HELPER_SECRET="+canary) || !strings.Contains(string(captured), "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("auth environment capture=%q err=%v", captured, readErr)
	}
	for _, root := range []string{base.Path, request.DataDir} {
		releaseMaterializeAssertNoCanary(t, root, canary)
	}
	if leaked, globErr := filepath.Glob(filepath.Join(filepath.Dir(base.Path), ".wtree-release-*")); globErr != nil || len(leaked) != 0 {
		t.Fatalf("authentication failure left staging artifact: %v %v", leaked, globErr)
	}
}

func releaseMaterializeAssertNoCanary(t *testing.T, root, canary string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || path == root {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(data), canary) {
			t.Fatalf("credential canary persisted at %s", path)
		}
		return nil
	})
}

func releaseMaterializeChildFixture(t *testing.T, id string) (testutil.GitRepository, cloneExecutionRemote, ReleaseMaterializeRequest) {
	t.Helper()
	child := newCloneExecutionRemote(t, "child", "published", map[string]string{"child": "ok\n"})
	base := testutil.NewGitRepository(t)
	base.CommitFile(".gitignore", "/.wtree.yml\n/backend/\n", "ignore")
	root := cloneGitOutput(t, base.Path, "rev-parse", "HEAD")
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: id, Name: id, BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "root", URL: child.remote}, Upstream: config.Upstream{Branch: "main", Remote: "root", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{root}}, Mount: ".", DefaultBranch: "main"}, "child": {Clone: config.CloneSource{Remote: "child", URL: child.remote}, Upstream: config.Upstream{Branch: "main", Remote: "child", Merge: "refs/heads/published"}, Identity: config.RepositoryIdentity{InitialCommits: []string{child.identity}}, Parent: "root", Mount: "backend", DefaultBranch: "main"}}}
	mb, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lb, err := config.MarshalReleaseLock(config.ReleaseLock{Version: 1, Project: config.ReleaseLockProject{ID: id, ManifestSHA256: config.ReleaseManifestSHA256(mb)}, Release: config.ReleaseLockRelease{Name: "v1"}, Repositories: map[string]config.ReleaseLockRepository{"child": {Revision: child.identity}}})
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitCloneFiles(t, base.Path, map[string]string{"project.wtree.yml": string(mb), ReleaseLockFilename: string(lb)}, "input")
	return base, child, ReleaseMaterializeRequest{LockPath: filepath.Join(base.Path, ReleaseLockFilename), DataDir: filepath.Join(t.TempDir(), "data")}
}
