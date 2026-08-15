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
	if paths.ConfigDir != filepath.Join("/cfg", "wtree") || paths.DataDir != "/data" {
		t.Fatalf("paths = %#v", paths)
	}
	paths, err = config.ResolvePaths("darwin", "/Users/test", map[string]string{})
	if err != nil || paths.DataDir != "/Users/test/Library/Application Support/wtree" {
		t.Fatalf("darwin paths = %#v, %v", paths, err)
	}
	paths, err = config.ResolvePaths("windows", "", map[string]string{"AppData": "C:/roaming", "LocalAppData": "C:/local"})
	if err != nil || paths.ConfigDir != filepath.Join("C:/roaming", "wtree") || paths.DataDir != filepath.Join("C:/local", "wtree") {
		t.Fatalf("windows paths = %#v, %v", paths, err)
	}
	paths, err = config.ResolvePaths("windows", "", map[string]string{"AppData": "C:/roaming", "LocalAppData": "C:/local", "WTREE_DATA_HOME": "D:/data"})
	if err != nil || paths.ConfigDir != filepath.Join("C:/roaming", "wtree") || paths.DataDir != "D:/data" {
		t.Fatalf("windows override paths=%#v %v", paths, err)
	}
	paths, err = config.ResolvePaths("linux", "/home/test", map[string]string{})
	if err != nil || paths.DataDir != "/home/test/.local/share/wtree" {
		t.Fatal(paths, err)
	}
	paths, err = config.ResolvePaths("darwin", "/Users/test", map[string]string{"WTREE_DATA_HOME": "/data"})
	if err != nil || paths.ConfigDir != "/Users/test/Library/Application Support/wtree" || paths.DataDir != "/data" {
		t.Fatal(paths, err)
	}
}
