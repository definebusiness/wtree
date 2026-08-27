package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

// TestEndToEndCompositionAcceptance is the deliberately small public-command
// composition loop. It reuses the published nested-forest fixture so its only
// fixture-owned mutation is seeding local bare remotes; wtree itself neither
// publishes nor uses a shell in this flow.
func TestEndToEndCompositionAcceptance(t *testing.T) {
	manifest, projectID := publishedCloneFixture(t)
	data := t.TempDir()
	destination := filepath.Join(cloneWorkingDirectory(t), "composition")

	dryClone := testutil.RunCommand(t, cli.Execute, "clone", manifest, destination, "--data-dir", data, "--dry-run", "--json")
	if dryClone.Err != nil || dryClone.Stderr != "" || !json.Valid([]byte(dryClone.Stdout)) {
		t.Fatalf("clone dry-run = %#v", dryClone)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("clone dry-run created destination: %v", err)
	}
	cloned := testutil.RunCommand(t, cli.Execute, "clone", manifest, destination, "--data-dir", data, "--json")
	if cloned.Err != nil || cloned.Stderr != "" || !json.Valid([]byte(cloned.Stdout)) {
		t.Fatalf("clone = %#v", cloned)
	}

	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: destination, ProjectPath: destination, DataDir: data})
	if err != nil || resolution.Project.ID != projectID || resolution.Workspace.Name != "default" {
		t.Fatalf("clone resolver = %#v, %v", resolution, err)
	}
	for id, want := range map[string]string{"root": destination, "backend": filepath.Join(destination, "backend")} {
		got, resolveErr := resolution.Workspace.ResolveRepository(id)
		if resolveErr != nil || got != want {
			t.Fatalf("workspace %s = %q, %v; want %q", id, got, resolveErr, want)
		}
	}

	// update must observe the configured ref, dry-run without mutation, then
	// make the regular checkout usable by the later aggregate commands.
	updateArgs := []string{"update", "--project", destination, "--data-dir", data, "--from", manifest, "--json"}
	beforeDryRun := compositionCaptureInventory(t, destination, data, manifest, projectID)
	dryUpdate := testutil.RunCommand(t, cli.Execute, append(append([]string(nil), updateArgs...), "--dry-run")...)
	if dryUpdate.Err != nil || dryUpdate.Stderr != "" || !json.Valid([]byte(dryUpdate.Stdout)) {
		t.Fatalf("update dry-run = %#v", dryUpdate)
	}
	compositionAssertInventory(t, "update dry-run", beforeDryRun, compositionCaptureInventory(t, destination, data, manifest, projectID))
	writerErr := errors.New("partial acceptance writer")
	partial := &partialUpdateWriter{err: writerErr}
	var stderr bytes.Buffer
	if err := cli.ExecuteContext(context.Background(), append(append([]string(nil), updateArgs...), "--dry-run"), partial, &stderr); !errors.Is(err, writerErr) || partial.calls != 1 || json.Valid(partial.Bytes()) {
		t.Fatalf("partial update writer = %v calls=%d bytes=%q", err, partial.calls, partial.String())
	}
	compositionAssertInventory(t, "update dry-run partial writer", beforeDryRun, compositionCaptureInventory(t, destination, data, manifest, projectID))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cli.ExecuteContext(canceled, append(append([]string(nil), updateArgs...), "--dry-run"), &bytes.Buffer{}, &stderr); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update = %v", err)
	}
	compositionAssertInventory(t, "canceled update", beforeDryRun, compositionCaptureInventory(t, destination, data, manifest, projectID))
	updated := testutil.RunCommand(t, cli.Execute, updateArgs...)
	if updated.Err != nil || updated.Stderr != "" || !json.Valid([]byte(updated.Stdout)) {
		t.Fatalf("update = %#v", updated)
	}
	// Move the fixture-owned configured remote only after update. fetch is the
	// sole later command that may refresh the selected remote-tracking ref;
	// status then reports those local facts without a second network action.
	source := testutil.GitRepository{Path: filepath.Dir(manifest)}
	source.CommitFile("after-clone.txt", "remote change\n", "remote change")
	source.Run(t, "push", "origin", "main")

	beforeDoctor := compositionCaptureInventory(t, destination, data, manifest, projectID)
	doctor := testutil.RunCommand(t, cli.Execute, "doctor", "--project", destination, "--data-dir", data, "--json")
	var doctorReport service.DoctorReport
	if doctor.Err != nil || doctor.Stderr != "" || json.Unmarshal([]byte(doctor.Stdout), &doctorReport) != nil || doctorReport.ProjectID != projectID || doctorReport.Workspace != "default" {
		t.Fatalf("doctor = %#v report=%#v", doctor, doctorReport)
	}
	compositionAssertDoctorRows(t, doctorReport.Repositories, []string{"root", "backend"}, map[string]string{"root": destination, "backend": filepath.Join(destination, "backend")})
	compositionAssertInventory(t, "doctor", beforeDoctor, compositionCaptureInventory(t, destination, data, manifest, projectID))

	beforeStatus := compositionCaptureInventory(t, destination, data, manifest, projectID)
	status := testutil.RunCommand(t, cli.Execute, "status", "--project", destination, "--data-dir", data, "--json")
	var initialStatus service.WorkspaceStatus
	if status.Err != nil || status.Stderr != "" || json.Unmarshal([]byte(status.Stdout), &initialStatus) != nil || initialStatus.Workspace != "default" {
		t.Fatalf("initial status = %#v report=%#v", status, initialStatus)
	}
	compositionAssertStatusRows(t, initialStatus.Repositories, []string{"root", "backend"}, map[string]string{"root": destination, "backend": filepath.Join(destination, "backend")})
	compositionAssertInventory(t, "status before fetch", beforeStatus, compositionCaptureInventory(t, destination, data, manifest, projectID))

	beforeExec := compositionCaptureInventory(t, destination, data, manifest, projectID)
	execution := testutil.RunCommand(t, cli.Execute, "exec", "--project", destination, "--data-dir", data, "--json", "--", "git", "rev-parse", "--is-inside-work-tree")
	var executed service.ExecResult
	if execution.Err != nil || execution.Stderr != "" || json.Unmarshal([]byte(execution.Stdout), &executed) != nil || executed.Version != 1 || executed.Operation != "exec" || executed.Status != service.AggregateStatusCompleted || executed.Command.Program != "git" || !reflect.DeepEqual(executed.Command.Args, []string{"rev-parse", "--is-inside-work-tree"}) {
		t.Fatalf("exec = %#v decoded=%#v", execution, executed)
	}
	compositionAssertExecRows(t, executed.Repositories, []string{"root", "backend"}, map[string]string{"root": destination, "backend": filepath.Join(destination, "backend")})
	compositionAssertInventory(t, "direct exec", beforeExec, compositionCaptureInventory(t, destination, data, manifest, projectID))

	fetched := testutil.RunCommand(t, cli.Execute, "fetch", "--project", destination, "--data-dir", data, "--json")
	var fetchResult service.FetchResult
	if fetched.Err != nil || fetched.Stderr != "" || json.Unmarshal([]byte(fetched.Stdout), &fetchResult) != nil || fetchResult.Version != 1 || fetchResult.Operation != "fetch" || fetchResult.Status != service.AggregateStatusCompleted {
		t.Fatalf("fetch = %#v decoded=%#v", fetched, fetchResult)
	}
	compositionAssertFetchRows(t, fetchResult.Repositories, []string{"root", "backend"}, map[string]string{"root": destination, "backend": filepath.Join(destination, "backend")})

	// Status remains observational and deterministic after the explicit fetch.
	statusArgs := []string{"status", "--project", destination, "--data-dir", data, "--json"}
	firstStatus := testutil.RunCommand(t, cli.Execute, statusArgs...)
	secondStatus := testutil.RunCommand(t, cli.Execute, statusArgs...)
	if firstStatus.Err != nil || secondStatus.Err != nil || firstStatus.Stderr != "" || secondStatus.Stderr != "" || firstStatus.Stdout != secondStatus.Stdout || !json.Valid([]byte(firstStatus.Stdout)) {
		t.Fatalf("deterministic status = %#v / %#v", firstStatus, secondStatus)
	}
	var refreshed struct {
		Repositories []struct {
			ID     string `json:"id"`
			Behind int    `json:"behind"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(firstStatus.Stdout), &refreshed); err != nil {
		t.Fatal(err)
	}
	rootBehind := false
	for _, repository := range refreshed.Repositories {
		rootBehind = rootBehind || repository.ID == "root" && repository.Behind == 1
	}
	if !rootBehind {
		t.Fatalf("status did not report the fetched configured remote movement: %s", firstStatus.Stdout)
	}

	// Push is intentionally only a readiness check. Its local ref inventory is
	// identical before and after, proving this aggregate command did not publish
	// or create a branch/tag/ref in the selected checkout.
	t.Setenv("WTREE_DATA_HOME", data)
	beforePush := compositionCaptureInventory(t, destination, data, manifest, projectID)
	push := testutil.RunCommand(t, cli.Execute, "push", "--project", destination, "--json")
	var pushResult service.PushResult
	if push.Stderr != "" || json.Unmarshal([]byte(push.Stdout), &pushResult) != nil || pushResult.Version != 1 || pushResult.Operation != "push" || pushResult.Status != service.PushStatusBlocked {
		t.Fatalf("push readiness output = %#v decoded=%#v", push, pushResult)
	}
	compositionAssertPushRows(t, pushResult.Repositories, []string{"root", "backend"}, map[string]string{"root": ".", "backend": "backend"})
	compositionAssertInventory(t, "push readiness", beforePush, compositionCaptureInventory(t, destination, data, manifest, projectID))
	if strings.Contains(push.Stdout+push.Stderr, "remote change") {
		t.Fatalf("push readiness leaked fixture content: %#v", push)
	}
}

// TestEndToEndCompositionAcceptanceMembership keeps the expensive membership
// transitions in the same small public-command fixture. A third repository is
// added from a new tracked manifest generation, then the existing child is
// removed from that generation and retained locally with strict reconciliation
// evidence. No fixture uses an external network or production publication.
func TestEndToEndCompositionAcceptanceMembership(t *testing.T) {
	manifestPath, projectID := publishedCloneFixture(t)
	data := t.TempDir()
	destination := filepath.Join(cloneWorkingDirectory(t), "membership")
	added := testutil.NewPushedGitRepository(t)
	added.CommitFile("extra.txt", "extra\n", "initial extra")
	addedHead := strings.TrimSpace(compositionGitOutput(t, added.Path, "rev-parse", "HEAD"))
	addedRemote := strings.TrimSpace(compositionGitOutput(t, added.Path, "remote", "get-url", "origin"))
	// Ignore coverage is a pre-existing parent safety condition. Seed it before
	// clone so the subsequent manifest addition is a supported transition,
	// rather than testing an intentional preflight refusal.
	source := testutil.GitRepository{Path: filepath.Dir(manifestPath)}
	ignorePath := filepath.Join(source.Path, ".gitignore")
	ignore, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	source.CommitFile(".gitignore", string(ignore)+"/extra/\n", "ignore extra mount")
	source.Run(t, "push", "origin", "main")
	if result := testutil.RunCommand(t, cli.Execute, "clone", manifestPath, destination, "--data-dir", data); result.Err != nil {
		t.Fatalf("clone = %#v", result)
	}
	compositionPublishCandidate(t, manifestPath, func(candidate *config.PortableManifest) {
		candidate.Repositories["extra"] = config.PortableRepository{
			Clone:         config.CloneSource{Remote: "origin", URL: addedRemote},
			Upstream:      config.Upstream{Remote: "origin", Branch: "main", Merge: "refs/heads/main"},
			Identity:      config.RepositoryIdentity{InitialCommits: []string{addedHead}},
			Parent:        "root",
			Mount:         "extra",
			DefaultBranch: "main",
		}
	})
	compositionUpdate(t, destination, data, manifestPath)
	compositionAssertWorkspaceAuthority(t, destination, data, projectID, map[string]string{
		"root": destination, "backend": filepath.Join(destination, "backend"), "extra": filepath.Join(destination, "extra"),
	})
	if _, err := os.Stat(filepath.Join(destination, "extra", "extra.txt")); err != nil {
		t.Fatalf("added checkout missing: %v", err)
	}
	if reconciliation, err := os.ReadFile(filepath.Join(data, "projects", projectID, "reconciliation.json")); err != nil {
		t.Fatalf("addition reconciliation = %v", err)
	} else if retained, decodeErr := service.DecodeUpdateReconciliation(reconciliation); decodeErr != nil || len(retained) != 0 {
		t.Fatalf("addition reconciliation = %#v, %v", retained, decodeErr)
	}
	beforeFailure := compositionCaptureInventory(t, destination, data, manifestPath, projectID)
	failure := testutil.RunCommand(t, cli.Execute, "exec", "--project", destination, "--data-dir", data, "--json", "--", "git", "rev-parse", "--verify", "refs/heads/not-present")
	var failed struct {
		Repositories []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"repositories"`
	}
	if failure.Err == nil || failure.Stderr != "" || json.Unmarshal([]byte(failure.Stdout), &failed) != nil || len(failed.Repositories) != 3 {
		t.Fatalf("aggregate exec failure = %#v decoded=%#v", failure, failed)
	}
	failureIDs := make([]string, 0, len(failed.Repositories))
	for _, repository := range failed.Repositories {
		failureIDs = append(failureIDs, repository.ID)
		if repository.Status != "failed" {
			t.Fatalf("aggregate exec did not continue through %s: %#v", repository.ID, failed.Repositories)
		}
	}
	compositionIDs(t, "aggregate exec failure", failureIDs, []string{"root", "backend", "extra"})
	compositionAssertInventory(t, "aggregate exec failure", beforeFailure, compositionCaptureInventory(t, destination, data, manifestPath, projectID))

	namedPath := filepath.Join(t.TempDir(), "named-workspace")
	if result := testutil.RunCommand(t, cli.Execute, "create", "acceptance/named", "--project", destination, "--data-dir", data, "--path", namedPath); result.Err != nil {
		t.Fatalf("create named workspace = %#v", result)
	}
	project, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: destination, ProjectPath: destination, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	named, err := service.RequireWorkspace(project, data, "acceptance/named")
	if err != nil {
		t.Fatal(err)
	}
	if got, resolveErr := named.ResolveRepository("extra"); resolveErr != nil || got != filepath.Join(namedPath, "extra") {
		t.Fatalf("named extra = %q, %v", got, resolveErr)
	}
	if result := testutil.RunCommand(t, cli.Execute, "delete", "acceptance/named", "--project", destination, "--data-dir", data); result.Err != nil {
		t.Fatalf("delete named workspace = %#v", result)
	}

	compositionPublishCandidate(t, manifestPath, func(candidate *config.PortableManifest) { delete(candidate.Repositories, "backend") })
	compositionUpdate(t, destination, data, manifestPath)
	compositionAssertWorkspaceAuthority(t, destination, data, projectID, map[string]string{
		"root": destination, "extra": filepath.Join(destination, "extra"),
	})
	if _, err := os.Stat(filepath.Join(destination, "backend")); err != nil {
		t.Fatalf("removed repository was not retained: %v", err)
	}
	reconciliation, err := os.ReadFile(filepath.Join(data, "projects", projectID, "reconciliation.json"))
	if err != nil {
		t.Fatal(err)
	}
	retained, err := service.DecodeUpdateReconciliation(reconciliation)
	if err != nil || len(retained) != 1 || retained[0].RepositoryID != "backend" || retained[0].Path != filepath.Join(destination, "backend") {
		t.Fatalf("retained reconciliation = %#v, %v", retained, err)
	}
	compositionAssertPostMembershipUsability(t, destination, data, manifestPath, projectID, reconciliation)
}

