package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
)

func TestLoadProjectConfigStrictlyDecodesVersionOne(t *testing.T) {
	loaded, err := config.LoadProject([]byte("version: 1\nproject:\n  id: p1\n  name: product\nrepositories:\n  root:\n    source: .\n    mount: .\n"))
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	if loaded.Repositories["root"].DefaultMount != "." {
		t.Errorf("root mount = %q", loaded.Repositories["root"].DefaultMount)
	}
	if _, err := config.LoadProject([]byte("version: 1\nunknown: true\n")); err == nil {
		t.Fatal("LoadProject() error = nil, want unknown field rejection")
	}
}

func TestWorktreeRootPrecedenceAndExpansion(t *testing.T) {
	got, err := config.EffectiveWorktreeRoot("~/cli", "~/project", "~/global", "/default", "/home/test")
	if want := filepath.Join("/home/test", "cli"); err != nil || got != want {
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
	if want := filepath.Join("/home/test", "project"); err != nil || got != want {
		t.Fatalf("project precedence = %q, %v", got, err)
	}
	project.Worktrees.Root = ""
	got, err = config.ResolveWorktreeRoot("", project, global, "/os-default", "/home/test")
	if want := filepath.Join("/home/test", "global"); err != nil || got != want {
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
