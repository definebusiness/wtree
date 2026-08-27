package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteExecRequiresSeparatorAndHasNoForeachAlias(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := cli.Execute([]string{"exec", "echo"}, &stdout, &stderr); err == nil || cli.ExitCode(err) != 2 {
		t.Fatalf("exec without separator error = %v", err)
	}
	stdout.Reset()
	if err := cli.Execute([]string{"exec", "--help"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "exec -- <executable> [argument...]") || strings.Contains(stdout.String(), "foreach") {
		t.Fatalf("exec help = %q, %v", stdout.String(), err)
	}
}

func TestExecuteExecJSONUsesDirectArgvAndDryRun(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if initialized := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); initialized.Err != nil {
		t.Fatalf("init = %#v", initialized)
	}
	arguments := []string{"exec", "--project", project.Path, "--data-dir", data, "--json", "--", os.Args[0], "-test.run=^TestExecCLIHelper$", "literal;touch"}
	result := testutil.RunCommand(t, cli.Execute, arguments...)
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("exec = %#v", result)
	}
	var value struct {
		DryRun         bool     `json:"dryRun"`
		ExecutionOrder []string `json:"executionOrder"`
		Command        struct {
			Program string `json:"program"`
		} `json:"command"`
		Repositories []struct {
			Status      string            `json:"status"`
			Stdout      string            `json:"stdout"`
			ExitCode    *int              `json:"exitCode"`
			Environment map[string]string `json:"environment"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil || value.DryRun || strings.Join(value.ExecutionOrder, ",") != "root" || value.Command.Program != os.Args[0] || len(value.Repositories) != 1 || value.Repositories[0].Status != "completed" || value.Repositories[0].ExitCode == nil || *value.Repositories[0].ExitCode != 0 || value.Repositories[0].Environment != nil || !strings.Contains(value.Repositories[0].Stdout, "literal;touch") {
		t.Fatalf("exec JSON = %s, decoded=%#v, err=%v", result.Stdout, value, err)
	}
	dryRun := testutil.RunCommand(t, cli.Execute, "exec", "--project", project.Path, "--data-dir", data, "--json", "--dry-run", "--", "does-not-exist")
	var planned struct {
		DryRun         bool     `json:"dryRun"`
		ExecutionOrder []string `json:"executionOrder"`
		Repositories   []struct {
			Environment map[string]string `json:"environment"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(dryRun.Stdout), &planned); dryRun.Err != nil || dryRun.Stderr != "" || err != nil || !planned.DryRun || strings.Join(planned.ExecutionOrder, ",") != "root" || len(planned.Repositories) != 1 || len(planned.Repositories[0].Environment) != 7 || planned.Repositories[0].Environment["WTREE_REPOSITORY_ID"] != "root" {
		t.Fatalf("exec dry-run = %#v", dryRun)
	}
	failed := testutil.RunCommand(t, cli.Execute, "exec", "--project", project.Path, "--data-dir", data, "--json", "--", os.Args[0], "-test.run=^TestExecCLIExitFailureHelper$")
	if failed.Err == nil || cli.ExitCode(failed.Err) != 8 || failed.Stderr != "" || strings.Count(failed.Stdout, "\n") != 1 || !strings.Contains(failed.Stdout, `"version":1`) || !strings.Contains(failed.Stdout, `"status":"failed"`) || !strings.Contains(failed.Stdout, `"code":"conflict"`) {
		t.Fatalf("exec child failure = %#v", failed)
	}
}

