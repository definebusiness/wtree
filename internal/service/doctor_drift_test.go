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
