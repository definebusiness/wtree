package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecutePushHasExactReadinessSurface(t *testing.T) {
	result := testutil.RunCommand(t, cli.Execute, "push", "--help")
	if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "never runs git push") || !strings.Contains(result.Stdout, "Publication remains a manual or future workflow") {
		t.Fatalf("push help = %#v", result)
	}
	if strings.Contains(result.Stdout, "--data-dir") || !strings.Contains(result.Stdout, "--workspace") || !strings.Contains(result.Stdout, "--json") || !strings.Contains(result.Stdout, "--project") {
		t.Fatalf("push help options = %q", result.Stdout)
	}
	for _, arguments := range [][]string{{"push", "--dry-run"}, {"push", "--verbose"}, {"push", "--data-dir", "x"}, {"push", "extra"}} {
		result = testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Errorf("push surface %v = %#v", arguments, result)
		}
	}
}

func TestExecutePushJSONIsOneSilentReadinessDocument(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if initialized := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", data); initialized.Err != nil {
		t.Fatalf("init = %#v", initialized)
	}
	repository.Run(t, "add", ".gitignore", "project.wtree.yml")
	repository.Run(t, "commit", "-m", "publish manifest")
	repository.Run(t, "push", "origin", "main")
	t.Setenv("WTREE_DATA_HOME", data)
	result := testutil.RunCommand(t, cli.Execute, "push", "--project", repository.Path, "--json")
	if result.Err != nil || result.Stderr != "" || strings.Count(result.Stdout, "\n") != 1 {
		t.Fatalf("push json = %#v", result)
	}
	var value struct {
		Version      int    `json:"version"`
		Operation    string `json:"operation"`
		Status       string `json:"status"`
		Repositories []struct {
			Status   string `json:"status"`
			Findings []struct {
				Code string `json:"code"`
			} `json:"findings"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil || value.Version != 1 || value.Operation != "push" || value.Status != "ready" || len(value.Repositories) != 1 || value.Repositories[0].Status != "ready" || len(value.Repositories[0].Findings) != 0 {
		t.Fatalf("push document = %s %#v %v", result.Stdout, value, err)
	}
}

func TestExecutePushBlockedHumanAndJSONHaveOneDeterministicDocument(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if initialized := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", data); initialized.Err != nil {
		t.Fatalf("init = %#v", initialized)
	}
	repository.Run(t, "add", ".gitignore", "project.wtree.yml")
	repository.Run(t, "commit", "-m", "publish manifest")
	repository.Run(t, "push", "origin", "main")
	if err := os.WriteFile(filepath.Join(repository.Path, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTREE_DATA_HOME", data)
	jsonResult := testutil.RunCommand(t, cli.Execute, "push", "--project", repository.Path, "--json")
	if jsonResult.Err == nil || cli.ExitCode(jsonResult.Err) != 8 || jsonResult.Stderr != "" || strings.Count(jsonResult.Stdout, "\n") != 1 {
		t.Fatalf("blocked JSON = %#v", jsonResult)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(jsonResult.Stdout), &document); err != nil || document["version"] != float64(1) || document["operation"] != "push" || document["status"] != "blocked" {
		t.Fatalf("blocked JSON document = %q, %#v, %v", jsonResult.Stdout, document, err)
	}
	repositories, ok := document["repositories"].([]any)
	if !ok || len(repositories) != 1 {
		t.Fatalf("blocked JSON repositories = %#v", document)
	}
	entry := repositories[0].(map[string]any)
	if entry["status"] != "blocked" || entry["findings"].([]any)[0].(map[string]any)["code"] != "dirty" {
		t.Fatalf("blocked JSON entry = %#v", entry)
	}
	human := testutil.RunCommand(t, cli.Execute, "push", "--project", repository.Path)
	if human.Err == nil || cli.ExitCode(human.Err) != 8 || human.Stderr != "" || !strings.Contains(human.Stdout, "Repository: root") || !strings.Contains(human.Stdout, "finding: dirty") || !strings.Contains(human.Stdout, "Workspace: default status=blocked") {
		t.Fatalf("blocked human = %#v", human)
	}
}

// This is the public-boundary regression for the R5 evidence fix.  Every
// invocation goes through ExecuteContext, which in turn uses
// Resolver.ResolveReadOnly and the process runtime data-home seam.  It would
// fail if a test-only resolver/subcommand or an unregistered data directory
// were substituted for the production path.
func TestExecuteContextPushResolverAuthorityAndReadOnlySnapshots(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		fixture := newPublicPushFixture(t)
		before := pushPublicAuthoritySnapshot(t, fixture)
		stdout, stderr, err := executePublicPush(t, context.Background(), fixture, nil, nil)
		if err != nil || stderr != "" || strings.Count(stdout, "\n") != 1 {
			t.Fatalf("ready push = stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		pushPublicDocument(t, stdout, "ready")
		pushPublicAssertNoPrivateOutput(t, fixture, stdout+stderr+errorString(err))
		pushPublicAssertSnapshot(t, fixture, before, "ready")
	})

	t.Run("blocked partial workspace", func(t *testing.T) {
		fixture := newPublicPushFixture(t)
		defaultWorkspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
		if err != nil {
			t.Fatal(err)
		}
		partial := publicPushWorkspaceState(defaultWorkspace)
		partial.ID, partial.Name = "partial", "partial"
		partial.Partial, partial.MissingRepositoryIDs = true, []string{fixture.childID}
		partial.Repositories = map[string]store.CheckoutState{fixture.project.BaseRepository: {
			Branch:       publicPushCheckout(defaultWorkspace, fixture.project.BaseRepository).Branch,
			Mount:        ".",
			ResolvedPath: defaultWorkspace.RootPath,
			Head:         publicPushCheckout(defaultWorkspace, fixture.project.BaseRepository).Head,
		}}
		if err := store.WriteWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, partial.ID), partial); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, partial.ID)); err != nil {
			t.Fatal(err)
		}
		before := pushPublicAuthoritySnapshot(t, fixture)
		stdout, stderr, err := executePublicPush(t, context.Background(), fixture, []string{"--workspace", "partial"}, nil)
		if err == nil || cli.ExitCode(err) != 8 || stderr != "" {
			t.Fatalf("partial push = stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		value := pushPublicDocument(t, stdout, "blocked")
		if value["partial"] != true || value["workspace"] != "partial" {
			t.Fatalf("partial document = %#v", value)
		}
		pushPublicAssertNoPrivateOutput(t, fixture, stdout+stderr+errorString(err))
		pushPublicAssertSnapshot(t, fixture, before, "partial")
	})

	t.Run("operational failure", func(t *testing.T) {
		fixture := newPublicPushFixture(t)
		remote := strings.TrimSpace(pushPublicGitOutput(t, fixture.root.Path, "remote", "get-url", "origin"))
		if err := os.RemoveAll(remote); err != nil {
			t.Fatal(err)
		}
		before := pushPublicAuthoritySnapshot(t, fixture)
		stdout, stderr, err := executePublicPush(t, context.Background(), fixture, nil, nil)
		if err == nil || cli.ExitCode(err) != 6 || stderr != "" {
			t.Fatalf("failed push = stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		pushPublicDocument(t, stdout, "failed")
		pushPublicAssertNoPrivateOutput(t, fixture, stdout+stderr+errorString(err))
		pushPublicAssertSnapshot(t, fixture, before, "failure")
	})

	t.Run("canceled resolver boundary", func(t *testing.T) {
		fixture := newPublicPushFixture(t)
		before := pushPublicAuthoritySnapshot(t, fixture)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stdout, stderr, err := executePublicPush(t, ctx, fixture, nil, nil)
		if !errors.Is(err, context.Canceled) || stderr != "" {
			t.Fatalf("canceled push = stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		var envelope struct {
			Success bool `json:"success"`
			Error   struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if strings.Count(stdout, "\n") != 1 || json.Unmarshal([]byte(stdout), &envelope) != nil || envelope.Success || envelope.Error.Code != "internal" || envelope.Error.Message != "internal: context canceled" {
			t.Fatalf("canceled envelope = %q %#v", stdout, envelope)
		}
		pushPublicAssertNoPrivateOutput(t, fixture, stdout+stderr+errorString(err))
		pushPublicAssertSnapshot(t, fixture, before, "canceled")
	})

	t.Run("partial writer failure", func(t *testing.T) {
		fixture := newPublicPushFixture(t)
		before := pushPublicAuthoritySnapshot(t, fixture)
		sentinel := errors.New("partial writer sentinel")
		writer := &publicPushFailingWriter{cause: sentinel}
		stdout, stderr, err := executePublicPush(t, context.Background(), fixture, nil, writer)
		if !errors.Is(err, sentinel) || stderr != "" || writer.writes != 1 || len(stdout) == 0 || strings.Count(stdout, "\n{") != 0 || strings.Contains(stdout, `"error"`) {
			t.Fatalf("writer push = stdout=%q stderr=%q err=%v writes=%d", stdout, stderr, err, writer.writes)
		}
		pushPublicAssertNoPrivateOutput(t, fixture, stdout+stderr+errorString(err))
		pushPublicAssertSnapshot(t, fixture, before, "writer")
	})
}

type publicPushFixture struct {
	root               testutil.PushedGitRepository
	project            domain.Project
	data               string
	childID            string
	checkouts          []domain.Checkout
	markers            []string
	reconciliationPath string
	journalPath        string
	backupPath         string
	recoveryPath       string
}

func newPublicPushFixture(t *testing.T) publicPushFixture {
	t.Helper()
	base := newExecCLIFixture(t)
	base.root.Run(t, "add", ".gitignore", "project.wtree.yml")
	base.root.Run(t, "commit", "-m", "publish manifest")
	base.root.Run(t, "push", "origin", "main")
	workspace, err := service.RequireWorkspace(base.project, base.data, "default")
	if err != nil {
		t.Fatal(err)
	}
	fixture := publicPushFixture{root: base.root, project: base.project, data: base.data, childID: base.childID, checkouts: append([]domain.Checkout(nil), workspace.Checkouts...)}
	seedPublicPushResolverAuthority(t, &fixture, workspace)
	for _, checkout := range workspace.Checkouts {
		marker := filepath.Join(t.TempDir(), checkout.RepositoryID+"-fsmonitor-marker")
		hook := filepath.Join(t.TempDir(), checkout.RepositoryID+"-fsmonitor-hook")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch \""+marker+"\"\nprintf '2\\n'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		pushPublicGit(t, checkout.ResolvedPath, "config", "core.fsmonitor", hook)
		fixture.markers = append(fixture.markers, marker)
	}
	t.Setenv("WTREE_DATA_HOME", fixture.data)
	return fixture
}

// seedPublicPushResolverAuthority is deliberately built from the persisted
// production formats that a public push invocation consumes.  In particular,
// it carries an active but read-only-safe update operation with one opaque
// backup blob; Resolver.ResolveReadOnly must remain available while mutators
// correctly see that authority as active.
func seedPublicPushResolverAuthority(t *testing.T, fixture *publicPushFixture, workspace domain.Workspace) {
	t.Helper()
	named := publicPushWorkspaceState(workspace)
	named.ID, named.Name = "named", "named"
	named.Path = filepath.Join(fixture.data, "named-workspace")
	mounts := make(map[string]string, len(named.Repositories))
	for id, checkout := range named.Repositories {
		mounts[id] = checkout.Mount
	}
	paths, err := fixture.project.EffectivePaths(named.Path, mounts)
	if err != nil {
		t.Fatal(err)
	}
	for id, checkout := range named.Repositories {
		checkout.ResolvedPath = paths[id]
		named.Repositories[id] = checkout
	}
	if err := store.WriteWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, named.ID), named); err != nil {
		t.Fatal(err)
	}
	fixture.reconciliationPath = filepath.Join(fixture.data, "projects", fixture.project.ID, "reconciliation.json")
	reconciliation, err := service.EncodeUpdateReconciliation(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRawAtomic(fixture.reconciliationPath, reconciliation); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(fixture.root.Path, "project.wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.journalPath, err = service.UpdateJournalPath(fixture.data, fixture.project.ID, "push-evidence")
	if err != nil {
		t.Fatal(err)
	}
	fixture.backupPath = filepath.Join(filepath.Dir(fixture.journalPath), "backups", "tracked-manifest.bin")
	if err := os.MkdirAll(filepath.Dir(fixture.backupPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRawAtomic(fixture.backupPath, backup); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(backup)
	digest := fmt.Sprintf("%x", sum[:])
	journal := service.UpdateJournal{Version: service.UpdateJournalVersion, OperationID: "push-evidence", ProjectID: fixture.project.ID, PlanDigest: digest, Generations: service.UpdatePlanGenerations{CurrentManifestSHA256: digest, CandidateManifestSHA256: digest, LocalConfigSHA256: digest, RegistrySHA256: digest, DefaultStateSHA256: digest, ReconciliationSHA256: digest}, Backups: []service.UpdateJournalBackup{{Kind: "tracked-manifest", File: "tracked-manifest.bin", Existed: true, Mode: 0o600, Length: int64(len(backup)), SHA256: digest}}, RollbackState: "active", Progress: []service.UpdateJournalEffect{}}
	journalBytes, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRawAtomic(fixture.journalPath, append(journalBytes, '\n')); err != nil {
		t.Fatal(err)
	}
	fixture.recoveryPath = service.RecoveryRecordPath(fixture.data, plan.WorkspacePlan{ProjectID: fixture.project.ID, WorkspaceID: named.ID})
	if err := store.WriteRecovery(fixture.recoveryPath, store.RecoveryRecord{Version: store.Version, ProjectID: fixture.project.ID, WorkspaceID: named.ID, Operation: "update", FailedStep: "terminal-cleanup", CompletedSteps: []string{"repository-effects-terminal"}, UnrevertedSteps: []string{"terminal-cleanup"}}); err != nil {
		t.Fatal(err)
	}
	pushPublicReopenAuthority(t, *fixture)
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: fixture.root.Path, ProjectPath: fixture.root.Path, DataDir: fixture.data})
	if err != nil {
		t.Fatalf("ResolveReadOnly seeded authority: %v", err)
	}
	fixture.project, fixture.checkouts = resolution.Project, append([]domain.Checkout(nil), resolution.Workspace.Checkouts...)
}

func pushPublicReopenAuthority(t *testing.T, fixture publicPushFixture) {
	t.Helper()
	if _, err := store.ReadRegistry(filepath.Join(fixture.data, "registry.json")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"default", "named"} {
		if _, err := store.ReadWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, name)); err != nil {
			t.Fatal(err)
		}
	}
	reconciliation, err := os.ReadFile(fixture.reconciliationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DecodeUpdateReconciliation(reconciliation); err != nil {
		t.Fatal(err)
	}
	journalBytes, err := os.ReadFile(fixture.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var journal service.UpdateJournal
	if err := json.Unmarshal(journalBytes, &journal); err != nil || journal.Validate() != nil || len(journal.Backups) != 1 {
		t.Fatalf("reopen journal = %#v, %v", journal, err)
	}
	backup, err := os.Stat(fixture.backupPath)
	if err != nil || backup.Mode().Perm() != 0o600 || backup.Size() != journal.Backups[0].Length {
		t.Fatalf("reopen backup = %#v, %v", backup, err)
	}
	bytes, err := os.ReadFile(fixture.backupPath)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(bytes)) != journal.Backups[0].SHA256 {
		t.Fatalf("reopen backup bytes = %v", err)
	}
	if _, err := store.ReadRecovery(fixture.recoveryPath); err != nil {
		t.Fatal(err)
	}
	if err := service.RefuseActiveUpdateJournal(fixture.data, fixture.project.ID); err == nil || !strings.Contains(err.Error(), "an update journal is active") {
		t.Fatalf("strictly reopen active journal = %v", err)
	}
}

func executePublicPush(t *testing.T, ctx context.Context, fixture publicPushFixture, extra []string, stdoutOverride *publicPushFailingWriter) (string, string, error) {
	t.Helper()
	arguments := append([]string{"push", "--project", fixture.root.Path, "--json"}, extra...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var writer io.Writer = &stdout
	if stdoutOverride != nil {
		stdoutOverride.output.Reset()
		writer = stdoutOverride
	}
	err := cli.ExecuteContext(ctx, arguments, writer, &stderr)
	if stdoutOverride != nil {
		return stdoutOverride.output.String(), stderr.String(), err
	}
	return stdout.String(), stderr.String(), err
}

func publicPushWorkspaceState(workspace domain.Workspace) store.WorkspaceState {
	checkouts := make(map[string]store.CheckoutState, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = store.CheckoutState{Branch: checkout.Branch, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath, Head: checkout.Head, Detached: checkout.Detached}
	}
	return store.WorkspaceState{Version: store.Version, ID: workspace.ID, Name: workspace.Name, Path: workspace.RootPath, Partial: workspace.Partial, MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...), Repositories: checkouts}
}

func publicPushCheckout(workspace domain.Workspace, repositoryID string) domain.Checkout {
	for _, checkout := range workspace.Checkouts {
		if checkout.RepositoryID == repositoryID {
			return checkout
		}
	}
	panic("missing public push checkout " + repositoryID)
}

func pushPublicDocument(t *testing.T, output, status string) map[string]any {
	t.Helper()
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("push JSON documents = %q", output)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(output), &value); err != nil || value["version"] != float64(1) || value["operation"] != "push" || value["status"] != status {
		t.Fatalf("push document = %q %#v %v", output, value, err)
	}
	return value
}

func pushPublicAuthoritySnapshot(t *testing.T, fixture publicPushFixture) string {
	t.Helper()
	parts := []string{"data=" + pushPublicPathSnapshot(t, fixture.data), "project=" + pushPublicPathSnapshot(t, fixture.root.Path)}
	for _, workspaceName := range []string{"default", "partial", "named"} {
		parts = append(parts, "workspace:"+workspaceName+"="+pushPublicPathSnapshot(t, service.WorkspaceStatePath(fixture.data, fixture.project.ID, workspaceName)))
	}
	for _, relative := range []string{
		"registry.json", filepath.Join("projects", fixture.project.ID, "reconciliation.json"), filepath.Join("projects", fixture.project.ID, "update"), filepath.Join("projects", fixture.project.ID, "recovery"), filepath.Join("locks", "registry.lock"), filepath.Join("locks", fixture.project.ID+".lock"), "staging", "tmp",
	} {
		parts = append(parts, "authority:"+relative+"="+pushPublicPathSnapshot(t, filepath.Join(fixture.data, relative)))
	}
	for label, path := range map[string]string{"reconciliation": fixture.reconciliationPath, "journal": fixture.journalPath, "backup": fixture.backupPath, "recovery": fixture.recoveryPath} {
		if path == "" || pushPublicPathSnapshot(t, path) == "ABSENT" {
			t.Fatalf("public authority %s is absent", label)
		}
		parts = append(parts, label+"="+pushPublicPathSnapshot(t, path))
	}
	for _, checkout := range fixture.checkouts {
		parts = append(parts, "checkout:"+checkout.RepositoryID+"="+pushPublicCheckoutSnapshot(t, checkout.ResolvedPath))
		remote := strings.TrimSpace(pushPublicGitOutput(t, checkout.ResolvedPath, "remote", "get-url", "origin"))
		parts = append(parts, "remote:"+checkout.RepositoryID+"="+pushPublicPathSnapshot(t, remote))
	}
	for _, marker := range fixture.markers {
		parts = append(parts, "fsmonitor="+pushPublicPathSnapshot(t, marker))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n\x00\n")
}

func pushPublicAssertSnapshot(t *testing.T, fixture publicPushFixture, before, label string) {
	t.Helper()
	if after := pushPublicAuthoritySnapshot(t, fixture); after != before {
		t.Fatalf("push %s mutated public authority", label)
	}
}

func pushPublicCheckoutSnapshot(t *testing.T, path string) string {
	t.Helper()
	gitPath := func(name string) string {
		value := strings.TrimSpace(pushPublicGitOutput(t, path, "rev-parse", "--path-format=absolute", "--git-path", name))
		return pushPublicPathSnapshot(t, value)
	}
	return strings.Join([]string{
		"worktree=" + pushPublicPathSnapshot(t, path),
		"head=" + pushPublicGitOutput(t, path, "rev-parse", "HEAD"),
		"refs=" + pushPublicGitOutput(t, path, "for-each-ref", "--format=%(refname):%(objectname)"),
		"index=" + gitPath("index"), "write-tree=" + pushPublicGitOutput(t, path, "write-tree"),
		"config=" + gitPath("config"), "fetch-head=" + gitPath("FETCH_HEAD"),
	}, "\n\x00\n")
}

func pushPublicPathSnapshot(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "ABSENT"
	}
	if err != nil {
		t.Fatal(err)
	}
	value := fmt.Sprintf("type=%s:mode=%#o", pushPublicFileType(info.Mode()), info.Mode())
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
		return value + ":target=" + target
	}
	if info.Mode().IsRegular() {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return value + ":bytes=" + string(contents)
	}
	if !info.IsDir() {
		return value
	}
	entries := []string{value}
	if err := filepath.WalkDir(path, func(current string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		entry, err := os.Lstat(current)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		line := filepath.ToSlash(relative) + ":" + fmt.Sprintf("type=%s:mode=%#o", pushPublicFileType(entry.Mode()), entry.Mode())
		if entry.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			line += ":target=" + target
		} else if entry.Mode().IsRegular() {
			contents, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			line += ":bytes=" + string(contents)
		}
		entries = append(entries, line)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

func pushPublicFileType(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "regular"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	default:
		return mode.Type().String()
	}
}

func pushPublicGitOutput(t *testing.T, path string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return string(output)
}

func pushPublicGit(t *testing.T, path string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func pushPublicAssertNoPrivateOutput(t *testing.T, fixture publicPushFixture, output string) {
	t.Helper()
	for _, forbidden := range []string{fixture.root.Path, fixture.data, "https://", "super-secret", "remote.origin.url", ".wtree.yml"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("push public output leaked %q: %q", forbidden, output)
		}
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type publicPushFailingWriter struct {
	cause  error
	writes int
	output bytes.Buffer
}

func (writer *publicPushFailingWriter) Write(value []byte) (int, error) {
	writer.writes++
	accepted := len(value) / 2
	if accepted == 0 && len(value) > 0 {
		accepted = 1
	}
	_, _ = writer.output.Write(value[:accepted])
	return accepted, writer.cause
}
