package service_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

// RED: companion update needs an independent read-only inventory rather than
// changing ListWorkspaces, which intentionally aborts on a bad state file.
func TestCompanionUpdateDryRunKeepsInvalidStateVisibleWithoutMutation(t *testing.T) {
	project, root, _, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	stateDir := service.WorkspaceStateDirectory(data, project.ID)
	bad := filepath.Join(stateDir, "zzz-broken.json")
	firstBad := filepath.Join(stateDir, "aaa-broken.json")
	if err := os.WriteFile(firstBad, []byte("{also-not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.NewCompanionUpdateService().Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
	if err != nil {
		t.Fatalf("dry update = %v", err)
	}
	if result.Version != 1 || result.Operation != "companion-update" || !result.DryRun || len(result.Entries) < 2 || result.Entries[0].Kind != "baseline" || result.Entries[0].Status != "planned" || !result.Entries[0].Deferred {
		t.Fatalf("dry result = %#v", result)
	}
	foundInvalid := []string{}
	for _, entry := range result.Entries {
		if entry.Status == "failed" && entry.Reason == "invalid-state" {
			foundInvalid = append(foundInvalid, entry.Workspace)
		}
	}
	if !reflect.DeepEqual(foundInvalid, []string{"aaa-broken.json", "zzz-broken.json"}) {
		t.Fatalf("invalid state was not retained: %#v", result.Entries)
	}
	after, err := os.ReadFile(bad)
	if err != nil || string(after) != string(before) {
		t.Fatalf("dry run changed invalid state: %q %v", after, err)
	}
}

func TestCompanionUpdateNeverFollowsWorkspaceStateSymlinks(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	stateDir := service.WorkspaceStateDirectory(data, project.ID)
	if err := os.Symlink("default.json", filepath.Join(stateDir, "linked.json")); err != nil {
		t.Skipf("workspace does not permit symlink fixture: %v", err)
	}
	before := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project)
	result, err := service.NewCompanionUpdateService().Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range result.Entries {
		if entry.Workspace == "linked.json" && entry.Reason == "invalid-state" {
			found = true
		}
	}
	if !found {
		t.Fatalf("symlink state authority was not retained as invalid: %#v", result.Entries)
	}
	if after := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project); !reflect.DeepEqual(after, before) {
		t.Fatal("dry run followed or changed symlinked state authority")
	}
}

func TestCompanionUpdateExecutePlanRejectsRetargetedStateSymlinkBeforeFetch(t *testing.T) {
	project, root, _, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	link := filepath.Join(service.WorkspaceStateDirectory(data, project.ID), "linked.json")
	if err := os.Symlink("default.json", link); err != nil {
		t.Skipf("workspace does not permit symlink fixture: %v", err)
	}
	git := &companionCountingFetchGit{Git: gitadapter.NewAdapter("git")}
	serviceValue := service.NewCompanionUpdateServiceWith(git, lock.Manager{})
	plan, err := serviceValue.Plan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("replacement.json", link); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceValue.ExecutePlan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"}, plan); err == nil || git.fetches != 0 {
		t.Fatalf("retargeted symlink execute err=%v fetches=%d", err, git.fetches)
	}
}

func TestCompanionUpdateRejectsOrdinaryRepositoryBeforeFetch(t *testing.T) {
	project, _, _, data := createFixture(t)
	if _, err := service.NewCompanionUpdateService().Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "root", DryRun: true}); err == nil {
		t.Fatal("ordinary repository was accepted")
	}
}

func TestCompanionUpdateRejectsManifestRemoteURLDriftBeforeFetch(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	backend.Run(t, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "different.git"))
	if _, err := service.NewCompanionUpdateService().Plan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"}); err == nil {
		t.Fatal("remote URL drift was accepted")
	}
}

func TestCompanionUpdatePlanDefensivelyCopiesCallerProject(t *testing.T) {
	project, root, _, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	serviceValue := service.NewCompanionUpdateService()
	plan, err := serviceValue.Plan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	for index := range project.Repositories {
		if project.Repositories[index].ID == "backend" {
			project.Repositories[index].DefaultBranch = "mutated"
		}
	}
	result, err := serviceValue.ExecutePlan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true}, plan)
	if err != nil || result.Baseline != "main" || result.Entries[0].Branch != "main" {
		t.Fatalf("defensive plan = %#v, %v", result, err)
	}
}

type companionReceiptErrorGit struct {
	gitadapter.Git
	target string
}

func (g companionReceiptErrorGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}
func (g companionReceiptErrorGit) FastForwardRef(ctx context.Context, repository, branch, old, next string) (gitadapter.FastForwardReceipt, error) {
	receipt, err := g.Git.FastForwardRef(ctx, repository, branch, old, next)
	if err != nil {
		return receipt, err
	}
	return receipt, errors.New("injected post-CAS cancellation")
}

// RED: an adapter may own a newly moved inactive ref while returning an error
// (cancellation/activation after CAS). The service must leave an actionable
// recovery record and must not start workspace work.
func TestCompanionUpdateReceiptErrorRecordsRecoveryAndStopsBeforeWorkspaces(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "stable")
	backend.CommitFile("new.txt", "new\n", "new")
	publishCompanionBaseline(t, project, root, "backend", "stable", false)
	project = reloadCompanionFixtureProject(t, project, data)
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	base := gitadapter.NewAdapter("git")
	writerCalled := false
	serviceValue := service.NewCompanionUpdateServiceWithRecovery(companionReceiptErrorGit{Git: base, target: target}, lock.Manager{}, func(path string, value store.RecoveryRecord, compare func() error) error {
		writerCalled = true
		if err := compare(); err != nil {
			return err
		}
		return store.WriteRecovery(path, value)
	})
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "rollback-incomplete" || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete) || !writerCalled {
		t.Fatalf("receipt result = %#v, %v", result, err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "companion-update-backend.json")
	recovery, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || recovery.Operation != "companion-update" || len(recovery.UnrevertedSteps) != 1 {
		t.Fatalf("recovery = %#v, %v", recovery, readErr)
	}
}

func TestCompanionUpdateReceiptErrorReportsRecoveryWriterFailureWithoutContinuing(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "stable")
	backend.CommitFile("new.txt", "new\n", "new")
	publishCompanionBaseline(t, project, root, "backend", "stable", false)
	project = reloadCompanionFixtureProject(t, project, data)
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.NewCompanionUpdateServiceWithRecovery(companionReceiptErrorGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}, func(string, store.RecoveryRecord, func() error) error { return errors.New("recovery disk full") }).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "rollback-incomplete" || result.Failure == nil || !strings.Contains(result.Failure.Message, "recovery disk full") {
		t.Fatalf("recovery failure result = %#v, %v", result, err)
	}
}

type companionStaticFetchGit struct {
	gitadapter.Git
	target string
}

type companionCountingStaticFetchGit struct {
	gitadapter.Git
	target  string
	fetches int
}

type companionStaticFetchIdentityMismatchGit struct {
	gitadapter.Git
	target, mismatchPath string
}

type companionFetchReceiptFailureGit struct {
	gitadapter.Git
	target            string
	fetches, forwards int
}

type companionForwardReceiptlessErrorGit struct {
	gitadapter.Git
	target string
}

func (g companionForwardReceiptlessErrorGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func (g companionForwardReceiptlessErrorGit) FastForward(ctx context.Context, repository, branch, old, next string) (gitadapter.FastForwardReceipt, error) {
	_, err := g.Git.FastForward(ctx, repository, branch, old, next)
	if err != nil {
		return gitadapter.FastForwardReceipt{}, err
	}
	return gitadapter.FastForwardReceipt{}, errors.New("injected receiptless post-transition failure")
}

func (g *companionFetchReceiptFailureGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	g.fetches++
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, context.Canceled
}

func (g *companionFetchReceiptFailureGit) FastForward(context.Context, string, string, string, string) (gitadapter.FastForwardReceipt, error) {
	g.forwards++
	return gitadapter.FastForwardReceipt{}, errors.New("baseline advanced after failed fetch")
}

func (g *companionCountingStaticFetchGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	g.fetches++
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func (g companionStaticFetchIdentityMismatchGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func (g companionStaticFetchIdentityMismatchGit) CommonGitDir(ctx context.Context, path string) (string, error) {
	if filepath.Clean(path) == filepath.Clean(g.mismatchPath) {
		return "unexpected-common-git-dir", nil
	}
	return g.Git.CommonGitDir(ctx, path)
}

type companionFetchMutationGit struct {
	gitadapter.Git
	target string
	mutate func()
}

func (g companionFetchMutationGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	if g.mutate != nil {
		g.mutate()
	}
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

type companionCancelAfterForwardGit struct {
	gitadapter.Git
	cancel context.CancelFunc
	target string
}

type companionCancelAfterWorkspaceForwardGit struct {
	gitadapter.Git
	cancel   context.CancelFunc
	target   string
	forwards int
	restore  error
}

type companionCancelAfterAncestryGit struct {
	gitadapter.Git
	cancel context.CancelFunc
	target string
	calls  int
}

func (g *companionCancelAfterAncestryGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func (g *companionCancelAfterAncestryGit) IsAncestor(ctx context.Context, repository, ancestor, descendant string) (bool, error) {
	result, err := g.Git.IsAncestor(ctx, repository, ancestor, descendant)
	if err == nil {
		g.calls++
		// The third successful ancestry fact is the unchanged named
		// workspace's baseline containment check: baseline owner validation is
		// first and persisted-to-observed validation is second.
		if g.calls == 3 {
			g.cancel()
		}
	}
	return result, err
}

func (g companionCancelAfterForwardGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func (g companionCancelAfterForwardGit) FastForward(ctx context.Context, repository, branch, old, next string) (gitadapter.FastForwardReceipt, error) {
	receipt, err := g.Git.FastForward(ctx, repository, branch, old, next)
	if err == nil {
		g.cancel()
	}
	return receipt, err
}

func (g *companionCancelAfterWorkspaceForwardGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func (g *companionCancelAfterWorkspaceForwardGit) FastForward(ctx context.Context, repository, branch, old, next string) (gitadapter.FastForwardReceipt, error) {
	receipt, err := g.Git.FastForward(ctx, repository, branch, old, next)
	if err == nil {
		g.forwards++
		if g.forwards == 2 {
			g.cancel()
		}
	}
	return receipt, err
}

func (g *companionCancelAfterWorkspaceForwardGit) RestoreFastForward(ctx context.Context, repository string, receipt gitadapter.FastForwardReceipt) error {
	if g.restore != nil {
		return g.restore
	}
	return g.Git.RestoreFastForward(ctx, repository, receipt)
}

type companionDryRunGit struct {
	gitadapter.Git
	fetches int
}

type companionCancelDryRunBaselineGit struct {
	gitadapter.Git
	cancel                                                func()
	branchCalls                                           int
	commonCalls, commonAfterCancellation                  int
	fetches, forwards, refForwards, restores, refRestores int
}

func (g *companionCancelDryRunBaselineGit) BranchCheckedOut(ctx context.Context, repository, branch string) (bool, error) {
	checkedOut, err := g.Git.BranchCheckedOut(ctx, repository, branch)
	g.branchCalls++
	g.cancel()
	return checkedOut, err
}

func (g *companionCancelDryRunBaselineGit) CommonGitDir(ctx context.Context, repository string) (string, error) {
	g.commonCalls++
	if ctx.Err() != nil {
		g.commonAfterCancellation++
	}
	return g.Git.CommonGitDir(ctx, repository)
}

func (g *companionCancelDryRunBaselineGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	g.fetches++
	return gitadapter.ConfiguredRefFetch{}, errors.New("dry run fetched")
}

func (g *companionCancelDryRunBaselineGit) FastForward(context.Context, string, string, string, string) (gitadapter.FastForwardReceipt, error) {
	g.forwards++
	return gitadapter.FastForwardReceipt{}, errors.New("dry run fast-forwarded checkout")
}

func (g *companionCancelDryRunBaselineGit) FastForwardRef(context.Context, string, string, string, string) (gitadapter.FastForwardReceipt, error) {
	g.refForwards++
	return gitadapter.FastForwardReceipt{}, errors.New("dry run fast-forwarded ref")
}

func (g *companionCancelDryRunBaselineGit) RestoreFastForward(context.Context, string, gitadapter.FastForwardReceipt) error {
	g.restores++
	return errors.New("dry run restored checkout")
}

func (g *companionCancelDryRunBaselineGit) RestoreFastForwardRef(context.Context, string, gitadapter.FastForwardReceipt) error {
	g.refRestores++
	return errors.New("dry run restored ref")
}

func (g *companionDryRunGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	g.fetches++
	return gitadapter.ConfiguredRefFetch{}, errors.New("dry run fetched")
}

type companionDryRunLocker struct{ called bool }

func (l *companionDryRunLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	l.called = true
	return nil, errors.New("dry run locked")
}

// companionDryRunEffectsGit turns every mutating Git entry point used by
// companion update into an observable test failure. Its fact overrides model
// independently discovered bad checkouts without weakening the source
// companion observations made while planning.
type companionDryRunEffectsGit struct {
	gitadapter.Git
	fetches, forwards, refForwards, restores, refRestores int
	commonMismatch, topMismatch                           string
	forceBaselineCheckedOut                               bool
}

func (g *companionDryRunEffectsGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	g.fetches++
	return gitadapter.ConfiguredRefFetch{}, errors.New("dry run fetched")
}
func (g *companionDryRunEffectsGit) FastForward(context.Context, string, string, string, string) (gitadapter.FastForwardReceipt, error) {
	g.forwards++
	return gitadapter.FastForwardReceipt{}, errors.New("dry run fast-forwarded checkout")
}
func (g *companionDryRunEffectsGit) FastForwardRef(context.Context, string, string, string, string) (gitadapter.FastForwardReceipt, error) {
	g.refForwards++
	return gitadapter.FastForwardReceipt{}, errors.New("dry run fast-forwarded ref")
}
func (g *companionDryRunEffectsGit) RestoreFastForward(context.Context, string, gitadapter.FastForwardReceipt) error {
	g.restores++
	return errors.New("dry run restored checkout")
}
func (g *companionDryRunEffectsGit) RestoreFastForwardRef(context.Context, string, gitadapter.FastForwardReceipt) error {
	g.refRestores++
	return errors.New("dry run restored ref")
}
func (g *companionDryRunEffectsGit) CommonGitDir(ctx context.Context, path string) (string, error) {
	if filepath.Clean(path) == filepath.Clean(g.commonMismatch) {
		return "different-common-git-dir", nil
	}
	return g.Git.CommonGitDir(ctx, path)
}
func (g *companionDryRunEffectsGit) TopLevel(ctx context.Context, path string) (string, error) {
	if filepath.Clean(path) == filepath.Clean(g.topMismatch) {
		return filepath.Join(path, "not-the-top-level"), nil
	}
	return g.Git.TopLevel(ctx, path)
}
func (g *companionDryRunEffectsGit) BranchCheckedOut(ctx context.Context, repository, branch string) (bool, error) {
	if g.forceBaselineCheckedOut {
		return true, nil
	}
	return g.Git.BranchCheckedOut(ctx, repository, branch)
}

func TestCompanionUpdateDryRunAcquiresNoLockAndFetchesNothing(t *testing.T) {
	project, root, _, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	git := &companionDryRunGit{Git: gitadapter.NewAdapter("git")}
	locker := &companionDryRunLocker{}
	result, err := service.NewCompanionUpdateServiceWith(git, locker).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
	if err != nil || git.fetches != 0 || locker.called || !result.DryRun || len(result.Entries) == 0 || !result.Entries[0].Deferred {
		t.Fatalf("dry no-effect result=%#v err=%v fetches=%d locked=%t", result, err, git.fetches, locker.called)
	}
}

func TestCompanionUpdateDryRunCancellationStopsObservationsAndMarksEligibleEntriesCanceled(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"alpha", "zeta"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := createFixtureWorkspace(t, project, "removed", filepath.Join(t.TempDir(), "removed"), data)
	if err != nil {
		t.Fatal(err)
	}
	removedState := companionUpdateReadState(t, data, project.ID, removed.WorkspaceID)
	removedPath := removedState.Repositories["backend"].ResolvedPath
	backend.Run(t, "worktree", "remove", "--force", removedPath)
	if _, err := os.Lstat(removedPath); !os.IsNotExist(err) {
		t.Fatalf("removed workspace remains: %v", err)
	}
	stateDir := service.WorkspaceStateDirectory(data, project.ID)
	if err := os.WriteFile(filepath.Join(stateDir, "aaa-broken.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	git := &companionCancelDryRunBaselineGit{Git: gitadapter.NewAdapter("git"), cancel: cancel}
	locker := &companionDryRunLocker{}
	stateWrites, recoveryWrites := 0, 0
	result, err := service.NewCompanionUpdateServiceWithDependencies(git, locker,
		func(string, store.WorkspaceState, func() error) error {
			stateWrites++
			return errors.New("dry run wrote state")
		},
		func(string, store.RecoveryRecord, func() error) error {
			recoveryWrites++
			return errors.New("dry run wrote recovery")
		},
	).Execute(ctx, service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
	if err != nil || result.Status != "failed" || result.Failure == nil || result.Failure.Code != string(service.ErrorInternal) {
		t.Fatalf("dry-run cancellation result=%#v err=%v", result, err)
	}
	if git.branchCalls != 1 || git.commonAfterCancellation != 0 {
		t.Fatalf("dry-run cancellation observations branch=%d common=%d common-after-cancellation=%d entries=%#v", git.branchCalls, git.commonCalls, git.commonAfterCancellation, result.Entries)
	}
	wantNames := []string{"", "default", "aaa-broken.json", "alpha", "zeta"}
	if len(result.Entries) != len(wantNames) {
		t.Fatalf("dry-run cancellation entries=%#v", result.Entries)
	}
	for index, entry := range result.Entries {
		if entry.Action != "none" || entry.Workspace != wantNames[index] {
			t.Fatalf("dry-run cancellation entry[%d]=%#v", index, entry)
		}
		if entry.Workspace == "aaa-broken.json" {
			if entry.Status != "failed" || entry.Deferred || entry.Reason != "invalid-state" {
				t.Fatalf("invalid state was concealed by cancellation: %#v", entry)
			}
			continue
		}
		if entry.Status != "canceled" || entry.Deferred || entry.Reason != "" {
			t.Fatalf("eligible canceled dry-run entry=%#v", entry)
		}
	}
	if locker.called || git.fetches != 0 || git.forwards != 0 || git.refForwards != 0 || git.restores != 0 || git.refRestores != 0 || stateWrites != 0 || recoveryWrites != 0 {
		t.Fatalf("dry-run cancellation effects lock=%t fetch=%d ff=%d ref-ff=%d restore=%d ref-restore=%d state=%d recovery=%d", locker.called, git.fetches, git.forwards, git.refForwards, git.restores, git.refRestores, stateWrites, recoveryWrites)
	}
	if after := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project); !reflect.DeepEqual(after, snapshot) {
		t.Fatal("dry-run cancellation changed refs, HEAD/index/worktrees, state, configuration, manifest, or recovery bytes")
	}
}

// RED: dry-run must give a complete, deterministic local account of the
// independently inventoried state files while leaving every local generation
// exactly as it was. In particular, it may not acquire mutation authority to
// discover facts that Git and state files already make available.
func TestCompanionUpdateDryRunClassifiesMixedLocalFactsWithoutAnyEffect(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)

	paths, ids := map[string]string{}, map[string]string{}
	for _, name := range []string{"identity", "top", "detached", "dirty", "recovery", "rewritten", "removed", "valid"} {
		value, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		state := companionUpdateReadState(t, data, project.ID, value.WorkspaceID)
		paths[name] = state.Repositories["backend"].ResolvedPath
		if paths[name] == "" {
			t.Fatalf("workspace %s has no backend checkout: %#v", name, value.Repositories)
		}
		ids[name] = value.WorkspaceID
	}
	// Make the recorded main baseline inactive. The default state remains a
	// present checkout record and therefore supplies the required default-first
	// branch-mismatch entry.
	backend.Run(t, "checkout", "-b", "dry-run-source")
	companionUpdateGit(t, paths["detached"], "checkout", "--detach")
	if err := os.WriteFile(filepath.Join(paths["dirty"], "dry-run-dirty"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rewritten := companionUpdateReadState(t, data, project.ID, ids["rewritten"])
	backend.Run(t, "branch", "dry-run-rewritten", rewritten.Repositories["backend"].Head)
	backend.Run(t, "checkout", "dry-run-rewritten")
	backend.CommitFile("dry-run-rewritten.txt", "side history\n", "dry-run side history")
	sideHead, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "checkout", "dry-run-source")
	checkout := rewritten.Repositories["backend"]
	checkout.Head = sideHead
	rewritten.Repositories["backend"] = checkout
	companionUpdateWriteState(t, data, project.ID, ids["rewritten"], rewritten)

	// This removes a real temporary worktree but deliberately preserves its
	// valid state file. Dry-run must omit it rather than report a mutation.
	backend.Run(t, "worktree", "remove", "--force", paths["removed"])
	if _, err := os.Lstat(paths["removed"]); !os.IsNotExist(err) {
		t.Fatalf("removed checkout remains: %v", err)
	}
	stateDir := service.WorkspaceStateDirectory(data, project.ID)
	if err := os.WriteFile(filepath.Join(stateDir, "aaa-broken.json"), []byte("{broken-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "zzz-broken.json"), []byte("{broken-z"), 0o600); err != nil {
		t.Fatal(err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", ids["recovery"]+".json")
	if err := os.MkdirAll(filepath.Dir(recoveryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryPath, []byte("{\"workspaceRecovery\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	identityState := companionUpdateReadState(t, data, project.ID, ids["identity"])
	topState := companionUpdateReadState(t, data, project.ID, ids["top"])
	snapshot := companionUpdateSnapshot(t, backend.Path, append([]string{backend.Path}, companionPresentPaths(paths)...), data, project)
	git := &companionDryRunEffectsGit{Git: gitadapter.NewAdapter("git"), commonMismatch: identityState.Repositories["backend"].ResolvedPath, topMismatch: topState.Repositories["backend"].ResolvedPath}
	locker := &companionDryRunLocker{}
	stateWrites, recoveryWrites := 0, 0
	result, err := service.NewCompanionUpdateServiceWithDependencies(git, locker,
		func(string, store.WorkspaceState, func() error) error {
			stateWrites++
			return errors.New("dry run wrote state")
		},
		func(string, store.RecoveryRecord, func() error) error {
			recoveryWrites++
			return errors.New("dry run wrote recovery")
		},
	).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Status != "failed" || !result.DryRun {
		t.Fatalf("dry result = %#v", result)
	}
	wantNames := []string{"", "default", "aaa-broken.json", "detached", "dirty", "identity", "recovery", "rewritten", "top", "valid", "zzz-broken.json"}
	wantReasons := map[string]string{
		"default": "branch-mismatch", "aaa-broken.json": "invalid-state", "detached": "detached", "dirty": "dirty",
		"identity": "identity-mismatch", "recovery": "recovery-blocked", "rewritten": "rewritten", "top": "identity-mismatch", "zzz-broken.json": "invalid-state",
	}
	if len(result.Entries) != len(wantNames) {
		t.Fatalf("entry count=%d entries=%#v", len(result.Entries), result.Entries)
	}
	for index, entry := range result.Entries {
		if index == 0 {
			if entry.Kind != "baseline" || entry.Status != "planned" || !entry.Deferred || entry.Branch != "main" {
				t.Fatalf("baseline entry=%#v", entry)
			}
			continue
		}
		if entry.Kind != "workspace" || entry.Workspace != wantNames[index] || entry.Reason != wantReasons[entry.Workspace] {
			t.Fatalf("entry %d=%#v, want workspace=%q reason=%q", index, entry, wantNames[index], wantReasons[wantNames[index]])
		}
		if entry.Workspace == "valid" {
			if entry.Status != "planned" || !entry.Deferred {
				t.Fatalf("valid local entry must defer only remote facts: %#v", entry)
			}
		} else if entry.Status != "failed" || entry.Deferred {
			t.Fatalf("local fact must be settled: %#v", entry)
		}
	}
	if locker.called || git.fetches != 0 || git.forwards != 0 || git.refForwards != 0 || git.restores != 0 || git.refRestores != 0 || stateWrites != 0 || recoveryWrites != 0 {
		t.Fatalf("dry-run effects lock=%t fetch=%d ff=%d ref-ff=%d restore=%d ref-restore=%d state=%d recovery=%d", locker.called, git.fetches, git.forwards, git.refForwards, git.restores, git.refRestores, stateWrites, recoveryWrites)
	}
	if after := companionUpdateSnapshot(t, backend.Path, append([]string{backend.Path}, companionPresentPaths(paths)...), data, project); !reflect.DeepEqual(after, snapshot) {
		t.Fatal("dry-run changed refs, HEAD/index/worktrees, state, configuration, manifest, or recovery bytes")
	}
}

func TestCompanionUpdateDryRunClassifiesActiveBaselineAuthorityWithoutAnyEffect(t *testing.T) {
	for _, test := range []struct {
		name, reason string
	}{
		{name: "no unique valid owner", reason: "missing-baseline"},
		{name: "dirty owner", reason: "dirty-baseline"},
		{name: "rewritten owner", reason: "diverged-baseline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			git := &companionDryRunEffectsGit{Git: gitadapter.NewAdapter("git")}
			switch test.name {
			case "no unique valid owner":
				backend.Run(t, "checkout", "-b", "not-main")
				git.forceBaselineCheckedOut = true
			case "dirty owner":
				if err := os.WriteFile(filepath.Join(backend.Path, "dry-run-dirty-baseline"), []byte("dirty\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "rewritten owner":
				state := companionUpdateReadState(t, data, project.ID, "default")
				backend.Run(t, "checkout", "-b", "dry-run-baseline-side")
				backend.CommitFile("dry-run-baseline-side.txt", "side history\n", "dry-run side history")
				side, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
				if err != nil {
					t.Fatal(err)
				}
				backend.Run(t, "checkout", "main")
				checkout := state.Repositories["backend"]
				checkout.Head = side
				state.Repositories["backend"] = checkout
				companionUpdateWriteState(t, data, project.ID, "default", state)
			}
			snapshot := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project)
			locker := &companionDryRunLocker{}
			result, err := service.NewCompanionUpdateServiceWithDependencies(git, locker,
				func(string, store.WorkspaceState, func() error) error { return errors.New("dry run wrote state") },
				func(string, store.RecoveryRecord, func() error) error { return errors.New("dry run wrote recovery") },
			).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
			if err != nil || len(result.Entries) == 0 || result.Entries[0].Reason != test.reason || result.Entries[0].Status != "failed" || result.Entries[0].Deferred {
				t.Fatalf("active baseline result=%#v err=%v", result, err)
			}
			if locker.called || git.fetches != 0 || git.forwards != 0 || git.refForwards != 0 || git.restores != 0 || git.refRestores != 0 {
				t.Fatalf("active dry-run effects lock=%t fetch=%d ff=%d ref-ff=%d restore=%d ref-restore=%d", locker.called, git.fetches, git.forwards, git.refForwards, git.restores, git.refRestores)
			}
			if after := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project); !reflect.DeepEqual(after, snapshot) {
				t.Fatal("active dry-run changed local bytes")
			}
		})
	}
}

func TestCompanionUpdateDryRunProjectRecoveryIsTypedPreResultWithoutAnyEffect(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "project-authority.json")
	if err := os.MkdirAll(filepath.Dir(recoveryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryPath, []byte("{\"existing\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project)
	git := &companionDryRunEffectsGit{Git: gitadapter.NewAdapter("git")}
	locker := &companionDryRunLocker{}
	result, err := service.NewCompanionUpdateServiceWithDependencies(git, locker,
		func(string, store.WorkspaceState, func() error) error { return errors.New("dry run wrote state") },
		func(string, store.RecoveryRecord, func() error) error { return errors.New("dry run wrote recovery") },
	).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorConflict || result.Version != 0 || len(result.Entries) != 0 {
		t.Fatalf("project recovery result=%#v err=%v", result, err)
	}
	if locker.called || git.fetches != 0 || git.forwards != 0 || git.refForwards != 0 || git.restores != 0 || git.refRestores != 0 {
		t.Fatalf("project recovery dry-run effects lock=%t fetch=%d ff=%d ref-ff=%d restore=%d ref-restore=%d", locker.called, git.fetches, git.forwards, git.refForwards, git.restores, git.refRestores)
	}
	if after := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project); !reflect.DeepEqual(after, snapshot) {
		t.Fatal("project-recovery dry-run changed local bytes")
	}
}

func TestCompanionUpdateDryRunRecoveryNodesFailClosedWithoutAnyEffect(t *testing.T) {
	for _, test := range []struct {
		name, recoveryName string
		known              bool
		create             func(*testing.T, string)
	}{
		{name: "known workspace directory", recoveryName: "default.json", known: true, create: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "known workspace dangling symlink", recoveryName: "default.json", known: true, create: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Symlink("missing-recovery.json", path); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
		}},
		{name: "unknown recovery directory", recoveryName: "unknown.json", create: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown dangling recovery symlink", recoveryName: "unknown.json", create: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Symlink("missing-recovery.json", path); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", test.recoveryName)
			if err := os.MkdirAll(filepath.Dir(recoveryPath), 0o700); err != nil {
				t.Fatal(err)
			}
			test.create(t, recoveryPath)
			snapshot := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project)
			git := &companionDryRunEffectsGit{Git: gitadapter.NewAdapter("git")}
			locker := &companionDryRunLocker{}
			result, err := service.NewCompanionUpdateServiceWithDependencies(git, locker,
				func(string, store.WorkspaceState, func() error) error { return errors.New("dry run wrote state") },
				func(string, store.RecoveryRecord, func() error) error { return errors.New("dry run wrote recovery") },
			).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
			if test.known {
				if err != nil {
					t.Fatalf("known recovery dry run: %v", err)
				}
				found := false
				for _, entry := range result.Entries {
					if entry.Workspace == "default" && entry.Status == "failed" && entry.Reason == "recovery-blocked" && !entry.Deferred {
						found = true
					}
				}
				if !found {
					t.Fatalf("known recovery result=%#v", result)
				}
			} else {
				var application *service.Error
				if !errors.As(err, &application) || application.Kind != service.ErrorConflict || result.Version != 0 || len(result.Entries) != 0 {
					t.Fatalf("unknown recovery result=%#v err=%v", result, err)
				}
			}
			if locker.called || git.fetches != 0 || git.forwards != 0 || git.refForwards != 0 || git.restores != 0 || git.refRestores != 0 {
				t.Fatalf("recovery node dry-run effects lock=%t fetch=%d ff=%d ref-ff=%d restore=%d ref-restore=%d", locker.called, git.fetches, git.forwards, git.refForwards, git.restores, git.refRestores)
			}
			if after := companionUpdateSnapshot(t, backend.Path, []string{backend.Path}, data, project); !reflect.DeepEqual(after, snapshot) {
				t.Fatal("recovery-node dry-run changed exact bytes or node kinds")
			}
		})
	}
}

type companionCountingFetchGit struct {
	gitadapter.Git
	fetches int
}

func (g *companionCountingFetchGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	g.fetches++
	return gitadapter.ConfiguredRefFetch{}, errors.New("unexpected fetch")
}

func TestCompanionUpdateExecutePlanRejectsRemoteURLDriftUnderLockBeforeFetch(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	git := &companionCountingFetchGit{Git: gitadapter.NewAdapter("git")}
	serviceValue := service.NewCompanionUpdateServiceWith(git, lock.Manager{})
	plan, err := serviceValue.Plan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "changed.git"))
	if _, err := serviceValue.ExecutePlan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"}, plan); err == nil || git.fetches != 0 {
		t.Fatalf("drift execute err=%v fetches=%d", err, git.fetches)
	}
}

func TestCompanionUpdateExecutePlanRejectsExactSourceAndInvalidStateInventoryDriftBeforeFetch(t *testing.T) {
	for _, mutation := range []string{"source", "invalid-state"} {
		t.Run(mutation, func(t *testing.T) {
			project, root, _, data := createFixture(t)
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			git := &companionCountingFetchGit{Git: gitadapter.NewAdapter("git")}
			serviceValue := service.NewCompanionUpdateServiceWith(git, lock.Manager{})
			plan, err := serviceValue.Plan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "source" {
				path := project.ConfigPath
				if err := os.WriteFile(path, append(companionUpdateReadFile(t, path), []byte("# generation drift\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(service.WorkspaceStateDirectory(data, project.ID), "late-invalid.json"), []byte("{not-json"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := serviceValue.ExecutePlan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"}, plan); err == nil || git.fetches != 0 {
				t.Fatalf("mutation %s execute err=%v fetches=%d", mutation, err, git.fetches)
			}
		})
	}
}

type companionRestoreFailGit struct {
	gitadapter.Git
	target  string
	restore func(context.Context, string, gitadapter.FastForwardReceipt) error
}

type companionUnobservableRestoreGit struct {
	gitadapter.Git
	target, branch string
}

func (g companionRestoreFailGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}
func (g companionRestoreFailGit) RestoreFastForward(ctx context.Context, repository string, receipt gitadapter.FastForwardReceipt) error {
	return g.restore(ctx, repository, receipt)
}

func (g companionUnobservableRestoreGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func (g companionUnobservableRestoreGit) RestoreFastForward(context.Context, string, gitadapter.FastForwardReceipt) error {
	return errors.New("injected restore failure")
}

func (g companionUnobservableRestoreGit) ResolveRef(ctx context.Context, repository, ref string) (string, error) {
	if ref == "refs/heads/"+g.branch {
		return "", errors.New("injected ref observation unavailable")
	}
	return g.Git.ResolveRef(ctx, repository, ref)
}

func TestCompanionUpdateActiveRestoreFailureRecordsOwnedBaselineResidual(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", "HEAD~1")
	project = reloadCompanionFixtureProject(t, project, data)
	serviceValue := service.NewCompanionUpdateServiceWithDependencies(companionRestoreFailGit{Git: gitadapter.NewAdapter("git"), target: target, restore: func(context.Context, string, gitadapter.FastForwardReceipt) error {
		return errors.New("restore failed")
	}}, lock.Manager{}, func(string, store.WorkspaceState, func() error) error { return errors.New("state writer failed") }, nil)
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "rollback-incomplete" || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete) {
		t.Fatalf("owned restore result = %#v, %v", result, err)
	}
	recovery, err := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", "companion-update-backend.json"))
	if err != nil || len(recovery.UnrevertedSteps) != 1 {
		t.Fatalf("owned recovery = %#v, %v", recovery, err)
	}
}

func TestCompanionUpdateActiveRestoreFailurePreservesForeignBaselineGeneration(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("foreign.txt", "foreign\n", "foreign")
	foreign, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", "HEAD~2")
	project = reloadCompanionFixtureProject(t, project, data)
	serviceValue := service.NewCompanionUpdateServiceWithDependencies(companionRestoreFailGit{Git: gitadapter.NewAdapter("git"), target: target, restore: func(_ context.Context, _ string, receipt gitadapter.FastForwardReceipt) error {
		backend.Run(t, "update-ref", "refs/heads/main", foreign, receipt.NewCommit)
		return errors.New("restore ownership lost")
	}}, lock.Manager{}, func(string, store.WorkspaceState, func() error) error { return errors.New("state writer failed") }, nil)
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "rollback-incomplete" || result.Failure == nil {
		t.Fatalf("foreign restore result = %#v, %v", result, err)
	}
	got, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil || got != foreign {
		t.Fatalf("foreign head = %q, %v; want %q", got, err, foreign)
	}
	recovery, err := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", "companion-update-backend.json"))
	if err != nil || len(recovery.UnrevertedSteps) != 1 || !strings.Contains(recovery.UnrevertedSteps[0], "worktree-index=possible@") || !strings.Contains(recovery.RollbackFailures[0].Error, "ownership lost") {
		t.Fatalf("foreign recovery = %#v, %v", recovery, err)
	}
}

func TestCompanionUpdateActiveStatePublicationFailureRestoresOwnedGeneration(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		advance bool
	}{
		{name: "changed-active-baseline", advance: true},
		{name: "unchanged-active-baseline"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			if _, err := createFixtureWorkspace(t, project, "zz-workspace", filepath.Join(t.TempDir(), "workspace"), data); err != nil {
				t.Fatal(err)
			}
			project = reloadCompanionFixtureProject(t, project, data)
			beforeState := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, "default"))
			serviceValue := service.NewCompanionUpdateServiceWithDependencies(gitadapter.NewAdapter("git"), lock.Manager{}, func(string, store.WorkspaceState, func() error) error {
				return errors.New("injected active state writer failure")
			}, nil)
			updatePlan, err := serviceValue.Plan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
			if err != nil {
				t.Fatal(err)
			}
			target := old
			if scenario.advance {
				backend.CommitFile("target.txt", "target\n", "target")
				target, err = gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
				if err != nil {
					t.Fatal(err)
				}
				backend.Run(t, "push", "origin", "main")
				backend.Run(t, "reset", "--hard", old)
			}
			result, err := serviceValue.ExecutePlan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"}, updatePlan)
			if err != nil || result.Status != "failed" || len(result.Entries) != 1 {
				t.Fatalf("active publication result=%#v err=%v", result, err)
			}
			entry := result.Entries[0]
			if entry.Kind != "baseline" || entry.Reason != "state-publication-failed" || entry.Status != "failed" || entry.Action != map[bool]string{true: "none", false: "unchanged"}[scenario.advance] {
				t.Fatalf("active publication entry=%#v", entry)
			}
			if scenario.advance && entry.ResultingHEAD != old {
				t.Fatalf("restored active entry=%#v want old %q", entry, old)
			}
			if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path); headErr != nil || got != old {
				t.Fatalf("active head=%q err=%v want=%q", got, headErr, old)
			}
			if after := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, "default")); !bytes.Equal(after, beforeState) {
				t.Fatal("active state changed after failed publication")
			}
			if scenario.advance {
				if got, refErr := gitadapter.NewAdapter("git").ResolveRef(context.Background(), backend.Path, "refs/remotes/origin/main"); refErr != nil || got != target {
					t.Fatalf("fetched tracking ref=%q err=%v want=%q", got, refErr, target)
				}
			}
		})
	}
}

func (g companionStaticFetchGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: g.target}, nil
}

func TestCompanionUpdateActiveCurrentBaselinePublishesStateOnce(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	backend.CommitFile("observed.txt", "observed\n", "observed")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.NewCompanionUpdateServiceWith(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "completed" || len(result.Entries) != 1 || result.Entries[0].Kind != "baseline" || result.Entries[0].Action != "unchanged" {
		t.Fatalf("active baseline result = %#v, %v", result, err)
	}
	state, err := store.ReadWorkspace(service.WorkspaceStatePath(data, project.ID, "default"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Repositories["backend"].Head != target {
		t.Fatalf("stored baseline head = %q, want %q", state.Repositories["backend"].Head, target)
	}
}

// RED: an inactive baseline is a local-ref-only transaction. The real path
// must use the configured fetch boundary once, retain that fetched generation,
// and avoid inventing a workspace/state effect for a branch no worktree owns.
func TestCompanionUpdateFastForwardsInactiveBaselineOnceWithoutWorkspaceState(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "stable")
	publishCompanionBaseline(t, project, root, "backend", "stable", false)
	project = reloadCompanionFixtureProject(t, project, data)
	old, err := gitadapter.NewAdapter("git").ResolveRef(context.Background(), backend.Path, "refs/heads/stable")
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "checkout", "stable")
	backend.CommitFile("stable.txt", "stable\n", "stable target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	backend.Run(t, "checkout", "main")
	if err := os.Remove(service.WorkspaceStatePath(data, project.ID, "default")); err != nil {
		t.Fatal(err)
	}
	git := &companionCountingStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "completed" || git.fetches != 1 || len(result.Entries) != 1 || result.Entries[0].Action != "fast-forward" || result.Entries[0].PreviousHEAD != old || result.Entries[0].ResultingHEAD != target {
		t.Fatalf("inactive baseline result=%#v err=%v fetches=%d", result, err, git.fetches)
	}
	if got, resolveErr := gitadapter.NewAdapter("git").ResolveRef(context.Background(), backend.Path, "refs/heads/stable"); resolveErr != nil || got != target {
		t.Fatalf("inactive stable ref=%q err=%v want=%q", got, resolveErr, target)
	}
	if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, "default")); !os.IsNotExist(statErr) {
		t.Fatalf("inactive baseline created or changed workspace state: %v", statErr)
	}
}

// RED: a configured baseline may be attached in a named workspace while the
// registered source checkout is on another branch. It has the same single
// baseline transaction and must not be rediscovered as a later workspace.
func TestCompanionUpdateFastForwardsActiveNamedBaselineAndPublishesOnce(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "stable")
	publishCompanionBaseline(t, project, root, "backend", "stable", false)
	project = reloadCompanionFixtureProject(t, project, data)
	old, err := gitadapter.NewAdapter("git").ResolveRef(context.Background(), backend.Path, "refs/heads/stable")
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "checkout", "stable")
	backend.CommitFile("stable.txt", "stable\n", "stable target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	backend.Run(t, "checkout", "main")
	if _, err := createFixtureWorkspace(t, project, "named", filepath.Join(t.TempDir(), "named"), data); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "named")
	if err != nil {
		t.Fatal(err)
	}
	state := companionUpdateReadState(t, data, project.ID, workspace.ID)
	checkout := state.Repositories["backend"]
	testutil.GitRepository{Path: checkout.ResolvedPath}.Run(t, "checkout", "stable")
	checkout.Branch, checkout.Head, checkout.Detached = "stable", old, false
	state.Repositories["backend"] = checkout
	companionUpdateWriteState(t, data, project.ID, workspace.ID, state)
	if err := os.Remove(service.WorkspaceStatePath(data, project.ID, "default")); err != nil {
		t.Fatal(err)
	}
	project = reloadCompanionFixtureProject(t, project, data)
	result, err := service.NewCompanionUpdateServiceWith(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "completed" || len(result.Entries) != 1 || result.Entries[0].Kind != "baseline" || result.Entries[0].Action != "fast-forward" || result.Entries[0].ResultingHEAD != target {
		t.Fatalf("named active baseline result=%#v err=%v", result, err)
	}
	state = companionUpdateReadState(t, data, project.ID, workspace.ID)
	if state.Repositories["backend"].Head != target {
		t.Fatalf("named active baseline state=%q want=%q", state.Repositories["backend"].Head, target)
	}
}

func TestCompanionUpdateFetchReceiptErrorKeepsTrackingReceiptAndSkipsBaselineMutation(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	git := &companionFetchReceiptFailureGit{Git: gitadapter.NewAdapter("git"), target: target}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "fetch-failed" || git.fetches != 1 || git.forwards != 0 {
		t.Fatalf("fetch receipt failure result=%#v err=%v fetches=%d forwards=%d", result, err, git.fetches, git.forwards)
	}
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path); headErr != nil || got != old {
		t.Fatalf("failed fetch moved baseline=%q err=%v want=%q", got, headErr, old)
	}
}

func TestCompanionUpdateRejectsActiveBaselineMovedAfterFetchBeforeStatePublication(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	before := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, "default"))
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	git := companionFetchMutationGit{Git: gitadapter.NewAdapter("git"), target: target, mutate: func() {
		backend.CommitFile("late.txt", "late\n", "late active baseline movement")
	}}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "diverged-baseline" || result.Entries[0].Action != "none" {
		t.Fatalf("late baseline result=%#v err=%v", result, err)
	}
	if after := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, "default")); !bytes.Equal(after, before) {
		t.Fatal("late active baseline movement published a stale state head")
	}
}

