package testutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandCapturesStreamsAndError(t *testing.T) {
	wantErr := errors.New("command failed")
	result := RunCommand(t, func(_ []string, stdout, stderr io.Writer) error {
		fmt.Fprint(stdout, "standard output")
		fmt.Fprint(stderr, "standard error")
		return wantErr
	})

	if result.Stdout != "standard output" {
		t.Errorf("stdout = %q", result.Stdout)
	}
	if result.Stderr != "standard error" {
		t.Errorf("stderr = %q", result.Stderr)
	}
	if !errors.Is(result.Err, wantErr) {
		t.Errorf("error = %v, want %v", result.Err, wantErr)
	}
}

func TestSetenvRestoresEnvironmentAfterSubtest(t *testing.T) {
	const key = "WTREE_TESTUTIL_ISOLATED_ENV"
	t.Setenv(key, "outside")

	t.Run("isolated", func(t *testing.T) {
		Setenv(t, key, "inside")
		if got := os.Getenv(key); got != "inside" {
			t.Errorf("environment value = %q, want inside", got)
		}
	})

	if got := os.Getenv(key); got != "outside" {
		t.Errorf("environment value after subtest = %q, want outside", got)
	}
}

func TestRequireIntegrationRunsOutsideShortMode(t *testing.T) {
	RequireIntegration(t, "probe")
}

func TestNewGitRepositoryRunsOutsideShortMode(t *testing.T) {
	repository := NewGitRepository(t)
	repository.Run(t, "status", "--porcelain")
}

func TestNewGitRepositoryUsesEnvironmentIdentityWithoutLocalConfig(t *testing.T) {
	repository := NewGitRepository(t)
	repository.CommitFile("fixture.txt", "fixture\n", "fixture commit")
	command := exec.Command("git", "-C", repository.Path, "config", "--local", "--get-regexp", "^user\\.")
	command.Env = gitFixtureEnvironment()
	if output, err := command.CombinedOutput(); err == nil || len(output) != 0 {
		t.Fatalf("local identity config = %q, %v", output, err)
	}
	command = exec.Command("git", "-C", repository.Path, "log", "-1", "--format=%an <%ae>|%cn <%ce>")
	command.Env = gitFixtureEnvironment()
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "wtree test <wtree@example.invalid>|wtree test <wtree@example.invalid>\n" {
		t.Fatalf("commit identity = %q, %v", output, err)
	}
}

func TestGitCommandIgnoresHostileGlobalConfigAndHookPath(t *testing.T) {
	repository := NewGitRepository(t)
	hookPath := filepath.Join(t.TempDir(), "hostile-hooks")
	configPath := filepath.Join(t.TempDir(), "hostile.gitconfig")
	if err := os.WriteFile(configPath, []byte("[core]\n\thooksPath = "+hookPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", configPath)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")

	command := GitCommand(t, "-C", repository.Path, "config", "--get", "core.hooksPath")
	output, err := command.CombinedOutput()
	if err == nil || len(output) != 0 {
		t.Fatalf("hostile hook config was visible: output=%q err=%v", output, err)
	}
	if !containsEnvironment(command.Env, "GIT_CONFIG_GLOBAL="+os.DevNull) || !containsEnvironment(command.Env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Fatalf("GitCommand environment is not hermetic: %q", command.Env)
	}
}

func containsEnvironment(environment []string, want string) bool {
	for _, value := range environment {
		if value == want {
			return true
		}
	}
	return false
}

func TestRequireIntegrationSkipsBeforeExternalBoundaryInShortMode(t *testing.T) {
	if os.Getenv("WTREE_TESTUTIL_SHORT_CHILD") == "1" {
		RequireIntegration(t, "probe")
		t.Fatal("short-mode classifier returned after the external boundary")
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRequireIntegrationSkipsBeforeExternalBoundaryInShortMode$", "-test.short", "-test.v")
	command.Env = append(os.Environ(), "WTREE_TESTUTIL_SHORT_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("short child failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "short mode skips probe integration fixture") {
		t.Fatalf("short child did not report common-boundary skip: %s", output)
	}
}