func TestExecuteExecJSONExactSchemasAndApplicationCategories(t *testing.T) {
	fixture := newExecCLIFixture(t)
	trueProgram, err := exec.LookPath("true")
	if err != nil {
		t.Skip("a direct zero-output executable is unavailable")
	}

	normal := testutil.RunCommand(t, cli.Execute, "exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json", "--", trueProgram)
	if normal.Err != nil || normal.Stderr != "" {
		t.Fatalf("normal exec = %#v", normal)
	}
	normalWire := decodeExecCLIDocument(t, normal.Stdout)
	assertExecCLIKeys(t, normalWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "command", "executionOrder", "repositories"})
	command := normalWire["command"].(map[string]any)
	assertExecCLIKeys(t, command, []string{"program", "args"})
	if command["args"] == nil || len(command["args"].([]any)) != 0 {
		t.Fatalf("zero-argument command encoded args = %#v", command["args"])
	}
	for _, raw := range normalWire["repositories"].([]any) {
		entry := raw.(map[string]any)
		want := []string{"id", "mount", "path", "branch", "head", "status", "stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode"}
		if entry["id"] != fixture.project.BaseRepository {
			want = append(want, "parentId")
		}
		assertExecCLIKeys(t, entry, want)
		if entry["stdout"] != "" || entry["stderr"] != "" || entry["stdoutTruncated"] != false || entry["stderrTruncated"] != false || entry["exitCode"] != float64(0) {
			t.Fatalf("normal started fields = %#v", entry)
		}
		if _, exists := entry["environment"]; exists {
			t.Fatalf("normal result exposed environment: %#v", entry)
		}
	}

	dry := testutil.RunCommand(t, cli.Execute, "exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json", "--dry-run", "--", trueProgram)
	if dry.Err != nil || dry.Stderr != "" {
		t.Fatalf("dry-run exec = %#v", dry)
	}
	dryWire := decodeExecCLIDocument(t, dry.Stdout)
	assertExecCLIKeys(t, dryWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "command", "executionOrder", "repositories"})
	for _, raw := range dryWire["repositories"].([]any) {
		entry := raw.(map[string]any)
		assertExecCLIKeys(t, entry, execCLIRepositoryKeys(entry, false, false, true))
		for _, key := range []string{"stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode", "failure"} {
			if _, exists := entry[key]; exists {
				t.Fatalf("dry-run exposed inapplicable %q: %#v", key, entry)
			}
		}
		environment := entry["environment"].(map[string]any)
		assertExecCLIKeys(t, environment, execCLIEnvironmentKeys())
	}

	start := testutil.RunCommand(t, cli.Execute, "exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json", "--", filepath.Join(t.TempDir(), "missing"))
	if start.Err == nil || cli.ExitCode(start.Err) != 1 || start.Stderr != "" {
		t.Fatalf("process-start failure = %#v", start)
	}
	startWire := decodeExecCLIDocument(t, start.Stdout)
	assertExecCLIKeys(t, startWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "command", "executionOrder", "repositories", "failure"})
	startEntry := startWire["repositories"].([]any)[0].(map[string]any)
	assertExecCLIKeys(t, startEntry, execCLIRepositoryKeys(startEntry, false, true, false))
	assertNoStartedExecCLIFields(t, startEntry)
	assertExecCLIKeys(t, startEntry["failure"].(map[string]any), []string{"code", "message"})

	marker := t.TempDir()
	child := testutil.RunCommand(t, cli.Execute, "exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json", "--", os.Args[0], "-test.run=^TestExecCLIEarlyFailureHelper$", fixture.project.BaseRepository, marker)
	if child.Err == nil || cli.ExitCode(child.Err) != 8 || child.Stderr != "" {
		t.Fatalf("child failure = %#v", child)
	}
	childWire := decodeExecCLIDocument(t, child.Stdout)
	assertExecCLIKeys(t, childWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "command", "executionOrder", "repositories", "failure"})
	childEntries := childWire["repositories"].([]any)
	first := childEntries[0].(map[string]any)
	assertExecCLIKeys(t, first, execCLIRepositoryKeys(first, true, true, false))
	assertExecCLIKeys(t, childEntries[1].(map[string]any), execCLIRepositoryKeys(childEntries[1].(map[string]any), true, false, false))
	assertExecCLIKeys(t, first["failure"].(map[string]any), []string{"code", "message"})
	if first["status"] != "failed" || first["exitCode"] != float64(7) || childWire["failure"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("early child failure schema = %#v", childWire)
	}
	if _, statErr := os.Stat(filepath.Join(marker, fixture.childID)); statErr != nil {
		t.Fatalf("later child did not run after first failure: %v", statErr)
	}

	cancelMarker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan testutil.Result, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		err := cli.ExecuteContext(ctx, []string{"exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json", "--", os.Args[0], "-test.run=^TestExecCLIBlockingHelper$", cancelMarker}, &stdout, &stderr)
		finished <- testutil.Result{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
	}()
	waitExecCLIPath(t, cancelMarker)
	cancel()
	canceled := <-finished
	if canceled.Err == nil || cli.ExitCode(canceled.Err) != 1 || canceled.Stderr != "" {
		t.Fatalf("canceled exec = %#v", canceled)
	}
	canceledWire := decodeExecCLIDocument(t, canceled.Stdout)
	assertExecCLIKeys(t, canceledWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "command", "executionOrder", "repositories", "failure"})
	canceledEntries := canceledWire["repositories"].([]any)
	assertExecCLIKeys(t, canceledEntries[0].(map[string]any), execCLIRepositoryKeys(canceledEntries[0].(map[string]any), true, true, false))
	if canceledEntries[0].(map[string]any)["status"] != "canceled" {
		t.Fatalf("started cancellation = %#v", canceledEntries[0])
	}
	for _, key := range []string{"stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode"} {
		if _, exists := canceledEntries[0].(map[string]any)[key]; !exists {
			t.Fatalf("started cancellation omitted %q: %#v", key, canceledEntries[0])
		}
	}
	if len(canceledEntries) > 1 {
		assertExecCLIKeys(t, canceledEntries[1].(map[string]any), execCLIRepositoryKeys(canceledEntries[1].(map[string]any), false, true, false))
		assertNoStartedExecCLIFields(t, canceledEntries[1].(map[string]any))
	}
}