func TestCompanionUpdateReceiptlessFastForwardErrorRecordsRecoveryAndStops(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	result, err := service.NewCompanionUpdateServiceWith(companionForwardReceiptlessErrorGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "rollback-incomplete" || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete) {
		t.Fatalf("receiptless fast-forward result=%#v err=%v", result, err)
	}
	if _, readErr := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", "companion-update-backend.json")); readErr != nil {
		t.Fatalf("receiptless fast-forward recovery = %v", readErr)
	}
}

// RED: a state record can change after the project-wide lock revalidation but
// before its independent workspace attempt. That later generation must not
// cause a branch/worktree transition merely for the state CAS to reject it.
func TestCompanionUpdateStaleWorkspaceStateSkipsFastForwardAndContinues(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	if _, err := createFixtureWorkspace(t, project, "feature", filepath.Join(t.TempDir(), "feature"), data); err != nil {
		t.Fatal(err)
	}
	feature, err := service.RequireWorkspace(project, data, "feature")
	if err != nil {
		t.Fatal(err)
	}
	project = reloadCompanionFixtureProject(t, project, data)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	serviceValue := service.NewCompanionUpdateServiceWith(companionFetchMutationGit{Git: gitadapter.NewAdapter("git"), target: target, mutate: func() {
		path := service.WorkspaceStatePath(data, project.ID, feature.ID)
		if err := os.WriteFile(path, append([]byte(" "), companionUpdateReadFile(t, path)...), 0o600); err != nil {
			t.Fatal(err)
		}
	}}, lock.Manager{})
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 2 || result.Entries[1].Reason != "stale-generation" {
		t.Fatalf("stale workspace result = %#v, %v", result, err)
	}
	state := companionUpdateReadState(t, data, project.ID, feature.ID)
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), state.Repositories["backend"].ResolvedPath); headErr != nil || got != old {
		t.Fatalf("stale workspace branch moved to %q, %v; want %q", got, headErr, old)
	}
}

