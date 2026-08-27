package cli_test

import (
	"bytes"
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
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteFetchHasExactSurfaceAndNoSyncAlias(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := cli.Execute([]string{"fetch", "--help"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "fetch") {
		t.Fatalf("fetch help = %q, %v", stdout.String(), err)
	}
	stdout.Reset()
	if err := cli.Execute([]string{"sync"}, &stdout, &stderr); err == nil || cli.ExitCode(err) != 2 {
		t.Fatalf("sync alias error = %v", err)
	}
}

func TestExecuteFetchJSONIsSingleSilentDocument(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if initialized := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); initialized.Err != nil {
		t.Fatalf("init = %#v", initialized)
	}
	result := testutil.RunCommand(t, cli.Execute, "fetch", "--project", project.Path, "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" || strings.Count(result.Stdout, "\n") != 1 {
		t.Fatalf("fetch = %#v", result)
	}
	var value struct {
		Version      int    `json:"version"`
		Operation    string `json:"operation"`
		Status       string `json:"status"`
		DryRun       bool   `json:"dryRun"`
		Repositories []struct {
			Status    string `json:"status"`
			Remote    string `json:"remote"`
			RemoteRef string `json:"remoteRef"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil || value.Version != 1 || value.Operation != "fetch" || value.Status != "completed" || value.DryRun || len(value.Repositories) != 1 || value.Repositories[0].Status != "completed" || value.Repositories[0].Remote != "origin" || value.Repositories[0].RemoteRef != "refs/heads/main" {
		t.Fatalf("fetch JSON = %s, %#v, %v", result.Stdout, value, err)
	}
}

func TestExecuteFetchJSONSchemaFailuresAndWorkspaceSelection(t *testing.T) {
	fixture := newExecCLIFixture(t)
	dry := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--dry-run", "--json")
	if dry.Err != nil || dry.Stderr != "" {
		t.Fatalf("dry fetch = %#v", dry)
	}
	dryWire := decodeFetchCLIDocument(t, dry.Stdout)
	assertFetchCLIKeys(t, dryWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "repositories"})
	if dryWire["version"] != float64(1) || dryWire["operation"] != "fetch" || dryWire["status"] != "completed" || dryWire["dryRun"] != true {
		t.Fatalf("dry envelope = %#v", dryWire)
	}
	for _, raw := range dryWire["repositories"].([]any) {
		entry := raw.(map[string]any)
		assertFetchCLIKeys(t, entry, fetchCLIRepositoryKeys(entry, false, false))
		if entry["status"] != "completed" || entry["remote"] != "origin" || entry["remoteRef"] != "refs/heads/main" || entry["actualRemoteCommit"] == "" {
			t.Fatalf("dry repository = %#v", entry)
		}
		if _, exists := entry["previousRemoteCommit"]; exists {
			t.Fatalf("dry-run exposed prior tracking generation: %#v", entry)
		}
	}

	execution := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json")
	if execution.Err != nil || execution.Stderr != "" {
		t.Fatalf("execution fetch = %#v", execution)
	}
	executionWire := decodeFetchCLIDocument(t, execution.Stdout)
	assertFetchCLIKeys(t, executionWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "repositories"})
	for _, raw := range executionWire["repositories"].([]any) {
		entry := raw.(map[string]any)
		assertFetchCLIKeys(t, entry, fetchCLIRepositoryKeys(entry, true, false))
		if entry["actualRemoteCommit"] == "" || entry["previousRemoteCommit"] == "" {
			t.Fatalf("execution receipt omits either generation: %#v", entry)
		}
	}

	workspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
	if err != nil {
		t.Fatal(err)
	}
	base, found := execCLICheckout(workspace, fixture.project.BaseRepository)
	if !found {
		t.Fatalf("base checkout missing: %#v", workspace)
	}
	if err := store.WriteWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, "imported-partial"), store.WorkspaceState{
		ID: "imported-partial", Name: "imported-partial", Path: workspace.RootPath, Partial: true, MissingRepositoryIDs: []string{fixture.childID},
		Repositories: map[string]store.CheckoutState{fixture.project.BaseRepository: {Branch: base.Branch, Head: base.Head, Mount: base.Mount, ResolvedPath: base.ResolvedPath}},
	}); err != nil {
		t.Fatal(err)
	}
	partial := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--workspace", "imported-partial", "--dry-run", "--json")
	if partial.Err != nil || partial.Stderr != "" {
		t.Fatalf("partial fetch = %#v", partial)
	}
	partialWire := decodeFetchCLIDocument(t, partial.Stdout)
	assertFetchCLIKeys(t, partialWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "partial", "missingRepositoryIds", "repositories"})
	if partialWire["workspace"] != "imported-partial" || partialWire["partial"] != true || !reflect.DeepEqual(partialWire["missingRepositoryIds"], []any{fixture.childID}) || len(partialWire["repositories"].([]any)) != 1 {
		t.Fatalf("partial fetch selection = %#v", partialWire)
	}

	// A malformed configured upstream is a command-owned, already-formed
	// result. JSON remains one silent v1 document rather than adding a generic
	// root-level error document.
	if output, runErr := exec.Command("git", "-C", base.ResolvedPath, "config", "--unset", "branch."+base.Branch+".merge").CombinedOutput(); runErr != nil {
		t.Fatalf("malform upstream: %v\n%s", runErr, output)
	}
	failed := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json")
	if failed.Err == nil || cli.ExitCode(failed.Err) != 6 || failed.Stderr != "" {
		t.Fatalf("failed fetch = %#v", failed)
	}
	failedWire := decodeFetchCLIDocument(t, failed.Stdout)
	assertFetchCLIKeys(t, failedWire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "repositories", "failure"})
	assertFetchCLIKeys(t, failedWire["failure"].(map[string]any), []string{"code", "message"})
	for _, raw := range failedWire["repositories"].([]any) {
		entry := raw.(map[string]any)
		if entry["status"] == "failed" {
			assertFetchCLIKeys(t, entry, []string{"id", "mount", "path", "branch", "head", "status", "failure"})
			assertFetchCLIKeys(t, entry["failure"].(map[string]any), []string{"code", "message"})
		} else {
			assertFetchCLIKeys(t, entry, fetchCLIRepositoryKeys(entry, true, false))
		}
	}
	if output, runErr := exec.Command("git", "-C", base.ResolvedPath, "config", "branch."+base.Branch+".merge", "refs/tags/not-a-branch").CombinedOutput(); runErr != nil {
		t.Fatalf("set malformed upstream value: %v\n%s", runErr, output)
	}
	invalid := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json")
	if invalid.Err == nil || cli.ExitCode(invalid.Err) != 6 || invalid.Stderr != "" {
		t.Fatalf("invalid upstream fetch = %#v", invalid)
	}
	invalidWire := decodeFetchCLIDocument(t, invalid.Stdout)
	if invalidWire["failure"].(map[string]any)["code"] != "git" {
		t.Fatalf("invalid upstream category = %#v", invalidWire)
	}
}

func TestExecuteFetchHumanWritersStopWithoutFalseCompletion(t *testing.T) {
	fixture := newExecCLIFixture(t)
	want := errors.New("fetch writer failed")
	writer := &fetchCLIPartialWriter{err: want}
	var stderr bytes.Buffer
	err := cli.Execute([]string{"fetch", "--project", fixture.root.Path, "--data-dir", fixture.data}, writer, &stderr)
	if !errors.Is(err, want) || cli.ExitCode(err) != 1 || stderr.String() != "" || strings.Contains(writer.String(), "Workspace:") || strings.Count(writer.String(), "Repository:") > 1 {
		t.Fatalf("human writer failure = %v exit=%d stdout=%q stderr=%q", err, cli.ExitCode(err), writer.String(), stderr.String())
	}

	// A JSON writer failure must return its exact error without letting the root
	// append a second generic JSON document. The partial bytes are intentionally
	// not asserted as valid JSON because the writer interrupted that document.
	writer = &fetchCLIPartialWriter{err: want}
	err = cli.Execute([]string{"fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json"}, writer, io.Discard)
	if !errors.Is(err, want) || cli.ExitCode(err) != 1 || strings.Count(writer.String(), "\n") > 1 {
		t.Fatalf("JSON writer failure = %v exit=%d stdout=%q", err, cli.ExitCode(err), writer.String())
	}
}

func TestExecuteFetchHumanAndVerboseRowsAreDeterministic(t *testing.T) {
	fixture := newExecCLIFixture(t)
	workspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
	if err != nil {
		t.Fatal(err)
	}
	base, found := execCLICheckout(workspace, fixture.project.BaseRepository)
	if !found {
		t.Fatalf("base checkout missing: %#v", workspace)
	}
	child, found := execCLICheckout(workspace, fixture.childID)
	if !found {
		t.Fatalf("child checkout missing: %#v", workspace)
	}
	for _, test := range []struct {
		name, suffix string
		arguments    []string
	}{
		{name: "human", arguments: []string{"fetch", "--project", fixture.root.Path, "--data-dir", fixture.data}, suffix: ""},
		{name: "verbose", arguments: []string{"fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--verbose"}, suffix: " remote=origin ref=refs/heads/main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, test.arguments...)
			want := fmt.Sprintf("Repository: %s path=%s status=completed%s\nRepository: %s path=%s status=completed%s\nWorkspace: default status=completed\n", fixture.project.BaseRepository, base.ResolvedPath, test.suffix, fixture.childID, child.ResolvedPath, test.suffix)
			if result.Err != nil || result.Stderr != "" || result.Stdout != want {
				t.Fatalf("%s fetch = %#v, want %q", test.name, result, want)
			}
		})
	}

	if output, runErr := exec.Command("git", "-C", base.ResolvedPath, "config", "--unset", "branch."+base.Branch+".merge").CombinedOutput(); runErr != nil {
		t.Fatalf("malform upstream: %v\n%s", runErr, output)
	}
	failed := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--verbose")
	if failed.Err == nil || cli.ExitCode(failed.Err) != 6 || failed.Stderr != "" {
		t.Fatalf("failed human fetch = %#v", failed)
	}
	failedRow := "Repository: " + fixture.project.BaseRepository + " path=" + base.ResolvedPath + " status=failed remote= ref=\n"
	childRow := "Repository: " + fixture.childID + " path=" + child.ResolvedPath + " status=completed remote=origin ref=refs/heads/main\n"
	if !strings.HasPrefix(failed.Stdout, failedRow+"failure: git: ") || !strings.Contains(failed.Stdout, childRow) || !strings.HasSuffix(failed.Stdout, "Workspace: default status=failed\n") || strings.Count(failed.Stdout, "Repository: "+fixture.project.BaseRepository) != 1 || strings.Count(failed.Stdout, "Repository: "+fixture.childID) != 1 {
		t.Fatalf("failed verbose fetch = %q", failed.Stdout)
	}
}

func TestExecuteFetchMalformedChildRowsStayParentFirst(t *testing.T) {
	for _, test := range []struct {
		name, suffix string
		arguments    []string
	}{
		{name: "human", suffix: ""},
		{name: "verbose", suffix: " remote=origin ref=refs/heads/main", arguments: []string{"--verbose"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecCLIFixture(t)
			workspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
			if err != nil {
				t.Fatal(err)
			}
			base, found := execCLICheckout(workspace, fixture.project.BaseRepository)
			if !found {
				t.Fatalf("base checkout missing: %#v", workspace)
			}
			child, found := execCLICheckout(workspace, fixture.childID)
			if !found {
				t.Fatalf("child checkout missing: %#v", workspace)
			}
			if output, runErr := exec.Command("git", "-C", child.ResolvedPath, "config", "--unset", "branch."+child.Branch+".merge").CombinedOutput(); runErr != nil {
				t.Fatalf("malform child upstream: %v\n%s", runErr, output)
			}
			arguments := []string{"fetch", "--project", fixture.root.Path, "--data-dir", fixture.data}
			arguments = append(arguments, test.arguments...)
			result := testutil.RunCommand(t, cli.Execute, arguments...)
			parentRow := fmt.Sprintf("Repository: %s path=%s status=completed%s\n", fixture.project.BaseRepository, base.ResolvedPath, test.suffix)
			childRow := fmt.Sprintf("Repository: %s path=%s status=failed", fixture.childID, child.ResolvedPath)
			if test.name == "verbose" {
				childRow += " remote= ref="
			}
			childRow += "\n"
			failurePrefix := fmt.Sprintf("failure: git: git: fetch preflight %q: read configured upstream:", fixture.childID)
			if result.Err == nil || cli.ExitCode(result.Err) != 6 || result.Stderr != "" || !strings.HasPrefix(result.Stdout, parentRow+childRow+failurePrefix) || !strings.HasSuffix(result.Stdout, "Workspace: default status=failed\n") {
				t.Fatalf("malformed child %s fetch = %#v", test.name, result)
			}
			if strings.Count(result.Stdout, "Repository: "+fixture.project.BaseRepository) != 1 || strings.Count(result.Stdout, "Repository: "+fixture.childID) != 1 {
				t.Fatalf("malformed child duplicated row = %q", result.Stdout)
			}
		})
	}

	t.Run("json", func(t *testing.T) {
		fixture := newExecCLIFixture(t)
		workspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
		if err != nil {
			t.Fatal(err)
		}
		child, found := execCLICheckout(workspace, fixture.childID)
		if !found {
			t.Fatalf("child checkout missing: %#v", workspace)
		}
		if output, runErr := exec.Command("git", "-C", child.ResolvedPath, "config", "--unset", "branch."+child.Branch+".merge").CombinedOutput(); runErr != nil {
			t.Fatalf("malform child upstream: %v\n%s", runErr, output)
		}
		result := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json")
		wire := decodeFetchCLIDocument(t, result.Stdout)
		repositories := wire["repositories"].([]any)
		parent := repositories[0].(map[string]any)
		failed := repositories[1].(map[string]any)
		if result.Err == nil || cli.ExitCode(result.Err) != 6 || result.Stderr != "" || wire["status"] != "failed" || len(repositories) != 2 || parent["id"] != fixture.project.BaseRepository || parent["status"] != "completed" || failed["id"] != fixture.childID || failed["status"] != "failed" || failed["failure"].(map[string]any)["code"] != "git" || !strings.Contains(failed["failure"].(map[string]any)["message"].(string), fmt.Sprintf("fetch preflight %q: read configured upstream", fixture.childID)) {
			t.Fatalf("malformed child JSON = %#v, wire=%#v", result, wire)
		}
	})
}

func TestExecuteFetchMalformedChildWriterErrorIsExact(t *testing.T) {
	fixture := newExecCLIFixture(t)
	workspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
	if err != nil {
		t.Fatal(err)
	}
	base, found := execCLICheckout(workspace, fixture.project.BaseRepository)
	if !found {
		t.Fatalf("base checkout missing: %#v", workspace)
	}
	child, found := execCLICheckout(workspace, fixture.childID)
	if !found {
		t.Fatalf("child checkout missing: %#v", workspace)
	}
	if output, runErr := exec.Command("git", "-C", child.ResolvedPath, "config", "--unset", "branch."+child.Branch+".merge").CombinedOutput(); runErr != nil {
		t.Fatalf("malform child upstream: %v\n%s", runErr, output)
	}
	want := errors.New("reject malformed child row")
	writer := &fetchCLIRejectingWriter{reject: "Repository: " + fixture.childID, err: want}
	var stderr bytes.Buffer
	err = cli.Execute([]string{"fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--verbose"}, writer, &stderr)
	parentRow := fmt.Sprintf("Repository: %s path=%s status=completed remote=origin ref=refs/heads/main\n", fixture.project.BaseRepository, base.ResolvedPath)
	if !errors.Is(err, want) || cli.ExitCode(err) != 1 || stderr.String() != "" || writer.String() != parentRow || strings.Contains(writer.String(), "Workspace:") || strings.Contains(writer.String(), "status=failed") {
		t.Fatalf("malformed child writer = %v exit=%d stdout=%q stderr=%q", err, cli.ExitCode(err), writer.String(), stderr.String())
	}
}

func TestExecuteFetchRealResolverAuthoritiesRemainUntouched(t *testing.T) {
	fixture := newExecCLIFixture(t)
	workspace, err := service.RequireWorkspace(fixture.project, fixture.data, "default")
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotFetchCLIInventory(t, fixture.root.Path, fixture.data, workspace)
	if result := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--dry-run", "--json"); result.Err != nil || result.Stderr != "" {
		t.Fatalf("dry fetch = %#v", result)
	}
	assertFetchCLIInventory(t, before, snapshotFetchCLIInventory(t, fixture.root.Path, fixture.data, workspace), nil)
	if result := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--json"); result.Err != nil || result.Stderr != "" {
		t.Fatalf("execution fetch = %#v", result)
	}
	assertFetchCLIInventory(t, before, snapshotFetchCLIInventory(t, fixture.root.Path, fixture.data, workspace), map[string]string{fixture.project.BaseRepository: "refs/remotes/origin/main", fixture.childID: "refs/remotes/origin/main"})

	base, found := execCLICheckout(workspace, fixture.project.BaseRepository)
	if !found {
		t.Fatalf("base checkout missing: %#v", workspace)
	}
	// This is the production workspace-state path chosen by the resolver, not a
	// sibling test sentinel. Fetch must leave it, the real registry, and every
	// other present/absent data-tree authority exactly alone.
	if err := store.WriteWorkspace(service.WorkspaceStatePath(fixture.data, fixture.project.ID, "imported-partial"), store.WorkspaceState{ID: "imported-partial", Name: "imported-partial", Path: workspace.RootPath, Partial: true, MissingRepositoryIDs: []string{fixture.childID}, Repositories: map[string]store.CheckoutState{fixture.project.BaseRepository: {Branch: base.Branch, Head: base.Head, Mount: base.Mount, ResolvedPath: base.ResolvedPath}}}); err != nil {
		t.Fatal(err)
	}
	partial, err := service.RequireWorkspace(fixture.project, fixture.data, "imported-partial")
	if err != nil {
		t.Fatal(err)
	}
	partialBefore := snapshotFetchCLIInventory(t, fixture.root.Path, fixture.data, partial)
	if result := testutil.RunCommand(t, cli.Execute, "fetch", "--project", fixture.root.Path, "--data-dir", fixture.data, "--workspace", "imported-partial", "--dry-run", "--json"); result.Err != nil || result.Stderr != "" {
		t.Fatalf("partial dry fetch = %#v", result)
	}
	assertFetchCLIInventory(t, partialBefore, snapshotFetchCLIInventory(t, fixture.root.Path, fixture.data, partial), nil)

	if output, runErr := exec.Command("git", "-C", base.ResolvedPath, "config", "--unset", "branch."+base.Branch+".merge").CombinedOutput(); runErr != nil {
		t.Fatalf("malform upstream: %v\n%s", runErr, output)
	}
	failedBefore := snapshotFetchCLIInventory(t, fixture.root.Path, fixture.data, workspace)
	writerErr := errors.New("preflight output failed")
	writer := &fetchCLIPartialWriter{err: writerErr}
	if err := cli.Execute([]string{"fetch", "--project", fixture.root.Path, "--data-dir", fixture.data}, writer, io.Discard); !errors.Is(err, writerErr) {
		t.Fatalf("preflight writer error = %v", err)
	}
	assertFetchCLIInventory(t, failedBefore, snapshotFetchCLIInventory(t, fixture.root.Path, fixture.data, workspace), nil)
}

type fetchCLIInventory struct {
	Project, Data map[string]fetchCLITreeEntry
	Authorities   map[string]fetchCLITreeEntry
	Git           map[string]fetchCLIGitInventory
}
type fetchCLITreeEntry struct{ Kind, Mode, Bytes string }
type fetchCLIGitInventory struct {
	Head, WriteTree, Refs, Config string
	Index, FetchHead              fetchCLITreeEntry
}

func snapshotFetchCLIInventory(t *testing.T, projectRoot, data string, workspace domain.Workspace) fetchCLIInventory {
	t.Helper()
	value := fetchCLIInventory{Project: snapshotFetchCLITree(t, projectRoot), Data: snapshotFetchCLITree(t, data), Authorities: snapshotFetchCLIAuthorities(t, data, workspace), Git: map[string]fetchCLIGitInventory{}}
	for _, checkout := range workspace.Checkouts {
		path := checkout.ResolvedPath
		value.Git[checkout.RepositoryID] = fetchCLIGitInventory{
			Head:      fetchCLIGitOutput(t, path, "rev-parse", "HEAD"),
			WriteTree: fetchCLIGitOutput(t, path, "write-tree"),
			Refs:      fetchCLIGitOutput(t, path, "for-each-ref", "--format=%(refname):%(objectname)"),
			Config:    fetchCLIGitOutput(t, path, "config", "--list", "--local"),
			Index:     snapshotFetchCLIPath(t, strings.TrimSpace(fetchCLIGitOutput(t, path, "rev-parse", "--path-format=absolute", "--git-path", "index"))),
			FetchHead: snapshotFetchCLIPath(t, strings.TrimSpace(fetchCLIGitOutput(t, path, "rev-parse", "--path-format=absolute", "--git-path", "FETCH_HEAD"))),
		}
	}
	return value
}

func assertFetchCLIInventory(t *testing.T, before, after fetchCLIInventory, allowed map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before.Project, after.Project) || !reflect.DeepEqual(before.Data, after.Data) || !reflect.DeepEqual(before.Authorities, after.Authorities) {
		t.Fatalf("fetch changed real project/data authority: before=%#v after=%#v", before, after)
	}
	if !reflect.DeepEqual(sortedFetchCLIGitIDs(before.Git), sortedFetchCLIGitIDs(after.Git)) {
		t.Fatalf("fetch checkout inventory changed: before=%#v after=%#v", before.Git, after.Git)
	}
	for id, left := range before.Git {
		right := after.Git[id]
		if left.Head != right.Head || left.WriteTree != right.WriteTree || left.Config != right.Config || !reflect.DeepEqual(left.Index, right.Index) || !reflect.DeepEqual(left.FetchHead, right.FetchHead) {
			t.Fatalf("fetch changed protected Git authority %s: before=%#v after=%#v", id, left, right)
		}
		if ref := allowed[id]; ref != "" {
			if !fetchCLIOnlyRefMayDiffer(left.Refs, right.Refs, ref) {
				t.Fatalf("fetch changed undeclared refs for %s: before=%q after=%q", id, left.Refs, right.Refs)
			}
		} else if left.Refs != right.Refs {
			t.Fatalf("fetch changed refs without a permitted selected ref for %s", id)
		}
	}
}

func snapshotFetchCLIAuthorities(t *testing.T, data string, workspace domain.Workspace) map[string]fetchCLITreeEntry {
	t.Helper()
	projectID := workspace.ID
	// The workspace ID is not the project ID; infer it from the persisted
	// default state path already present in the data tree's sole project scope.
	projects, err := os.ReadDir(filepath.Join(data, "projects"))
	if err != nil || len(projects) != 1 {
		t.Fatalf("resolver project authority = %v, entries=%v", err, projects)
	}
	projectID = projects[0].Name()
	paths := []string{
		filepath.Join(data, "registry.json"),
		filepath.Join(data, "registry.lock"),
		filepath.Join(data, "state", projectID, workspace.ID+".json"),
		filepath.Join(data, "projects", projectID, "reconciliation.json"),
		filepath.Join(data, "projects", projectID, "project.lock"),
		filepath.Join(data, "projects", projectID, "recovery", workspace.ID+".json"),
		filepath.Join(data, "projects", projectID, "update", "fetch-inventory", "journal.json"),
		filepath.Join(data, "projects", projectID, "update", "fetch-inventory", "staging"),
		filepath.Join(data, "projects", projectID, "update", "fetch-inventory", "backups"),
	}
	values := map[string]fetchCLITreeEntry{}
	for _, path := range paths {
		values[path] = snapshotFetchCLIPath(t, path)
	}
	return values
}

func sortedFetchCLIGitIDs(values map[string]fetchCLIGitInventory) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func fetchCLIOnlyRefMayDiffer(before, after, allowed string) bool {
	parse := func(value string) map[string]string {
		out := map[string]string{}
		for _, line := range strings.Fields(value) {
			pair := strings.SplitN(line, ":", 2)
			if len(pair) == 2 {
				out[pair[0]] = pair[1]
			}
		}
		return out
	}
	left, right := parse(before), parse(after)
	for ref, value := range left {
		if ref != allowed && right[ref] != value {
			return false
		}
	}
	for ref, value := range right {
		if ref != allowed && left[ref] != value {
			return false
		}
	}
	return true
}
func snapshotFetchCLITree(t *testing.T, root string) map[string]fetchCLITreeEntry {
	t.Helper()
	values := map[string]fetchCLITreeEntry{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		values[filepath.ToSlash(relative)] = snapshotFetchCLIPath(t, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return values
}
func snapshotFetchCLIPath(t *testing.T, path string) fetchCLITreeEntry {
	t.Helper()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fetchCLITreeEntry{Kind: "absent"}
	}
	if err != nil {
		t.Fatal(err)
	}
	value := fetchCLITreeEntry{Mode: fmt.Sprintf("%#o", info.Mode())}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
		value.Kind, value.Bytes = "symlink", fmt.Sprintf("%x", target)
	} else if info.IsDir() {
		value.Kind = "directory"
	} else {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value.Kind, value.Bytes = "file", fmt.Sprintf("%x", data)
	}
	return value
}
func fetchCLIGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", directory}, arguments...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return string(output)
}

func TestExecuteFetchDocumentationRemainsExplicit(t *testing.T) {
	for _, arguments := range [][]string{{"fetch", "--help"}, {"--how-to"}} {
		var stdout, stderr bytes.Buffer
		if err := cli.Execute(arguments, &stdout, &stderr); err != nil || stderr.String() != "" || !strings.Contains(stdout.String(), "fetch") || !strings.Contains(stdout.String(), "remote") {
			t.Fatalf("%v = stdout=%q stderr=%q err=%v", arguments, stdout.String(), stderr.String(), err)
		}
	}
}

func decodeFetchCLIDocument(t *testing.T, output string) map[string]any {
	t.Helper()
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("fetch JSON emitted more than one stdout document: %q", output)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode fetch JSON %q: %v", output, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("fetch JSON has trailing document/content: %q (%v)", output, err)
	}
	return value
}

func assertFetchCLIKeys(t *testing.T, value map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fetch JSON keys = %v, want %v in %#v", got, want, value)
	}
}

func fetchCLIRepositoryKeys(entry map[string]any, execution, failed bool) []string {
	keys := []string{"id", "mount", "path", "branch", "head", "status", "remote", "remoteRef", "actualRemoteCommit"}
	if _, exists := entry["parentId"]; exists {
		keys = append(keys, "parentId")
	}
	if execution {
		keys = append(keys, "previousRemoteCommit")
	}
	if failed {
		keys = append(keys, "failure")
	}
	return keys
}

type fetchCLIPartialWriter struct {
	err error
	buf bytes.Buffer
}

func (writer *fetchCLIPartialWriter) Write(value []byte) (int, error) {
	count := len(value) / 2
	if count == 0 && len(value) > 0 {
		count = 1
	}
	_, _ = writer.buf.Write(value[:count])
	return count, writer.err
}
func (writer *fetchCLIPartialWriter) String() string { return writer.buf.String() }

type fetchCLIRejectingWriter struct {
	reject string
	err    error
	buf    bytes.Buffer
}

func (writer *fetchCLIRejectingWriter) Write(value []byte) (int, error) {
	if strings.Contains(string(value), writer.reject) {
		return 0, writer.err
	}
	return writer.buf.Write(value)
}

func (writer *fetchCLIRejectingWriter) String() string { return writer.buf.String() }
