package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
)

func TestDoctorDriftFindingsProjectsStableNonFixableCodes(t *testing.T) {
	manifest := config.PortableManifest{Repositories: map[string]config.PortableRepository{
		"root":  {Clone: config.CloneSource{URL: "https://example.invalid/root"}},
		"child": {Clone: config.CloneSource{URL: "https://example.invalid/child"}},
	}}
	snapshot := DriftSnapshot{
		currentManifest: mustDoctorManifestBytes(t, manifest),
		failures: []DriftFailure{
			{RepositoryID: "root", Check: "checkout"},
			{RepositoryID: "child", Check: "state-only"},
			{RepositoryID: "child", Check: "disk-only"},
			{RepositoryID: "child", Check: "identity"},
			{RepositoryID: "child", Check: "path"},
			{RepositoryID: "child", Check: "branch"},
			{RepositoryID: "child", Check: "parent-ignore"},
			{RepositoryID: "child", Check: "upstream"},
			{RepositoryID: "root", Check: "tracked-manifest"},
		},
		observations: []DriftRepositoryObservation{{RepositoryID: "child", Upstream: gitadapter.Upstream{FetchURL: "https://other.invalid/child"}}},
		retained:     []RetainedUnmanagedFact{{RepositoryID: "retained"}},
		operations: []DriftOperationRecord{
			{Path: "/data/projects/project/recovery/default.json", Operation: "remove"},
			{Path: "/data/projects/project/update/update-0123456789abcdef01234567", Operation: "update"},
		},
	}
	got := doctorDriftFindings(snapshot)
	codes := make([]string, len(got))
	for index, finding := range got {
		if finding.Fixable || finding.Severity == "" || finding.Message == "" {
			t.Fatalf("finding must remain non-fixable and descriptive: %#v", finding)
		}
		codes[index] = finding.RepositoryID + ":" + finding.Code
	}
	want := []string{
		":update-in-progress", ":update-recovery-record",
		"child:branch-mismatch", "child:manifest-repository-unmanaged", "child:mount-mismatch", "child:parent-ignore-missing", "child:repository-url-mismatch", "child:source-identity-mismatch",
		"retained:retained-unmanaged-repository", "root:manifest-configuration-mismatch", "root:manifest-repository-missing",
	}
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("projected codes = %#v, want %#v", codes, want)
	}
}

