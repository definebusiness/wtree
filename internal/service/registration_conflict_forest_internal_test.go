package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/testutil"
)

func TestInitializerRejectsHealthyRegisteredLogicalRootWithoutSharedGitIdentity(t *testing.T) {
	parallelM07RealGitTest(t)
	project, data := forestWorkspaceProject(t)
	registryPath := filepath.Join(data, "registry.json")
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	statePath := WorkspaceStatePath(data, project.ID, "default")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := testutil.NewPushedGitRepository(t)
	unrelated.CommitFile("unrelated.txt", "unrelated\n", "unrelated")
	other := filepath.Join(project.LogicalRoot, "other")
	if err := os.Rename(unrelated.Path, other); err != nil {
		t.Fatal(err)
	}
	result, err := NewInitializer().Init(context.Background(), InitRequest{Path: project.LogicalRoot, DataDir: data, BaseRepository: "other", Ignores: []string{"api", "web"}})
	if err == nil || result.ProjectID != "" || !strings.Contains(err.Error(), project.ID) {
		t.Fatalf("logical-root conflict result=%#v error=%v", result, err)
	}
	registryAfter, readErr := os.ReadFile(registryPath)
	if readErr != nil || !bytes.Equal(registryBefore, registryAfter) {
		t.Fatalf("logical-root conflict changed registry: before=%q after=%q err=%v", registryBefore, registryAfter, readErr)
	}
	stateAfter, readErr := os.ReadFile(statePath)
	if readErr != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("logical-root conflict changed state: before=%q after=%q err=%v", stateBefore, stateAfter, readErr)
	}
	for _, path := range []string{filepath.Join(other, ".wtree.yml"), filepath.Join(other, "project.wtree.yml")} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("logical-root conflict published %q: %v", path, statErr)
		}
	}
}
