package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecRunsDirectArgvInDeterministicOrder(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	service := NewExecService()
	request := ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecProcessHelper$", "literal;touch", "$(not-a-shell)"}, Environment: []string{"PATH=" + os.Getenv("PATH"), "WTREE_PROJECT_ID=hostile"}}
	result, err := service.Exec(context.Background(), project, workspace, request)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if got, want := execResultIDs(result), "alpha,beta"; got != want {
		t.Fatalf("repository order = %q, want %q", got, want)
	}
	for _, repository := range result.Repositories {
		if repository.Status != "completed" || !strings.Contains(repository.Stdout, "literal;touch") || !strings.Contains(repository.Stdout, "$(not-a-shell)") || !strings.Contains(repository.Stdout, "WTREE_PROJECT_ID="+project.ID) || strings.Contains(repository.Stdout, "hostile") {
			t.Fatalf("repository result = %#v", repository)
		}
	}
	request.Reverse = true
	var completed []string
	request.OnComplete = func(repository ExecRepositoryResult) error {
		completed = append(completed, repository.ID)
		return nil
	}
	result, err = service.Exec(context.Background(), project, workspace, request)
	if err != nil || execResultIDs(result) != "alpha,beta" || strings.Join(completed, ",") != "beta,alpha" {
		t.Fatalf("reverse Exec() = %#v, %v", result, err)
	}
}

func TestExecPreflightRefusesBeforeStartingAnyProgram(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	project.Repositories[1].CommonGitDir = filepath.Join(t.TempDir(), "wrong-identity")
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "literal;touch never", Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err == nil || result.Status != AggregateStatusFailed || len(result.Repositories) != 2 || result.Repositories[0].Status != AggregateStatusFailed || result.Repositories[1].Status != AggregateStatusPlanned {
		t.Fatalf("Exec() = %#v, %v; want failed preflight envelope without a started process", result, err)
	}
}

func TestExecPreflightDriftMatrixNeverStartsAProgram(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.Project, *domain.Workspace)
		service func() *ExecService
	}{
		{
			name: "missing checkout path",
			mutate: func(_ *domain.Project, workspace *domain.Workspace) {
				workspace.Checkouts[1].ResolvedPath = filepath.Join(t.TempDir(), "missing")
			},
		},
		{
			name: "common Git identity",
			mutate: func(project *domain.Project, _ *domain.Workspace) {
				project.Repositories[1].CommonGitDir = filepath.Join(t.TempDir(), "wrong")
			},
		},
		{
			name:   "attached branch",
			mutate: func(_ *domain.Project, workspace *domain.Workspace) { workspace.Checkouts[1].Branch = "other" },
		},
		{
			name: "detached state",
			mutate: func(_ *domain.Project, workspace *domain.Workspace) {
				workspace.Checkouts[1].Detached, workspace.Checkouts[1].Branch = true, ""
			},
		},
		{
			name: "HEAD",
			mutate: func(_ *domain.Project, workspace *domain.Workspace) {
				workspace.Checkouts[1].Head = strings.Repeat("0", 40)
			},
		},
		{
			name:   "checkout top level",
			mutate: func(_ *domain.Project, _ *domain.Workspace) {},
			service: func() *ExecService {
				return NewExecServiceWith(execTopLevelMismatchGit{Git: gitadapter.NewAdapter("git")})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := execTestWorkspace(t)
			test.mutate(&project, &workspace)
			marker := t.TempDir()
			service := NewExecService()
			if test.service != nil {
				service = test.service()
			}
			_, err := service.Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
			if err == nil {
				t.Fatal("Exec() succeeded after preflight drift")
			}
			for _, id := range []string{"alpha", "beta"} {
				if _, statErr := os.Stat(filepath.Join(marker, id)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("%s started despite preflight failure: %v", id, statErr)
				}
			}
		})
	}
}

func TestExecContinuesAfterNonZeroExit(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecFailureHelper$"}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorConflict || result.Status != AggregateStatusFailed || len(result.Repositories) != 2 || result.Repositories[0].Status != "completed" || result.Repositories[1].Status != "failed" || result.Repositories[1].ExitCode == nil || *result.Repositories[1].ExitCode != 7 || result.Repositories[1].Failure == nil || result.Repositories[1].Failure.Code != ErrorConflict {
		t.Fatalf("Exec() = %#v, %v", result, err)
	}
}

func TestExecDryRunPreflightsWithoutStartingProgram(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "does-not-exist", DryRun: true, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil || !result.DryRun || execResultIDs(result) != "alpha,beta" || result.Repositories[0].Status != "planned" {
		t.Fatalf("dry-run Exec() = %#v, %v", result, err)
	}
}

func TestExecSelectionOccursBeforePreflightAndBindsResultMembership(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	for index := range project.Repositories {
		if project.Repositories[index].ID == "beta" {
			project.Repositories[index].Companion = true
		}
	}

	ordinary, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", DryRun: true, NoCompanions: true})
	if err != nil || execResultIDs(ordinary) != "alpha" || strings.Join(ordinary.ExecutionOrder, ",") != "alpha" || ordinary.Repositories[0].Companion {
		t.Fatalf("ordinary selection = %#v, %v", ordinary, err)
	}
	companion, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", DryRun: true, RepositoryID: "beta", Reverse: true})
	if err != nil || execResultIDs(companion) != "beta" || strings.Join(companion.ExecutionOrder, ",") != "beta" || !companion.Repositories[0].Companion {
		t.Fatalf("exact companion selection = %#v, %v", companion, err)
	}
	entry := marshalExecObject(t, companion)["repositories"].([]any)[0].(map[string]any)
	if entry["companion"] != true {
		t.Fatalf("companion JSON row = %#v", entry)
	}

	workspace.Checkouts[execCheckoutIndex(t, workspace, "beta")].ResolvedPath = filepath.Join(t.TempDir(), "unselected-broken")
	workspace.MissingRepositoryIDs = []string{"beta", "beta"}
	ignored, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", DryRun: true, NoCompanions: true})
	if err != nil || execResultIDs(ignored) != "alpha" {
		t.Fatalf("unselected broken checkout blocked ordinary selection: %#v, %v", ignored, err)
	}
	failed, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", DryRun: true, RepositoryID: "beta"})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || failed.Status != AggregateStatusFailed || execResultIDs(failed) != "beta" {
		t.Fatalf("selected broken checkout = %#v, %v", failed, err)
	}
}