func TestExactCompanionBaselinePublicationAllowsOnlyOneMatchedCompanionBaseline(t *testing.T) {
	tracked, working, local, project := companionBaselinePublicationFixture(t)
	publication, exact := exactCompanionBaselinePublication(tracked, working, local, project)
	if !exact || publication.repositoryID != "root" || publication.baseline != "next" {
		t.Fatalf("exact companion baseline publication = %#v, %t", publication, exact)
	}
	if !companionBaselinePublicationStatus(gitadapter.Status{Entries: []gitadapter.StatusEntry{{Index: ' ', Worktree: 'M', Path: ".wtree.yml"}, {Index: ' ', Worktree: 'M', Path: "project.wtree.yml"}}}, "project.wtree.yml", ".wtree.yml") {
		t.Fatal("exact unstaged configuration status was rejected")
	}
	if companionBaselinePublicationStatus(gitadapter.Status{Entries: []gitadapter.StatusEntry{{Index: ' ', Worktree: 'M', Path: "project.wtree.yml"}, {Index: ' ', Worktree: 'M', Path: "README.md"}}}, "project.wtree.yml", ".wtree.yml") {
		t.Fatal("unrelated working-tree dirt was accepted")
	}
	if companionBaselinePublicationStatus(gitadapter.Status{Entries: []gitadapter.StatusEntry{{Index: 'M', Worktree: ' ', Path: "project.wtree.yml"}}}, "project.wtree.yml", ".wtree.yml") {
		t.Fatal("staged manifest was accepted")
	}

	for _, test := range []struct {
		name   string
		change func(*config.PortableManifest, *config.ProjectConfig)
	}{
		{name: "split local baseline", change: func(_ *config.PortableManifest, local *config.ProjectConfig) {
			repository := local.Repositories["root"]
			repository.DefaultBranch = "main"
			local.Repositories["root"] = repository
		}},
		{name: "ordinary baseline", change: func(working *config.PortableManifest, _ *config.ProjectConfig) {
			repository := working.Repositories["root"]
			repository.Companion = false
			working.Repositories["root"] = repository
		}},
		{name: "merge changed", change: func(working *config.PortableManifest, _ *config.ProjectConfig) {
			repository := working.Repositories["root"]
			repository.Upstream.Merge = "refs/heads/changed"
			working.Repositories["root"] = repository
		}},
		{name: "remote changed", change: func(working *config.PortableManifest, _ *config.ProjectConfig) {
			repository := working.Repositories["root"]
			repository.Upstream.Remote = "other"
			working.Repositories["root"] = repository
		}},
		{name: "topology changed", change: func(working *config.PortableManifest, _ *config.ProjectConfig) {
			repository := working.Repositories["child"]
			repository.Mount = "moved"
			working.Repositories["child"] = repository
		}},
		{name: "second repository changed", change: func(working *config.PortableManifest, _ *config.ProjectConfig) {
			repository := working.Repositories["child"]
			repository.DefaultBranch, repository.Upstream.Branch = "other", "other"
			working.Repositories["child"] = repository
		}},
		{name: "hooks changed", change: func(working *config.PortableManifest, _ *config.ProjectConfig) {
			working.Hooks = config.HookEvents{"post-create": {{ID: "changed", Command: []string{"echo", "changed"}}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, candidate, candidateLocal, candidateProject := companionBaselinePublicationFixture(t)
			// Start from the exact working generation, then introduce exactly one
			// forbidden difference.
			test.change(&candidate, &candidateLocal)
			if _, exact := exactCompanionBaselinePublication(tracked, candidate, candidateLocal, candidateProject); exact {
				t.Fatalf("forbidden %s was accepted", test.name)
			}
		})
	}
	ordinaryTracked, ordinaryWorking, ordinaryLocal, ordinaryProject := companionBaselinePublicationFixture(t)
	ordinaryBefore := ordinaryTracked.Repositories["root"]
	ordinaryBefore.Companion = false
	ordinaryTracked.Repositories["root"] = ordinaryBefore
	ordinaryAfter := ordinaryWorking.Repositories["root"]
	ordinaryAfter.Companion = false
	ordinaryWorking.Repositories["root"] = ordinaryAfter
	ordinaryLocalRepository := ordinaryLocal.Repositories["root"]
	ordinaryLocalRepository.Companion = false
	ordinaryLocal.Repositories["root"] = ordinaryLocalRepository
	for index := range ordinaryProject.Repositories {
		if ordinaryProject.Repositories[index].ID == "root" {
			ordinaryProject.Repositories[index].Companion = false
		}
	}
	if _, exact := exactCompanionBaselinePublication(ordinaryTracked, ordinaryWorking, ordinaryLocal, ordinaryProject); exact {
		t.Fatal("ordinary baseline edit was accepted")
	}
}

type companionBaselineAuthorityGit struct {
	gitadapter.Git
	common         string
	commonErr      error
	branchExists   bool
	branchErr      error
	commonPath     string
	branchPath     string
	observedBranch string
	cancelOnCommon context.CancelFunc
	cancelOnBranch context.CancelFunc
}

func (g *companionBaselineAuthorityGit) CommonGitDir(_ context.Context, repository string) (string, error) {
	g.commonPath = repository
	if g.cancelOnCommon != nil {
		g.cancelOnCommon()
	}
	return g.common, g.commonErr
}

func (g *companionBaselineAuthorityGit) BranchExists(_ context.Context, repository, branch string) (bool, error) {
	g.branchPath, g.observedBranch = repository, branch
	if g.cancelOnBranch != nil {
		g.cancelOnBranch()
	}
	return g.branchExists, g.branchErr
}

func TestCompanionBaselinePublicationAuthorityRequiresResolvedIdentityAndExistingBranch(t *testing.T) {
	_, _, _, project := companionBaselinePublicationFixture(t)
	publication := companionBaselinePublication{repositoryID: "root", baseline: "next"}
	for _, test := range []struct {
		name         string
		common       string
		commonErr    error
		branchExists bool
		branchErr    error
		want         bool
		wantBranch   bool
	}{
		{name: "authorized", common: "/git/root", branchExists: true, want: true, wantBranch: true},
		{name: "identity mismatch", common: "/git/other"},
		{name: "identity observation error", commonErr: errors.New("common failed")},
		{name: "missing branch", common: "/git/root", wantBranch: true},
		{name: "branch observation error", common: "/git/root", branchErr: errors.New("branch failed"), wantBranch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			git := &companionBaselineAuthorityGit{common: test.common, commonErr: test.commonErr, branchExists: test.branchExists, branchErr: test.branchErr}
			got, err := companionBaselinePublicationAuthorized(context.Background(), git, project, publication)
			if err != nil || got != test.want {
				t.Fatalf("authority = %t, %v, want %t, nil", got, err, test.want)
			}
			if git.commonPath != "/tree" {
				t.Fatalf("CommonGitDir path = %q, want resolved source /tree", git.commonPath)
			}
			if test.wantBranch {
				if git.branchPath != "/tree" || git.observedBranch != "next" {
					t.Fatalf("BranchExists = (%q, %q), want resolved source and proposed baseline", git.branchPath, git.observedBranch)
				}
			} else if git.branchPath != "" {
				t.Fatalf("BranchExists called after failed identity authority: %q", git.branchPath)
			}
		})
	}

	if authorized, err := companionBaselinePublicationAuthorized(context.Background(), &companionBaselineAuthorityGit{common: "/git/root", branchExists: true}, project, companionBaselinePublication{repositoryID: "missing", baseline: "next"}); err != nil || authorized {
		t.Fatalf("unknown repository authority = %t, %v", authorized, err)
	}
}

func TestCompanionBaselinePublicationAuthorityPropagatesCancellation(t *testing.T) {
	_, _, _, project := companionBaselinePublicationFixture(t)
	publication := companionBaselinePublication{repositoryID: "root", baseline: "next"}

	commonContext, cancelCommon := context.WithCancel(context.Background())
	commonGit := &companionBaselineAuthorityGit{common: "/git/root", branchExists: true, cancelOnCommon: cancelCommon}
	if authorized, err := companionBaselinePublicationAuthorized(commonContext, commonGit, project, publication); authorized || !errors.Is(err, context.Canceled) || commonGit.branchPath != "" {
		t.Fatalf("common cancellation = %t, %v, branch path %q", authorized, err, commonGit.branchPath)
	}

	branchContext, cancelBranch := context.WithCancel(context.Background())
	branchGit := &companionBaselineAuthorityGit{common: "/git/root", branchExists: true, cancelOnBranch: cancelBranch}
	if authorized, err := companionBaselinePublicationAuthorized(branchContext, branchGit, project, publication); authorized || !errors.Is(err, context.Canceled) {
		t.Fatalf("branch cancellation = %t, %v", authorized, err)
	}
}

func companionBaselinePublicationFixture(t *testing.T) (config.PortableManifest, config.PortableManifest, config.ProjectConfig, domain.Project) {
	t.Helper()
	root := driftRepository("", ".")
	root.Companion = true
	child := driftRepository("root", "child")
	tracked := config.PortableManifest{Version: config.PortableManifestVersion4, Project: config.PortableProject{ID: "project", Name: "Project", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{"root": root, "child": child}}
	working := tracked
	working.Repositories = map[string]config.PortableRepository{"root": root, "child": child}
	changed := working.Repositories["root"]
	changed.DefaultBranch, changed.Upstream.Branch = "next", "next"
	working.Repositories["root"] = changed
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "next", Companion: true, CommonGitDir: "/git/root", SourcePath: "/tree"}, {ID: "child", ParentID: "root", DefaultMount: "child", DefaultBranch: "main", CommonGitDir: "/git/child", SourcePath: "/tree/child"}})
	local := driftLocalConfig(project)
	local.Version = config.ProjectConfigVersion4
	local.Repositories["root"] = config.Repository{Source: ".", DefaultMount: ".", DefaultBranch: "next", Companion: true}
	return tracked, working, local, project
}