func compositionPublishCandidate(t *testing.T, manifestPath string, mutate func(*config.PortableManifest)) {
	t.Helper()
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := config.LoadPortableManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&candidate)
	encoded, err := config.MarshalPortableManifest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	source := testutil.GitRepository{Path: filepath.Dir(manifestPath)}
	source.Run(t, "add", "--", filepath.Base(manifestPath))
	source.Run(t, "commit", "-m", "advance composition manifest")
	source.Run(t, "push", "origin", "main")
}

func compositionUpdate(t *testing.T, destination, data, manifestPath string) {
	t.Helper()
	// Update preflight deliberately proves fast-forward ancestry from local
	// configured-ref facts. Refresh the declared refs explicitly first; this
	// cannot advance a branch or worktree and keeps the later update authority
	// fully observable.
	fetched := testutil.RunCommand(t, cli.Execute, "fetch", "--project", destination, "--data-dir", data, "--json")
	if fetched.Err != nil || fetched.Stderr != "" || !json.Valid([]byte(fetched.Stdout)) {
		t.Fatalf("pre-update fetch = %#v", fetched)
	}
	arguments := []string{"update", "--project", destination, "--data-dir", data, "--from", manifestPath, "--json"}
	dry := testutil.RunCommand(t, cli.Execute, append(append([]string(nil), arguments...), "--dry-run")...)
	if dry.Err != nil || dry.Stderr != "" || !json.Valid([]byte(dry.Stdout)) {
		project, projectErr := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: destination, ProjectPath: destination, DataDir: data})
		if projectErr != nil {
			t.Fatalf("update dry-run = %#v; resolve=%v", dry, projectErr)
		}
		workspace, workspaceErr := service.RequireWorkspace(project, data, "default")
		snapshot, _, snapshotErr := service.CollectUpdateSnapshot(context.Background(), project, workspace, data, manifestPath, nil)
		t.Fatalf("update dry-run = %#v; workspace=%v snapshot=%#v err=%v", dry, workspaceErr, snapshot.Failures(), snapshotErr)
	}
	result := testutil.RunCommand(t, cli.Execute, arguments...)
	if result.Err != nil || result.Stderr != "" || !json.Valid([]byte(result.Stdout)) {
		t.Fatalf("update = %#v", result)
	}
}

