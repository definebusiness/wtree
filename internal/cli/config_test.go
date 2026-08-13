package cli_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marcel/wtree/internal/cli"
	"github.com/marcel/wtree/internal/config"
	"github.com/marcel/wtree/internal/testutil"
)

type brokenPipeWriter struct{ err error }

func (w brokenPipeWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExecuteConfigSetAndGetJSON(t *testing.T) {
	hostile := t.TempDir()
	setHostilePathEnvironment(t, hostile)
	paths := isolateCLIPathEnvironment(t)
	want := filepath.Join(t.TempDir(), "worktrees")
	set := testutil.RunCommand(t, cli.Execute, "config", "set", "worktrees.root", want)
	if set.Err != nil || set.Stderr != "" {
		t.Fatalf("config set = %#v", set)
	}
	if got, wantOutput := set.Stdout, "worktrees.root = "+want+"\n"; got != wantOutput {
		t.Fatalf("config set stdout = %q, want %q", got, wantOutput)
	}

	get := testutil.RunCommand(t, cli.Execute, "config", "get", "worktrees.root", "--json")
	if get.Err != nil || get.Stderr != "" {
		t.Fatalf("config get = %#v", get)
	}
	var result struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(get.Stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Key != "worktrees.root" || result.Value != want || result.Source != "global" {
		t.Fatalf("config get JSON = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(paths.ConfigDir, "config.yml")); err != nil {
		t.Fatalf("isolated config was not written: %v", err)
	}
	hostilePaths, err := config.ResolvePaths(runtime.GOOS, hostile, pathEnvironment(hostile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(hostilePaths.ConfigDir, "config.yml")); !os.IsNotExist(err) {
		t.Fatalf("config write used inherited path %q: %v", hostilePaths.ConfigDir, err)
	}
}

func TestExecuteConfigProjectScopeUsesRootProjectSelector(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("readme", "x\n", "initial")
	isolateCLIPathEnvironment(t)
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	global := filepath.Join(t.TempDir(), "global-worktrees")
	if result := testutil.RunCommand(t, cli.Execute, "config", "set", "worktrees.root", global); result.Err != nil {
		t.Fatalf("global config set = %#v", result)
	}
	projectValue := filepath.Join(t.TempDir(), "project-worktrees")
	set := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "config", "set", "--project", "worktrees.root", projectValue)
	if set.Err != nil || set.Stderr != "" {
		t.Fatalf("project config set = %#v", set)
	}
	get := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "config", "get", "--project", "worktrees.root", "--json")
	if get.Err != nil || get.Stderr != "" {
		t.Fatalf("project config get = %#v", get)
	}
	var result struct {
		Value  string `json:"value"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(get.Stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != projectValue || result.Source != "project" {
		t.Fatalf("project config get JSON = %#v", result)
	}
}

func TestExecuteConfigUnsetFallsBackAndListReportsEffectiveSource(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("readme", "x\n", "initial")
	isolateCLIPathEnvironment(t)
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	global := filepath.Join(t.TempDir(), "global-worktrees")
	projectValue := filepath.Join(t.TempDir(), "project-worktrees")
	for _, arguments := range [][]string{
		{"config", "set", "worktrees.root", global},
		{"--project", project.Path, "config", "set", "--project", "worktrees.root", projectValue},
		{"--project", project.Path, "config", "unset", "--project", "worktrees.root"},
	} {
		if result := testutil.RunCommand(t, cli.Execute, arguments...); result.Err != nil {
			t.Fatalf("%v = %#v", arguments, result)
		}
	}
	get := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "config", "get", "--project", "worktrees.root")
	if get.Err != nil || get.Stdout != global+"\n" || get.Stderr != "" {
		t.Fatalf("config get after unset = %#v", get)
	}
	list := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "config", "list", "--project")
	if list.Err != nil || !strings.Contains(list.Stdout, "worktrees.root = "+global+" (global)\n") || list.Stderr != "" {
		t.Fatalf("config list = %#v", list)
	}
}

func TestExecuteConfigRejectsInvalidKeysValuesScopesAndOptions(t *testing.T) {
	isolateCLIPathEnvironment(t)
	for _, arguments := range [][]string{
		{"config", "set", "unknown.key", "value"},
		{"config", "set", "worktrees.root", ""},
		{"config", "get", "worktrees.root", "--dry-run"},
		{"config", "set", "--project", "worktrees.root", "value"},
	} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil {
			t.Fatalf("%v succeeded", arguments)
		}
		if result.Stdout != "" || result.Stderr != "" {
			t.Fatalf("%v output = %#v, want no direct CLI output on error", arguments, result)
		}
	}
}

func TestExecuteConfigJSONValidationErrorAndBrokenPipe(t *testing.T) {
	isolateCLIPathEnvironment(t)
	invalid := testutil.RunCommand(t, cli.Execute, "config", "get", "unknown.key", "--json")
	if invalid.Err == nil || invalid.Stderr != "" {
		t.Fatalf("invalid JSON config get = %#v", invalid)
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(invalid.Stdout), &envelope); err != nil || envelope.Success || envelope.Error.Code != "validation" {
		t.Fatalf("invalid config JSON = %q, envelope=%#v, error=%v", invalid.Stdout, envelope, err)
	}

	want := errors.New("broken pipe")
	err := cli.Execute([]string{"config", "get", "worktrees.root"}, brokenPipeWriter{err: want}, io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("Execute() error = %v, want %v", err, want)
	}
}

func isolateCLIPathEnvironment(t *testing.T) config.Paths {
	t.Helper()
	root := t.TempDir()
	setCLIPathEnvironment(t, root)
	paths, err := config.ResolvePaths(runtime.GOOS, filepath.Join(root, "home"), pathEnvironment(root))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func setHostilePathEnvironment(t *testing.T, root string) {
	t.Helper()
	setCLIPathEnvironment(t, root)
}

func setCLIPathEnvironment(t *testing.T, root string) {
	t.Helper()
	values := pathEnvironment(root)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", values["XDG_CONFIG_HOME"])
	t.Setenv("XDG_DATA_HOME", values["XDG_DATA_HOME"])
	t.Setenv("WTREE_DATA_HOME", values["WTREE_DATA_HOME"])
	// environmentMap accepts either original Windows spellings and uppercase
	// environment names, so isolate both on every host platform.
	t.Setenv("APPDATA", values["AppData"])
	t.Setenv("AppData", values["AppData"])
	t.Setenv("LOCALAPPDATA", values["LocalAppData"])
	t.Setenv("LocalAppData", values["LocalAppData"])
}

func pathEnvironment(root string) map[string]string {
	return map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME":   filepath.Join(root, "xdg-data"),
		"WTREE_DATA_HOME": filepath.Join(root, "data"),
		"AppData":         filepath.Join(root, "app-data"),
		"LocalAppData":    filepath.Join(root, "local-app-data"),
	}
}