// RED: cancellation that becomes observable just after an active-baseline
// transition must restore the owned ref/worktree before state publication and
// must not start later workspace work.
func TestCompanionUpdateCanceledActiveBaselineRestoresBeforeStatePublication(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := service.NewCompanionUpdateServiceWith(companionCancelAfterForwardGit{Git: gitadapter.NewAdapter("git"), cancel: cancel, target: target}, lock.Manager{}).Execute(ctx, service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Status != "canceled" || result.Entries[0].Action != "none" {
		t.Fatalf("canceled baseline result = %#v, %v", result, err)
	}
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path); headErr != nil || got != old {
		t.Fatalf("canceled baseline head = %q, %v; want %q", got, headErr, old)
	}
	state := companionUpdateReadState(t, data, project.ID, "default")
	if state.Repositories["backend"].Head != old {
		t.Fatalf("canceled baseline published state %q, want %q", state.Repositories["backend"].Head, old)
	}
}

func TestCompanionUpdateCanceledUnchangedWorkspaceDoesNotPublishState(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	if _, err := createFixtureWorkspace(t, project, "feature", filepath.Join(t.TempDir(), "feature"), data); err != nil {
		t.Fatal(err)
	}
	feature, err := service.RequireWorkspace(project, data, "feature")
	if err != nil {
		t.Fatal(err)
	}
	project = reloadCompanionFixtureProject(t, project, data)
	before := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, feature.ID))
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	git := &companionCancelAfterAncestryGit{Git: gitadapter.NewAdapter("git"), cancel: cancel, target: target}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(ctx, service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 2 || result.Entries[1].Status != "canceled" || result.Entries[1].Action != "unchanged" {
		t.Fatalf("canceled unchanged workspace result = %#v, %v", result, err)
	}
	if after := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, feature.ID)); !bytes.Equal(after, before) {
		t.Fatal("canceled unchanged workspace published state")
	}
}