type doctorCancellationGit struct {
	gitadapter.Git
	stage        string
	cancel       context.CancelFunc
	commonCalls  int
	ignoreCalls  int
	historyCalls int
	waitForCtx   bool
}

type doctorHistoryGit struct {
	gitadapter.Git
	contains bool
	err      error
}

type doctorBaseErrorGit struct {
	gitadapter.Git
	err   error
	calls int
}

func (g *doctorBaseErrorGit) CommonGitDir(context.Context, string) (string, error) {
	g.calls++
	return "", g.err
}

func (g doctorHistoryGit) ContainsCommits(context.Context, string, []string) (bool, error) {
	return g.contains, g.err
}

func (g *doctorCancellationGit) CommonGitDir(ctx context.Context, repository string) (string, error) {
	g.commonCalls++
	if g.stage == "base-identity" && g.commonCalls == 1 {
		g.cancel()
		return "", errors.New("late base identity failure")
	}
	return g.Git.CommonGitDir(ctx, repository)
}

func (g *doctorCancellationGit) IsIgnoredAt(ctx context.Context, repository, ref, mount string) (bool, error) {
	g.ignoreCalls++
	if g.stage == "parent-ignore" {
		if g.waitForCtx {
			<-ctx.Done()
		} else {
			g.cancel()
		}
		return false, errors.New("late parent ignore failure")
	}
	return g.Git.IsIgnoredAt(ctx, repository, ref, mount)
}

