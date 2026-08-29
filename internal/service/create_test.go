package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
	"github.com/definebusiness/wtree/internal/transaction"
)

func TestWorkspaceCreatorCreatesNestedWorkspaceParentFirst(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	value, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := len(value.Steps), 4; got != want || value.Steps[0].Action != plan.CreateBranch || value.Steps[2].RepositoryID != "backend" {
		t.Fatalf("plan steps = %#v", value.Steps)
	}
	root.Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
	backend.Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
	for _, path := range []string{target, filepath.Join(target, "api")} {
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("created worktree %q: %v", path, err)
		}
	}
	state, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, value.WorkspaceID))
	if err != nil {
		t.Fatalf("ReadWorkspace: %v", err)
	}
	if got, want := state.Repositories["backend"].ResolvedPath, filepath.Join(target, "api"); got != want {
		t.Fatalf("backend state path = %q, want %q", got, want)
	}
}

func TestWorkspaceCreatorCreatesThreeLevelRenamedWorkspace(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	value, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{
			{RepositoryID: "backend", Mount: "api"},
			{RepositoryID: "shared", Mount: "common"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	paths := map[string]string{
		"root":    target,
		"backend": filepath.Join(target, "api"),
		"shared":  filepath.Join(target, "api", "common"),
	}
	sources := map[string]testutil.GitRepository{"root": root, "backend": backend, "shared": shared}
	for id, path := range paths {
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("%s worktree at %q: %v", id, path, err)
		}
		identity, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), path)
		if err != nil {
			t.Fatalf("%s created identity: %v", id, err)
		}
		sourceIdentity, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), sources[id].Path)
		if err != nil || identity != sourceIdentity {
			t.Fatalf("%s identity = %q source = %q error = %v", id, identity, sourceIdentity, err)
		}
		sources[id].Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
	}
	state, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, value.WorkspaceID))
	if err != nil {
		t.Fatalf("ReadWorkspace: %v", err)
	}
	if state.Path != target {
		t.Fatalf("workspace state path = %q, want %q", state.Path, target)
	}
	for id, path := range paths {
		checkout, found := state.Repositories[id]
		if !found || checkout.Branch != "feature/login" || checkout.ResolvedPath != path {
			t.Fatalf("state %s = %#v, want branch/path %q", id, checkout, path)
		}
	}
}

func TestWorkspaceCreatorCreatesRootGitWorkspaceWithGroupedChildren(t *testing.T) {
	project, _, _, _, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "root-git-workspace")
	value, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/grouped-root",
		TargetPath:    target,
		DataDir:       data,
		Mounts: []service.MountOverride{
			{RepositoryID: "backend", Mount: "packages/backend"},
			{RepositoryID: "shared", Mount: "tools/shared"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"root":    target,
		"backend": filepath.Join(target, "packages", "backend"),
		"shared":  filepath.Join(target, "packages", "backend", "tools", "shared"),
	}
	for id, path := range paths {
		if got := createRepositoryPlanPath(value, id); got != path {
			t.Fatalf("%s path = %q, want %q", id, got, path)
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("%s checkout: %v", id, err)
		}
	}
	contents, err := os.ReadFile(filepath.Join(target, ".gitignore"))
	if err != nil || !strings.Contains(string(contents), "/packages/backend/") {
		t.Fatalf("root parent ignore = %q, %v", contents, err)
	}
	backendIgnore, err := os.ReadFile(filepath.Join(paths["backend"], ".gitignore"))
	if err != nil || !strings.Contains(string(backendIgnore), "/tools/shared/") {
		t.Fatalf("backend parent ignore = %q, %v", backendIgnore, err)
	}
}

func TestWorkspacePlannerForestIsIndependentOfRepositoryInsertionOrder(t *testing.T) {
	project, _, _, _, data := createThreeLevelFixture(t)
	request := service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "feature/order", TargetPath: filepath.Join(t.TempDir(), "workspace"), DataDir: data}
	first, err := service.NewWorkspacePlanner().Plan(context.Background(), project, request)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := project
	shuffled.Repositories = append([]domain.Repository(nil), project.Repositories...)
	slices.Reverse(shuffled.Repositories)
	second, err := service.NewWorkspacePlanner().Plan(context.Background(), shuffled, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("order-dependent plan:\nfirst=%#v\nsecond=%#v", first, second)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil || !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("order-dependent plan JSON:\nfirst=%s\nsecond=%s\nerror=%v", firstJSON, secondJSON, err)
	}
}