func TestCompanionUpdateDirtyActiveBaselineStopsBeforeWorkspaces(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	if err := os.WriteFile(filepath.Join(backend.Path, "dirty"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.NewCompanionUpdateServiceWith(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "dirty-baseline" {
		t.Fatalf("dirty baseline = %#v, %v", result, err)
	}
}

func TestCompanionUpdateActiveBaselineRequiresExactlyOneValidStateOwner(t *testing.T) {
	for _, scenario := range []string{"missing", "invalid", "multiple"} {
		t.Run(scenario, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			statePath := service.WorkspaceStatePath(data, project.ID, "default")
			if scenario == "missing" {
				if err := os.Remove(statePath); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "invalid" {
				if err := os.WriteFile(statePath, []byte("{malformed"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(service.WorkspaceStateDirectory(data, project.ID), "duplicate.json"), companionUpdateReadFile(t, statePath), 0o600); err != nil {
				t.Fatal(err)
			}
			target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.NewCompanionUpdateServiceWith(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
			if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "missing-baseline" || result.Entries[0].Action != "none" {
				t.Fatalf("%s owner authority result=%#v err=%v", scenario, result, err)
			}
		})
	}
}

// RED: one fetched baseline must settle first, then independently retain every
// safe workspace outcome. This fixture distinguishes exact equality from a
// descendant observed with an older persisted head.
func TestCompanionUpdateOneFetchWorkspaceOutcomeMatrixRetainsSuccesses(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"behind", "equal", "descendant", "diverged", "removed"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	states := map[string]store.WorkspaceState{}
	ids := map[string]string{}
	for _, name := range []string{"behind", "equal", "descendant", "diverged", "removed"} {
		workspace, requireErr := service.RequireWorkspace(project, data, name)
		if requireErr != nil {
			t.Fatal(requireErr)
		}
		ids[name] = workspace.ID
		states[name] = companionUpdateReadState(t, data, project.ID, workspace.ID)
	}
	for _, name := range []string{"equal", "descendant"} {
		checkout := states[name].Repositories["backend"]
		testutil.GitRepository{Path: checkout.ResolvedPath}.Run(t, "merge", "--ff-only", target)
		if name == "equal" {
			checkout.Head = target
			states[name].Repositories["backend"] = checkout
			companionUpdateWriteState(t, data, project.ID, ids[name], states[name])
		}
	}
	descendantCheckout := states["descendant"].Repositories["backend"]
	testutil.GitRepository{Path: descendantCheckout.ResolvedPath}.CommitFile("descendant.txt", "descendant\n", "descendant after baseline")
	descendantHead, err := gitadapter.NewAdapter("git").Head(context.Background(), descendantCheckout.ResolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	diverged := states["diverged"].Repositories["backend"]
	testutil.GitRepository{Path: diverged.ResolvedPath}.CommitFile("local.txt", "local\n", "local divergence")
	removed := states["removed"].Repositories["backend"]
	if err := os.RemoveAll(removed.ResolvedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.WorkspaceStateDirectory(data, project.ID), "aaa-invalid.json"), []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := &companionCountingStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || git.fetches != 1 || len(result.Entries) != 6 || result.Entries[0].Kind != "baseline" {
		t.Fatalf("matrix result=%#v err=%v fetches=%d", result, err, git.fetches)
	}
	entries := map[string]service.CompanionUpdateEntry{}
	for _, entry := range result.Entries[1:] {
		entries[entry.Workspace] = entry
	}
	if entries["aaa-invalid.json"].Reason != "invalid-state" || entries["behind"].Action != "fast-forward" || entries["behind"].Status != "completed" || entries["equal"].Action != "unchanged" || entries["equal"].Status != "completed" || entries["descendant"].Action != "unchanged" || entries["descendant"].ResultingHEAD != descendantHead || entries["diverged"].Reason != "diverged" {
		t.Fatalf("matrix entries=%#v", result.Entries)
	}
	for _, name := range []string{"behind", "equal", "descendant"} {
		expected := target
		if name == "descendant" {
			expected = descendantHead
		}
		state := companionUpdateReadState(t, data, project.ID, ids[name])
		checkout := state.Repositories["backend"]
		if checkout.Head != expected {
			t.Fatalf("%s state head=%q want=%q", name, checkout.Head, expected)
		}
		if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), checkout.ResolvedPath); headErr != nil || got != expected {
			t.Fatalf("%s checkout head=%q err=%v want=%q", name, got, headErr, expected)
		}
	}
	if state := companionUpdateReadState(t, data, project.ID, ids["diverged"]); state.Repositories["backend"].Head != old {
		t.Fatalf("diverged state was changed: %#v", state.Repositories["backend"])
	}
}

// RED: settling an inactive baseline is deliberately independent of the
// source checkout.  The first workspace pass must still be default-first and
// retain each later safe result when one fetched stable generation produces a
// mixture of unchanged, advanced, rejected, and removed records.
func TestCompanionUpdateInactiveStableOrdersAndRetainsWorkspaceOutcomes(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "stable")
	publishCompanionBaseline(t, project, root, "backend", "stable", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"behind", "equal", "descendant", "diverged", "dirty", "removed"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	adapter := gitadapter.NewAdapter("git")
	old, err := adapter.ResolveRef(context.Background(), backend.Path, "refs/heads/stable")
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "checkout", "stable")
	backend.CommitFile("target.txt", "target\n", "stable target")
	target, err := adapter.Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	backend.Run(t, "checkout", "main")

	states := map[string]store.WorkspaceState{}
	ids := map[string]string{"default": "default"}
	states["default"] = companionUpdateReadState(t, data, project.ID, "default")
	for _, name := range []string{"behind", "equal", "descendant", "diverged", "dirty", "removed"} {
		workspace, requireErr := service.RequireWorkspace(project, data, name)
		if requireErr != nil {
			t.Fatal(requireErr)
		}
		ids[name] = workspace.ID
		states[name] = companionUpdateReadState(t, data, project.ID, workspace.ID)
	}
	for _, name := range []string{"equal", "descendant"} {
		checkout := states[name].Repositories["backend"]
		testutil.GitRepository{Path: checkout.ResolvedPath}.Run(t, "merge", "--ff-only", target)
		if name == "equal" {
			checkout.Head = target
			states[name].Repositories["backend"] = checkout
			companionUpdateWriteState(t, data, project.ID, ids[name], states[name])
		}
	}
	descendant := states["descendant"].Repositories["backend"]
	testutil.GitRepository{Path: descendant.ResolvedPath}.CommitFile("descendant.txt", "descendant\n", "descendant after stable")
	descendantHead, err := adapter.Head(context.Background(), descendant.ResolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	diverged := states["diverged"].Repositories["backend"]
	testutil.GitRepository{Path: diverged.ResolvedPath}.CommitFile("local.txt", "local\n", "local divergence")
	dirty := states["dirty"].Repositories["backend"]
	if err := os.WriteFile(filepath.Join(dirty.ResolvedPath, "dirty"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed := states["removed"].Repositories["backend"]
	if err := os.RemoveAll(removed.ResolvedPath); err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(service.WorkspaceStateDirectory(data, project.ID), "aaa-invalid.json")
	if err := os.WriteFile(invalidPath, []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeDiverged := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, ids["diverged"]))
	beforeDirty := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, ids["dirty"]))
	beforeRemoved := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, ids["removed"]))
	beforeInvalid := companionUpdateReadFile(t, invalidPath)
	unsafeHeads := map[string]string{}
	for _, name := range []string{"dirty", "diverged"} {
		checkout := states[name].Repositories["backend"]
		unsafeHeads[name], err = adapter.Head(context.Background(), checkout.ResolvedPath)
		if err != nil {
			t.Fatal(err)
		}
	}

	git := &companionCountingStaticFetchGit{Git: adapter, target: target}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || git.fetches != 1 {
		t.Fatalf("stable matrix result=%#v err=%v fetches=%d", result, err, git.fetches)
	}
	wantOrder := []string{"baseline", "default", "aaa-invalid.json", "behind", "descendant", "dirty", "diverged", "equal"}
	gotOrder := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if entry.Kind == "baseline" {
			gotOrder = append(gotOrder, "baseline")
		} else {
			gotOrder = append(gotOrder, entry.Workspace)
		}
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("entry order=%#v want=%#v", gotOrder, wantOrder)
	}
	entries := map[string]service.CompanionUpdateEntry{}
	for _, entry := range result.Entries[1:] {
		entries[entry.Workspace] = entry
	}
	if _, present := entries["removed"]; present {
		t.Fatalf("removed workspace was rendered: %#v", entries["removed"])
	}
	if baseline := result.Entries[0]; baseline.Action != "fast-forward" || baseline.PreviousHEAD != old || baseline.ResultingHEAD != target || baseline.Status != "completed" {
		t.Fatalf("inactive stable baseline=%#v", baseline)
	}
	if entries["aaa-invalid.json"].Reason != "invalid-state" || entries["dirty"].Reason != "dirty" || entries["diverged"].Reason != "diverged" {
		t.Fatalf("unsafe entries=%#v", entries)
	}
	for _, name := range []string{"default", "behind", "equal", "descendant"} {
		expected := target
		if name == "descendant" {
			expected = descendantHead
		}
		entry := entries[name]
		wantAction := "unchanged"
		if name == "default" || name == "behind" {
			wantAction = "fast-forward"
		}
		if entry.Status != "completed" || entry.Action != wantAction || entry.ResultingHEAD != expected {
			t.Fatalf("%s entry=%#v want action=%s head=%s", name, entry, wantAction, expected)
		}
		state := companionUpdateReadState(t, data, project.ID, ids[name])
		if state.Repositories["backend"].Head != expected {
			t.Fatalf("%s state head=%q want=%q", name, state.Repositories["backend"].Head, expected)
		}
		checkout := state.Repositories["backend"]
		if got, headErr := adapter.Head(context.Background(), checkout.ResolvedPath); headErr != nil || got != expected {
			t.Fatalf("%s checkout head=%q err=%v want=%q", name, got, headErr, expected)
		}
	}
	if after := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, ids["diverged"])); !bytes.Equal(after, beforeDiverged) {
		t.Fatal("diverged workspace state changed")
	}
	if after := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, ids["dirty"])); !bytes.Equal(after, beforeDirty) {
		t.Fatal("dirty workspace state changed")
	}
	for _, name := range []string{"dirty", "diverged"} {
		checkout := states[name].Repositories["backend"]
		if got, headErr := adapter.Head(context.Background(), checkout.ResolvedPath); headErr != nil || got != unsafeHeads[name] {
			t.Fatalf("%s checkout head=%q err=%v want=%q", name, got, headErr, unsafeHeads[name])
		}
	}
	if after := companionUpdateReadFile(t, invalidPath); !bytes.Equal(after, beforeInvalid) {
		t.Fatal("invalid state changed")
	}
	if after := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, ids["removed"])); !bytes.Equal(after, beforeRemoved) {
		t.Fatal("removed state changed")
	}
	if got, resolveErr := adapter.ResolveRef(context.Background(), backend.Path, "refs/heads/stable"); resolveErr != nil || got != target {
		t.Fatalf("stable ref=%q err=%v want=%q", got, resolveErr, target)
	}
	if _, statErr := os.Stat(service.WorkspaceStatePath(data, project.ID, ids["removed"])); statErr != nil {
		t.Fatalf("removed state should remain untouched: %v", statErr)
	}
}