func compositionAssertWorkspaceAuthority(t *testing.T, destination, data, projectID string, expected map[string]string) {
	t.Helper()
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: destination, ProjectPath: destination, DataDir: data})
	if err != nil || resolution.Project.ID != projectID || resolution.Workspace.Name != "default" {
		t.Fatalf("resolver authority = %#v, %v", resolution, err)
	}
	if len(resolution.Project.Repositories) != len(expected) || len(resolution.Workspace.Checkouts) != len(expected) {
		t.Fatalf("project/workspace membership = %#v / %#v", resolution.Project.Repositories, resolution.Workspace.Checkouts)
	}
	for id, want := range expected {
		got, resolveErr := resolution.Workspace.ResolveRepository(id)
		if resolveErr != nil || got != want {
			t.Fatalf("resolved %s = %q, %v; want %q", id, got, resolveErr, want)
		}
		if common := strings.TrimSpace(compositionGitOutput(t, got, "rev-parse", "--git-common-dir")); common == "" {
			t.Fatalf("repository %s has no common Git directory", id)
		}
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil || registry.Projects[projectID].ConfigPath != filepath.Join(destination, ".wtree.yml") {
		t.Fatalf("registry authority = %#v, %v", registry, err)
	}
}

func compositionAssertPostMembershipUsability(t *testing.T, destination, data, candidate, projectID string, reconciliation []byte) {
	t.Helper()
	expectedIDs := []string{"root", "extra"}
	expectedPaths := map[string]string{"root": destination, "extra": filepath.Join(destination, "extra")}
	retainedPath := filepath.Join(destination, "backend")
	if output := compositionGitOutput(t, retainedPath, "status", "--porcelain"); output != "" {
		t.Fatalf("retained backend is not usable: %q", output)
	}

	beforeDoctor := compositionCaptureInventory(t, destination, data, candidate, projectID)
	doctor := testutil.RunCommand(t, cli.Execute, "doctor", "--project", destination, "--data-dir", data, "--json")
	var doctorReport service.DoctorReport
	if doctor.Err != nil || doctor.Stderr != "" || json.Unmarshal([]byte(doctor.Stdout), &doctorReport) != nil {
		t.Fatalf("post-membership doctor = %#v decoded=%#v", doctor, doctorReport)
	}
	compositionAssertDoctorRows(t, doctorReport.Repositories, expectedIDs, expectedPaths)
	if !compositionHasFinding(doctorReport.Findings, "retained-unmanaged-repository", "backend") {
		t.Fatalf("post-membership doctor omitted retained backend: %#v", doctorReport.Findings)
	}
	compositionAssertInventory(t, "post-membership doctor", beforeDoctor, compositionCaptureInventory(t, destination, data, candidate, projectID))

	beforeStatus := compositionCaptureInventory(t, destination, data, candidate, projectID)
	status := testutil.RunCommand(t, cli.Execute, "status", "--project", destination, "--data-dir", data, "--json")
	var statusReport service.WorkspaceStatus
	if status.Err != nil || status.Stderr != "" || json.Unmarshal([]byte(status.Stdout), &statusReport) != nil {
		t.Fatalf("post-membership status = %#v decoded=%#v", status, statusReport)
	}
	compositionAssertStatusRows(t, statusReport.Repositories, expectedIDs, expectedPaths)
	if !compositionHasStatusDrift(statusReport.Drift, "backend", "retained-unmanaged") {
		t.Fatalf("post-membership status omitted retained backend: %#v", statusReport.Drift)
	}
	compositionAssertInventory(t, "post-membership status", beforeStatus, compositionCaptureInventory(t, destination, data, candidate, projectID))

	beforeExec := compositionCaptureInventory(t, destination, data, candidate, projectID)
	execution := testutil.RunCommand(t, cli.Execute, "exec", "--project", destination, "--data-dir", data, "--json", "--", "git", "rev-parse", "--is-inside-work-tree")
	var execResult service.ExecResult
	if execution.Err != nil || execution.Stderr != "" || json.Unmarshal([]byte(execution.Stdout), &execResult) != nil || execResult.Status != service.AggregateStatusCompleted {
		t.Fatalf("post-membership exec = %#v decoded=%#v", execution, execResult)
	}
	compositionAssertExecRows(t, execResult.Repositories, expectedIDs, expectedPaths)
	compositionAssertInventory(t, "post-membership exec", beforeExec, compositionCaptureInventory(t, destination, data, candidate, projectID))

	fetched := testutil.RunCommand(t, cli.Execute, "fetch", "--project", destination, "--data-dir", data, "--json")
	var fetchResult service.FetchResult
	if fetched.Err != nil || fetched.Stderr != "" || json.Unmarshal([]byte(fetched.Stdout), &fetchResult) != nil || fetchResult.Status != service.AggregateStatusCompleted {
		t.Fatalf("post-membership fetch = %#v decoded=%#v", fetched, fetchResult)
	}
	compositionAssertFetchRows(t, fetchResult.Repositories, expectedIDs, expectedPaths)

	t.Setenv("WTREE_DATA_HOME", data)
	beforePush := compositionCaptureInventory(t, destination, data, candidate, projectID)
	push := testutil.RunCommand(t, cli.Execute, "push", "--project", destination, "--json")
	var pushResult service.PushResult
	if push.Err != nil || push.Stderr != "" || json.Unmarshal([]byte(push.Stdout), &pushResult) != nil || pushResult.Status != service.PushStatusReady {
		t.Fatalf("post-membership push = %#v decoded=%#v", push, pushResult)
	}
	compositionAssertPushRows(t, pushResult.Repositories, expectedIDs, map[string]string{"root": ".", "extra": "extra"})
	compositionAssertInventory(t, "post-membership push", beforePush, compositionCaptureInventory(t, destination, data, candidate, projectID))
	if after, readErr := os.ReadFile(filepath.Join(data, "projects", projectID, "reconciliation.json")); readErr != nil || !bytes.Equal(after, reconciliation) {
		t.Fatalf("post-membership commands changed reconciliation: %v", readErr)
	}
}