func createRepositoryPlanPath(value plan.WorkspacePlan, id string) string {
	for _, repository := range value.Repositories {
		if repository.ID == id {
			return repository.Path
		}
	}
	return ""
}

func TestWorkspaceCreatorEnsuresEveryParentBeforeAddingItsChild(t *testing.T) {
	project, root, backend, _, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	sourceIgnores := make(map[string][]byte)
	for _, source := range []testutil.GitRepository{root, backend} {
		contents, readErr := os.ReadFile(filepath.Join(source.Path, ".gitignore"))
		if readErr != nil {
			t.Fatalf("read source ignore %q: %v", source.Path, readErr)
		}
		sourceIgnores[source.Path] = contents
	}
	var events []string
	creator := service.NewWorkspaceCreator()
	created, err := creator.CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/ordered", TargetPath: target, DataDir: data,
	}, func(event transaction.Event) {
		if event.Kind == transaction.ExecuteSucceeded {
			events = append(events, event.Step)
		}
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := created.IgnoreUpdates, []service.IgnoreFileUpdate{
		{ParentRepositoryID: "root", Path: filepath.Join(target, ".gitignore"), AddedRules: []string{"/backend/"}},
		{ParentRepositoryID: "backend", Path: filepath.Join(target, "backend", ".gitignore"), AddedRules: []string{"/shared/"}},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("automatic ignore evidence = %#v, want %#v", got, want)
	}
	for path, want := range map[string]string{
		filepath.Join(target, ".gitignore"):            "/api/\n/backend/\n",
		filepath.Join(target, "backend", ".gitignore"): "/common/\n/shared/\n",
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("automatic ignore %q = %q, %v; want %q", path, got, readErr, want)
		}
	}
	if got, want := events, []string{
		"create_branch:root", "prepare_grouping:root", "add_worktree:root", "inspect_ignore_owner:root", "ensure_ignore:root",
		"create_branch:backend", "add_worktree:backend", "inspect_ignore_owner:backend", "ensure_ignore:backend",
		"create_branch:shared", "add_worktree:shared",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered effects = %v, want %v", got, want)
	}
	for _, check := range []struct{ repository, mount string }{
		{target, "backend"},
		{filepath.Join(target, "backend"), "shared"},
	} {
		if output, checkErr := exec.Command("git", "-C", check.repository, "check-ignore", "--no-index", "--", check.mount+"/").CombinedOutput(); checkErr != nil {
			t.Fatalf("check-ignore %s in %s: %v\n%s", check.mount, check.repository, checkErr, output)
		}
	}
	for _, source := range []testutil.GitRepository{root, backend} {
		contents, readErr := os.ReadFile(filepath.Join(source.Path, ".gitignore"))
		if readErr != nil || !bytes.Equal(contents, sourceIgnores[source.Path]) {
			t.Fatalf("source ignore %q = %q, %v; want %q", source.Path, contents, readErr, sourceIgnores[source.Path])
		}
	}
}

func TestWorkspaceCreatorGrandchildFailureRollsBackRenamedParents(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 6}
	_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}, {RepositoryID: "shared", Mount: "common"}},
	}, nil)
	if err == nil {
		t.Fatal("Create succeeded, want grandchild add-worktree failure")
	}
	for _, repository := range []testutil.GitRepository{root, backend, shared} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("renamed workspace remains after grandchild failure: %v", statErr)
	}
	if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, pathutil.StorageName("feature/login"))); !os.IsNotExist(statErr) {
		t.Fatalf("state remains after grandchild failure: %v", statErr)
	}
}

func TestWorkspaceCreatorRollsBackEveryCreateEffectFailure(t *testing.T) {
	for failureAt := 1; failureAt <= 4; failureAt++ {
		t.Run(fmt.Sprintf("effect-%d", failureAt), func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			target := filepath.Join(t.TempDir(), "workspace")
			git := &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: failureAt}
			_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{
				WorkspaceName: "feature/login", TargetPath: target, DataDir: data,
			}, nil)
			if err == nil {
				t.Fatal("Create succeeded, want injected effect failure")
			}
			for _, repository := range []testutil.GitRepository{root, backend} {
				exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
				if branchErr != nil || exists {
					t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
				}
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("target remains after rollback: %v", statErr)
			}
			if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, "feature-login")); !os.IsNotExist(statErr) {
				t.Fatalf("state remains after rollback: %v", statErr)
			}
		})
	}
}