// Every local workspace rejection is independent once the baseline is
// settled. This uses real checkouts and state files so the later successful
// workspace proves that the command does not turn a local safety fact into a
// global rollback or early stop.
func TestCompanionUpdateContinuesAfterRealUnsafeWorkspaceFacts(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"branch-mismatch", "detached", "identity", "recovery", "rewritten", "zz-success"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	states := map[string]store.WorkspaceState{}
	ids := map[string]string{}
	for _, name := range []string{"branch-mismatch", "detached", "identity", "recovery", "rewritten", "zz-success"} {
		workspace, err := service.RequireWorkspace(project, data, name)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = workspace.ID
		states[name] = companionUpdateReadState(t, data, project.ID, workspace.ID)
	}
	branchMismatch := states["branch-mismatch"].Repositories["backend"]
	testutil.GitRepository{Path: branchMismatch.ResolvedPath}.Run(t, "checkout", "-b", "other-branch")
	detached := states["detached"].Repositories["backend"]
	testutil.GitRepository{Path: detached.ResolvedPath}.Run(t, "checkout", "--detach")
	identity := states["identity"].Repositories["backend"]
	if err := os.MkdirAll(filepath.Join(data, "projects", project.ID, "recovery"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "projects", project.ID, "recovery", ids["recovery"]+".json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	rewritten := states["rewritten"].Repositories["backend"]
	rewrittenRepository := testutil.GitRepository{Path: rewritten.ResolvedPath}
	rewrittenRepository.Run(t, "checkout", "--orphan", "companion-rewrite-orphan")
	rewrittenRepository.Run(t, "rm", "-rf", ".")
	rewrittenRepository.CommitFile("rewritten.txt", "rewritten\n", "unrelated rewrite")
	rewrittenRepository.Run(t, "branch", "-f", rewritten.Branch, "HEAD")
	rewrittenRepository.Run(t, "checkout", rewritten.Branch)
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.NewCompanionUpdateServiceWith(companionStaticFetchIdentityMismatchGit{Git: gitadapter.NewAdapter("git"), target: target, mismatchPath: identity.ResolvedPath}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" {
		t.Fatalf("unsafe continuation result=%#v err=%v", result, err)
	}
	wantOrder := []string{"baseline", "branch-mismatch", "detached", "identity", "recovery", "rewritten", "zz-success"}
	gotOrder := make([]string, 0, len(result.Entries))
	entries := map[string]service.CompanionUpdateEntry{}
	for _, entry := range result.Entries {
		if entry.Kind == "baseline" {
			gotOrder = append(gotOrder, "baseline")
			continue
		}
		gotOrder = append(gotOrder, entry.Workspace)
		entries[entry.Workspace] = entry
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("entry order=%#v want=%#v", gotOrder, wantOrder)
	}
	for workspace, reason := range map[string]string{
		"branch-mismatch": "branch-mismatch",
		"detached":        "detached",
		"identity":        "identity-mismatch",
		"recovery":        "recovery-blocked",
		"rewritten":       "rewritten",
	} {
		if entry := entries[workspace]; entry.Status != "failed" || entry.Reason != reason || entry.Action != "none" {
			t.Fatalf("%s entry=%#v want reason=%q", workspace, entry, reason)
		}
	}
	if success := entries["zz-success"]; success.Status != "completed" || success.Action != "unchanged" || success.ResultingHEAD != target {
		t.Fatalf("later success=%#v want unchanged at %q", success, target)
	}
}

// A path absent while planning is intentionally excluded, but a checkout
// removed after that immutable inventory has crossed the fetch boundary is a
// real failed entry. The later workspace confirms that this local failure is
// still best-effort rather than a command-wide stop.
func TestCompanionUpdateReportsFetchTimeWorkspaceRemovalAndContinues(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"missing", "zz-success"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	missing, err := service.RequireWorkspace(project, data, "missing")
	if err != nil {
		t.Fatal(err)
	}
	beforeMissing := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, missing.ID))
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	git := companionFetchMutationGit{Git: gitadapter.NewAdapter("git"), target: target, mutate: func() {
		checkout := companionUpdateReadState(t, data, project.ID, missing.ID).Repositories["backend"]
		if err := os.RemoveAll(checkout.ResolvedPath); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" {
		t.Fatalf("fetch-time removal result=%#v err=%v", result, err)
	}
	entries := map[string]service.CompanionUpdateEntry{}
	for _, entry := range result.Entries {
		entries[entry.Workspace] = entry
	}
	if entry := entries["missing"]; entry.Status != "failed" || entry.Action != "none" || entry.Reason != "missing" {
		t.Fatalf("removed-after-plan entry=%#v", entry)
	}
	if entry := entries["zz-success"]; entry.Status != "completed" || entry.Action != "unchanged" || entry.ResultingHEAD != target {
		t.Fatalf("later entry=%#v want unchanged at %q", entry, target)
	}
	if after := companionUpdateReadFile(t, service.WorkspaceStatePath(data, project.ID, missing.ID)); !bytes.Equal(after, beforeMissing) {
		t.Fatal("removed workspace state changed")
	}
}

func TestCompanionUpdateDryRunRetainsPlannedPresentWorkspaceRemovedAfterPlan(t *testing.T) {
	project, root, _, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	if _, err := createFixtureWorkspace(t, project, "missing", filepath.Join(t.TempDir(), "missing"), data); err != nil {
		t.Fatal(err)
	}
	project = reloadCompanionFixtureProject(t, project, data)
	serviceValue := service.NewCompanionUpdateService()
	updatePlan, err := serviceValue.Plan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(companionUpdateReadState(t, data, project.ID, workspace.ID).Repositories["backend"].ResolvedPath); err != nil {
		t.Fatal(err)
	}
	result, err := serviceValue.ExecutePlan(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", DryRun: true}, updatePlan)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]service.CompanionUpdateEntry{}
	for _, entry := range result.Entries {
		entries[entry.Workspace] = entry
	}
	if entry := entries["missing"]; entry.Status != "failed" || entry.Reason != "missing" || entry.Action != "none" {
		t.Fatalf("dry missing entry=%#v", entry)
	}
}

func TestCompanionUpdateCancellationRetainsPlannedPresentWorkspaceRemovedAfterFetch(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	if _, err := createFixtureWorkspace(t, project, "missing", filepath.Join(t.TempDir(), "missing"), data); err != nil {
		t.Fatal(err)
	}
	project = reloadCompanionFixtureProject(t, project, data)
	workspace, err := service.RequireWorkspace(project, data, "missing")
	if err != nil {
		t.Fatal(err)
	}
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	git := companionFetchMutationGit{Git: gitadapter.NewAdapter("git"), target: target, mutate: func() {
		checkout := companionUpdateReadState(t, data, project.ID, workspace.ID).Repositories["backend"]
		if err := os.RemoveAll(checkout.ResolvedPath); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", Progress: func(service.CompanionUpdateEntry) error {
		return errors.New("output closed")
	}})
	if err != nil || result.Status != "failed" || len(result.Entries) != 2 {
		t.Fatalf("canceled removed result=%#v err=%v", result, err)
	}
	if entry := result.Entries[1]; entry.Workspace != "missing" || entry.Status != "canceled" || entry.Action != "none" {
		t.Fatalf("canceled missing entry=%#v", entry)
	}
}

func TestCompanionUpdateWorkspaceStateFailureRollsBackAndContinues(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"failure", "zz-success"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	failure, err := service.RequireWorkspace(project, data, "failure")
	if err != nil {
		t.Fatal(err)
	}
	failureStatePath := service.WorkspaceStatePath(data, project.ID, failure.ID)
	beforeFailure := companionUpdateReadFile(t, failureStatePath)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	serviceValue := service.NewCompanionUpdateServiceWithDependencies(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}, func(path string, state store.WorkspaceState, compare func() error) error {
		if path == failureStatePath {
			if err := compare(); err != nil {
				return err
			}
			return errors.New("injected workspace state failure")
		}
		return store.WriteWorkspaceCAS(path, state, compare)
	}, nil)
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 3 {
		t.Fatalf("workspace rollback result=%#v err=%v", result, err)
	}
	if entry := result.Entries[1]; entry.Workspace != "failure" || entry.Reason != "state-publication-failed" || entry.Action != "none" || entry.ResultingHEAD != old {
		t.Fatalf("failed workspace entry=%#v", entry)
	}
	if after := companionUpdateReadFile(t, failureStatePath); !bytes.Equal(after, beforeFailure) {
		t.Fatal("failed workspace state was changed")
	}
	failureState := companionUpdateReadState(t, data, project.ID, failure.ID)
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), failureState.Repositories["backend"].ResolvedPath); headErr != nil || got != old {
		t.Fatalf("failure checkout head=%q err=%v want=%q", got, headErr, old)
	}
	if entry := result.Entries[2]; entry.Workspace != "zz-success" || entry.Status != "completed" || entry.Action != "fast-forward" || entry.ResultingHEAD != target {
		t.Fatalf("later success entry=%#v", entry)
	}
}

func TestCompanionUpdateObservedDescendantStateFailureLeavesBranchAndContinues(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"failure", "zz-success"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	failure, err := service.RequireWorkspace(project, data, "failure")
	if err != nil {
		t.Fatal(err)
	}
	failureStatePath := service.WorkspaceStatePath(data, project.ID, failure.ID)
	beforeFailure := companionUpdateReadFile(t, failureStatePath)
	failureCheckout := companionUpdateReadState(t, data, project.ID, failure.ID).Repositories["backend"]
	testutil.GitRepository{Path: failureCheckout.ResolvedPath}.CommitFile("descendant.txt", "descendant\n", "safe descendant")
	descendant, err := gitadapter.NewAdapter("git").Head(context.Background(), failureCheckout.ResolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	serviceValue := service.NewCompanionUpdateServiceWithDependencies(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}, func(path string, state store.WorkspaceState, compare func() error) error {
		if path == failureStatePath {
			if err := compare(); err != nil {
				return err
			}
			return errors.New("injected descendant state failure")
		}
		return store.WriteWorkspaceCAS(path, state, compare)
	}, nil)
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 3 {
		t.Fatalf("descendant state failure result=%#v err=%v", result, err)
	}
	if entry := result.Entries[1]; entry.Workspace != "failure" || entry.Action != "unchanged" || entry.ResultingHEAD != descendant || entry.Reason != "state-publication-failed" {
		t.Fatalf("descendant failure entry=%#v", entry)
	}
	if after := companionUpdateReadFile(t, failureStatePath); !bytes.Equal(after, beforeFailure) {
		t.Fatal("descendant failure state changed")
	}
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), failureCheckout.ResolvedPath); headErr != nil || got != descendant {
		t.Fatalf("descendant branch=%q err=%v want=%q", got, headErr, descendant)
	}
	if entry := result.Entries[2]; entry.Workspace != "zz-success" || entry.Status != "completed" || entry.Action != "unchanged" || entry.ResultingHEAD != target {
		t.Fatalf("later unchanged entry=%#v", entry)
	}
}

