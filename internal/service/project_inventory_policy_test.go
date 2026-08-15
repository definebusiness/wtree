package service

import "testing"

func TestInventoryPrunePolicyDoesNotAuthorizeFromFindingText(t *testing.T) {
	entry := ProjectInventoryEntry{
		Findings: []ProjectInventoryFinding{
			{Code: "config-id-mismatch", Severity: "error", Message: "changed human wording"},
			{Code: "duplicate-config-path", Severity: "warning", Message: "also changed human wording"},
		},
		prunePolicy: inventoryPrunePolicy{staleEvidence: true, ambiguousDuplicatePath: true},
	}
	finalizeInventoryEntry(&entry)
	if entry.Prunable {
		t.Fatal("ambiguous structured policy authorized prune")
	}

	entry.prunePolicy = inventoryPrunePolicy{supersededDuplicate: true}
	entry.Findings = []ProjectInventoryFinding{{Code: "duplicate-config-path", Severity: "warning", Message: "wording deliberately unrelated"}}
	finalizeInventoryEntry(&entry)
	if !entry.Prunable {
		t.Fatal("superseded structured policy did not authorize prune")
	}
	if reasons := pruneReasons(entry); len(reasons) != 1 || reasons[0] != "duplicate-config-path" {
		t.Fatalf("stable reasons=%v", reasons)
	}
}
