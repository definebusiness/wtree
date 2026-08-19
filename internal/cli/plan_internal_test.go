package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
)

func TestRenderCleanRollbackDiagnosticUsesStderrOnlyForHumanOutput(t *testing.T) {
	err := service.NewError(service.ErrorGit, service.NewCleanRollbackError(errors.New("add worktree failed")))
	var human, json bytes.Buffer
	if renderErr := renderCleanRollbackDiagnostic(&human, false, err); renderErr != nil {
		t.Fatal(renderErr)
	}
	if got, want := human.String(), "Rollback complete.\n"; got != want {
		t.Fatalf("human diagnostic = %q, want %q", got, want)
	}
	if renderErr := renderCleanRollbackDiagnostic(&json, true, err); renderErr != nil {
		t.Fatal(renderErr)
	}
	if json.Len() != 0 {
		t.Fatalf("JSON diagnostic = %q, want empty", json.String())
	}
}

func TestRenderCreateFailureDiagnosticReportsExactInternalEvidence(t *testing.T) {
	result := service.CreateResult{
		RetainedIgnoreFiles: []service.IgnoreFileUpdate{{Path: "/worktrees/retained/.gitignore"}},
		RemovedIgnoreFiles:  []service.IgnoreFileUpdate{{Path: "/worktrees/removed/.gitignore"}},
		UnverifiedMounts: []service.UnverifiedMount{{
			ParentPath: "/worktrees/parent", ChildPath: "/worktrees/parent/backend", Mount: "backend",
		}},
	}
	var human, json bytes.Buffer
	if err := renderCreateFailureDiagnostic(&human, false, result); err != nil {
		t.Fatal(err)
	}
	want := "Retained changed .gitignore files:\n" +
		"  /worktrees/retained/.gitignore\n" +
		"Removed .gitignore files with clean rollback:\n" +
		"  /worktrees/removed/.gitignore\n" +
		"Unverified mounts; child worktrees were not added:\n" +
		"  /worktrees/parent -> /worktrees/parent/backend (backend)\n"
	if human.String() != want {
		t.Fatalf("human diagnostic = %q, want %q", human.String(), want)
	}
	if err := renderCreateFailureDiagnostic(&json, true, result); err != nil {
		t.Fatal(err)
	}
	if json.Len() != 0 {
		t.Fatalf("JSON diagnostic = %q, want empty", json.String())
	}
}

func TestRenderCreateFailureDiagnosticReportsOnlyRemainingUnverifiedChild(t *testing.T) {
	result := service.CreateResult{UnverifiedMounts: []service.UnverifiedMount{{
		ParentPath: "/worktrees/parent", ChildPath: "/worktrees/parent/beta", Mount: "beta",
	}}}
	var output bytes.Buffer
	if err := renderCreateFailureDiagnostic(&output, false, result); err != nil {
		t.Fatal(err)
	}
	want := "Unverified mounts; child worktrees were not added:\n  /worktrees/parent -> /worktrees/parent/beta (beta)\n"
	if output.String() != want {
		t.Fatalf("human diagnostic = %q, want %q", output.String(), want)
	}
}

func TestRenderWorkspacePlanAlignsEveryColumn(t *testing.T) {
	value := plan.WorkspacePlan{
		Operation:     plan.Create,
		WorkspaceName: "feature/customer-search",
		RootPath:      "/worktrees/feature-customer-search",
		Repositories: []plan.RepositoryPlan{
			{ID: "root", Base: "4516c867", Branch: "feature/customer-search", Mount: ".", Path: "/worktrees/feature-customer-search"},
			{ID: "backend", ParentID: "root", Base: "8b7c9ba0", Branch: "feature/customer-search", Mount: "backend", Path: "/worktrees/feature-customer-search/backend"},
		},
	}
	var output bytes.Buffer
	if err := renderWorkspacePlan(&output, value); err != nil {
		t.Fatal(err)
	}
	want := "Operation: create\n" +
		"Workspace: feature/customer-search\n" +
		"Target: /worktrees/feature-customer-search\n\n" +
		"REPOSITORY  BASE      BRANCH                   MOUNT    PATH\n" +
		"root        4516c867  feature/customer-search  .        /worktrees/feature-customer-search\n" +
		"backend     8b7c9ba0  feature/customer-search  backend  /worktrees/feature-customer-search/backend\n\n" +
		"Automatic ignore protection (execution will ensure):\n" +
		"  /worktrees/feature-customer-search/.gitignore\n" +
		"    /backend/\n\n" +
		"No changes made. Dry run performs no mutation.\n"
	if output.String() != want {
		t.Fatalf("renderWorkspacePlan() = %q, want %q", output.String(), want)
	}
}
