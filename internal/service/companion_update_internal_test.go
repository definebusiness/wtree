package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
)

// The plan inventory must decode the immutable generation captured by Plan,
// not reopen a mutable pathname after that snapshot. This models the ABA
// window directly: disk contains valid-looking replacement bytes while the
// retained snapshot is malformed, and only the retained generation may drive
// the result.
func TestCompanionUpdateInventoryUsesCapturedStateGeneration(t *testing.T) {
	dataDir := t.TempDir()
	project := domain.Project{ID: "project"}
	generation := map[string][]byte{
		"state.json": append([]byte("-rw-------\x00"), []byte("{malformed")...),
	}
	states := companionUpdateInventory(dataDir, project, "companion", generation)
	if len(states) != 1 || states[0].valid || states[0].name != "state.json" || states[0].statePath != filepath.Join(WorkspaceStateDirectory(dataDir, project.ID), "state.json") {
		t.Fatalf("captured inventory = %#v", states)
	}
}

// A cancellation that races immediately before a workspace observation cannot
// turn an already-captured invalid state record into an unobserved plan entry.
func TestCompanionUpdateDryRunWorkspaceCanceledRetainsInvalidState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entry := (&CompanionUpdateService{}).dryRunWorkspace(ctx, CompanionUpdatePlan{}, companionUpdateState{name: "broken.json", valid: false})
	if entry.Kind != "workspace" || entry.Workspace != "broken.json" || entry.Action != "none" || entry.Status != "failed" || entry.Deferred || entry.Reason != "invalid-state" {
		t.Fatalf("canceled invalid dry-run entry = %#v", entry)
	}
}

func TestCompanionUpdateFailedResultAlwaysCarriesStructuredFailure(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		want         ErrorKind
	}{
		{"dirty workspace", "dirty", ErrorDirtyWorkspace},
		{"fetch", "fetch-failed", ErrorGit},
		{"rollback", "rollback-incomplete", ErrorRollbackIncomplete},
		{"invalid state", "invalid-state", ErrorValidation},
		{"diverged", "diverged", ErrorConflict},
		{"state publication", "state-publication-failed", ErrorInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := companionUpdateResultWithFailure(CompanionUpdateResult{Status: "failed", Entries: []CompanionUpdateEntry{{Kind: "workspace", Workspace: "default", Status: "failed", Reason: test.reason}}})
			if result.Failure == nil || result.Failure.Code != string(test.want) || result.Failure.Message == "" {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}