func TestExecuteExecWorkspacePartialZeroPresentAndExactEnvironment(t *testing.T) {
	fixture := newExecCLIFixture(t)
	defaultWorkspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
	if err != nil {
		t.Fatal(err)
	}
	baseID := fixture.project.BaseRepository
	baseCheckout, found := execCLICheckout(defaultWorkspace, baseID)
	if !found {
		t.Fatalf("default workspace has no base checkout: %#v", defaultWorkspace)
	}
	if err := store.WriteWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, "imported-partial"), store.WorkspaceState{
		ID: "imported-partial", Name: "imported-partial", Path: defaultWorkspace.RootPath, Partial: true, MissingRepositoryIDs: []string{fixture.childID},
		Repositories: map[string]store.CheckoutState{baseID: {Branch: baseCheckout.Branch, Head: baseCheckout.Head, Mount: baseCheckout.Mount, ResolvedPath: baseCheckout.ResolvedPath}},
	}); err != nil {
		t.Fatal(err)
	}
	zeroRoot := t.TempDir()
	if err := store.WriteWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, "zero-present"), store.WorkspaceState{
		ID: "zero-present", Name: "zero-present", Path: zeroRoot, Partial: true, MissingRepositoryIDs: []string{baseID, fixture.childID}, Repositories: map[string]store.CheckoutState{},
	}); err != nil {
		t.Fatal(err)
	}

	partial := testutil.RunCommand(t, cli.Execute, "exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--workspace", "imported-partial", "--dry-run", "--json", "--", "not-started")
	partialWire := decodeExecCLIDocument(t, partial.Stdout)
	assertExecCLIKeys(t, partialWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "partial", "missingRepositoryIds", "command", "executionOrder", "repositories"})
	if partial.Err != nil || partialWire["partial"] != true || partialWire["workspace"] != "imported-partial" || len(partialWire["repositories"].([]any)) != 1 || partialWire["missingRepositoryIds"].([]any)[0] != fixture.childID {
		t.Fatalf("selected imported partial = %#v, wire=%#v", partial, partialWire)
	}
	zero := testutil.RunCommand(t, cli.Execute, "exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--workspace", "zero-present", "--dry-run", "--json", "--", "not-started")
	zeroWire := decodeExecCLIDocument(t, zero.Stdout)
	assertExecCLIKeys(t, zeroWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "partial", "missingRepositoryIds", "command", "executionOrder", "repositories"})
	if zero.Err != nil || zero.Stderr != "" || zeroWire["partial"] != true || len(zeroWire["repositories"].([]any)) != 0 || len(zeroWire["executionOrder"].([]any)) != 0 || len(zeroWire["missingRepositoryIds"].([]any)) != 2 {
		t.Fatalf("zero-present envelope = %#v, wire=%#v", zero, zeroWire)
	}

	for _, key := range append(execCLIEnvironmentKeys(), "WTREE_UNKNOWN_HOSTILE") {
		t.Setenv(key, "hostile-"+key)
	}
	arguments := []string{"exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json", "--", os.Args[0], "-test.run=^TestExecCLIEnvironmentHelper$", "literal;touch", "$(literal)", "*"}
	actual := testutil.RunCommand(t, cli.Execute, arguments...)
	if actual.Err != nil || actual.Stderr != "" {
		t.Fatalf("environment exec = %#v", actual)
	}
	actualWire := decodeExecCLIDocument(t, actual.Stdout)
	for _, raw := range actualWire["repositories"].([]any) {
		entry := raw.(map[string]any)
		if _, exists := entry["environment"]; exists {
			t.Fatalf("non-dry JSON exposed environment: %#v", entry)
		}
		var observation struct {
			Args []string          `json:"args"`
			Cwd  string            `json:"cwd"`
			Env  map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(entry["stdout"].(string)), &observation); err != nil {
			t.Fatalf("decode helper observation %q: %v", entry["stdout"], err)
		}
		wantArgs := []string{"-test.run=^TestExecCLIEnvironmentHelper$", "literal;touch", "$(literal)", "*"}
		if !reflect.DeepEqual(observation.Args, wantArgs) || observation.Cwd != entry["path"] {
			t.Fatalf("argv/cwd observation = %#v, want args=%#v path=%#v", observation, wantArgs, entry["path"])
		}
		if got := sortedExecCLIMapKeys(observation.Env); !reflect.DeepEqual(got, execCLIEnvironmentKeys()) {
			t.Fatalf("WTREE environment keys = %v, want %v", got, execCLIEnvironmentKeys())
		}
		if observation.Env["WTREE_PROJECT_ID"] != fixture.project.ID || observation.Env["WTREE_WORKSPACE"] != "default" || observation.Env["WTREE_REPOSITORY_ID"] != entry["id"] || observation.Env["WTREE_MOUNT"] != entry["mount"] || observation.Env["WTREE_PATH"] != entry["path"] || observation.Env["WTREE_BRANCH"] != entry["branch"] || observation.Env["WTREE_COMMIT"] != entry["head"] {
			t.Fatalf("WTREE environment values = %#v for %#v", observation.Env, entry)
		}
	}
}