func (g *doctorCancellationGit) InspectCommittedIgnore(ctx context.Context, repository, ref, mount string) (bool, error) {
	return g.IsIgnoredAt(ctx, repository, ref, mount)
}

func (g *doctorCancellationGit) ContainsCommits(ctx context.Context, repository string, commits []string) (bool, error) {
	g.historyCalls++
	if g.stage == "final-history" {
		g.cancel()
		return false, errors.New("late identity history failure")
	}
	return g.Git.ContainsCommits(ctx, repository, commits)
}

func TestDoctorCollectionPreservesMidCallCancellationAndDeadline(t *testing.T) {
	t.Run("base identity cancellation", func(t *testing.T) {
		fixture := newUpdateExecutionCrashFixture(t, false)
		ctx, cancel := context.WithCancel(context.Background())
		git := &doctorCancellationGit{Git: fixture.git, stage: "base-identity", cancel: cancel}
		doctor := &DoctorService{git: git}
		_, err := doctor.collectDriftSnapshot(ctx, fixture.snapshot.Project(), fixture.request.DataDir)
		if !errors.Is(err, context.Canceled) || git.commonCalls != 1 || git.historyCalls != 0 {
			t.Fatalf("base cancellation = %v calls(common=%d history=%d), want exact cancellation and no later Git call", err, git.commonCalls, git.historyCalls)
		}
	})

	t.Run("parent ignore deadline", func(t *testing.T) {
		fixture := newUpdateExecutionCrashFixture(t, true)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		git := &doctorCancellationGit{Git: fixture.git, stage: "parent-ignore", waitForCtx: true}
		doctor := &DoctorService{git: git}
		_, err := doctor.collectDriftSnapshot(ctx, fixture.snapshot.Project(), fixture.request.DataDir)
		if !errors.Is(err, context.DeadlineExceeded) || git.ignoreCalls != 1 || git.historyCalls != 1 {
			t.Fatalf("parent-ignore deadline = %v calls(ignore=%d history=%d), want exact deadline and no later history call", err, git.ignoreCalls, git.historyCalls)
		}
	})

	t.Run("final history cancellation", func(t *testing.T) {
		fixture := newUpdateExecutionCrashFixture(t, false)
		ctx, cancel := context.WithCancel(context.Background())
		git := &doctorCancellationGit{Git: fixture.git, stage: "final-history", cancel: cancel}
		doctor := &DoctorService{git: git}
		_, err := doctor.collectDriftSnapshot(ctx, fixture.snapshot.Project(), fixture.request.DataDir)
		if !errors.Is(err, context.Canceled) || git.historyCalls != 1 {
			t.Fatalf("final-history cancellation = %v history calls=%d, want exact cancellation after one final history call", err, git.historyCalls)
		}
	})
}