// RED: a filesystem durability error can be reported after the workspace
// generation has already replaced the old file. The service must use exact
// bytes as its ownership receipt and restore both state and the active ref.
func TestCompanionUpdateActiveBaselinePostReplacementStateFailureRestoresExactGeneration(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	statePath := service.WorkspaceStatePath(data, project.ID, "default")
	before := companionUpdateReadFile(t, statePath)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	writer := func(path string, state store.WorkspaceState, compare func() error) error {
		if err := store.WriteWorkspaceCAS(path, state, compare); err != nil {
			return err
		}
		if path == statePath {
			return errors.New("injected post-replacement directory sync failure")
		}
		return nil
	}
	result, err := service.NewCompanionUpdateServiceWithDependencies(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}, writer, nil).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "state-publication-failed" || result.Entries[0].Action != "none" || result.Entries[0].ResultingHEAD != old {
		t.Fatalf("post-replacement baseline result=%#v err=%v", result, err)
	}
	if after := companionUpdateReadFile(t, statePath); !bytes.Equal(after, before) {
		t.Fatalf("post-replacement baseline state=%q, want exact prior generation", after)
	}
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path); headErr != nil || got != old {
		t.Fatalf("post-replacement baseline ref=%q err=%v want=%q", got, headErr, old)
	}
}

func TestCompanionUpdateActiveBaselineUncertainPostReplacementStatePreservesItAndRecordsRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		mutate    func(t *testing.T, path string, value store.WorkspaceState)
		assertion func(t *testing.T, path string)
	}{
		{name: "foreign", mutate: func(t *testing.T, path string, value store.WorkspaceState) {
			checkout := value.Repositories["backend"]
			checkout.Head = "foreign-state-generation"
			value.Repositories["backend"] = checkout
			if err := store.WriteWorkspace(path, value); err != nil {
				t.Fatal(err)
			}
		}, assertion: func(t *testing.T, path string) {
			state, err := store.ReadWorkspace(path)
			if err != nil || state.Repositories["backend"].Head != "foreign-state-generation" {
				t.Fatalf("foreign state=%#v err=%v", state, err)
			}
		}},
		{name: "unobservable", mutate: func(t *testing.T, path string, _ store.WorkspaceState) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, assertion: func(t *testing.T, path string) {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("unobservable state exists or has wrong error: %v", err)
			}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			statePath := service.WorkspaceStatePath(data, project.ID, "default")
			old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
			if err != nil {
				t.Fatal(err)
			}
			backend.CommitFile("target.txt", "target\n", "target")
			target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
			if err != nil {
				t.Fatal(err)
			}
			backend.Run(t, "reset", "--hard", old)
			writer := func(path string, state store.WorkspaceState, compare func() error) error {
				if err := store.WriteWorkspaceCAS(path, state, compare); err != nil {
					return err
				}
				if path == statePath {
					scenario.mutate(t, path, state)
					return errors.New("injected post-replacement outcome lost")
				}
				return nil
			}
			result, err := service.NewCompanionUpdateServiceWithDependencies(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}, writer, nil).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
			if err != nil || result.Status != "failed" || len(result.Entries) != 1 || result.Entries[0].Reason != "rollback-incomplete" || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete) {
				t.Fatalf("uncertain post-replacement result=%#v err=%v", result, err)
			}
			scenario.assertion(t, statePath)
			if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path); headErr != nil || got != old {
				t.Fatalf("owned baseline ref did not restore=%q err=%v want=%q", got, headErr, old)
			}
			recovery, readErr := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", "default.json"))
			if readErr != nil || len(recovery.UnrevertedSteps) == 0 || !strings.Contains(strings.Join(recovery.UnrevertedSteps, ","), "state:") || strings.Contains(strings.Join(recovery.UnrevertedSteps, ","), "worktree-index") {
				t.Fatalf("uncertain state recovery=%#v err=%v", recovery, readErr)
			}
		})
	}
}

func TestCompanionUpdateActiveBaselinePreReplacementStateFailureIsCleanRollback(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	statePath := service.WorkspaceStatePath(data, project.ID, "default")
	before := companionUpdateReadFile(t, statePath)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	writer := func(path string, _ store.WorkspaceState, compare func() error) error {
		if path == statePath {
			if err := compare(); err != nil {
				return err
			}
			return errors.New("injected pre-replacement failure")
		}
		return errors.New("unexpected state publication")
	}
	result, err := service.NewCompanionUpdateServiceWithDependencies(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}, writer, nil).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || result.Entries[0].Reason != "state-publication-failed" || result.Entries[0].Action != "none" {
		t.Fatalf("pre-replacement result=%#v err=%v", result, err)
	}
	if after := companionUpdateReadFile(t, statePath); !bytes.Equal(after, before) {
		t.Fatal("pre-replacement failure changed workspace state")
	}
	if _, statErr := os.Stat(filepath.Join(data, "projects", project.ID, "recovery", "default.json")); !os.IsNotExist(statErr) {
		t.Fatalf("clean pre-replacement rollback wrote recovery: %v", statErr)
	}
}

func TestCompanionUpdateWorkspacePostReplacementStateFailureRestoresAndContinues(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"failure", "zz-later"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	failure, err := service.RequireWorkspace(project, data, "failure")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, failure.ID)
	before := companionUpdateReadFile(t, statePath)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	writer := func(path string, state store.WorkspaceState, compare func() error) error {
		if err := store.WriteWorkspaceCAS(path, state, compare); err != nil {
			return err
		}
		if path == statePath {
			return errors.New("injected post-replacement directory sync failure")
		}
		return nil
	}
	result, err := service.NewCompanionUpdateServiceWithDependencies(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}, writer, nil).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 3 {
		t.Fatalf("post-replacement workspace result=%#v err=%v", result, err)
	}
	if entry := result.Entries[1]; entry.Workspace != "failure" || entry.Reason != "state-publication-failed" || entry.Action != "none" || entry.ResultingHEAD != old {
		t.Fatalf("post-replacement workspace entry=%#v", entry)
	}
	if after := companionUpdateReadFile(t, statePath); !bytes.Equal(after, before) {
		t.Fatalf("post-replacement workspace state=%q, want exact prior generation", after)
	}
	checkout := companionUpdateReadState(t, data, project.ID, failure.ID).Repositories["backend"]
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), checkout.ResolvedPath); headErr != nil || got != old {
		t.Fatalf("post-replacement workspace ref=%q err=%v want=%q", got, headErr, old)
	}
	if entry := result.Entries[2]; entry.Workspace != "zz-later" || entry.Status != "completed" || entry.ResultingHEAD != target {
		t.Fatalf("later workspace was not safely continued=%#v", entry)
	}
}

func TestCompanionUpdateWorkspaceRollbackIncompleteRecordsRecoveryAndCancelsLater(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"failure", "zz-later"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	failure, err := service.RequireWorkspace(project, data, "failure")
	if err != nil {
		t.Fatal(err)
	}
	failureStatePath := service.WorkspaceStatePath(data, project.ID, failure.ID)
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	serviceValue := service.NewCompanionUpdateServiceWithDependencies(companionRestoreFailGit{Git: gitadapter.NewAdapter("git"), target: target, restore: func(context.Context, string, gitadapter.FastForwardReceipt) error {
		return errors.New("injected workspace restore failure")
	}}, lock.Manager{}, func(path string, state store.WorkspaceState, compare func() error) error {
		if path == failureStatePath {
			if err := compare(); err != nil {
				return err
			}
			return errors.New("injected workspace state failure")
		}
		return store.WriteWorkspaceCAS(path, state, compare)
	}, nil)
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 3 || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete) {
		t.Fatalf("workspace incomplete rollback result=%#v err=%v", result, err)
	}
	if entry := result.Entries[1]; entry.Workspace != "failure" || entry.Reason != "rollback-incomplete" || entry.Action != "fast-forward" || entry.ResultingHEAD != target {
		t.Fatalf("incomplete workspace entry=%#v", entry)
	}
	if entry := result.Entries[2]; entry.Workspace != "zz-later" || entry.Status != "canceled" || entry.Action != "none" {
		t.Fatalf("later canceled entry=%#v", entry)
	}
	recovery, readErr := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", failure.ID+".json"))
	if readErr != nil || len(recovery.UnrevertedSteps) != 1 || !strings.Contains(recovery.UnrevertedSteps[0], target) {
		t.Fatalf("workspace recovery=%#v err=%v", recovery, readErr)
	}
}

func TestCompanionUpdateWorkspaceRollbackIncompletePreservesForeignGeneration(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"failure", "zz-later"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	failure, err := service.RequireWorkspace(project, data, "failure")
	if err != nil {
		t.Fatal(err)
	}
	failureStatePath := service.WorkspaceStatePath(data, project.ID, failure.ID)
	failureCheckout := companionUpdateReadState(t, data, project.ID, failure.ID).Repositories["backend"]
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	failureRepository := testutil.GitRepository{Path: failureCheckout.ResolvedPath}
	failureRepository.CommitFile("foreign.txt", "foreign\n", "foreign generation")
	foreign, err := gitadapter.NewAdapter("git").Head(context.Background(), failureCheckout.ResolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	failureRepository.Run(t, "reset", "--hard", old)
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	serviceValue := service.NewCompanionUpdateServiceWithDependencies(companionRestoreFailGit{Git: gitadapter.NewAdapter("git"), target: target, restore: func(_ context.Context, repository string, receipt gitadapter.FastForwardReceipt) error {
		testutil.GitRepository{Path: repository}.Run(t, "update-ref", "refs/heads/"+receipt.Branch, foreign, receipt.NewCommit)
		return errors.New("restore lost ownership")
	}}, lock.Manager{}, func(path string, state store.WorkspaceState, compare func() error) error {
		if path == failureStatePath {
			if err := compare(); err != nil {
				return err
			}
			return errors.New("injected workspace state failure")
		}
		return store.WriteWorkspaceCAS(path, state, compare)
	}, nil)
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 3 || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete) {
		t.Fatalf("foreign workspace rollback result=%#v err=%v", result, err)
	}
	if entry := result.Entries[1]; entry.Reason != "rollback-incomplete" || entry.ResultingHEAD != target {
		t.Fatalf("foreign workspace entry=%#v", entry)
	}
	if got, headErr := gitadapter.NewAdapter("git").ResolveRef(context.Background(), backend.Path, "refs/heads/"+failureCheckout.Branch); headErr != nil || got != foreign {
		t.Fatalf("foreign branch=%q err=%v want=%q", got, headErr, foreign)
	}
	recovery, readErr := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", failure.ID+".json"))
	if readErr != nil || len(recovery.UnrevertedSteps) != 1 || !strings.Contains(recovery.UnrevertedSteps[0], "worktree-index=possible@") || len(recovery.RollbackFailures) != 1 || !strings.Contains(recovery.RollbackFailures[0].Error, "ownership lost") {
		t.Fatalf("foreign workspace recovery=%#v err=%v", recovery, readErr)
	}
	if entry := result.Entries[2]; entry.Workspace != "zz-later" || entry.Status != "canceled" {
		t.Fatalf("foreign later entry=%#v", entry)
	}
}