func compositionHasFinding(findings []service.DoctorFinding, code, repositoryID string) bool {
	for _, finding := range findings {
		if finding.Code == code && finding.RepositoryID == repositoryID {
			return true
		}
	}
	return false
}

func compositionHasStatusDrift(drift []service.StatusDrift, id, status string) bool {
	for _, entry := range drift {
		if entry.ID == id && entry.Status == status {
			return true
		}
	}
	return false
}

func compositionGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}

type compositionInventory struct {
	Authority updateDryRunInventory
	Files     map[string]updateDryRunFileFact
	Git       map[string]string
}

// compositionCaptureInventory confines recursive inspection to resolver-owned
// checkout paths, the command-owned data tree, and local bare origins that
// those checkouts name. It never follows a symlink or discovers ambient user
// paths. Git facts include the mutable index, refs, config, FETCH_HEAD, and
// common identity so a read-only command cannot hide an authority mutation.
func compositionCaptureInventory(t *testing.T, projectPath, data, candidate, projectID string) compositionInventory {
	t.Helper()
	inventory := compositionInventory{Authority: captureUpdateDryRunInventory(t, projectPath, data, candidate, projectID), Files: map[string]updateDryRunFileFact{}, Git: map[string]string{}}
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: projectPath, ProjectPath: projectPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(resolution.Project.Repositories))
	for _, repository := range resolution.Project.ParentFirst() {
		path, resolveErr := resolution.Workspace.ResolveRepository(repository.ID)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		paths = append(paths, path)
		compositionCaptureTree(t, inventory.Files, "checkout:"+repository.ID, path)
		for _, arguments := range [][]string{{"rev-parse", "HEAD"}, {"show-ref", "--head"}, {"write-tree"}, {"config", "--list", "--show-origin"}, {"rev-parse", "--git-common-dir"}} {
			inventory.Git["checkout:"+repository.ID+":"+strings.Join(arguments, " ")] = compositionGitOutput(t, path, arguments...)
		}
		fetchHead := strings.TrimSpace(compositionGitOutput(t, path, "rev-parse", "--git-path", "FETCH_HEAD"))
		if !filepath.IsAbs(fetchHead) {
			fetchHead = filepath.Join(path, fetchHead)
		}
		if contents, readErr := os.ReadFile(fetchHead); readErr == nil {
			inventory.Files["checkout:"+repository.ID+":FETCH_HEAD"] = updateDryRunFileFact{Mode: 0o600, Bytes: contents}
		} else if !os.IsNotExist(readErr) {
			t.Fatalf("read FETCH_HEAD for %s: %v", repository.ID, readErr)
		}
	}
	remotes := map[string]bool{}
	for _, path := range paths {
		remote := strings.TrimSpace(compositionGitOutput(t, path, "remote", "get-url", "origin"))
		if !filepath.IsAbs(remote) || remotes[remote] {
			continue
		}
		if bare := strings.TrimSpace(compositionGitOutput(t, remote, "rev-parse", "--is-bare-repository")); bare != "true" {
			t.Fatalf("fixture origin %q is not bare", remote)
		}
		remotes[remote] = true
		compositionCaptureTree(t, inventory.Files, "origin:"+remote, remote)
		for _, arguments := range [][]string{{"rev-parse", "HEAD"}, {"show-ref", "--head"}, {"config", "--list", "--show-origin"}} {
			inventory.Git["origin:"+remote+":"+strings.Join(arguments, " ")] = compositionGitOutput(t, remote, arguments...)
		}
	}
	return inventory
}