func TestExecDefaultAllRetainsWorkspaceValidationBeforeLaunch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.Project, *domain.Workspace)
	}{
		{name: "complete workspace lists missing repository", mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.MissingRepositoryIDs = []string{"beta"}
		}},
		{name: "partial workspace omits missing repository", mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Partial = true
		}},
		{name: "duplicate checkout", mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts = append(workspace.Checkouts, workspace.Checkouts[execCheckoutIndex(t, *workspace, "alpha")])
		}},
		{name: "unknown checkout", mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts = append(workspace.Checkouts, domain.Checkout{RepositoryID: "unknown", Branch: "main", Head: strings.Repeat("a", 40), Mount: "unknown", ResolvedPath: filepath.Join(workspace.RootPath, "unknown")})
		}},
		{name: "unaccounted repository", mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts = []domain.Checkout{workspace.Checkouts[execCheckoutIndex(t, *workspace, "alpha")]}
		}},
		{name: "mount path mismatch", mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[execCheckoutIndex(t, *workspace, "beta")].Mount = "renamed"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := execTestWorkspace(t)
			test.mutate(&project, &workspace)
			marker := t.TempDir()
			result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
			var application *Error
			if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Version != 0 {
				t.Fatalf("default validation = %#v, %v", result, err)
			}
			assertExecMarkerAbsent(t, marker, "alpha", "beta")
		})
	}
}

func TestExecScopedSelectionBindsOnlySelectedMountAndPathAuthority(t *testing.T) {
	tests := []struct {
		name    string
		request ExecRequest
		mutate  func(*domain.Project, *domain.Workspace)
		wantErr bool
	}{
		{name: "selected mount mismatch", request: ExecRequest{RepositoryID: "beta"}, mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[execCheckoutIndex(t, *workspace, "beta")].Mount = "renamed"
		}, wantErr: true},
		{name: "selected resolved path mismatch", request: ExecRequest{RepositoryID: "beta"}, mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[execCheckoutIndex(t, *workspace, "beta")].ResolvedPath = filepath.Join(workspace.RootPath, "alpha", "other")
		}, wantErr: true},
		{name: "unselected child mount mismatch", request: ExecRequest{RepositoryID: "alpha"}, mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[execCheckoutIndex(t, *workspace, "beta")].Mount = "renamed"
		}},
		{name: "unselected child path mismatch", request: ExecRequest{RepositoryID: "alpha"}, mutate: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[execCheckoutIndex(t, *workspace, "beta")].ResolvedPath = filepath.Join(workspace.RootPath, "alpha", "other")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := execTestWorkspace(t)
			test.mutate(&project, &workspace)
			marker := t.TempDir()
			request := test.request
			request.Program = os.Args[0]
			request.Args = []string{"-test.run=^TestExecMarkerHelper$", marker}
			request.Environment = []string{"PATH=" + os.Getenv("PATH")}
			result, err := NewExecService().Exec(context.Background(), project, workspace, request)
			if test.wantErr {
				var application *Error
				if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Status != AggregateStatusFailed {
					t.Fatalf("selected authority validation = %#v, %v", result, err)
				}
				assertExecMarkerAbsent(t, marker, "alpha", "beta")
				return
			}
			if err != nil || execResultIDs(result) != "alpha" {
				t.Fatalf("unselected corruption blocked selection = %#v, %v", result, err)
			}
			if _, statErr := os.Stat(filepath.Join(marker, "alpha")); statErr != nil {
				t.Fatalf("selected repository did not launch: %v", statErr)
			}
			assertExecMarkerAbsent(t, marker, "beta")
		})
	}
}

func TestExecSelectedCheckoutPathCannotBeSubstitutedWithLinkedWorktree(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	betaIndex := execCheckoutIndex(t, workspace, "beta")
	other := filepath.Join(t.TempDir(), "other-beta")
	betaSource := project.Repositories[0].SourcePath
	testutil.GitRepository{Path: betaSource}.Run(t, "worktree", "add", "-b", "exec-beta-other", other, "HEAD")
	adapter := gitadapter.NewAdapter("git")
	head, err := adapter.Head(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Checkouts[betaIndex].Branch = "exec-beta-other"
	workspace.Checkouts[betaIndex].Head = head
	workspace.Checkouts[betaIndex].ResolvedPath = other
	marker := t.TempDir()
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
		t.Fatalf("substituted linked worktree = %#v, %v", result, err)
	}
	assertExecMarkerAbsent(t, marker, "alpha", "beta")
}

func TestExecSelectedNestedCheckoutUsesPersistedAncestorMount(t *testing.T) {
	project, workspace := execNestedMountWorkspace(t)
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", DryRun: true, RepositoryID: "beta"})
	if err != nil || execResultIDs(result) != "beta" || !sameCheckoutPath(result.Repositories[0].Path, workspace.Checkouts[execCheckoutIndex(t, workspace, "beta")].ResolvedPath) {
		t.Fatalf("nested selected checkout = %#v, %v", result, err)
	}
}

