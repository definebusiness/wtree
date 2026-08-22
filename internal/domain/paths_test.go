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

	_, err := project.EffectivePaths(workspaceRoot, map[string]string{"backend": `services\..\api`})
	if err == nil || !(strings.Contains(err.Error(), "clean") || strings.Contains(err.Error(), "relative")) {
		t.Fatalf("EffectivePaths() error = %v, want canonical alias rejection", err)
	}
}

func TestEffectivePathsResolvesGroupedTopLevelAndChildren(t *testing.T) {
	project := domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "backend", Repositories: []domain.Repository{
		{ID: "backend", DefaultMount: "services/backend"},
		{ID: "frontend", DefaultMount: "services/frontend"},
		{ID: "api", ParentID: "backend", DefaultMount: "apps/api"},
	}}
	root := t.TempDir()
	paths, err := project.EffectivePaths(root, nil)
	if err != nil {
		t.Fatalf("EffectivePaths() error = %v", err)
	}
	if got, want := paths["backend"], filepath.Join(root, "services", "backend"); got != want {
		t.Errorf("backend path = %q, want %q", got, want)
	}
	if got, want := paths["api"], filepath.Join(root, "services", "backend", "apps", "api"); got != want {
		t.Errorf("api path = %q, want %q", got, want)
	}
}

func TestEffectivePathsResolvesSingleComponentTopLevelSiblings(t *testing.T) {
	project := domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "api", Repositories: []domain.Repository{
		{ID: "api", DefaultMount: "api"},
		{ID: "web", DefaultMount: "web"},
	}}
	root := t.TempDir()
	paths, err := project.EffectivePaths(root, nil)
	if err != nil {
		t.Fatalf("EffectivePaths() error = %v", err)
	}
	for id, want := range map[string]string{"api": filepath.Join(root, "api"), "web": filepath.Join(root, "web")} {
		if got := paths[id]; got != want {
			t.Errorf("%s path = %q, want %q", id, got, want)
		}
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

func TestEffectivePathsRejectsChildSymlinkOutsideImmediateParent(t *testing.T) {
	workspaceRoot := t.TempDir()
	basePath := filepath.Join(workspaceRoot, "services", "base")
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspaceRoot, "grouping"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspaceRoot, "grouping"), filepath.Join(basePath, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "base", Repositories: []domain.Repository{
		{ID: "base", DefaultMount: "services/base"},
		{ID: "child", ParentID: "base", DefaultMount: "link/child"},
	}}
	if _, err := project.EffectivePaths(workspaceRoot, nil); err == nil {
		t.Fatal("EffectivePaths() error = nil, want immediate-parent containment rejection")
	}
}

func TestEffectivePathsRejectsSiblingMountCollisions(t *testing.T) {
	project := testProject()
	project.Repositories = append(project.Repositories, domain.Repository{ID: "frontend", ParentID: "root", DefaultMount: "api"})

	_, err := project.EffectivePaths(t.TempDir(), nil)
	if err == nil || !(strings.Contains(err.Error(), "conflicts") || strings.Contains(err.Error(), "duplicates")) {
		t.Fatalf("EffectivePaths() error = %v, want sibling collision error", err)
	}
}

func TestEffectivePathsRejectsCaseFoldedAliasesBeforeTargetsExist(t *testing.T) {
	for _, test := range []struct {
		name    string
		project domain.Project
	}{
		{
			name: "top-level aliases",
			project: domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "root", Repositories: []domain.Repository{
				{ID: "root", DefaultMount: "api"},
				{ID: "other", DefaultMount: "API"},
			}},
		},
		{
			name: "same-parent child aliases",
			project: domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "root", Repositories: []domain.Repository{
				{ID: "root", DefaultMount: "project"},
				{ID: "child-a", ParentID: "root", DefaultMount: "api"},
				{ID: "child-b", ParentID: "root", DefaultMount: "API"},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.project.EffectivePaths(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "duplicates") {
				t.Fatalf("EffectivePaths() error = %v, want case-folded conflict before targets exist", err)
			}
		})
	}
}

func TestEffectivePathsRejectsCanonicalAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "actual"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "actual"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "one", Repositories: []domain.Repository{
		{ID: "one", DefaultMount: "group/one"},
		{ID: "two", DefaultMount: "group/two"},
	}}
	_, err := project.EffectivePaths(root, map[string]string{"one": "actual/one", "two": "alias/one"})
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("EffectivePaths() error = %v, want canonical alias rejection", err)
	}
}

func TestEffectivePathsRejectsPlatformUnsafeMountOverrides(t *testing.T) {
	project := testProject()
	for _, mount := range []string{".git.", "NUL.txt", "component.", "component "} {
		t.Run(mount, func(t *testing.T) {
			if _, err := project.EffectivePaths(t.TempDir(), map[string]string{"backend": mount}); err == nil {
				t.Fatal("EffectivePaths() error = nil, want platform-unsafe mount rejection")
			}
		})
	}
}

func TestEffectivePathsRejectsDeclaredAncestorCanonicalEquality(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(".", filepath.Join(root, "self")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "root", Repositories: []domain.Repository{
		{ID: "root", DefaultMount: "."},
		{ID: "child", ParentID: "root", DefaultMount: "self"},
	}}
	_, err := project.EffectivePaths(root, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("EffectivePaths() error = %v, want declared-ancestor duplicate rejection", err)
	}
}