func compositionCaptureTree(t *testing.T, destination map[string]updateDryRunFileFact, label, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		key := label + ":" + filepath.ToSlash(strings.TrimPrefix(path, root))
		fact := updateDryRunFileFact{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			fact.Bytes, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		destination[key] = fact
		return nil
	}); err != nil {
		t.Fatalf("capture %s: %v", label, err)
	}
}

func compositionAssertInventory(t *testing.T, stage string, before, after compositionInventory) {
	t.Helper()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("%s mutated resolver-owned checkout/origin/project/data/Git authority:\nbefore=%#v\nafter=%#v", stage, before, after)
	}
}

func compositionIDs(t *testing.T, label string, actual, expected []string) {
	t.Helper()
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s IDs = %#v, want %#v", label, actual, expected)
	}
}

func compositionSortedIDs(values []string) []string {
	value := append([]string(nil), values...)
	sort.Strings(value)
	return value
}

func compositionAssertDoctorRows(t *testing.T, rows []service.DoctorRepository, expected []string, paths map[string]string) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.Mount == "" || row.ResolvedPath != paths[row.ID] || row.Status != "known" || row.Missing || row.IdentityMismatch || row.MountMismatch || row.BranchMismatch || row.HeadMismatch {
			t.Fatalf("doctor row = %#v", row)
		}
	}
	compositionIDs(t, "doctor", ids, expected)
}