func TestWorkspaceCreatorStateCommitFailureRollsBackGitEffects(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	coordinator := service.NewWorkspaceTransactionWithFiles(
		projectLocker{},
		func(string, store.WorkspaceState) error { return errors.New("injected state failure") },
		store.WriteRecovery, os.Remove, os.ReadFile, store.WriteRawAtomic,
	)
	_, err := service.NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), coordinator).Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	if err == nil {
		t.Fatal("Create succeeded, want state commit failure")
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target remains after state failure: %v", statErr)
	}
}

func TestWorkspaceCreatorResultValidationFailureRollsBackGitEffects(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := validationFailingCreateGit{Git: gitadapter.NewAdapter("git"), target: target}
	_, err := service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	if err == nil {
		t.Fatal("Create succeeded, want result validation failure")
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target remains after validation failure: %v", statErr)
	}
}

func TestWorkspaceCreatorIncompleteRollbackWritesRecovery(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := rollbackFailingCreateGit{Git: gitadapter.NewAdapter("git")}
	_, err := service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	record, readErr := store.ReadRecovery(service.RecoveryRecordPath(data, plan.WorkspacePlan{ProjectID: project.ID, WorkspaceID: pathutil.StorageName("feature/login")}))
	if readErr != nil {
		t.Fatalf("ReadRecovery: %v", readErr)
	}
	if got, want := record.UnrevertedSteps, []string{"create_branch:root"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("recovery unreverted steps = %v, want %v", got, want)
	}
}

func TestWorkspaceCreatorPreservesUnexpectedWorktreeDirtDuringRollback(t *testing.T) {
	project, root, _, data := createFixture(t)
	root.CommitFile(".gitignore", "/api/\n/api space/\n/unrelated.txt\n", "ignore unrelated rollback fixture")
	target := filepath.Join(t.TempDir(), "workspace")
	git := &dirtyRollbackCreateGit{Git: gitadapter.NewAdapter("git"), target: target}
	_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/dirty", TargetPath: target, DataDir: data,
	}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(target, "unrelated.txt")); readErr != nil || string(contents) != "preserve\n" {
		t.Fatalf("unexpected dirt was not preserved: %q, %v", contents, readErr)
	}
	record, readErr := store.ReadRecovery(service.RecoveryRecordPath(data, plan.WorkspacePlan{ProjectID: project.ID, WorkspaceID: pathutil.StorageName("feature/dirty")}))
	if readErr != nil {
		t.Fatalf("ReadRecovery: %v", readErr)
	}
	found := false
	for _, step := range record.UnrevertedSteps {
		found = found || step == "add_worktree:root"
	}
	if !found {
		t.Fatalf("recovery unreverted steps = %v, want root worktree preservation", record.UnrevertedSteps)
	}
}

func TestWorkspaceCreatorPreservesPrePlanIgnoreDirtDuringRollback(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &planWindowCreateGit{failingCreateGit: &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 4}, target: target}
	creator := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction())
	result, err := creator.CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/pre-plan-dirt", TargetPath: target, DataDir: data,
	}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(target, ".gitignore")); readErr != nil || string(got) != "# pre-plan user edit\n/backend/\n" {
		t.Fatalf("pre-plan ignore dirt = %q, %v", got, readErr)
	}
	if got, want := result.RetainedIgnoreFiles, []service.IgnoreFileUpdate{{ParentRepositoryID: "root", Path: filepath.Join(target, ".gitignore"), AddedRules: []string{"/backend/"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retained ignore files = %#v, want %#v", got, want)
	}
}

func TestWorkspaceCreatorPreservesIgnoreChangedAfterCleanupRead(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &cleanupBoundaryCreateGit{
		failingCreateGit: &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 4},
		target:           target,
		mutate: func(path string) error {
			return os.WriteFile(filepath.Join(path, ".gitignore"), []byte("# concurrent user edit\n"), 0o644)
		},
	}
	creator := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction())
	result, err := creator.CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/cleanup-race", TargetPath: target, DataDir: data,
	}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(target, ".gitignore")); readErr != nil || string(got) != "# concurrent user edit\n" {
		t.Fatalf("concurrent ignore dirt = %q, %v", got, readErr)
	}
	if len(result.RetainedIgnoreFiles) != 1 || len(result.RemovedIgnoreFiles) != 0 {
		t.Fatalf("cleanup result = %#v, want retained automatic file only", result)
	}
}

