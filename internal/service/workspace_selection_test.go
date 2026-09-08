package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
)

func TestWorkspaceSelectionMatchingAndDetails(t *testing.T) {
	project, data, paths := workspaceSelectionFixture(t, []workspaceSelectionState{
		{id: "harden-id", name: "feat/harden-loops"},
		{id: "hardening-id", name: "feat/hardening"},
		{id: "literal-id", name: "feat/a.b[c]"},
		{id: "case-id", name: "Feat/Case"},
		{id: "same-id", name: "same"},
		{id: "same-one", name: "feature/two"},
		{id: "same-two", name: "feature/two"},
		{id: "cross-id", name: "target"},
		{id: "target", name: "other"},
	})
	selector := NewWorkspaceSelectorWith(&workspaceSelectionGit{})
	for _, test := range []struct {
		name      string
		query     string
		mode      WorkspaceSelectionMode
		wantID    string
		wantKind  ErrorKind
		wantNames []string
	}{
		{name: "prefix", query: "feat/harden-loops", mode: WorkspaceSelectionSubstring, wantID: "harden-id"},
		{name: "middle", query: "harden-", mode: WorkspaceSelectionSubstring, wantID: "harden-id"},
		{name: "suffix", query: "loops", mode: WorkspaceSelectionSubstring, wantID: "harden-id"},
		{name: "literal punctuation", query: "a.b[c]", mode: WorkspaceSelectionSubstring, wantID: "literal-id"},
		{name: "case sensitive", query: "case", mode: WorkspaceSelectionSubstring, wantKind: ErrorWorkspaceNotFound},
		{name: "identical full name has no priority", query: "feature/two", mode: WorkspaceSelectionSubstring, wantKind: ErrorConflict, wantNames: []string{"feature/two", "feature/two"}},
		{name: "partial id excluded", query: "harden-id", mode: WorkspaceSelectionSubstring, wantKind: ErrorWorkspaceNotFound},
		{name: "exact name", query: "feat/harden-loops", mode: WorkspaceSelectionExact, wantID: "harden-id"},
		{name: "exact id", query: "harden-id", mode: WorkspaceSelectionExact, wantID: "harden-id"},
		{name: "cross identity conflict", query: "target", mode: WorkspaceSelectionExact, wantKind: ErrorConflict, wantNames: []string{"other", "target"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: test.query, Mode: test.mode, Policy: WorkspaceSelectionEligible})
			if test.wantKind == "" {
				if err != nil || value.ID != test.wantID {
					t.Fatalf("Select() = %#v, %v; want ID %q", value, err, test.wantID)
				}
				return
			}
			assertWorkspaceSelectionError(t, err, test.wantKind, test.query, test.mode, test.wantNames)
		})
	}
	if _, err := selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Mode: WorkspaceSelectionSubstring, Policy: WorkspaceSelectionEligible}); !selectionErrorKind(err, ErrorInvalidArguments) {
		t.Fatalf("empty selector = %v, want invalid arguments", err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("selection changed fixture path %q: %v", path, err)
		}
	}
}

