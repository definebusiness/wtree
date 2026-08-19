package domain_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
)

func TestEffectivePathsRelocatesDescendantsWhenParentMountChanges(t *testing.T) {
	project := testProject()
	workspaceRoot := filepath.Join(t.TempDir(), "feature-login")

	paths, err := project.EffectivePaths(workspaceRoot, map[string]string{
		"backend": "services/api",
		"shared":  "common",
	})
	if err != nil {
		t.Fatalf("EffectivePaths() error = %v", err)
	}
	if got, want := paths["shared"], filepath.Join(workspaceRoot, "services", "api", "common"); got != want {
		t.Errorf("shared path = %q, want %q", got, want)
	}
}

func TestEffectivePathsUsesNormalizedMountForPlacement(t *testing.T) {
	project := testProject()
	workspaceRoot := filepath.Join(t.TempDir(), "feature-login")

	paths, err := project.EffectivePaths(workspaceRoot, map[string]string{"backend": `services\..\api`})
	if err != nil {
		t.Fatalf("EffectivePaths() error = %v", err)
	}
	if got, want := paths["backend"], filepath.Join(workspaceRoot, "api"); got != want {
		t.Fatalf("backend path = %q, want %q", got, want)
	}
}

func TestEffectivePathsRejectsExistingIntermediateSymlinkEscapeForPlannedLeaf(t *testing.T) {
	project := testProject()
	workspaceRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspaceRoot, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := project.EffectivePaths(workspaceRoot, map[string]string{"backend": "escape/planned-leaf"})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("EffectivePaths() error = %v, want physical containment rejection", err)
	}
}

func TestEffectivePathsRejectsSiblingMountCollisions(t *testing.T) {
	project := testProject()
	project.Repositories = append(project.Repositories, domain.Repository{ID: "frontend", ParentID: "root", DefaultMount: "api"})

	_, err := project.EffectivePaths(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("EffectivePaths() error = %v, want sibling collision error", err)
	}
}