func TestDoctorCollectionRequiresPortableInitialCommitIdentity(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	project, dataDir := fixture.snapshot.Project(), fixture.request.DataDir

	for _, test := range []struct {
		name      string
		contains  bool
		err       error
		wantCode  string
		wantError bool
	}{
		{name: "present roots", contains: true},
		{name: "rewritten history", contains: false, wantCode: "source-identity-mismatch"},
		{name: "operational failure", err: fmt.Errorf("history https://user:history-secret@example.invalid failed"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			doctor := &DoctorService{git: doctorHistoryGit{Git: fixture.git, contains: test.contains, err: test.err}}
			snapshot, err := doctor.collectDriftSnapshot(context.Background(), project, dataDir)
			if test.wantError != (err != nil) {
				t.Fatalf("collect error = %v, want error %t", err, test.wantError)
			}
			if strings.Contains(fmt.Sprintf("%v %#v", err, snapshot.Failures()), "history-secret") {
				t.Fatalf("history observation leaked credentials: %v %#v", err, snapshot.Failures())
			}
			if test.wantCode != "" {
				found := false
				for _, finding := range doctorDriftFindings(snapshot) {
					found = found || finding.Code == test.wantCode
				}
				if !found {
					t.Fatalf("history mismatch findings = %#v", doctorDriftFindings(snapshot))
				}
			}
		})
	}
}

func TestDoctorCollectionWrapsAndRedactsBaseIdentityError(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	cause := errors.New("identity https://user:base-secret@example.invalid failed")
	git := &doctorBaseErrorGit{Git: fixture.git, err: cause}
	doctor := &DoctorService{git: git}
	_, err := doctor.collectDriftSnapshot(context.Background(), fixture.snapshot.Project(), fixture.request.DataDir)
	if !errors.Is(err, cause) || git.calls != 1 || strings.Contains(err.Error(), "user:base-secret") || len(err.Error()) > 8192 {
		t.Fatalf("base identity error = %v calls=%d, want wrapped bounded credential-safe cause", err, git.calls)
	}
}

func TestDoctorAcceptsNestedCommittedImmediateParentIgnore(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, true)
	project, workspace := fixture.snapshot.Project(), fixture.snapshot.DefaultWorkspace()
	report, err := NewDoctorService().Doctor(context.Background(), project, workspace, fixture.request.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		if finding.Code == "parent-ignore-missing" && finding.RepositoryID == "child" {
			t.Fatalf("nested committed parent rule was rejected: %#v", report.Findings)
		}
	}
}