func TestWorkspaceCreatorPreservesIgnoredDirtIntroducedAtRemovalBoundary(t *testing.T) {
	project, root, _, data := createFixture(t)
	root.CommitFile(".gitignore", "/api/\n/api space/\n/late/\n", "ignore late rollback fixture")
	target := filepath.Join(t.TempDir(), "workspace")
	git := &cleanupBoundaryCreateGit{
		failingCreateGit: &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 4},
		target:           target,
		mutate: func(path string) error {
			if err := os.MkdirAll(filepath.Join(path, "late"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(path, "late", "x"), []byte("preserve\n"), 0o644)
		},
	}
	_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/ignored-cleanup-race", TargetPath: target, DataDir: data,
	}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(target, "late", "x")); readErr != nil || string(got) != "preserve\n" {
		t.Fatalf("ignored late dirt was not preserved: %q, %v", got, readErr)
	}
	record, readErr := store.ReadRecovery(service.RecoveryRecordPath(data, plan.WorkspacePlan{ProjectID: project.ID, WorkspaceID: pathutil.StorageName("feature/ignored-cleanup-race")}))
	if readErr != nil {
		t.Fatalf("ReadRecovery: %v", readErr)
	}
	if !slices.Contains(record.UnrevertedSteps, "add_worktree:root") {
		t.Fatalf("recovery unreverted steps = %v, want root worktree preservation", record.UnrevertedSteps)
	}
}

func TestWorkspaceCreatorRemovesIsolatedTreeAfterPostUnregisterPublicRecreation(t *testing.T) {
	project, root, _, data := createFixture(t)
	targetParent := t.TempDir()
	target := filepath.Join(targetParent, "workspace")
	publicBytes := []byte("concurrent public checkout\n")
	git := &postUnregisterPublicRecreationGit{
		failingCreateGit: &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 4},
		target:           target,
		publicBytes:      publicBytes,
	}
	_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/post-unregister-recreation", TargetPath: target, DataDir: data,
	}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	if !git.recreated {
		t.Fatal("post-unregister public recreation was not exercised")
	}
	if got, readErr := os.ReadFile(filepath.Join(target, "public.txt")); readErr != nil || !bytes.Equal(got, publicBytes) {
		t.Fatalf("recreated public bytes = %q, %v, want %q", got, readErr, publicBytes)
	}
	if _, statErr := os.Stat(filepath.Join(target, "destroy")); !os.IsNotExist(statErr) {
		t.Fatalf("public path was modified with a destroy tree: %v", statErr)
	}
	quarantines, globErr := filepath.Glob(filepath.Join(targetParent, ".wtree-worktree-rollback-*"))
	if globErr != nil || len(quarantines) != 0 {
		t.Fatalf("worktree quarantines = %v, %v, want none", quarantines, globErr)
	}
	worktrees, listErr := gitadapter.NewAdapter("git").ListWorktrees(context.Background(), root.Path)
	if listErr != nil || slices.ContainsFunc(worktrees, func(worktree gitadapter.Worktree) bool {
		return servicePathEqual(worktree.Path, target)
	}) {
		t.Fatalf("registered worktrees = %#v, %v, want removed public checkout omitted", worktrees, listErr)
	}
	branchExists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), root.Path, "feature/post-unregister-recreation")
	if branchErr != nil || !branchExists {
		t.Fatalf("root branch = exists:%t error:%v, want preserved branch", branchExists, branchErr)
	}
	recoveryPlan := plan.WorkspacePlan{ProjectID: project.ID, WorkspaceID: pathutil.StorageName("feature/post-unregister-recreation")}
	record, readErr := store.ReadRecovery(service.RecoveryRecordPath(data, recoveryPlan))
	if readErr != nil || record.Operation != string(plan.Create) || record.FailedStep != "add_worktree:backend" || !reflect.DeepEqual(record.UnrevertedSteps, []string{"create_branch:root"}) || len(record.RollbackFailures) != 1 || record.RollbackFailures[0].Step != "create_branch:root" {
		t.Fatalf("recovery = %#v, %v, want branch-only actionable recovery", record, readErr)
	}
	_, retryErr := service.NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), service.NewWorkspaceTransaction()).Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/post-unregister-recreation", TargetPath: target, DataDir: data,
	}, nil)
	if !errors.As(retryErr, &application) || application.Kind != service.ErrorConflict {
		t.Fatalf("retry error = %v, want actionable recovery conflict", retryErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(target, "public.txt")); readErr != nil || !bytes.Equal(got, publicBytes) {
		t.Fatalf("retry changed recreated public bytes = %q, %v, want %q", got, readErr, publicBytes)
	}
}

