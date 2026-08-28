package config_test

import (
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
)

func TestPathsUseInjectableOSAndWTREEDataHome(t *testing.T) {
	paths, err := config.ResolvePaths("linux", "/home/test", map[string]string{"XDG_CONFIG_HOME": "/cfg", "WTREE_DATA_HOME": "/data"})
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigDir != filepath.Join("/cfg", "wtree") || paths.DataDir != "/data" || paths.WorktreeRoot != filepath.Join("/data", "worktrees") {
		t.Fatalf("paths = %#v", paths)
	}
	paths, err = config.ResolvePaths("darwin", "/Users/test", map[string]string{})
	darwinBase := filepath.Join("/Users/test", "Library", "Application Support", "wtree")
	if err != nil || paths.ConfigDir != darwinBase || paths.DataDir != darwinBase || paths.WorktreeRoot != filepath.Join(darwinBase, "worktrees") {
		t.Fatalf("darwin paths = %#v, %v", paths, err)
	}
	paths, err = config.ResolvePaths("windows", "", map[string]string{"AppData": "C:/roaming", "LocalAppData": "C:/local"})
	windowsData := filepath.Join("C:/local", "wtree")
	if err != nil || paths.ConfigDir != filepath.Join("C:/roaming", "wtree") || paths.DataDir != windowsData || paths.WorktreeRoot != filepath.Join(windowsData, "worktrees") {
		t.Fatalf("windows paths = %#v, %v", paths, err)
	}
	paths, err = config.ResolvePaths("windows", "", map[string]string{"AppData": "C:/roaming", "LocalAppData": "C:/local", "WTREE_DATA_HOME": "D:/data"})
	if err != nil || paths.ConfigDir != filepath.Join("C:/roaming", "wtree") || paths.DataDir != "D:/data" || paths.WorktreeRoot != filepath.Join("D:/data", "worktrees") {
		t.Fatalf("windows override paths=%#v %v", paths, err)
	}
	paths, err = config.ResolvePaths("linux", "/home/test", map[string]string{})
	linuxData := filepath.Join("/home/test", ".local", "share", "wtree")
	if err != nil || paths.ConfigDir != filepath.Join("/home/test", ".config", "wtree") || paths.DataDir != linuxData || paths.WorktreeRoot != filepath.Join(linuxData, "worktrees") {
		t.Fatal(paths, err)
	}
	paths, err = config.ResolvePaths("darwin", "/Users/test", map[string]string{"WTREE_DATA_HOME": "/data"})
	if err != nil || paths.ConfigDir != darwinBase || paths.DataDir != "/data" || paths.WorktreeRoot != filepath.Join("/data", "worktrees") {
		t.Fatal(paths, err)
	}
}