func TestWorkspaceSelectionErrorNamesCandidatesAndRootValidation(t *testing.T) {
	err := (&WorkspaceSelectionError{Query: "feature", Candidates: []WorkspaceSelectionCandidate{{Name: "feature/alpha", ID: "alpha-id"}, {Name: "feature/beta", ID: "beta-id"}}}).Error()
	for _, want := range []string{`"feature"`, `"feature/alpha"`, `"alpha-id"`, `"feature/beta"`, `"beta-id"`, "narrow the query", "--exact"} {
		if !strings.Contains(err, want) {
			t.Fatalf("selection error = %q, missing %q", err, want)
		}
	}

	directory := t.TempDir()
	if err := ValidateWorkspaceRoot(domain.Workspace{RootPath: directory}); err != nil {
		t.Fatalf("directory root = %v", err)
	}
	file := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !selectionErrorKind(ValidateWorkspaceRoot(domain.Workspace{RootPath: file}), ErrorValidation) {
		t.Fatal("file root did not return validation")
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	if !selectionErrorKind(ValidateWorkspaceRoot(domain.Workspace{RootPath: link}), ErrorValidation) {
		t.Fatal("symlink root did not return validation")
	}
	denied := validateWorkspaceRootWithDependencies(domain.Workspace{RootPath: directory}, workspaceRootDependencies{
		lstat: os.Lstat,
		accessible: func(string) error {
			return fs.ErrPermission
		},
	})
	if !selectionErrorKind(denied, ErrorValidation) || !strings.Contains(denied.Error(), "access workspace root") {
		t.Fatalf("inaccessible root = %v, want validation access error", denied)
	}
}

func TestWorkspaceSelectionRemovedWorkspacePoliciesAndObservation(t *testing.T) {
	project, data, paths := workspaceSelectionFixture(t, []workspaceSelectionState{
		{id: "removed", name: "feature/removed", missing: true},
		{id: "registered", name: "feature/registered", missing: true},
	})
	git := &workspaceSelectionGit{lists: map[string][]gitadapter.Worktree{
		project.Repositories[0].SourcePath: {{Path: paths["registered"]}},
	}}
	selector := NewWorkspaceSelectorWith(git)
	for _, test := range []struct {
		query  string
		policy WorkspaceSelectionPolicy
		wantID string
		kind   ErrorKind
	}{
		{query: "feature/removed", policy: WorkspaceSelectionEligible, kind: ErrorWorkspaceNotFound},
		{query: "feature/removed", policy: WorkspaceSelectionCheckout, wantID: "removed"},
		{query: "feature/registered", policy: WorkspaceSelectionEligible, wantID: "registered"},
	} {
		t.Run(test.query+"/"+string(test.policy), func(t *testing.T) {
			value, err := selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: test.query, Mode: WorkspaceSelectionExact, Policy: test.policy})
			if test.kind != "" {
				assertWorkspaceSelectionError(t, err, test.kind, test.query, WorkspaceSelectionExact, []string{})
			} else if err != nil || value.ID != test.wantID {
				t.Fatalf("Select() = %#v, %v; want ID %q", value, err, test.wantID)
			}
		})
	}
	if got := git.calls[project.Repositories[0].SourcePath]; got != 2 {
		t.Fatalf("ListWorktrees calls = %d, want one observation per eligible Select invocation", got)
	}
}

func TestWorkspaceSelectionKeepsPresentPartialImportEligible(t *testing.T) {
	rootSource := t.TempDir()
	childSource := t.TempDir()
	project := domain.Project{Version: domain.CurrentVersion, ID: "partial-project", BaseRepository: "root", Repositories: []domain.Repository{
		{ID: "root", SourcePath: rootSource, DefaultMount: "."},
		{ID: "child", ParentID: "root", SourcePath: childSource, DefaultMount: "child"},
	}}
	data, rootPath := t.TempDir(), t.TempDir()
	if err := store.WriteWorkspace(WorkspaceStatePath(data, project.ID, "partial"), store.WorkspaceState{
		Version: store.Version, ID: "partial", Name: "feature/partial", Path: rootPath, Partial: true, MissingRepositoryIDs: []string{"child"},
		Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Head: strings.Repeat("a", 40), Mount: ".", ResolvedPath: rootPath}},
	}); err != nil {
		t.Fatal(err)
	}
	value, err := NewWorkspaceSelectorWith(&workspaceSelectionGit{}).Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/partial", Mode: WorkspaceSelectionExact, Policy: WorkspaceSelectionEligible})
	if err != nil || value.ID != "partial" || !value.Partial {
		t.Fatalf("Select() = %#v, %v; want present partial workspace", value, err)
	}
}