func TestWorkspaceCreatorCleanRollbackRemovesOnlyItsRegisteredWorktree(t *testing.T) {
	project, root, _, data := createFixture(t)
	unrelatedPath := filepath.Join(t.TempDir(), "unrelated")
	root.Run(t, "branch", "unrelated-cleanup-proof")
	root.Run(t, "worktree", "add", unrelatedPath, "unrelated-cleanup-proof")
	targetParent := t.TempDir()
	target := filepath.Join(targetParent, "workspace")
	git := &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 4}
	_, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/targeted-cleanup", TargetPath: target, DataDir: data,
	}, nil)
	if err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("Create error = %v, want clean rollback", err)
	}
	worktrees, listErr := gitadapter.NewAdapter("git").ListWorktrees(context.Background(), root.Path)
	if listErr != nil || !slices.ContainsFunc(worktrees, func(worktree gitadapter.Worktree) bool {
		return servicePathEqual(worktree.Path, unrelatedPath)
	}) {
		t.Fatalf("registered worktrees = %#v, %v, want unrelated worktree retained", worktrees, listErr)
	}
	if _, statErr := os.Stat(filepath.Join(unrelatedPath, ".git")); statErr != nil {
		t.Fatalf("unrelated worktree was removed: %v", statErr)
	}
	if quarantines, globErr := filepath.Glob(filepath.Join(targetParent, ".wtree-worktree-rollback-*")); globErr != nil || len(quarantines) != 0 {
		t.Fatalf("successful cleanup quarantines = %v, %v, want none", quarantines, globErr)
	}
}

func TestWorkspaceCreatorCleanRollbackRemovesNewAutomaticIgnoreFile(t *testing.T) {
	project, data := createFixtureWithoutCommittedIgnore(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 4}
	result, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/new-ignore", TargetPath: target, DataDir: data,
	}, nil)
	if err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("Create error = %v, want clean rollback", err)
	}
	if got, want := result.RemovedIgnoreFiles, []service.IgnoreFileUpdate{{ParentRepositoryID: "root", Path: filepath.Join(target, ".gitignore"), AddedRules: []string{"/backend/"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed ignore files = %#v, want %#v", got, want)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("new automatic ignore worktree remains: %v", statErr)
	}
}

func TestWorkspaceCreatorPreservesConcurrentReplacementOfNewAutomaticIgnoreFile(t *testing.T) {
	project, data := createFixtureWithoutCommittedIgnore(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &cleanupBoundaryCreateGit{
		failingCreateGit: &failingCreateGit{Git: gitadapter.NewAdapter("git"), failAt: 4},
		target:           target,
		mutate: func(path string) error {
			return os.WriteFile(filepath.Join(path, ".gitignore"), []byte("# concurrent user edit\n"), 0o644)
		},
	}
	result, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/new-ignore-race", TargetPath: target, DataDir: data,
	}, nil)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("Create error = %v, want rollback incomplete", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(target, ".gitignore")); readErr != nil || string(got) != "# concurrent user edit\n" {
		t.Fatalf("concurrent replacement = %q, %v", got, readErr)
	}
	if len(result.RetainedIgnoreFiles) != 1 || len(result.RemovedIgnoreFiles) != 0 {
		t.Fatalf("cleanup result = %#v, want retained automatic file only", result)
	}
}