func TestExecSelectedChildUsesDefaultMountForMissingAncestor(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	workspace.Partial = true
	workspace.MissingRepositoryIDs = []string{"alpha"}
	workspace.Checkouts = []domain.Checkout{workspace.Checkouts[execCheckoutIndex(t, workspace, "beta")]}
	marker := t.TempDir()
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
	if err != nil || result.Status != AggregateStatusCompleted || execResultIDs(result) != "beta" {
		t.Fatalf("selected child with missing ancestor = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); statErr != nil {
		t.Fatalf("selected child did not launch: %v", statErr)
	}
	assertExecMarkerAbsent(t, marker, "alpha")
}

func TestExecDefaultDetachedCompatibilityAndScopedRejection(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	betaIndex := execCheckoutIndex(t, workspace, "beta")
	betaPath := workspace.Checkouts[betaIndex].ResolvedPath
	testutil.GitRepository{Path: betaPath}.Run(t, "checkout", "--detach")
	workspace.Checkouts[betaIndex].Detached = true
	workspace.Checkouts[betaIndex].Branch = ""

	defaultMarker := t.TempDir()
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", defaultMarker}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil || result.Status != AggregateStatusCompleted || execResultIDs(result) != "alpha,beta" || result.Repositories[1].Branch != "" {
		t.Fatalf("matching detached default exec = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(defaultMarker, "alpha")); statErr != nil {
		t.Fatalf("default alpha did not launch: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(defaultMarker, "beta")); statErr != nil {
		t.Fatalf("default detached beta did not launch: %v", statErr)
	}

	for _, request := range []ExecRequest{{RepositoryID: "beta"}, {NoCompanions: true}} {
		marker := t.TempDir()
		request.Program = os.Args[0]
		request.Args = []string{"-test.run=^TestExecMarkerHelper$", marker}
		request.Environment = []string{"PATH=" + os.Getenv("PATH")}
		failed, runErr := NewExecService().Exec(context.Background(), project, workspace, request)
		var application *Error
		if runErr == nil || !errors.As(runErr, &application) || application.Kind != ErrorValidation || failed.Status != AggregateStatusFailed {
			t.Fatalf("scoped detached request %#v = %#v, %v", request, failed, runErr)
		}
		assertExecMarkerAbsent(t, marker, "alpha", "beta")
	}
}

func TestExecScopedPathAuthorityExcludesUnselectedSiblings(t *testing.T) {
	project, workspace := execSiblingMountWorkspace(t)
	marker := t.TempDir()
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
	if err != nil || execResultIDs(result) != "beta" || result.Status != AggregateStatusCompleted {
		t.Fatalf("exact sibling selection with unused default collision = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); statErr != nil {
		t.Fatalf("selected beta did not launch: %v", statErr)
	}
	assertExecMarkerAbsent(t, marker, "gamma")

	for index := range project.Repositories {
		if project.Repositories[index].ID == "gamma" {
			project.Repositories[index].Companion = true
		}
	}
	marker = t.TempDir()
	result, err = NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, NoCompanions: true})
	if err != nil || execResultIDs(result) != "beta" || result.Status != AggregateStatusCompleted {
		t.Fatalf("ordinary sibling selection with excluded companion = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); statErr != nil {
		t.Fatalf("ordinary beta did not launch: %v", statErr)
	}
	assertExecMarkerAbsent(t, marker, "gamma")
}

func TestExecScopedPathAuthorityRejectsSelectedCollisionAndBrokenAncestor(t *testing.T) {
	t.Run("selected siblings collide", func(t *testing.T) {
		project, workspace := execSiblingMountWorkspace(t)
		workspace.Checkouts[execCheckoutIndex(t, workspace, "gamma")].Mount = "two"
		workspace.Checkouts[execCheckoutIndex(t, workspace, "gamma")].ResolvedPath = filepath.Join(workspace.RootPath, "two")
		marker := t.TempDir()
		result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, NoCompanions: true})
		var application *Error
		if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Status != AggregateStatusFailed {
			t.Fatalf("selected sibling collision = %#v, %v", result, err)
		}
		assertExecMarkerAbsent(t, marker, "beta", "gamma")
	})
	t.Run("required ancestor mount and path", func(t *testing.T) {
		for _, mutate := range []func(*domain.Workspace){
			func(workspace *domain.Workspace) {
				workspace.Checkouts[execCheckoutIndex(t, *workspace, "alpha")].Mount = "wrong"
			},
			func(workspace *domain.Workspace) {
				workspace.Checkouts[execCheckoutIndex(t, *workspace, "alpha")].ResolvedPath = filepath.Join(workspace.RootPath, "wrong")
			},
		} {
			project, workspace := execNestedMountWorkspace(t)
			mutate(&workspace)
			marker := t.TempDir()
			result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
			var application *Error
			if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
				t.Fatalf("broken required ancestor = %#v, %v", result, err)
			}
			assertExecMarkerAbsent(t, marker, "alpha", "beta")
		}
	})
}

func TestExecSelectionRejectsInvalidScopesBeforeProcessLaunch(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	for _, request := range []ExecRequest{
		{Program: "not-started", NoCompanions: true, RepositoryID: "alpha"},
		{Program: "not-started", RepositoryID: "unknown"},
	} {
		result, err := NewExecService().Exec(context.Background(), project, workspace, request)
		var application *Error
		if err == nil || !errors.As(err, &application) || application.Kind != ErrorInvalidArguments || result.Version != 0 {
			t.Fatalf("invalid selection %#v = %#v, %v", request, result, err)
		}
	}
	workspace.Checkouts = []domain.Checkout{workspace.Checkouts[execCheckoutIndex(t, workspace, "beta")]}
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", RepositoryID: "alpha"})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorInvalidArguments || result.Version != 0 {
		t.Fatalf("absent exact selection = %#v, %v", result, err)
	}
	for index := range project.Repositories {
		project.Repositories[index].Companion = true
	}
	result, err = NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", NoCompanions: true})
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorInvalidArguments || result.Version != 0 {
		t.Fatalf("empty ordinary selection = %#v, %v", result, err)
	}
}

func TestExecScopedSelectionRejectsContradictorySelectedMissingState(t *testing.T) {
	for _, missing := range [][]string{{"beta"}, {"beta", "beta"}} {
		project, workspace := execTestWorkspace(t)
		workspace.MissingRepositoryIDs = missing
		marker := t.TempDir()
		result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
		var application *Error
		if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
			t.Fatalf("selected missing state %v = %#v, %v", missing, result, err)
		}
		assertExecMarkerAbsent(t, marker, "alpha", "beta")
	}
}

func TestExecCompanionAllowsOnlyDescendantObservedHead(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	for index := range project.Repositories {
		if project.Repositories[index].ID == "beta" {
			project.Repositories[index].Companion = true
		}
	}
	betaIndex := execCheckoutIndex(t, workspace, "beta")
	betaPath := workspace.Checkouts[betaIndex].ResolvedPath
	testutil.GitRepository{Path: betaPath}.CommitFile("advanced.txt", "advanced\n", "advance companion")
	observed, err := gitadapter.NewAdapter("git").Head(context.Background(), betaPath)
	if err != nil || observed == workspace.Checkouts[betaIndex].Head {
		t.Fatalf("companion advance = %q, %v", observed, err)
	}
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecProcessHelper$"}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
	if err != nil || execResultIDs(result) != "beta" || result.Repositories[0].Head != observed || !result.Repositories[0].Companion || !strings.Contains(result.Repositories[0].Stdout, "WTREE_COMMIT="+observed) || !strings.Contains(result.Repositories[0].Stdout, "WTREE_REPOSITORY_ID=beta") {
		t.Fatalf("advanced companion execution = %#v, %v", result, err)
	}

	workspace.Checkouts[betaIndex].Head = strings.Repeat("f", 40)
	failed, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", DryRun: true, RepositoryID: "beta"})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || failed.Status != AggregateStatusFailed {
		t.Fatalf("rewritten companion history = %#v, %v", failed, err)
	}
}

func TestExecSelectedCompanionStillRejectsStructuralDriftBeforeLaunch(t *testing.T) {
	for _, mutate := range []struct {
		name  string
		apply func(*domain.Project, *domain.Workspace)
	}{
		{name: "identity", apply: func(project *domain.Project, _ *domain.Workspace) {
			for index := range project.Repositories {
				if project.Repositories[index].ID == "beta" {
					project.Repositories[index].CommonGitDir = filepath.Join(t.TempDir(), "wrong")
				}
			}
		}},
		{name: "branch", apply: func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[execCheckoutIndex(t, *workspace, "beta")].Branch = "other"
		}},
		{name: "detached", apply: func(_ *domain.Project, workspace *domain.Workspace) {
			checkout := &workspace.Checkouts[execCheckoutIndex(t, *workspace, "beta")]
			checkout.Detached, checkout.Branch = true, ""
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			project, workspace := execTestWorkspace(t)
			for index := range project.Repositories {
				if project.Repositories[index].ID == "beta" {
					project.Repositories[index].Companion = true
				}
			}
			mutate.apply(&project, &workspace)
			marker := t.TempDir()
			result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
			var application *Error
			if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
				t.Fatalf("selected companion %s = %#v, %v", mutate.name, result, err)
			}
			if _, statErr := os.Stat(filepath.Join(marker, "beta")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("selected companion started after %s drift: %v", mutate.name, statErr)
			}
		})
	}
}