func TestExecuteExecHumanWriterStopsAndStreamsBeforeLaterProcess(t *testing.T) {
	fixture := newExecCLIFixture(t)
	marker := t.TempDir()
	want := errors.New("exec human writer failed")
	writer := &execCLIFailingWriter{err: want}
	var stderr bytes.Buffer
	err := cli.Execute([]string{"exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--", os.Args[0], "-test.run=^TestExecCLIMarkerHelper$", marker}, writer, &stderr)
	if !errors.Is(err, want) || cli.ExitCode(err) != 1 || stderr.String() != "" || strings.Count(writer.String(), "Repository: "+fixture.project.BaseRepository) != 1 {
		t.Fatalf("human writer failure = %v exit=%d stdout=%q stderr=%q", err, cli.ExitCode(err), writer.String(), stderr.String())
	}
	if _, statErr := os.Stat(filepath.Join(marker, fixture.project.BaseRepository)); statErr != nil {
		t.Fatalf("first repository marker = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(marker, fixture.childID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("later repository started after writer failure: %v", statErr)
	}

	marker = t.TempDir()
	betaRelease := filepath.Join(t.TempDir(), "release-beta")
	stream := newExecCLIBlockingWriter("Repository: " + fixture.project.BaseRepository)
	finished := make(chan error, 1)
	go func() {
		finished <- cli.Execute([]string{"exec", "--project", fixture.root.Path, "--data-dir", fixture.data, "--", os.Args[0], "-test.run=^TestExecCLIStreamingHelper$", marker, betaRelease}, stream, io.Discard)
	}()
	select {
	case <-stream.observed:
	case <-time.After(10 * time.Second):
		t.Fatal("first human repository row was not streamed")
	}
	if !strings.Contains(stream.String(), "Repository: "+fixture.project.BaseRepository) {
		t.Fatalf("first repository row was not visible: %q", stream.String())
	}
	if _, statErr := os.Stat(filepath.Join(marker, fixture.childID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("second process started before first row was released: %v", statErr)
	}
	close(stream.release)
	waitExecCLIPath(t, filepath.Join(marker, fixture.childID))
	if err := os.WriteFile(betaRelease, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("streaming exec = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("streaming exec did not complete")
	}
	if strings.Count(stream.String(), "Repository: "+fixture.project.BaseRepository) != 1 || strings.Count(stream.String(), "Repository: "+fixture.childID) != 1 {
		t.Fatalf("human output rerendered a repository: %q", stream.String())
	}
}

func TestExecCLIHelper(t *testing.T) {
	if os.Getenv("WTREE_REPOSITORY_ID") == "" || !strings.Contains(strings.Join(os.Args, " "), "TestExecCLIHelper") {
		return
	}
	fmt.Printf("repository=%s args=%s", os.Getenv("WTREE_REPOSITORY_ID"), strings.Join(os.Args, "|"))
	os.Exit(0)
}

func TestExecCLIExitFailureHelper(t *testing.T) {
	if os.Getenv("WTREE_REPOSITORY_ID") == "" || !strings.Contains(strings.Join(os.Args, " "), "TestExecCLIExitFailureHelper") {
		return
	}
	os.Exit(7)
}

func TestExecCLIEarlyFailureHelper(t *testing.T) {
	if os.Getenv("WTREE_REPOSITORY_ID") == "" || !strings.Contains(strings.Join(os.Args, " "), "TestExecCLIEarlyFailureHelper") {
		return
	}
	failureID, marker := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if os.Getenv("WTREE_REPOSITORY_ID") == failureID {
		fmt.Fprintln(os.Stderr, "early child failure")
		os.Exit(7)
	}
	if err := os.WriteFile(filepath.Join(marker, os.Getenv("WTREE_REPOSITORY_ID")), []byte("started"), 0o600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestExecCLIBlockingHelper(t *testing.T) {
	if os.Getenv("WTREE_REPOSITORY_ID") == "" || !strings.Contains(strings.Join(os.Args, " "), "TestExecCLIBlockingHelper") {
		return
	}
	if err := os.WriteFile(os.Args[len(os.Args)-1], []byte("started"), 0o600); err != nil {
		os.Exit(1)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestExecCLIEnvironmentHelper(t *testing.T) {
	if os.Getenv("WTREE_REPOSITORY_ID") == "" || !strings.Contains(strings.Join(os.Args, " "), "TestExecCLIEnvironmentHelper") {
		return
	}
	environment := map[string]string{}
	for _, value := range os.Environ() {
		key, item, found := strings.Cut(value, "=")
		if found && strings.HasPrefix(key, "WTREE_") {
			environment[key] = item
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Args []string          `json:"args"`
		Cwd  string            `json:"cwd"`
		Env  map[string]string `json:"env"`
	}{Args: os.Args[1:], Cwd: cwd, Env: environment}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestExecCLIMarkerHelper(t *testing.T) {
	if os.Getenv("WTREE_REPOSITORY_ID") == "" || !strings.Contains(strings.Join(os.Args, " "), "TestExecCLIMarkerHelper") {
		return
	}
	if err := os.WriteFile(filepath.Join(os.Args[len(os.Args)-1], os.Getenv("WTREE_REPOSITORY_ID")), []byte("started"), 0o600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestExecCLIStreamingHelper(t *testing.T) {
	if os.Getenv("WTREE_REPOSITORY_ID") == "" || !strings.Contains(strings.Join(os.Args, " "), "TestExecCLIStreamingHelper") {
		return
	}
	marker, release := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	id := os.Getenv("WTREE_REPOSITORY_ID")
	if err := os.WriteFile(filepath.Join(marker, id), []byte("started"), 0o600); err != nil {
		os.Exit(1)
	}
	if os.Getenv("WTREE_MOUNT") != "." {
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(2)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	os.Exit(0)
}

type execCLIFixture struct {
	root    testutil.PushedGitRepository
	project domain.Project
	data    string
	childID string
}

func newExecCLIFixture(t *testing.T) execCLIFixture {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	child := testutil.NewPushedGitRepository(t)
	child.CommitFile("child.txt", "child\n", "child")
	if err := os.Rename(child.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if initialized := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); initialized.Err != nil {
		t.Fatalf("init exec fixture = %#v", initialized)
	}
	project, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	childID := ""
	for _, repository := range project.Repositories {
		if repository.ID != project.BaseRepository {
			childID = repository.ID
		}
	}
	if childID == "" {
		t.Fatalf("exec fixture has no child repository: %#v", project)
	}
	return execCLIFixture{root: root, project: project, data: data, childID: childID}
}

func execCLICheckout(workspace domain.Workspace, id string) (domain.Checkout, bool) {
	for _, checkout := range workspace.Checkouts {
		if checkout.RepositoryID == id {
			return checkout, true
		}
	}
	return domain.Checkout{}, false
}

func decodeExecCLIDocument(t *testing.T, output string) map[string]any {
	t.Helper()
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("exec JSON emitted more than one stdout document: %q", output)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode exec JSON %q: %v", output, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("exec JSON has trailing document/content: %q (%v)", output, err)
	}
	rejectExecCLINulls(t, value, "result")
	return value
}

func rejectExecCLINulls(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case nil:
		t.Fatalf("exec JSON contains null at %s", path)
	case map[string]any:
		for key, item := range typed {
			rejectExecCLINulls(t, item, path+"."+key)
		}
	case []any:
		for index, item := range typed {
			rejectExecCLINulls(t, item, fmt.Sprintf("%s[%d]", path, index))
		}
	}
}

func assertExecCLIKeys(t *testing.T, value map[string]any, want []string) {
	t.Helper()
	got := sortedExecCLIMapKeys(value)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exec JSON keys = %v, want %v in %#v", got, want, value)
	}
}

func sortedExecCLIMapKeys[T any](value map[string]T) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func assertNoStartedExecCLIFields(t *testing.T, entry map[string]any) {
	t.Helper()
	for _, key := range []string{"stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode"} {
		if _, exists := entry[key]; exists {
			t.Fatalf("unstarted repository exposed %q: %#v", key, entry)
		}
	}
}

func execCLIRepositoryKeys(entry map[string]any, started, failed, environment bool) []string {
	keys := []string{"id", "mount", "path", "branch", "head", "status"}
	if _, exists := entry["parentId"]; exists {
		keys = append(keys, "parentId")
	}
	if started {
		keys = append(keys, "stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode")
	}
	if failed {
		keys = append(keys, "failure")
	}
	if environment {
		keys = append(keys, "environment")
	}
	return keys
}

func execCLIEnvironmentKeys() []string {
	return []string{"WTREE_BRANCH", "WTREE_COMMIT", "WTREE_MOUNT", "WTREE_PATH", "WTREE_PROJECT_ID", "WTREE_REPOSITORY_ID", "WTREE_WORKSPACE"}
}

func waitExecCLIPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type execCLIFailingWriter struct {
	mu  sync.Mutex
	err error
	buf bytes.Buffer
}

func (writer *execCLIFailingWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	_, _ = writer.buf.Write(value)
	return 0, writer.err
}
func (writer *execCLIFailingWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buf.String()
}

type execCLIBlockingWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	needle   string
	once     sync.Once
	observed chan struct{}
	release  chan struct{}
}

func newExecCLIBlockingWriter(needle string) *execCLIBlockingWriter {
	return &execCLIBlockingWriter{needle: needle, observed: make(chan struct{}), release: make(chan struct{})}
}
func (writer *execCLIBlockingWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	_, _ = writer.buf.Write(value)
	matched := strings.Contains(string(value), writer.needle)
	writer.mu.Unlock()
	if matched {
		writer.once.Do(func() {
			close(writer.observed)
			<-writer.release
		})
	}
	return len(value), nil
}
func (writer *execCLIBlockingWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buf.String()
}
