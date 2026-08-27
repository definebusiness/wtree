package cli

import (
	"bytes"
	"testing"

	"github.com/definebusiness/wtree/internal/service"
)

func TestRenderUpdateSuccessIncludesMountStatusAndRetainedOutcome(t *testing.T) {
	for _, repository := range []service.UpdateRepositoryResult{
		{ID: "unchanged", Classification: service.UpdateClassificationUnchanged, Action: "unchanged", Status: "completed", Mount: ".", Path: "/tree", Branch: "main", ActualHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{ID: "fast-forward", Classification: service.UpdateClassificationFastForwardable, Action: "fast-forward", Status: "completed", Mount: "api", Path: "/tree/api", Branch: "main", ActualHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{ID: "added", Classification: service.UpdateClassificationAdded, Action: "add", Status: "completed", Mount: "web", Path: "/tree/web", Branch: "main", ActualHead: "cccccccccccccccccccccccccccccccccccccccc"},
		{ID: "removed", Classification: service.UpdateClassificationRemovedRetained, Action: "retain-unmanaged", Status: "completed", RollbackStatus: "retained-unmanaged", Mount: "old", Path: "/tree/old", Branch: "main", ActualHead: "dddddddddddddddddddddddddddddddddddddddd"},
	} {
		repository := repository
		t.Run(repository.ID, func(t *testing.T) {
			var output bytes.Buffer
			if err := renderUpdateSuccess(&output, service.UpdatePublicationResult{Repositories: []service.UpdateRepositoryResult{repository}}); err != nil {
				t.Fatal(err)
			}
			want := "Update complete:\n  " + repository.ID + ": " + string(repository.Classification) + " (" + repository.Action + ") status=completed mount=" + repository.Mount + " path=" + repository.Path + " branch=main head=" + repository.ActualHead
			if repository.RollbackStatus != "" {
				want += " rollback=" + repository.RollbackStatus
			}
			want += "\n"
			if output.String() != want {
				t.Fatalf("human output=%q, want %q", output.String(), want)
			}
		})
	}
}
