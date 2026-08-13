package cli

import (
	"bytes"
	"testing"

	"github.com/marcel/wtree/internal/service"
)

func TestRenderImportPlanAlignsEveryColumn(t *testing.T) {
	value := service.ImportPlan{
		WorkspaceName: "manual/import-demo",
		Repositories: []service.ImportRepository{
			{ID: "root", Mount: ".", Branch: "manual/import-demo"},
			{ID: "backend", Mount: "server", Branch: "manual/import-demo"},
		},
	}
	var output bytes.Buffer
	if err := renderImportPlan(&output, value, true); err != nil {
		t.Fatal(err)
	}
	want := "Operation: import\n" +
		"Workspace: manual/import-demo\n" +
		"REPOSITORY  MOUNT   BRANCH\n" +
		"root        .       manual/import-demo\n" +
		"backend     server  manual/import-demo\n" +
		"No changes made.\n"
	if output.String() != want {
		t.Fatalf("renderImportPlan() = %q, want %q", output.String(), want)
	}
}