func TestCompanionUpdateWorkspaceRollbackIncompleteRecordsUnobservableGeneration(t *testing.T) {
	project, root, backend, data := createFixture(t)
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	for _, name := range []string{"failure", "zz-later"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	project = reloadCompanionFixtureProject(t, project, data)
	failure, err := service.RequireWorkspace(project, data, "failure")
	if err != nil {
		t.Fatal(err)
	}
	failureStatePath := service.WorkspaceStatePath(data, project.ID, failure.ID)
	failureCheckout := companionUpdateReadState(t, data, project.ID, failure.ID).Repositories["backend"]
	old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.CommitFile("target.txt", "target\n", "target")
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	backend.Run(t, "reset", "--hard", old)
	serviceValue := service.NewCompanionUpdateServiceWithDependencies(companionUnobservableRestoreGit{Git: gitadapter.NewAdapter("git"), target: target, branch: failureCheckout.Branch}, lock.Manager{}, func(path string, state store.WorkspaceState, compare func() error) error {
		if path == failureStatePath {
			if err := compare(); err != nil {
				return err
			}
			return errors.New("injected workspace state failure")
		}
		return store.WriteWorkspaceCAS(path, state, compare)
	}, nil)
	result, err := serviceValue.Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
	if err != nil || result.Status != "failed" || len(result.Entries) != 3 || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete) {
		t.Fatalf("unobservable workspace rollback result=%#v err=%v", result, err)
	}
	if entry := result.Entries[1]; entry.Workspace != "failure" || entry.Reason != "rollback-incomplete" || entry.Action != "fast-forward" || entry.ResultingHEAD != target {
		t.Fatalf("unobservable workspace entry=%#v", entry)
	}
	if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), failureCheckout.ResolvedPath); headErr != nil || got != target {
		t.Fatalf("unobservable branch was overwritten=%q err=%v want=%q", got, headErr, target)
	}
	recovery, readErr := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", failure.ID+".json"))
	if readErr != nil || len(recovery.UnrevertedSteps) != 1 || !strings.Contains(recovery.UnrevertedSteps[0], "worktree-index=possible@"+target) || len(recovery.RollbackFailures) != 1 || !strings.Contains(recovery.RollbackFailures[0].Error, "ref observation unavailable") {
		t.Fatalf("unobservable workspace recovery=%#v err=%v", recovery, readErr)
	}
	if entry := result.Entries[2]; entry.Workspace != "zz-later" || entry.Status != "canceled" {
		t.Fatalf("unobservable later entry=%#v", entry)
	}
}

func TestCompanionUpdateCancellationAfterWorkspaceFastForwardPrioritizesIncompleteRollback(t *testing.T) {
	for _, scenario := range []struct {
		name, wantReason, wantHead string
		restore                    error
	}{
		{name: "clean-rollback", wantReason: "", wantHead: "old"},
		{name: "incomplete-rollback", wantReason: "rollback-incomplete", wantHead: "target", restore: errors.New("injected cancellation restore failure")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			for _, name := range []string{"feature", "zz-later"} {
				if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
					t.Fatal(err)
				}
			}
			project = reloadCompanionFixtureProject(t, project, data)
			feature, err := service.RequireWorkspace(project, data, "feature")
			if err != nil {
				t.Fatal(err)
			}
			featureCheckout := companionUpdateReadState(t, data, project.ID, feature.ID).Repositories["backend"]
			old, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
			if err != nil {
				t.Fatal(err)
			}
			backend.CommitFile("target.txt", "target\n", "target")
			target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
			if err != nil {
				t.Fatal(err)
			}
			backend.Run(t, "reset", "--hard", old)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			git := &companionCancelAfterWorkspaceForwardGit{Git: gitadapter.NewAdapter("git"), cancel: cancel, target: target, restore: scenario.restore}
			result, err := service.NewCompanionUpdateServiceWith(git, lock.Manager{}).Execute(ctx, service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend"})
			if err != nil || result.Status != "failed" || len(result.Entries) != 3 {
				t.Fatalf("canceled workspace result=%#v err=%v", result, err)
			}
			entry := result.Entries[1]
			if entry.Workspace != "feature" || entry.Status != "canceled" && scenario.restore == nil || entry.Reason != scenario.wantReason {
				t.Fatalf("feature cancellation entry=%#v", entry)
			}
			if scenario.restore == nil && (entry.Action != "none" || entry.ResultingHEAD != old) {
				t.Fatalf("clean cancellation entry=%#v", entry)
			}
			if scenario.restore != nil && (entry.Action != "fast-forward" || entry.ResultingHEAD != target || result.Failure == nil || result.Failure.Code != string(service.ErrorRollbackIncomplete)) {
				t.Fatalf("incomplete cancellation entry=%#v result=%#v", entry, result)
			}
			if scenario.restore != nil {
				recovery, readErr := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", feature.ID+".json"))
				if readErr != nil || len(recovery.UnrevertedSteps) != 1 || !strings.Contains(recovery.UnrevertedSteps[0], "="+target) {
					t.Fatalf("incomplete cancellation recovery=%#v err=%v", recovery, readErr)
				}
			}
			want := old
			if scenario.wantHead == "target" {
				want = target
			}
			if got, headErr := gitadapter.NewAdapter("git").Head(context.Background(), featureCheckout.ResolvedPath); headErr != nil || got != want {
				t.Fatalf("feature head=%q err=%v want=%q", got, headErr, want)
			}
			if later := result.Entries[2]; later.Workspace != "zz-later" || later.Status != "canceled" {
				t.Fatalf("later cancellation entry=%#v", later)
			}
		})
	}
}

func TestCompanionUpdateOutputFailureCancelsUnattemptedPresentWorkspaces(t *testing.T) {
	project, root, backend, data := createFixture(t)
	if _, err := createFixtureWorkspace(t, project, "feature-a", filepath.Join(t.TempDir(), "a"), data); err != nil {
		t.Fatal(err)
	}
	if _, err := createFixtureWorkspace(t, project, "feature-b", filepath.Join(t.TempDir(), "b"), data); err != nil {
		t.Fatal(err)
	}
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	result, err := service.NewCompanionUpdateServiceWith(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", Progress: func(entry service.CompanionUpdateEntry) error { completed++; return errors.New("writer closed") }})
	if err != nil || result.Status != "failed" || completed != 1 || len(result.Entries) != 3 || result.Entries[1].Status != "canceled" || result.Entries[2].Status != "canceled" {
		t.Fatalf("callback cancellation = %#v, %v", result, err)
	}
}

func TestCompanionUpdateOutputFailureAfterWorkspaceRetainsSettledResults(t *testing.T) {
	project, root, backend, data := createFixture(t)
	for _, name := range []string{"feature", "zz-later"} {
		if _, err := createFixtureWorkspace(t, project, name, filepath.Join(t.TempDir(), name), data); err != nil {
			t.Fatal(err)
		}
	}
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	target, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	result, err := service.NewCompanionUpdateServiceWith(companionStaticFetchGit{Git: gitadapter.NewAdapter("git"), target: target}, lock.Manager{}).Execute(context.Background(), service.CompanionUpdateRequest{Project: project, DataDir: data, RepositoryID: "backend", Progress: func(service.CompanionUpdateEntry) error {
		calls++
		if calls == 2 {
			return errors.New("writer closed after workspace")
		}
		return nil
	}})
	if err != nil || result.Status != "failed" || calls != 2 || len(result.Entries) != 3 || result.Failure == nil {
		t.Fatalf("workspace callback result=%#v err=%v calls=%d", result, err, calls)
	}
	if baseline := result.Entries[0]; baseline.Kind != "baseline" || baseline.Status != "completed" {
		t.Fatalf("baseline entry=%#v", baseline)
	}
	if settled := result.Entries[1]; settled.Workspace != "feature" || settled.Status != "completed" || settled.Action != "unchanged" {
		t.Fatalf("settled workspace=%#v", settled)
	}
	if canceled := result.Entries[2]; canceled.Workspace != "zz-later" || canceled.Status != "canceled" || canceled.Action != "none" {
		t.Fatalf("canceled workspace=%#v", canceled)
	}
}

type companionUpdateDrySnapshot struct {
	Refs, Head, Index       []byte
	Worktrees, State, Files map[string][]byte
}

func companionUpdateReadState(t *testing.T, dataDir, projectID, workspaceID string) store.WorkspaceState {
	t.Helper()
	value, err := store.ReadWorkspace(service.WorkspaceStatePath(dataDir, projectID, workspaceID))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func companionUpdateWriteState(t *testing.T, dataDir, projectID, workspaceID string, value store.WorkspaceState) {
	t.Helper()
	encoded, err := store.WorkspaceBytes(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.WorkspaceStatePath(dataDir, projectID, workspaceID), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func companionPresentPaths(paths map[string]string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			result = append(result, path)
		}
	}
	return result
}

// companionUpdateSnapshot records the exact local generations dry-run is
// forbidden to change: heads/ref namespace, each checkout's HEAD/index and
// worktree bytes, plus all configuration, manifest, state, and recovery data.
func companionUpdateSnapshot(t *testing.T, referenceRepository string, worktrees []string, dataDir string, project domain.Project) companionUpdateDrySnapshot {
	t.Helper()
	adapter := gitadapter.NewAdapter("git")
	gitDir, err := adapter.GitDir(context.Background(), referenceRepository)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := companionUpdateDrySnapshot{
		Refs:      companionUpdateGitOutput(t, referenceRepository, "for-each-ref", "--format=%(refname)%00%(objectname)"),
		Head:      companionUpdateReadFile(t, filepath.Join(gitDir, "HEAD")),
		Index:     companionUpdateReadFile(t, filepath.Join(gitDir, "index")),
		Worktrees: map[string][]byte{},
		State:     companionUpdateFiles(t, service.WorkspaceStateDirectory(dataDir, project.ID)),
		Files:     map[string][]byte{},
	}
	for _, path := range worktrees {
		for name, value := range companionUpdateFiles(t, path) {
			snapshot.Worktrees[filepath.Join(path, name)] = value
		}
		worktreeGitDir, gitErr := adapter.GitDir(context.Background(), path)
		if gitErr != nil {
			t.Fatal(gitErr)
		}
		snapshot.Worktrees[filepath.Join(path, ".git-HEAD")] = companionUpdateReadFile(t, filepath.Join(worktreeGitDir, "HEAD"))
		snapshot.Worktrees[filepath.Join(path, ".git-index")] = companionUpdateReadFile(t, filepath.Join(worktreeGitDir, "index"))
	}
	for _, path := range []string{project.ConfigPath, filepath.Join(filepath.Dir(project.ConfigPath), ".wtree.yml")} {
		snapshot.Files[path] = companionUpdateReadFile(t, path)
	}
	for name, value := range companionUpdateFiles(t, filepath.Join(dataDir, "projects", project.ID, "recovery")) {
		snapshot.Files[filepath.Join("recovery", name)] = value
	}
	return snapshot
}

func companionUpdateFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return result
	} else if err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		// A source checkout has a .git directory which holds mutable Git
		// internals. HEAD/index and refs are captured separately. A linked
		// worktree's .git is a regular pointer file and remains meaningful.
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			result[relative] = []byte("directory\x00")
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			result[relative] = append([]byte("symlink\x00"), target...)
			return nil
		}
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[relative] = append([]byte("file\x00"), data...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func companionUpdateReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return append([]byte(nil), data...)
}

func companionUpdateGit(t *testing.T, path string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func companionUpdateGitOutput(t *testing.T, path string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return append([]byte(nil), bytes.TrimSpace(output)...)
}