func TestExecSelectedSubsetGitObservationFailuresAndCancellationNeverLaunch(t *testing.T) {
	for _, observation := range []string{"CommonGitDir", "TopLevel", "CurrentBranch", "Head"} {
		t.Run("error-"+observation, func(t *testing.T) {
			project, workspace := execTestWorkspace(t)
			marker := t.TempDir()
			git := &execObservationErrorGit{Git: gitadapter.NewAdapter("git"), target: observation, cause: errors.New("selected observation failure")}
			result, err := NewExecServiceWith(git).Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
			var application *Error
			if err == nil || !errors.As(err, &application) || application.Kind != ErrorGit || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
				t.Fatalf("selected %s error = %#v, %v", observation, result, err)
			}
			if _, statErr := os.Stat(filepath.Join(marker, "beta")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("process started after selected %s error: %v", observation, statErr)
			}
		})
		t.Run("cancel-"+observation, func(t *testing.T) {
			project, workspace := execTestWorkspace(t)
			ctx := newExecControlledContext()
			git := &execCancelingGit{Git: gitadapter.NewAdapter("git"), target: observation, cancel: func() { ctx.stop(context.Canceled) }}
			marker := t.TempDir()
			result, err := NewExecServiceWith(git).Exec(ctx, project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
			if !errors.Is(err, context.Canceled) || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
				t.Fatalf("selected %s cancellation = %#v, %v", observation, result, err)
			}
			if _, statErr := os.Stat(filepath.Join(marker, "beta")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("process started after selected %s cancellation: %v", observation, statErr)
			}
		})
	}
	project, workspace := execTestWorkspace(t)
	marker := t.TempDir()
	result, err := NewExecServiceWith(execTopLevelMismatchGit{Git: gitadapter.NewAdapter("git")}).Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorValidation || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
		t.Fatalf("selected top-level path mismatch = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("process started after selected top-level path mismatch: %v", statErr)
	}
}

func TestExecSelectedCompanionAncestryObservationFailureNeverLaunches(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	for index := range project.Repositories {
		if project.Repositories[index].ID == "beta" {
			project.Repositories[index].Companion = true
		}
	}
	betaIndex := execCheckoutIndex(t, workspace, "beta")
	testutil.GitRepository{Path: workspace.Checkouts[betaIndex].ResolvedPath}.CommitFile("advanced.txt", "advanced\n", "advance companion")
	marker := t.TempDir()
	result, err := NewExecServiceWith(execAncestorErrorGit{Git: gitadapter.NewAdapter("git"), err: errors.New("ancestry unavailable")}).Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorGit || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
		t.Fatalf("selected companion ancestry error = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("process started after selected ancestry error: %v", statErr)
	}
	ctx := newExecControlledContext()
	marker = t.TempDir()
	result, err = NewExecServiceWith(execAncestorCancelGit{Git: gitadapter.NewAdapter("git"), cancel: func() { ctx.stop(context.Canceled) }}).Exec(ctx, project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
	if !errors.Is(err, context.Canceled) || result.Status != AggregateStatusFailed || execResultIDs(result) != "beta" {
		t.Fatalf("selected companion ancestry cancellation = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("process started after selected ancestry cancellation: %v", statErr)
	}
}

func TestExecSelectedSubsetPreservesCallbackAndCancellationBoundaries(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	marker := t.TempDir()
	writerErr := errors.New("selected output failed")
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta", OnComplete: func(ExecRepositoryResult) error { return writerErr }})
	if !errors.Is(err, writerErr) || execResultIDs(result) != "beta" || result.Repositories[0].Status != AggregateStatusCompleted {
		t.Fatalf("selected callback failure = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "alpha")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unselected repository started after selected callback failure: %v", statErr)
	}

	project, workspace = execTestWorkspace(t)
	started := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct {
		result ExecResult
		err    error
	}, 1)
	go func() {
		value, runErr := NewExecService().Exec(ctx, project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecBlockingHelper$", started}, Environment: []string{"PATH=" + os.Getenv("PATH")}, RepositoryID: "beta"})
		finished <- struct {
			result ExecResult
			err    error
		}{value, runErr}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(started); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selected blocking exec did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	canceled := <-finished
	if !errors.Is(canceled.err, context.Canceled) || execResultIDs(canceled.result) != "beta" || canceled.result.Repositories[0].Status != AggregateStatusCanceled {
		t.Fatalf("selected cancellation = %#v, %v", canceled.result, canceled.err)
	}
}

func TestExecV1EnvelopeKeepsFalseZeroPartialEnvironmentAndOrder(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	request := ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecProcessHelper$"}, Environment: []string{"PATH=" + os.Getenv("PATH"), "WTREE_PROJECT_ID=hostile", "WTREE_UNKNOWN=hostile"}}
	completed, err := NewExecService().Exec(context.Background(), project, workspace, request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if dryRun, exists := wire["dryRun"]; !exists || dryRun != false {
		t.Fatalf("ordinary envelope dryRun = %#v, exists=%t; want explicit false", dryRun, exists)
	}
	repositories, ok := wire["repositories"].([]any)
	if !ok || len(repositories) != 2 {
		t.Fatalf("repositories = %#v", wire["repositories"])
	}
	for _, repository := range repositories {
		entry := repository.(map[string]any)
		if exit, exists := entry["exitCode"]; !exists || exit != float64(0) {
			t.Fatalf("started repository exitCode = %#v, exists=%t", exit, exists)
		}
		if _, exists := entry["environment"]; exists {
			t.Fatalf("ordinary result exposed an environment: %#v", entry)
		}
	}

	request.DryRun, request.Reverse = true, true
	dryRun, err := NewExecService().Exec(context.Background(), project, workspace, request)
	if err != nil || strings.Join(dryRun.ExecutionOrder, ",") != "beta,alpha" || execResultIDs(dryRun) != "alpha,beta" {
		t.Fatalf("reverse dry-run = %#v, %v", dryRun, err)
	}
	for _, repository := range dryRun.Repositories {
		if len(repository.Environment) != 7 || repository.Environment["WTREE_REPOSITORY_ID"] != repository.ID || repository.Environment["WTREE_PATH"] != repository.Path || repository.Environment["WTREE_PROJECT_ID"] != project.ID {
			t.Fatalf("verified dry-run environment = %#v", repository.Environment)
		}
	}

	workspace.Partial = true
	workspace.Checkouts = nil
	workspace.MissingRepositoryIDs = []string{"alpha", "beta"}
	zeroPresent, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "not-started", DryRun: true})
	if err != nil || !zeroPresent.Partial || strings.Join(zeroPresent.MissingRepositoryIDs, ",") != "alpha,beta" || zeroPresent.Repositories == nil || len(zeroPresent.Repositories) != 0 || zeroPresent.ExecutionOrder == nil || len(zeroPresent.ExecutionOrder) != 0 {
		t.Fatalf("zero-present partial envelope = %#v, %v", zeroPresent, err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, err := NewExecService().Exec(canceledContext, project, workspace, ExecRequest{Program: "not-started"})
	if !errors.Is(err, context.Canceled) || canceled.Version != 1 || canceled.Repositories == nil || len(canceled.Repositories) != 0 || canceled.Failure == nil || canceled.Status != AggregateStatusFailed {
		t.Fatalf("zero-present cancellation envelope = %#v, %v", canceled, err)
	}
}

func TestExecJSONApplicabilityAndNonNullEmptyArgs(t *testing.T) {
	program, err := exec.LookPath("true")
	if err != nil {
		t.Skip("a direct zero-output executable is unavailable")
	}
	project, workspace := execTestWorkspace(t)
	completed, runErr := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: program, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if runErr != nil {
		t.Fatal(runErr)
	}
	wire := marshalExecObject(t, completed)
	command := wire["command"].(map[string]any)
	if args, exists := command["args"]; !exists || args == nil || len(args.([]any)) != 0 {
		t.Fatalf("command args = %#v, exists=%t; want non-null empty array", args, exists)
	}
	assertExecJSONKeys(t, wire, []string{"version", "operation", "status", "dryRun", "projectId", "workspace", "command", "executionOrder", "repositories"})
	for _, raw := range wire["repositories"].([]any) {
		entry := raw.(map[string]any)
		want := []string{"id", "mount", "path", "branch", "head", "status", "stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode"}
		if entry["id"] == "beta" {
			want = append(want, "parentId")
		}
		assertExecJSONKeys(t, entry, want)
		if entry["stdout"] != "" || entry["stderr"] != "" || entry["stdoutTruncated"] != false || entry["stderrTruncated"] != false || entry["exitCode"] != float64(0) {
			t.Fatalf("started zero-output applicability = %#v", entry)
		}
	}

	dryRun, runErr := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: program, DryRun: true})
	if runErr != nil {
		t.Fatal(runErr)
	}
	for _, raw := range marshalExecObject(t, dryRun)["repositories"].([]any) {
		entry := raw.(map[string]any)
		for _, key := range []string{"stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode"} {
			if _, exists := entry[key]; exists {
				t.Fatalf("dry-run repository exposed inapplicable %q: %#v", key, entry)
			}
		}
	}

	startFailure, runErr := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: filepath.Join(t.TempDir(), "missing")})
	if runErr == nil {
		t.Fatal("missing executable unexpectedly started")
	}
	entry := marshalExecObject(t, startFailure)["repositories"].([]any)[0].(map[string]any)
	for _, key := range []string{"stdout", "stderr", "stdoutTruncated", "stderrTruncated", "exitCode"} {
		if _, exists := entry[key]; exists {
			t.Fatalf("start failure exposed inapplicable %q: %#v", key, entry)
		}
	}
}