func TestWorkspaceSelectionRetainsPersistedPartialAndDamagedCandidates(t *testing.T) {
	data := t.TempDir()
	rootSource, otherSource := t.TempDir(), t.TempDir()
	project := domain.Project{Version: domain.CurrentVersion, ID: "damaged-project", BaseRepository: "a-root", Repositories: []domain.Repository{{ID: "a-root", SourcePath: rootSource, DefaultMount: "a-root"}, {ID: "z-other", SourcePath: otherSource, DefaultMount: "z-other"}}}
	writeState := func(id, name, root string) {
		t.Helper()
		state := store.WorkspaceState{Version: store.Version, ID: id, Name: name, Path: root, Repositories: map[string]store.CheckoutState{
			"a-root":  {Branch: "main", Head: strings.Repeat("a", 40), Mount: "a-root", ResolvedPath: filepath.Join(root, "a-root")},
			"z-other": {Branch: "main", Head: strings.Repeat("b", 40), Mount: "z-other", ResolvedPath: filepath.Join(root, "z-other")},
		}}
		if err := store.WriteWorkspace(WorkspaceStatePath(data, project.ID, id), state); err != nil {
			t.Fatal(err)
		}
	}
	wrongNodeRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(wrongNodeRoot, "z-other"), 0o755); err != nil {
		t.Fatal(err)
	}
	wrongNode := filepath.Join(wrongNodeRoot, "a-root")
	if err := os.WriteFile(wrongNode, []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeState("wrong-node", "feature/persisted/wrong-node", wrongNodeRoot)

	danglingRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(danglingRoot, "z-other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(danglingRoot, "a-root"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeState("dangling", "feature/persisted/dangling", danglingRoot)

	firstMissingRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(firstMissingRoot, "z-other"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The absent first checkout must not classify this persisted state as
	// removed while its other recorded checkout still exists.
	writeState("first-missing", "feature/persisted/first-missing", firstMissingRoot)

	// A persisted workspace cannot contain a real dangling symlink because the
	// validated inventory rejects it before selection. Model the filesystem
	// observation at the selector boundary while retaining the persisted,
	// validated candidate in the inventory.
	danglingObservation := filepath.Join(t.TempDir(), "dangling-observation")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), danglingObservation); err != nil {
		t.Fatal(err)
	}
	danglingInfo, err := os.Lstat(danglingObservation)
	if err != nil {
		t.Fatal(err)
	}
	targetPaths := map[string]string{
		"wrong-node":    wrongNode,
		"dangling":      filepath.Join(danglingRoot, "a-root"),
		"first-missing": filepath.Join(firstMissingRoot, "a-root"),
	}
	observations := make(map[string]int, len(targetPaths))
	selector := newWorkspaceSelectorWithDependencies(workspaceSelectionDependencies{
		git: &workspaceSelectionGit{},
		lstat: func(path string) (fs.FileInfo, error) {
			for id, target := range targetPaths {
				if path == target {
					observations[id]++
				}
			}
			if path == targetPaths["dangling"] {
				return danglingInfo, nil
			}
			return os.Lstat(path)
		},
	})
	for _, test := range []struct {
		query  string
		wantID string
	}{
		{query: "feature/persisted/wrong-node", wantID: "wrong-node"},
		{query: "feature/persisted/dangling", wantID: "dangling"},
		{query: "feature/persisted/first-missing", wantID: "first-missing"},
	} {
		value, err := selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: test.query, Mode: WorkspaceSelectionExact, Policy: WorkspaceSelectionEligible})
		if err != nil || value.ID != test.wantID {
			t.Fatalf("persisted candidate %q selection = %#v, %v; want ID %q", test.query, value, err, test.wantID)
		}
	}
	_, err = selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/persisted", Mode: WorkspaceSelectionSubstring, Policy: WorkspaceSelectionEligible})
	assertWorkspaceSelectionError(t, err, ErrorConflict, "feature/persisted", WorkspaceSelectionSubstring, []string{"feature/persisted/dangling", "feature/persisted/first-missing", "feature/persisted/wrong-node"})
	for id, want := range map[string]int{"wrong-node": 2, "dangling": 2, "first-missing": 2} {
		if got := observations[id]; got != want {
			t.Fatalf("lstat observations for %s = %d, want %d (exact selection and ambiguity)", id, got, want)
		}
	}
}