func TestWorkspaceCreatorFailureResultReportsRemovedAndUnverifiedMounts(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &verificationFailingCreateGit{Git: gitadapter.NewAdapter("git"), failPath: target}
	result, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/unverified", TargetPath: target, DataDir: data,
	}, nil)
	if err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("Create error = %v, want clean rollback", err)
	}
	if got, want := result.RemovedIgnoreFiles, []service.IgnoreFileUpdate{{ParentRepositoryID: "root", Path: filepath.Join(target, ".gitignore"), AddedRules: []string{"/backend/"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed ignore files = %#v, want %#v", got, want)
	}
	if len(result.RetainedIgnoreFiles) != 0 {
		t.Fatalf("retained ignore files = %#v, want none", result.RetainedIgnoreFiles)
	}
	if got, want := result.UnverifiedMounts, []service.UnverifiedMount{{ParentRepositoryID: "root", ChildRepositoryID: "backend", ParentPath: target, ChildPath: filepath.Join(target, "backend"), Mount: "backend"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unverified mounts = %#v, want %#v", got, want)
	}
	if _, statErr := os.Stat(filepath.Join(target, "backend", ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("unverified child was added: %v", statErr)
	}
}

func TestWorkspaceCreatorReportsOnlySecondDirectChildAsUnverified(t *testing.T) {
	project, data := createTwoChildFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	git := &secondChildVerificationFailingGit{Git: gitadapter.NewAdapter("git"), parentPath: target, failAt: 4}
	result, err := service.NewWorkspaceCreatorWith(git, service.NewWorkspaceTransaction()).CreateWithResult(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/two-children", TargetPath: target, DataDir: data,
	}, nil)
	if err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("Create error = %v, want clean rollback", err)
	}
	if got, want := result.UnverifiedMounts, []service.UnverifiedMount{{
		ParentRepositoryID: "root", ChildRepositoryID: "beta", ParentPath: target, ChildPath: filepath.Join(target, "beta"), Mount: "beta",
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unverified mounts = %#v, want %#v", got, want)
	}
	for _, child := range []string{"alpha", "beta"} {
		if _, statErr := os.Stat(filepath.Join(target, child, ".git")); !os.IsNotExist(statErr) {
			t.Fatalf("child %q was added before all direct mounts verified: %v", child, statErr)
		}
	}
}

func TestWorkspaceCreatorCancellationRollsBackCompletedGitEffects(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	git := cancelAfterWorktreeGit{Git: gitadapter.NewAdapter("git"), cancel: cancel}
	_, err := service.NewWorkspaceCreatorWith(&git, service.NewWorkspaceTransaction()).Create(ctx, project, service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want context canceled", err)
	}
	if !service.HasCleanRollback(err) {
		t.Fatalf("Create error = %v, want clean rollback outcome", err)
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, "feature/login")
		if branchErr != nil || exists {
			t.Fatalf("branch rollback for %q = exists:%t error:%v", repository.Path, exists, branchErr)
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target remains after cancellation: %v", statErr)
	}
}

func TestWorkspaceCreatorConcurrentSameWorkspaceHasOneWinner(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	request := service.WorkspacePlanRequest{WorkspaceName: "feature/login", TargetPath: target, DataDir: data}
	errors := runConcurrentCreates(project, request, request)
	successes := 0
	for _, err := range errors {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("same-workspace successes = %d, errors = %v", successes, errors)
	}
	if _, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, pathutil.StorageName("feature/login"))); err != nil {
		t.Fatalf("ReadWorkspace winner state: %v", err)
	}
}

func TestWorkspaceCreatorConcurrentDifferentWorkspacesLeaveConsistentState(t *testing.T) {
	project, root, backend, data := createFixture(t)
	one := service.WorkspacePlanRequest{WorkspaceName: "feature/one", TargetPath: filepath.Join(t.TempDir(), "one"), DataDir: data}
	two := service.WorkspacePlanRequest{WorkspaceName: "feature/two", TargetPath: filepath.Join(t.TempDir(), "two"), DataDir: data}
	for _, err := range runConcurrentCreates(project, one, two) {
		if err != nil {
			t.Fatalf("concurrent different create: %v", err)
		}
	}
	for _, request := range []service.WorkspacePlanRequest{one, two} {
		workspaceID := pathutil.StorageName(request.WorkspaceName)
		if _, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, workspaceID)); err != nil {
			t.Fatalf("ReadWorkspace %q: %v", request.WorkspaceName, err)
		}
		if _, err := os.Stat(request.TargetPath); err != nil {
			t.Fatalf("workspace target %q: %v", request.TargetPath, err)
		}
	}
	for _, repository := range []testutil.GitRepository{root, backend} {
		for _, branch := range []string{"feature/one", "feature/two"} {
			exists, err := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository.Path, branch)
			if err != nil || !exists {
				t.Fatalf("branch %q in %q = exists:%t error:%v", branch, repository.Path, exists, err)
			}
		}
	}
}

func runConcurrentCreates(project domain.Project, first, second service.WorkspacePlanRequest) []error {
	requests := []service.WorkspacePlanRequest{first, second}
	start := make(chan struct{})
	results := make(chan error, len(requests))
	var group sync.WaitGroup
	for _, request := range requests {
		request := request
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			coordinator := service.NewWorkspaceTransaction()
			// These integration calls intentionally serialize real Git worktree
			// mutations. Windows Git can hold the first project lease for several
			// seconds, so the fixture must wait for its legitimate peer rather than
			// testing the one-second interactive contention policy.
			coordinator.LockTimeout = 30 * time.Second
			creator := service.NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), coordinator)
			_, err := creator.Create(context.Background(), project, request, nil)
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	errors := make([]error, 0, len(requests))
	for err := range results {
		errors = append(errors, err)
	}
	return errors
}