func TestExecCompletionCallbackCannotMutateAuthoritativeResult(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	marker := t.TempDir()
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{
		Program: os.Args[0], Args: []string{"-test.run=^TestExecEarlyFailureHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")},
		OnComplete: func(entry ExecRepositoryResult) error {
			if entry.ExitCode != nil {
				*entry.ExitCode = 99
			}
			if entry.Failure != nil {
				entry.Failure.Code = ErrorInternal
				entry.Failure.Message = "callback mutation"
			}
			entry.Environment = map[string]string{"MUTATED": "yes"}
			return nil
		},
	})
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorConflict || result.Failure == nil || result.Failure.Code != ErrorConflict || !strings.Contains(result.Failure.Message, "status 7") {
		t.Fatalf("callback changed aggregate category/failure: result=%#v err=%v", result, err)
	}
	if result.Repositories[0].ExitCode == nil || *result.Repositories[0].ExitCode != 7 || result.Repositories[0].Failure == nil || result.Repositories[0].Failure.Code != ErrorConflict || result.Repositories[0].Environment != nil {
		t.Fatalf("callback changed stored failed result: %#v", result.Repositories[0])
	}
	if result.Repositories[1].ExitCode == nil || *result.Repositories[1].ExitCode != 0 || result.Repositories[1].Environment != nil {
		t.Fatalf("callback changed stored completed result: %#v", result.Repositories[1])
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); statErr != nil {
		t.Fatalf("later repository did not run after ordinary child failure: %v", statErr)
	}
	originalExit, originalFailure := 3, &AggregateFailure{Code: ErrorConflict, Message: "original"}
	original := ExecRepositoryResult{ExitCode: &originalExit, Failure: originalFailure, Environment: map[string]string{"A": "original"}}
	cloned := cloneExecRepositoryResult(original)
	*cloned.ExitCode = 4
	cloned.Failure.Message = "changed"
	cloned.Environment["A"] = "changed"
	if *original.ExitCode != 3 || original.Failure.Message != "original" || original.Environment["A"] != "original" {
		t.Fatalf("callback clone retained a mutable alias: %#v", original)
	}
}

func TestExecChecksContextImmediatelyAfterEveryGitObservation(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	for _, target := range []string{"CommonGitDir", "TopLevel", "CurrentBranch", "Head"} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry-run=%t", target, dryRun), func(t *testing.T) {
				ctx := newExecControlledContext()
				git := &execCancelingGit{Git: gitadapter.NewAdapter("git"), target: target, cancel: func() { ctx.stop(context.Canceled) }}
				marker := t.TempDir()
				result, err := NewExecServiceWith(git).Exec(ctx, project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, DryRun: dryRun, Environment: []string{"PATH=" + os.Getenv("PATH")}})
				if !errors.Is(err, context.Canceled) || result.Status != AggregateStatusFailed || result.Failure == nil || result.Failure.Code != ErrorInternal {
					t.Fatalf("Exec() after %s cancellation = %#v, %v", target, result, err)
				}
				if got := git.observed(); len(got) == 0 || got[len(got)-1] != target {
					t.Fatalf("Git observations = %v; want final %s", got, target)
				}
				for _, id := range []string{"alpha", "beta"} {
					if _, statErr := os.Stat(filepath.Join(marker, id)); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("%s started after %s cancellation: %v", id, target, statErr)
					}
				}
			})
		}
	}

	t.Run("deadline after final HEAD", func(t *testing.T) {
		ctx := newExecControlledContext()
		git := &execCancelingGit{Git: gitadapter.NewAdapter("git"), target: "Head", cancel: func() { ctx.stop(context.DeadlineExceeded) }}
		result, err := NewExecServiceWith(git).Exec(ctx, project, workspace, ExecRequest{Program: "not-started", DryRun: true})
		if !errors.Is(err, context.DeadlineExceeded) || result.Status != AggregateStatusFailed || result.Failure == nil {
			t.Fatalf("deadline after final HEAD = %#v, %v", result, err)
		}
	})
}