func compositionAssertStatusRows(t *testing.T, rows []service.RepositoryStatus, expected []string, paths map[string]string) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.Mount == "" || row.ResolvedPath != paths[row.ID] || !row.Clean || row.Status != "clean" || row.Missing || row.IdentityMismatch || row.MountMismatch || row.BranchMismatch || row.HeadMismatch {
			t.Fatalf("status row = %#v", row)
		}
	}
	compositionIDs(t, "status", ids, expected)
}

func compositionAssertExecRows(t *testing.T, rows []service.ExecRepositoryResult, expected []string, paths map[string]string) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.Mount == "" || row.Path != paths[row.ID] || row.Status != service.AggregateStatusCompleted || row.ExitCode == nil || *row.ExitCode != 0 || strings.TrimSpace(row.Stdout) != "true" || row.Failure != nil {
			t.Fatalf("exec row = %#v", row)
		}
	}
	compositionIDs(t, "exec", ids, expected)
}

func compositionAssertFetchRows(t *testing.T, rows []service.FetchRepositoryResult, expected []string, paths map[string]string) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.Mount == "" || row.Path != paths[row.ID] || row.Status != service.AggregateStatusCompleted || row.Remote != "origin" || row.RemoteRef != "refs/heads/main" || row.ActualRemoteCommit == "" || row.Failure != nil {
			t.Fatalf("fetch row = %#v", row)
		}
	}
	compositionIDs(t, "fetch", ids, expected)
}

func compositionAssertPushRows(t *testing.T, rows []service.PushRepositoryResult, expected []string, mounts map[string]string) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.Mount != mounts[row.ID] || row.Branch != "main" || row.Head == "" || row.ObservedCommit == "" || row.Failure != nil {
			t.Fatalf("push row = %#v", row)
		}
	}
	compositionIDs(t, "push", ids, expected)
}