func createFixture(t *testing.T) (domain.Project, testutil.GitRepository, testutil.GitRepository, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	root.CommitFile(".gitignore", "/api/\n/api space/\n", "ignore custom mounts")
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolution.Project, root.GitRepository, backend.GitRepository, data
}

func createFixtureWithoutCommittedIgnore(t *testing.T) (domain.Project, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolution.Project, data
}

func createTwoChildFixture(t *testing.T) (domain.Project, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	for _, child := range []string{"alpha", "beta"} {
		repository := testutil.NewPushedGitRepository(t)
		repository.CommitFile(child+".txt", child+"\n", child)
		if err := os.Rename(repository.Path, filepath.Join(root.Path, child)); err != nil {
			t.Fatal(err)
		}
	}
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	root.CommitFile(".gitignore", "# base\n", "base without child rules")
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolution.Project, data
}

// createFixtureWorkspace uses the committed fixture mount rule so callers
// testing remove/delete/doctor behavior do not accidentally add M04's
// intentional automatic .gitignore dirt to their unrelated fixture state.
func createFixtureWorkspace(t *testing.T, project domain.Project, workspaceName, target, data string) (plan.WorkspacePlan, error) {
	t.Helper()
	return service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: workspaceName,
		TargetPath:    target,
		DataDir:       data,
		Mounts:        []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
	}, nil)
}

func createThreeLevelFixture(t *testing.T) (domain.Project, testutil.GitRepository, testutil.GitRepository, testutil.GitRepository, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("shared.txt", "shared\n", "shared")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	sharedPath := filepath.Join(backendPath, "shared")
	if err := os.Rename(shared.Path, sharedPath); err != nil {
		t.Fatal(err)
	}
	backend.Path, shared.Path = backendPath, sharedPath
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	root.CommitFile(".gitignore", "/api/\n", "ignore custom backend mount")
	backend.CommitFile(".gitignore", "/common/\n", "ignore custom shared mount")
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolution.Project, root.GitRepository, backend.GitRepository, shared.GitRepository, data
}

type failingCreateGit struct {
	gitadapter.Git
	failAt int
	calls  int
}

// planWindowCreateGit writes through a real Git adapter immediately after the
// pre-plan status snapshot returns. This injects user bytes in the exact
// status-to-IgnorePlanner.Plan window.
type planWindowCreateGit struct {
	*failingCreateGit
	target      string
	statusCalls int
	mutated     bool
}

func (g *planWindowCreateGit) StatusIncludingIgnored(ctx context.Context, repository string) (gitadapter.Status, error) {
	status, err := g.failingCreateGit.Git.StatusIncludingIgnored(ctx, repository)
	if err == nil && servicePathEqual(repository, g.target) {
		g.statusCalls++
		if !g.mutated && g.statusCalls == 2 {
			g.mutated = true
			if writeErr := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("# pre-plan user edit\n"), 0o644); writeErr != nil {
				return gitadapter.Status{}, writeErr
			}
		}
	}
	return status, err
}

// cleanupBoundaryCreateGit delegates status inspection to the real adapter,
// then introduces one concurrent change before cleanup crosses its ownership
// boundary. The returned status is deliberately stale, reproducing the exact
// last-observation-to-destruction window rather than an earlier planning race.
type cleanupBoundaryCreateGit struct {
	*failingCreateGit
	target  string
	mutate  func(string) error
	mutated bool
}

// postUnregisterPublicRecreationGit recreates the original public name only
// after the real missing-owned Git unregister has succeeded.
type postUnregisterPublicRecreationGit struct {
	*failingCreateGit
	target      string
	publicBytes []byte
	recreated   bool
}

func (g *postUnregisterPublicRecreationGit) RemoveWorktree(ctx context.Context, repository, path string, force bool) error {
	if err := g.failingCreateGit.Git.RemoveWorktree(ctx, repository, path, force); err != nil {
		return err
	}
	if g.recreated {
		return nil
	}
	g.recreated = true
	if err := os.Mkdir(g.target, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(g.target, "public.txt"), g.publicBytes, 0o644)
}