func TestExecPreflightObservationContextErrorPrecedence(t *testing.T) {
	observations := []string{"CommonGitDir", "TopLevel", "CurrentBranch", "Head"}
	for _, observation := range observations {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			for _, dryRun := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/dry-run=%t", observation, cause, dryRun), func(t *testing.T) {
					project, workspace := execTestWorkspace(t)
					ctx := newExecControlledContext()
					git := &execObservationErrorGit{
						Git: gitadapter.NewAdapter("git"), target: observation, cause: cause,
						cancel: func() { ctx.stop(cause) },
					}
					marker := t.TempDir()
					result, err := NewExecServiceWith(git).Exec(ctx, project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, DryRun: dryRun, Environment: []string{"PATH=" + os.Getenv("PATH")}})
					if !errors.Is(err, cause) || result.Status != AggregateStatusFailed || result.Failure == nil || result.Failure.Code != ErrorInternal {
						t.Fatalf("Exec() context observation %s = %#v, %v", observation, result, err)
					}
					if got, want := git.observed(), observations[:observationIndex(observations, observation)+1]; !reflect.DeepEqual(got, want) {
						t.Fatalf("Git observations = %v, want no later observation after %s: %v", got, observation, want)
					}
					for _, id := range []string{"alpha", "beta"} {
						if _, statErr := os.Stat(filepath.Join(marker, id)); !errors.Is(statErr, os.ErrNotExist) {
							t.Fatalf("%s started after context observation %s: %v", id, observation, statErr)
						}
					}
				})
			}
		}
	}
}

func TestExecPreflightOrdinaryObservationErrorsRemainGit(t *testing.T) {
	for _, observation := range []string{"CommonGitDir", "TopLevel", "CurrentBranch", "Head"} {
		t.Run(observation, func(t *testing.T) {
			project, workspace := execTestWorkspace(t)
			marker := t.TempDir()
			git := &execObservationErrorGit{Git: gitadapter.NewAdapter("git"), target: observation, cause: errors.New("ordinary Git observation failure")}
			result, err := NewExecServiceWith(git).Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
			var application *Error
			if err == nil || !errors.As(err, &application) || application.Kind != ErrorGit || result.Failure == nil || result.Failure.Code != ErrorGit {
				t.Fatalf("Exec() ordinary %s error = %#v, %v", observation, result, err)
			}
			for _, id := range []string{"alpha", "beta"} {
				if _, statErr := os.Stat(filepath.Join(marker, id)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("%s started after ordinary Git error: %v", id, statErr)
				}
			}
		})
	}
}

func TestExecBoundsAndRedactsBothStreamsAtTheCommandBoundary(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecLargeSecretHelper$"}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range result.Repositories {
		if !repository.StdoutTruncated || !repository.StderrTruncated || strings.Count(repository.Stdout, directProcessTruncationMarker) != 1 || strings.Count(repository.Stderr, directProcessTruncationMarker) != 1 || strings.Contains(repository.Stdout, "exec-crossing-secret") || strings.Contains(repository.Stderr, "exec-crossing-secret") || !strings.Contains(repository.Stdout, "[REDACTED]") || !strings.Contains(repository.Stderr, "[REDACTED]") {
			t.Fatalf("bounded/redacted exec result = %#v", repository)
		}
	}
}

func TestExecOutputFailureStopsLaterInvocation(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	marker := t.TempDir()
	writerErr := errors.New("human output failed")
	result, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{
		Program: os.Args[0], Args: []string{"-test.run=^TestExecMarkerHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")},
		OnComplete: func(repository ExecRepositoryResult) error {
			if repository.ID == "alpha" {
				return writerErr
			}
			return nil
		},
	})
	if !errors.Is(err, writerErr) || result.Repositories[0].Status != AggregateStatusCompleted || result.Repositories[1].Status != AggregateStatusCanceled {
		t.Fatalf("output failure result = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(marker, "beta")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("later repository started after output failure: %v", statErr)
	}
}

func TestExecPartialWorkspaceAndCancellationDoNotStartLaterRepositories(t *testing.T) {
	project, workspace := execTestWorkspace(t)
	workspace.Partial = true
	workspace.MissingRepositoryIDs = []string{"beta"}
	workspace.Checkouts = workspace.Checkouts[1:]
	dryRun, err := NewExecService().Exec(context.Background(), project, workspace, ExecRequest{Program: "does-not-exist", DryRun: true})
	if err != nil || execResultIDs(dryRun) != "alpha" {
		t.Fatalf("partial dry-run = %#v, %v", dryRun, err)
	}

	project, workspace = execTestWorkspace(t)
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan struct {
		result ExecResult
		err    error
	}, 1)
	go func() {
		result, err := NewExecService().Exec(ctx, project, workspace, ExecRequest{Program: os.Args[0], Args: []string{"-test.run=^TestExecBlockingHelper$", marker}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
		resultCh <- struct {
			result ExecResult
			err    error
		}{result, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("blocking exec did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	completed := <-resultCh
	if completed.err == nil || len(completed.result.Repositories) != 2 || completed.result.Repositories[0].Status != "canceled" || completed.result.Repositories[1].Status != "canceled" {
		t.Fatalf("canceled Exec() = %#v, %v", completed.result, completed.err)
	}
}

func TestExecProcessHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestExecProcessHelper") {
		return
	}
	fmt.Printf("cwd=%s args=%s WTREE_PROJECT_ID=%s WTREE_WORKSPACE=%s WTREE_REPOSITORY_ID=%s WTREE_MOUNT=%s WTREE_PATH=%s WTREE_BRANCH=%s WTREE_COMMIT=%s\n", mustExecGetwd(), strings.Join(os.Args, "|"), os.Getenv("WTREE_PROJECT_ID"), os.Getenv("WTREE_WORKSPACE"), os.Getenv("WTREE_REPOSITORY_ID"), os.Getenv("WTREE_MOUNT"), os.Getenv("WTREE_PATH"), os.Getenv("WTREE_BRANCH"), os.Getenv("WTREE_COMMIT"))
	os.Exit(0)
}

func TestExecFailureHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestExecFailureHelper") {
		return
	}
	if os.Getenv("WTREE_REPOSITORY_ID") == "beta" {
		fmt.Fprintln(os.Stderr, "beta failure")
		os.Exit(7)
	}
	os.Exit(0)
}

func TestExecBlockingHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestExecBlockingHelper") {
		return
	}
	if len(os.Args) > 1 {
		_ = os.WriteFile(os.Args[len(os.Args)-1], []byte("started"), 0o600)
	}
	time.Sleep(10 * time.Second)
	os.Exit(0)
}

func TestExecLargeSecretHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestExecLargeSecretHelper") {
		return
	}
	// The sensitive value crosses the final 32 KiB head-retention boundary.
	// Exec therefore proves the command-level result receives M00's complete
	// redacted reconstruction before the bounded projection is rendered.
	value := strings.Repeat("x", directProcessRetainedStreamBytes-45) + "https://example.invalid/path?token=exec-crossing-secret&safe=visible " + strings.Repeat("y", directProcessRetainedStreamBytes+100)
	fmt.Fprint(os.Stdout, value)
	fmt.Fprint(os.Stderr, value)
	os.Exit(0)
}

func TestExecMarkerHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestExecMarkerHelper") {
		return
	}
	if err := os.WriteFile(filepath.Join(os.Args[len(os.Args)-1], os.Getenv("WTREE_REPOSITORY_ID")), []byte("started"), 0o600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestExecEarlyFailureHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestExecEarlyFailureHelper") {
		return
	}
	id := os.Getenv("WTREE_REPOSITORY_ID")
	if id == "alpha" {
		fmt.Fprintln(os.Stderr, "alpha failure")
		os.Exit(7)
	}
	if err := os.WriteFile(filepath.Join(os.Args[len(os.Args)-1], id), []byte("started"), 0o600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type execTopLevelMismatchGit struct{ gitadapter.Git }

func (git execTopLevelMismatchGit) TopLevel(_ context.Context, path string) (string, error) {
	return filepath.Join(path, "not-the-root"), nil
}

type execAncestorErrorGit struct {
	gitadapter.Git
	err error
}

func (git execAncestorErrorGit) IsAncestor(context.Context, string, string, string) (bool, error) {
	return false, git.err
}

type execAncestorCancelGit struct {
	gitadapter.Git
	cancel func()
}

func (git execAncestorCancelGit) IsAncestor(context.Context, string, string, string) (bool, error) {
	git.cancel()
	return false, context.Canceled
}

func mustExecGetwd() string {
	path, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return path
}

func execResultIDs(result ExecResult) string {
	ids := make([]string, 0, len(result.Repositories))
	for _, repository := range result.Repositories {
		ids = append(ids, repository.ID)
	}
	return strings.Join(ids, ",")
}

func marshalExecObject(t *testing.T, value ExecResult) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertExecJSONKeys(t *testing.T, value map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %v, want %v in %#v", got, want, value)
	}
}

type execControlledContext struct {
	context.Context
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newExecControlledContext() *execControlledContext {
	return &execControlledContext{Context: context.Background(), done: make(chan struct{})}
}

func (ctx *execControlledContext) Done() <-chan struct{} { return ctx.done }
func (ctx *execControlledContext) Err() error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	return ctx.err
}
func (ctx *execControlledContext) stop(err error) {
	ctx.mu.Lock()
	if ctx.err == nil {
		ctx.err = err
		close(ctx.done)
	}
	ctx.mu.Unlock()
}

type execCancelingGit struct {
	gitadapter.Git
	target string
	cancel func()
	mu     sync.Mutex
	calls  []string
}

// execObservationErrorGit models an adapter which returns a wrapped context
// result from the observation itself. It deliberately does not rely on a nil
// result followed by a separate cancellation.
type execObservationErrorGit struct {
	gitadapter.Git
	target string
	cause  error
	cancel func()
	mu     sync.Mutex
	calls  []string
}

func (git *execObservationErrorGit) observe(name string) error {
	git.mu.Lock()
	git.calls = append(git.calls, name)
	git.mu.Unlock()
	if name != git.target {
		return nil
	}
	if git.cancel != nil {
		git.cancel()
	}
	return fmt.Errorf("adapter %s observation: %w", name, git.cause)
}

func (git *execObservationErrorGit) observed() []string {
	git.mu.Lock()
	defer git.mu.Unlock()
	return append([]string(nil), git.calls...)
}

func (git *execObservationErrorGit) CommonGitDir(_ context.Context, path string) (string, error) {
	if err := git.observe("CommonGitDir"); err != nil {
		return "", err
	}
	return git.Git.CommonGitDir(context.Background(), path)
}

func (git *execObservationErrorGit) TopLevel(_ context.Context, path string) (string, error) {
	if err := git.observe("TopLevel"); err != nil {
		return "", err
	}
	return git.Git.TopLevel(context.Background(), path)
}

func (git *execObservationErrorGit) CurrentBranch(_ context.Context, path string) (string, bool, error) {
	if err := git.observe("CurrentBranch"); err != nil {
		return "", false, err
	}
	return git.Git.CurrentBranch(context.Background(), path)
}

func (git *execObservationErrorGit) Head(_ context.Context, path string) (string, error) {
	if err := git.observe("Head"); err != nil {
		return "", err
	}
	return git.Git.Head(context.Background(), path)
}

func observationIndex(observations []string, target string) int {
	for index, observation := range observations {
		if observation == target {
			return index
		}
	}
	panic("unknown observation")
}

func (git *execCancelingGit) record(name string, err error) {
	git.mu.Lock()
	git.calls = append(git.calls, name)
	git.mu.Unlock()
	if err == nil && name == git.target {
		git.cancel()
	}
}

func (git *execCancelingGit) observed() []string {
	git.mu.Lock()
	defer git.mu.Unlock()
	return append([]string(nil), git.calls...)
}

func (git *execCancelingGit) CommonGitDir(_ context.Context, path string) (string, error) {
	value, err := git.Git.CommonGitDir(context.Background(), path)
	git.record("CommonGitDir", err)
	return value, err
}
func (git *execCancelingGit) TopLevel(_ context.Context, path string) (string, error) {
	value, err := git.Git.TopLevel(context.Background(), path)
	git.record("TopLevel", err)
	return value, err
}
func (git *execCancelingGit) CurrentBranch(_ context.Context, path string) (string, bool, error) {
	value, detached, err := git.Git.CurrentBranch(context.Background(), path)
	git.record("CurrentBranch", err)
	return value, detached, err
}
func (git *execCancelingGit) Head(_ context.Context, path string) (string, error) {
	value, err := git.Git.Head(context.Background(), path)
	git.record("Head", err)
	return value, err
}

