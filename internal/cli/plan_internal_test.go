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

func TestRenderWorkspacePlanAlignsEveryColumn(t *testing.T) {
	value := plan.WorkspacePlan{
		Operation:     plan.Create,
		WorkspaceName: "feature/customer-search",
		RootPath:      "/worktrees/feature-customer-search",
		Repositories: []plan.RepositoryPlan{
			{ID: "root", Base: "4516c867", Branch: "feature/customer-search", Mount: ".", Path: "/worktrees/feature-customer-search"},
			{ID: "backend", Base: "8b7c9ba0", Branch: "feature/customer-search", Mount: "backend", Path: "/worktrees/feature-customer-search/backend"},
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
		"No changes made.\n"
	if output.String() != want {
		t.Fatalf("renderWorkspacePlan() = %q, want %q", output.String(), want)
	}
}