func (g *cleanupBoundaryCreateGit) StatusIncludingIgnored(ctx context.Context, repository string) (gitadapter.Status, error) {
	status, err := g.failingCreateGit.Git.StatusIncludingIgnored(ctx, repository)
	if err != nil || g.mutated || !servicePathEqual(repository, g.target) {
		return status, err
	}
	contents, readErr := os.ReadFile(filepath.Join(repository, ".gitignore"))
	if readErr == nil && bytes.Contains(contents, []byte("/backend/\n")) {
		g.mutated = true
		if mutateErr := g.mutate(repository); mutateErr != nil {
			return gitadapter.Status{}, mutateErr
		}
	}
	return status, err
}

type dirtyRollbackCreateGit struct {
	gitadapter.Git
	target string
	calls  int
}

func (g *dirtyRollbackCreateGit) AddWorktree(ctx context.Context, repository, path, branch string) error {
	g.calls++
	if g.calls == 2 {
		return errors.New("injected child worktree failure")
	}
	if err := g.Git.AddWorktree(ctx, repository, path, branch); err != nil {
		return err
	}
	if path == g.target {
		return os.WriteFile(filepath.Join(path, "unrelated.txt"), []byte("preserve\n"), 0o644)
	}
	return nil
}

func (g *failingCreateGit) CreateBranch(ctx context.Context, repository, branch, base string) error {
	g.calls++
	if g.calls == g.failAt {
		return errors.New("injected Git mutation failure")
	}
	return g.Git.CreateBranch(ctx, repository, branch, base)
}

func (g *failingCreateGit) AddWorktree(ctx context.Context, repository, path, branch string) error {
	g.calls++
	if g.calls == g.failAt {
		return errors.New("injected Git mutation failure")
	}
	return g.Git.AddWorktree(ctx, repository, path, branch)
}

type projectLocker struct{}

func (projectLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	return noOpLock{}, nil
}

type noOpLock struct{}

func (noOpLock) Unlock() error { return nil }

type rollbackFailingCreateGit struct{ gitadapter.Git }

func (g *rollbackFailingCreateGit) AddWorktree(context.Context, string, string, string) error {
	return errors.New("injected add worktree failure")
}

func (g *rollbackFailingCreateGit) DeleteBranch(context.Context, string, string, bool) error {
	return errors.New("injected delete branch rollback failure")
}

type validationFailingCreateGit struct {
	gitadapter.Git
	target string
	calls  int
}

type verificationFailingCreateGit struct {
	gitadapter.Git
	failPath string
	calls    int
}

type secondChildVerificationFailingGit struct {
	gitadapter.Git
	parentPath string
	checks     int
	failAt     int
}

func (g *secondChildVerificationFailingGit) InspectWorkingTreeIgnore(ctx context.Context, repository, mount string) (gitadapter.WorkingTreeIgnoreEvidence, error) {
	if servicePathEqual(repository, g.parentPath) {
		g.checks++
		if g.checks == g.failAt {
			return gitadapter.WorkingTreeIgnoreEvidence{}, errors.New("injected second-child verification failure")
		}
	}
	return g.Git.InspectWorkingTreeIgnore(ctx, repository, mount)
}

func (g *verificationFailingCreateGit) InspectWorkingTreeIgnore(ctx context.Context, repository, mount string) (gitadapter.WorkingTreeIgnoreEvidence, error) {
	g.calls++
	if servicePathEqual(repository, g.failPath) && g.calls > 1 {
		return gitadapter.WorkingTreeIgnoreEvidence{}, errors.New("injected verification failure")
	}
	return g.Git.InspectWorkingTreeIgnore(ctx, repository, mount)
}

type cancelAfterWorktreeGit struct {
	gitadapter.Git
	cancel func()
	once   sync.Once
}

func (g *cancelAfterWorktreeGit) AddWorktree(ctx context.Context, repository, path, branch string) error {
	if err := g.Git.AddWorktree(ctx, repository, path, branch); err != nil {
		return err
	}
	g.once.Do(g.cancel)
	return nil
}

func (g *validationFailingCreateGit) CurrentBranch(ctx context.Context, repository string) (string, bool, error) {
	branch, detached, err := g.Git.CurrentBranch(ctx, repository)
	if servicePathEqual(repository, g.target) {
		g.calls++
		if g.calls == 2 {
			return "unexpected", detached, err
		}
	}
	return branch, detached, err
}

func servicePathEqual(left, right string) bool {
	canonicalLeft, leftErr := filepath.EvalSymlinks(left)
	canonicalRight, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil && rightErr == nil {
		return canonicalLeft == canonicalRight
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