func TestWorkspaceSelectionRealForestRemovalIsReadOnly(t *testing.T) {
	project, data := forestWorkspaceProject(t)
	target := filepath.Join(t.TempDir(), "forest-workspace")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{WorkspaceName: "feature/removed", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatalf("create forest workspace: %v", err)
	}
	workspace, err := RequireWorkspace(project, data, "feature/removed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, true, nil); err != nil {
		t.Fatalf("remove forest workspace: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(workspace.RootPath), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, workspace.ID)
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	configPaths := make([]string, 0, len(project.Repositories))
	configBefore := make([][]byte, 0, len(project.Repositories))
	refsBefore := make(map[string]string, len(project.Repositories))
	worktreesBefore := make(map[string][]gitadapter.Worktree, len(project.Repositories))
	for _, repository := range project.Repositories {
		path := filepath.Join(repository.SourcePath, ".git", "config")
		value, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read source config %q: %v", path, readErr)
		}
		configPaths, configBefore = append(configPaths, path), append(configBefore, value)
		refsBefore[repository.ID] = workspaceSelectionGitOutput(t, repository.SourcePath, "for-each-ref", "--format=%(refname):%(objectname)")
		worktrees, listErr := gitadapter.NewAdapter("git").ListWorktrees(context.Background(), repository.SourcePath)
		if listErr != nil {
			t.Fatalf("list source worktrees: %v", listErr)
		}
		worktreesBefore[repository.ID] = worktrees
	}
	selector := NewWorkspaceSelector()
	_, err = selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/removed", Mode: WorkspaceSelectionExact, Policy: WorkspaceSelectionEligible})
	assertWorkspaceSelectionError(t, err, ErrorWorkspaceNotFound, "feature/removed", WorkspaceSelectionExact, []string{})
	value, err := selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/removed", Mode: WorkspaceSelectionExact, Policy: WorkspaceSelectionCheckout})
	if err != nil || value.ID != workspace.ID {
		t.Fatalf("checkout selection = %#v, %v; want retained forest workspace", value, err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !reflect.DeepEqual(stateBefore, stateAfter) {
		t.Fatalf("selection changed persistent state: %v", err)
	}
	for index, path := range configPaths {
		value, readErr := os.ReadFile(path)
		if readErr != nil || !reflect.DeepEqual(configBefore[index], value) {
			t.Fatalf("selection changed source config %q: %v", path, readErr)
		}
	}
	for _, repository := range project.Repositories {
		if refsAfter := workspaceSelectionGitOutput(t, repository.SourcePath, "for-each-ref", "--format=%(refname):%(objectname)"); refsBefore[repository.ID] != refsAfter {
			t.Fatalf("selection changed source refs %q", repository.ID)
		}
		worktrees, listErr := gitadapter.NewAdapter("git").ListWorktrees(context.Background(), repository.SourcePath)
		if listErr != nil || !reflect.DeepEqual(worktreesBefore[repository.ID], worktrees) {
			t.Fatalf("selection changed worktree registration %q: %v", repository.ID, listErr)
		}
	}
}

func TestWorkspaceSelectionUncertainObservationsDoNotNarrowCandidates(t *testing.T) {
	project, data, paths := workspaceSelectionFixture(t, []workspaceSelectionState{
		{id: "dangling", name: "feature/damaged"},
		{id: "node", name: "feature/damaged-node"},
	})
	selector := NewWorkspaceSelectorWith(&workspaceSelectionGit{})
	dangling := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "not-there"), dangling); err != nil {
		t.Fatal(err)
	}
	if absent, err := selector.definitelyAbsent(dangling); err != nil || absent {
		t.Fatalf("dangling symlink absence = %t, %v; want false, nil", absent, err)
	}
	canonicalParent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "canonical-alias")
	if err := os.Symlink(canonicalParent, alias); err != nil {
		t.Fatal(err)
	}
	if absent, err := selector.definitelyAbsent(filepath.Join(alias, "missing", "checkout")); err != nil || !absent {
		t.Fatalf("missing leaf under canonical alias = %t, %v; want true, nil", absent, err)
	}
	aliasPath := filepath.Join(alias, "missing", "checkout")
	if err := store.WriteWorkspace(WorkspaceStatePath(data, project.ID, "canonical-alias"), store.WorkspaceState{
		Version: store.Version, ID: "canonical-alias", Name: "feature/canonical-alias", Path: aliasPath,
		Repositories: map[string]store.CheckoutState{"root": {Branch: "main", Head: strings.Repeat("a", 40), Mount: ".", ResolvedPath: aliasPath}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/canonical-alias", Mode: WorkspaceSelectionExact, Policy: WorkspaceSelectionEligible})
	assertWorkspaceSelectionError(t, err, ErrorWorkspaceNotFound, "feature/canonical-alias", WorkspaceSelectionExact, []string{})
	wrongNode := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(wrongNode, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if absent, err := selector.definitelyAbsent(wrongNode); err != nil || absent {
		t.Fatalf("wrong node absence = %t, %v; want false, nil", absent, err)
	}
	_, err = selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/damaged", Mode: WorkspaceSelectionSubstring, Policy: WorkspaceSelectionEligible})
	assertWorkspaceSelectionError(t, err, ErrorConflict, "feature/damaged", WorkspaceSelectionSubstring, []string{"feature/damaged", "feature/damaged-node"})

	selector = newWorkspaceSelectorWithDependencies(workspaceSelectionDependencies{
		git: &workspaceSelectionGit{},
		lstat: func(path string) (fs.FileInfo, error) {
			if path == paths["dangling"] {
				return nil, &os.PathError{Op: "lstat", Path: path, Err: fs.ErrPermission}
			}
			return os.Lstat(path)
		},
	})
	_, err = selector.Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/damaged", Mode: WorkspaceSelectionSubstring, Policy: WorkspaceSelectionEligible})
	if !selectionErrorKind(err, ErrorInternal) {
		t.Fatalf("permission observation = %v, want internal error", err)
	}

	gitFailure := &workspaceSelectionGit{err: errors.New("git unavailable")}
	_, err = newWorkspaceSelectorWithDependencies(workspaceSelectionDependencies{
		git: gitFailure,
		lstat: func(path string) (fs.FileInfo, error) {
			if path == paths["dangling"] {
				return nil, &os.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist}
			}
			return os.Lstat(path)
		},
	}).Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/damaged", Mode: WorkspaceSelectionSubstring, Policy: WorkspaceSelectionEligible})
	if !selectionErrorKind(err, ErrorGit) {
		t.Fatalf("Git observation = %v, want git error", err)
	}
}

func TestWorkspaceSelectionRejectsZeroCheckoutState(t *testing.T) {
	project, data, _ := workspaceSelectionFixture(t, []workspaceSelectionState{{id: "empty", name: "feature/empty", empty: true}})
	_, err := NewWorkspaceSelectorWith(&workspaceSelectionGit{}).Select(context.Background(), project, WorkspaceSelectionRequest{DataDir: data, Query: "feature/empty", Mode: WorkspaceSelectionExact, Policy: WorkspaceSelectionEligible})
	if !selectionErrorKind(err, ErrorValidation) {
		t.Fatalf("zero checkout state = %v, want validation", err)
	}
}

func TestWorkspaceCheckoutSelectionRejectsEmptySelectorBeforeInventoryCapture(t *testing.T) {
	project, data, _ := workspaceSelectionFixture(t, nil)
	if err := os.MkdirAll(WorkspaceStateDirectory(data, project.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(WorkspaceStatePath(data, project.ID, "corrupt"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []WorkspaceSelectionMode{WorkspaceSelectionSubstring, WorkspaceSelectionExact} {
		t.Run(string(mode), func(t *testing.T) {
			_, err := NewWorkspaceSelectorWith(&workspaceSelectionGit{}).SelectCheckout(context.Background(), project, WorkspaceSelectionRequest{
				DataDir: data, Query: "", Mode: mode, Policy: WorkspaceSelectionCheckout,
			})
			if !selectionErrorKind(err, ErrorInvalidArguments) {
				t.Fatalf("SelectCheckout empty %s selector = %v, want invalid arguments before corrupt inventory", mode, err)
			}
		})
	}
}

func TestWorkspaceCheckoutSelectionPinsCapturedStateAndExactAbsence(t *testing.T) {
	project, data, paths := workspaceSelectionFixture(t, []workspaceSelectionState{{id: "selected", name: "feature/selected"}})
	selector := NewWorkspaceSelectorWith(&workspaceSelectionGit{})
	selection, err := selector.SelectCheckout(context.Background(), project, WorkspaceSelectionRequest{
		DataDir: data, Query: "selected", Mode: WorkspaceSelectionSubstring, Policy: WorkspaceSelectionCheckout,
	})
	if err != nil || selection.Workspace == nil || selection.Workspace.Name != "feature/selected" {
		t.Fatalf("SelectCheckout() = %#v, %v", selection, err)
	}
	// Replacing the selected file after its captured inventory must not become
	// a new checkout target and must fail before reconciliation or execution.
	state, err := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, "selected"))
	if err != nil {
		t.Fatal(err)
	}
	state.Path = filepath.Join(t.TempDir(), "replacement")
	state.Repositories["root"] = store.CheckoutState{Branch: "feature/selected", Head: strings.Repeat("b", 40), Mount: ".", ResolvedPath: state.Path}
	if err := os.Mkdir(state.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(WorkspaceStatePath(data, project.ID, "selected"), state); err != nil {
		t.Fatal(err)
	}
	if err := selection.Precondition.revalidate(project); !selectionErrorKind(err, ErrorConflict) {
		t.Fatalf("replacement revalidation = %v, want conflict", err)
	}
	if selection.Workspace.RootPath != paths["selected"] {
		t.Fatalf("frozen selection root = %q, want %q", selection.Workspace.RootPath, paths["selected"])
	}

	branch, err := selector.SelectExactBranch(context.Background(), project, data, "feature/branch-only")
	if err != nil || branch.Workspace != nil {
		t.Fatalf("SelectExactBranch() = %#v, %v", branch, err)
	}
	branchPath := filepath.Join(t.TempDir(), "branch-state")
	if err := os.Mkdir(branchPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(WorkspaceStatePath(data, project.ID, "branch-state"), store.WorkspaceState{
		Version: store.Version, ID: "branch-state", Name: "feature/branch-only", Path: branchPath,
		Repositories: map[string]store.CheckoutState{"root": {Branch: "feature/branch-only", Head: strings.Repeat("c", 40), Mount: ".", ResolvedPath: branchPath}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := branch.Precondition.revalidate(project); !selectionErrorKind(err, ErrorConflict) {
		t.Fatalf("branch appearance revalidation = %v, want conflict", err)
	}
	if _, err := NewWorkspaceCreator().PlanCheckout(context.Background(), project, WorkspaceCheckoutRequest{
		WorkspaceName: "feature/branch-only", DataDir: data, Precondition: branch.Precondition,
	}); !selectionErrorKind(err, ErrorConflict) {
		t.Fatalf("branch-only checkout planning after appearance = %v, want conflict without retarget", err)
	}
}

func TestWorkspaceSelectionPlatformPathComparison(t *testing.T) {
	if !sameCheckoutPathForPlatform(`C:\\Workspace\\API`, `c:\\workspace\\api`, true) {
		t.Fatal("Windows case aliases must match")
	}
	if sameCheckoutPathForPlatform("/Workspace/API", "/workspace/api", false) {
		t.Fatal("POSIX case-distinct paths must not match")
	}
}

type workspaceSelectionGit struct {
	gitadapter.Git
	lists map[string][]gitadapter.Worktree
	calls map[string]int
	err   error
}

func (g *workspaceSelectionGit) ListWorktrees(_ context.Context, source string) ([]gitadapter.Worktree, error) {
	if g.calls == nil {
		g.calls = map[string]int{}
	}
	g.calls[source]++
	if g.err != nil {
		return nil, g.err
	}
	return append([]gitadapter.Worktree(nil), g.lists[source]...), nil
}

type workspaceSelectionState struct {
	id      string
	name    string
	missing bool
	partial bool
	empty   bool
}

func workspaceSelectionFixture(t *testing.T, states []workspaceSelectionState) (domain.Project, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	project := domain.Project{Version: domain.CurrentVersion, ID: "selection-project", BaseRepository: "root", Repositories: []domain.Repository{
		{ID: "root", SourcePath: root, DefaultMount: "."},
	}}
	data := t.TempDir()
	paths := map[string]string{}
	for _, state := range states {
		rootPath := filepath.Join(t.TempDir(), strings.ReplaceAll(state.id, "/", "-"))
		checkouts := map[string]store.CheckoutState{}
		if !state.empty {
			checkouts["root"] = store.CheckoutState{Branch: "main", Head: strings.Repeat("a", 40), Mount: ".", ResolvedPath: rootPath}
			if !state.missing {
				if err := os.Mkdir(rootPath, 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}
		missing := []string(nil)
		if state.empty {
			missing = []string{"root"}
			state.partial = true
		}
		value := store.WorkspaceState{Version: store.Version, ID: state.id, Name: state.name, Path: rootPath, Partial: state.partial, MissingRepositoryIDs: missing, Repositories: checkouts}
		if err := store.WriteWorkspace(WorkspaceStatePath(data, project.ID, state.id), value); err != nil {
			t.Fatal(err)
		}
		paths[state.id] = rootPath
	}
	return project, data, paths
}

func workspaceSelectionGitOutput(t *testing.T, path string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(output)
}

func assertWorkspaceSelectionError(t *testing.T, err error, kind ErrorKind, query string, mode WorkspaceSelectionMode, names []string) {
	t.Helper()
	if names == nil {
		names = []string{}
	}
	if !selectionErrorKind(err, kind) {
		t.Fatalf("error kind = %v, want %s", err, kind)
	}
	var detail *WorkspaceSelectionError
	if !errors.As(err, &detail) {
		t.Fatalf("selection detail missing from %v", err)
	}
	if detail.Query != query || detail.Mode != mode {
		t.Fatalf("selection detail = %#v, want query %q mode %q", detail, query, mode)
	}
	got := make([]string, len(detail.Candidates))
	for index, candidate := range detail.Candidates {
		got[index] = candidate.Name
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("selection candidates = %#v, want names %v", detail.Candidates, names)
	}
	if detail.Candidates == nil {
		t.Fatal("selection candidates must be [] rather than nil")
	}
}

func selectionErrorKind(err error, want ErrorKind) bool {
	var application *Error
	return errors.As(err, &application) && application.Kind == want
}
