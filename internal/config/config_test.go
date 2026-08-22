package config_test

import (
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
)

func TestLoadProjectConfigStrictlyDecodesVersionTwoTopology(t *testing.T) {
	loaded, err := config.LoadProject([]byte("version: 2\nproject:\n  id: p1\n  name: product\n  base_repository: root\nlogical_root: .\nrepositories:\n  root:\n    source: .\n    parent: \"\"\n    mount: .\n    default_branch: main\nworktrees:\n  root: /worktrees\nmanifest:\n  path: project.wtree.yml\n  source: /projects/product/project.wtree.yml\n"))
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	if loaded.Repositories["root"].DefaultMount != "." {
		t.Errorf("root mount = %q", loaded.Repositories["root"].DefaultMount)
	}
	if loaded.Project.BaseRepository != "root" || loaded.LogicalRoot != "." {
		t.Fatalf("topology = %#v", loaded)
	}
	if _, err := config.LoadProject([]byte("version: 2\nunknown: true\n")); err == nil {
		t.Fatal("LoadProject() error = nil, want unknown field rejection")
	}
	if _, err := config.LoadProject([]byte("version: 1\nproject:\n  id: p1\n  name: product\nrepositories: {}\n")); err == nil || !strings.Contains(err.Error(), "reinitialization") {
		t.Fatalf("LoadProject(v1) error = %v, want reinitialization-required diagnostic", err)
	}
}

func TestLoadProjectConfigV2RequiresTopologyFieldsAndCleanPaths(t *testing.T) {
	valid := "version: 2\nproject:\n  id: p1\n  name: product\n  base_repository: root\nlogical_root: .\nrepositories:\n  root:\n    source: .\n    parent: \"\"\n    mount: .\n    default_branch: main\nworktrees: {}\nmanifest:\n  path: project.wtree.yml\n  source: /projects/product/project.wtree.yml\n"
	for _, test := range []struct {
		name, input, want string
	}{
		{"missing logical root", strings.Replace(valid, "logical_root: .\n", "", 1), "logical_root"},
		{"missing base", strings.Replace(valid, "  base_repository: root\n", "", 1), "base_repository"},
		{"missing repository parent", strings.Replace(valid, "    parent: \"\"\n", "", 1), "parent"},
		{"missing manifest", strings.Replace(valid, "manifest:\n  path: project.wtree.yml\n  source: /projects/product/project.wtree.yml\n", "", 1), "manifest"},
		{"unclean logical root", strings.Replace(valid, "logical_root: .", "logical_root: base/..", 1), "clean"},
		{"absolute source", strings.Replace(valid, "    source: .", "    source: /outside", 1), "relative"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := config.LoadProject([]byte(test.input)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadProject() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWorktreeRootPrecedenceAndExpansion(t *testing.T) {
	got, err := config.EffectiveWorktreeRoot("~/cli", "~/project", "~/global", "/default", "/home/test")
	if err != nil || got != "/home/test/cli" {
		t.Fatalf("EffectiveWorktreeRoot() = %q, %v", got, err)
	}
	if _, err := config.EffectiveWorktreeRoot("", "", "", "", "/home/test"); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("missing default error = %v", err)
	}
}

func TestWorktreeRootUsesProjectGlobalAndDefaultPrecedence(t *testing.T) {
	project := config.ProjectConfig{Worktrees: config.Worktrees{Root: "~/project"}}
	global := config.GlobalConfig{Version: 1, Worktrees: config.Worktrees{Root: "~/global"}}
	got, err := config.ResolveWorktreeRoot("", project, global, "/os-default", "/home/test")
	if err != nil || got != "/home/test/project" {
		t.Fatalf("project precedence = %q, %v", got, err)
	}
	project.Worktrees.Root = ""
	got, err = config.ResolveWorktreeRoot("", project, global, "/os-default", "/home/test")
	if err != nil || got != "/home/test/global" {
		t.Fatalf("global precedence = %q, %v", got, err)
	}
}

func TestGlobalRequiresVersionAndYAMLHasOneDocument(t *testing.T) {
	if _, err := config.LoadGlobal([]byte("worktrees:\n  root: /tmp\n")); err == nil {
		t.Fatal("global version omitted")
	}
	if _, err := config.LoadProject([]byte("version: 1\n---\nversion: 1\n")); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
	if _, err := config.LoadGlobal([]byte("version: 1\n---\nversion: 1\n")); err == nil {
		t.Fatal("global multiple documents accepted")
	}
	if _, err := config.LoadGlobal([]byte("version: 2\n")); err == nil {
		t.Fatal("newer global version accepted")
	}
}