func TestDoctorFallbackRetainsReconciliationAndCoexistingOperations(t *testing.T) {
	dataDir := t.TempDir()
	reconciliationPath := filepath.Join(dataDir, "projects", "project", "reconciliation.json")
	if err := os.MkdirAll(filepath.Dir(reconciliationPath), 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeUpdateReconciliation([]UpdateRetainedFact{{RepositoryID: "old", Path: filepath.Join(dataDir, "old"), CommonGitDir: filepath.Join(dataDir, "git", "old")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reconciliationPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	recoveryPath := filepath.Join(dataDir, "projects", "project", "recovery", "default.json")
	if err := store.WriteRecovery(recoveryPath, store.RecoveryRecord{ProjectID: "project", WorkspaceID: "default", Operation: "update", FailedStep: "publication"}); err != nil {
		t.Fatal(err)
	}

	findings, err := doctorFallbackFindings(context.Background(), dataDir, "project")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(findings))
	for _, finding := range findings {
		got = append(got, finding.RepositoryID+":"+finding.Code)
	}
	want := []string{":manifest-configuration-mismatch", "old:retained-unmanaged-repository", ":update-recovery-record"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback findings = %#v, want %#v", got, want)
	}
}

func TestDoctorFallbackRejectsAmbiguousReconciliationAuthority(t *testing.T) {
	path := filepath.Join("/data", "projects", "project", "reconciliation.json")
	regular := DriftDirectoryEntry{Name: "reconciliation.json", Regular: true}
	for _, test := range []struct {
		name  string
		stats []DriftDirectoryEntry
		reads [][]byte
	}{
		{name: "symlink", stats: []DriftDirectoryEntry{{Name: "reconciliation.json", Symlink: true}}},
		{name: "malformed", stats: []DriftDirectoryEntry{regular, regular}, reads: [][]byte{[]byte("not json"), []byte("not json")}},
		{name: "bytes changed", stats: []DriftDirectoryEntry{regular, regular}, reads: [][]byte{[]byte("first"), []byte("second")}},
		{name: "membership changed", stats: []DriftDirectoryEntry{regular, {Name: "replacement", Regular: true}}, reads: [][]byte{[]byte("same"), []byte("same")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			statIndex, readIndex := 0, 0
			reader := DriftInventoryReader{
				DataDir: "/data",
				Lstat: func(context.Context, string) (DriftDirectoryEntry, error) {
					index := statIndex
					statIndex++
					if index >= len(test.stats) {
						return regular, nil
					}
					return test.stats[index], nil
				},
				ReadFile: func(context.Context, string) ([]byte, error) {
					index := readIndex
					readIndex++
					if index >= len(test.reads) {
						return []byte("same"), nil
					}
					return test.reads[index], nil
				},
				DecodeReconciliation: func(string, []byte) ([]RetainedUnmanagedFact, error) {
					return nil, errors.New("decode history-secret https://user:secret@example.invalid")
				},
				ReadDir: func(context.Context, string) ([]DriftDirectoryEntry, error) { return nil, os.ErrNotExist },
			}
			_, err := doctorFallbackFindingsWithInventory(context.Background(), reader, "project")
			if err == nil || !strings.Contains(err.Error(), "retained-inventory") || strings.Contains(err.Error(), "user:secret") {
				t.Fatalf("fallback ambiguity at %q accepted: %v", path, err)
			}
		})
	}
}

func TestDoctorDriftFindingsDistinguishesUpstreamAndRetainedEvidence(t *testing.T) {
	manifest := config.PortableManifest{Repositories: map[string]config.PortableRepository{
		"root": {Clone: config.CloneSource{URL: "https://example.invalid/root"}},
	}}
	snapshot := DriftSnapshot{
		currentManifest: mustDoctorManifestBytes(t, manifest),
		failures:        []DriftFailure{{RepositoryID: "root", Check: "upstream"}, {RepositoryID: "root", Check: "retained-unmanaged"}},
		observations:    []DriftRepositoryObservation{{RepositoryID: "root", Upstream: gitadapter.Upstream{FetchURL: "https://example.invalid/root"}}},
		retained:        []RetainedUnmanagedFact{{RepositoryID: "root"}},
	}
	got := doctorDriftFindings(snapshot)
	want := []string{"root:repository-upstream-mismatch", "root:retained-unmanaged-repository"}
	actual := make([]string, len(got))
	for index, finding := range got {
		actual[index] = finding.RepositoryID + ":" + finding.Code
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("projected codes = %#v, want %#v", actual, want)
	}
}

func mustDoctorManifestBytes(t *testing.T, manifest config.PortableManifest) []byte {
	t.Helper()
	// The projection only needs a valid current generation. Use the existing
	// test manifest fixture shape rather than a second doctor-specific schema.
	manifest.Version = config.PortableManifestVersion
	manifest.Project = config.PortableProject{ID: "project", Name: "Project", BaseRepository: "root"}
	manifest.Repositories["root"] = config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: "https://example.invalid/root"}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, Mount: ".", DefaultBranch: "main"}
	if child, ok := manifest.Repositories["child"]; ok {
		child.Clone.Remote = "origin"
		child.Upstream = config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}
		child.Identity = config.RepositoryIdentity{InitialCommits: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
		child.Parent, child.Mount, child.DefaultBranch = "root", "child", "main"
		manifest.Repositories["child"] = child
	}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