func execTestWorkspace(t *testing.T) (domain.Project, domain.Workspace) {
	t.Helper()
	alpha, beta := testutil.NewGitRepository(t), testutil.NewGitRepository(t)
	alpha.CommitFile("alpha.txt", "alpha\n", "alpha")
	beta.CommitFile("beta.txt", "beta\n", "beta")
	root := t.TempDir()
	alphaPath, betaPath := filepath.Join(root, "alpha"), filepath.Join(root, "alpha", "beta")
	alpha.Run(t, "worktree", "add", "-b", "exec-alpha", alphaPath, "HEAD")
	beta.Run(t, "worktree", "add", "-b", "exec-beta", betaPath, "HEAD")
	adapter := gitadapter.NewAdapter("git")
	alphaCommon, err := adapter.CommonGitDir(context.Background(), alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	betaCommon, err := adapter.CommonGitDir(context.Background(), betaPath)
	if err != nil {
		t.Fatal(err)
	}
	alphaHead, err := adapter.Head(context.Background(), alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	betaHead, err := adapter.Head(context.Background(), betaPath)
	if err != nil {
		t.Fatal(err)
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: "exec-project", Name: "exec project", LogicalRoot: root, BaseRepository: "alpha", Repositories: []domain.Repository{
		{ID: "beta", ParentID: "alpha", CommonGitDir: betaCommon, SourcePath: beta.Path, DefaultMount: "beta", DefaultBranch: "main"},
		{ID: "alpha", CommonGitDir: alphaCommon, SourcePath: alpha.Path, DefaultMount: "alpha", DefaultBranch: "main"},
	}}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "exec-workspace", Name: "exec", RootPath: root, Checkouts: []domain.Checkout{
		{RepositoryID: "beta", Branch: "exec-beta", Head: betaHead, Mount: "beta", ResolvedPath: betaPath},
		{RepositoryID: "alpha", Branch: "exec-alpha", Head: alphaHead, Mount: "alpha", ResolvedPath: alphaPath},
	}}
	return project, workspace
}

func execNestedMountWorkspace(t *testing.T) (domain.Project, domain.Workspace) {
	t.Helper()
	alpha, beta := testutil.NewGitRepository(t), testutil.NewGitRepository(t)
	alpha.CommitFile("alpha.txt", "alpha\n", "alpha")
	beta.CommitFile("beta.txt", "beta\n", "beta")
	root := t.TempDir()
	alphaPath := filepath.Join(root, "groups", "alpha")
	betaPath := filepath.Join(alphaPath, "modules", "beta")
	alpha.Run(t, "worktree", "add", "-b", "nested-alpha", alphaPath, "HEAD")
	beta.Run(t, "worktree", "add", "-b", "nested-beta", betaPath, "HEAD")
	adapter := gitadapter.NewAdapter("git")
	alphaCommon, err := adapter.CommonGitDir(context.Background(), alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	betaCommon, err := adapter.CommonGitDir(context.Background(), betaPath)
	if err != nil {
		t.Fatal(err)
	}
	alphaHead, err := adapter.Head(context.Background(), alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	betaHead, err := adapter.Head(context.Background(), betaPath)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Project{
			Version: domain.CurrentVersion, ID: "nested-exec-project", Name: "nested exec project", LogicalRoot: root, BaseRepository: "alpha",
			Repositories: []domain.Repository{
				{ID: "beta", ParentID: "alpha", CommonGitDir: betaCommon, SourcePath: beta.Path, DefaultMount: "modules/beta", DefaultBranch: "main"},
				{ID: "alpha", CommonGitDir: alphaCommon, SourcePath: alpha.Path, DefaultMount: "groups/alpha", DefaultBranch: "main"},
			},
		}, domain.Workspace{
			Version: domain.CurrentVersion, ID: "nested-exec-workspace", Name: "nested-exec", RootPath: root,
			Checkouts: []domain.Checkout{
				{RepositoryID: "beta", Branch: "nested-beta", Head: betaHead, Mount: "modules/beta", ResolvedPath: betaPath},
				{RepositoryID: "alpha", Branch: "nested-alpha", Head: alphaHead, Mount: "groups/alpha", ResolvedPath: alphaPath},
			},
		}
}

// execSiblingMountWorkspace deliberately differs from the configured defaults:
// beta occupies "two" while gamma occupies "three". This is a valid complete
// workspace, but resolving beta with gamma's unused default would collide at
// "two" and incorrectly prevent an exact beta exec.
func execSiblingMountWorkspace(t *testing.T) (domain.Project, domain.Workspace) {
	t.Helper()
	beta, gamma := testutil.NewGitRepository(t), testutil.NewGitRepository(t)
	beta.CommitFile("beta.txt", "beta\n", "beta")
	gamma.CommitFile("gamma.txt", "gamma\n", "gamma")
	root := t.TempDir()
	betaPath, gammaPath := filepath.Join(root, "two"), filepath.Join(root, "three")
	beta.Run(t, "worktree", "add", "-b", "sibling-beta", betaPath, "HEAD")
	gamma.Run(t, "worktree", "add", "-b", "sibling-gamma", gammaPath, "HEAD")
	adapter := gitadapter.NewAdapter("git")
	betaCommon, err := adapter.CommonGitDir(context.Background(), betaPath)
	if err != nil {
		t.Fatal(err)
	}
	gammaCommon, err := adapter.CommonGitDir(context.Background(), gammaPath)
	if err != nil {
		t.Fatal(err)
	}
	betaHead, err := adapter.Head(context.Background(), betaPath)
	if err != nil {
		t.Fatal(err)
	}
	gammaHead, err := adapter.Head(context.Background(), gammaPath)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Project{
			Version: domain.CurrentVersion, ID: "sibling-exec-project", Name: "sibling exec project", LogicalRoot: root, BaseRepository: "beta",
			Repositories: []domain.Repository{
				{ID: "beta", CommonGitDir: betaCommon, SourcePath: beta.Path, DefaultMount: "one", DefaultBranch: "main"},
				{ID: "gamma", CommonGitDir: gammaCommon, SourcePath: gamma.Path, DefaultMount: "two", DefaultBranch: "main"},
			},
		}, domain.Workspace{
			Version: domain.CurrentVersion, ID: "sibling-exec-workspace", Name: "sibling-exec", RootPath: root,
			Checkouts: []domain.Checkout{
				{RepositoryID: "beta", Branch: "sibling-beta", Head: betaHead, Mount: "two", ResolvedPath: betaPath},
				{RepositoryID: "gamma", Branch: "sibling-gamma", Head: gammaHead, Mount: "three", ResolvedPath: gammaPath},
			},
		}
}

func assertExecMarkerAbsent(t *testing.T, marker string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := os.Stat(filepath.Join(marker, id)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s started despite preflight failure: %v", id, err)
		}
	}
}

func execCheckoutIndex(t *testing.T, workspace domain.Workspace, id string) int {
	t.Helper()
	for index, checkout := range workspace.Checkouts {
		if checkout.RepositoryID == id {
			return index
		}
	}
	t.Fatalf("workspace has no %q checkout: %#v", id, workspace)
	return -1
}
