package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/testutil"
)

// TestAutomaticNestedIgnoreProtectionThroughPublicCLI is deliberately limited
// to the public command boundary. The repositories are real Git repositories:
// this catches a nested checkout accidentally becoming a gitlink when a user
// runs the ordinary "git add ." workflow in each parent.
func TestAutomaticNestedIgnoreProtectionThroughPublicCLI(t *testing.T) {
	root, backend, _ := threeLevelIgnoreFixture(t)
	data := t.TempDir()

	init := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data)
	if init.Err != nil || init.Stderr != "" {
		t.Fatalf("init = %#v", init)
	}
	assertInitChangedIgnoreOutput(t, init.Stdout, filepath.Join(root.Path, "project.wtree.yml"), []string{
		filepath.Join(root.Path, ".gitignore"),
		filepath.Join(backend.Path, ".gitignore"),
	})
	assertFileBytes(t, filepath.Join(root.Path, ".gitignore"), "/.wtree.yml\n/backend/\n")
	assertFileBytes(t, filepath.Join(backend.Path, ".gitignore"), "/shared/\n")
	assertIgnoredAndNoGitlink(t, root.Path, "backend")
	assertIgnoredAndNoGitlink(t, backend.Path, "shared")
	assertNoStagedChanges(t, root.Path)
	assertNoStagedChanges(t, backend.Path)

	sourceIgnores := map[string][]byte{
		filepath.Join(root.Path, ".gitignore"):    mustReadFile(t, filepath.Join(root.Path, ".gitignore")),
		filepath.Join(backend.Path, ".gitignore"): mustReadFile(t, filepath.Join(backend.Path, ".gitignore")),
	}

	defaultTarget := filepath.Join(t.TempDir(), "default")
	createdDefault := testutil.RunCommand(t, cli.Execute,
		"create", "--project", root.Path, "feature/default", "--data-dir", data, "--path", defaultTarget)
	if createdDefault.Err != nil || createdDefault.Stderr != "" {
		t.Fatalf("default create = %#v", createdDefault)
	}
	assertCreateChangedIgnoreOutput(t, createdDefault.Stdout, "feature/default", defaultTarget, []ignoreOutputChange{
		{path: filepath.Join(defaultTarget, ".gitignore"), rules: []string{"/backend/"}},
		{path: filepath.Join(defaultTarget, "backend", ".gitignore"), rules: []string{"/shared/"}},
	})
	assertFileBytes(t, filepath.Join(defaultTarget, ".gitignore"), "/backend/\n")
	assertFileBytes(t, filepath.Join(defaultTarget, "backend", ".gitignore"), "/shared/\n")
	assertIgnoredAndNoGitlink(t, defaultTarget, "backend")
	assertIgnoredAndNoGitlink(t, filepath.Join(defaultTarget, "backend"), "shared")
	assertSourceBytesUnchanged(t, sourceIgnores)

	// The user may now review and commit init's deliberate source changes.
	// A later default create must inherit those committed rules without adding
	// a duplicate in its new worktrees.
	backend.Run(t, "add", ".gitignore")
	backend.Run(t, "commit", "-m", "protect shared mount")
	backend.Run(t, "push", "origin", "main")
	root.Run(t, "add", ".gitignore", "project.wtree.yml")
	root.Run(t, "commit", "-m", "protect backend mount")
	root.Run(t, "push", "origin", "main")

	overrideTarget := filepath.Join(t.TempDir(), "override")
	createdOverride := testutil.RunCommand(t, cli.Execute,
		"create", "--project", root.Path, "feature/override", "--data-dir", data, "--path", overrideTarget,
		"--mount", "backend=api", "--mount", "shared=common")
	if createdOverride.Err != nil || createdOverride.Stderr != "" {
		t.Fatalf("override create = %#v", createdOverride)
	}
	assertCreateChangedIgnoreOutput(t, createdOverride.Stdout, "feature/override", overrideTarget, []ignoreOutputChange{
		{path: filepath.Join(overrideTarget, ".gitignore"), rules: []string{"/api/"}},
		{path: filepath.Join(overrideTarget, "api", ".gitignore"), rules: []string{"/common/"}},
	})
	assertFileBytes(t, filepath.Join(overrideTarget, ".gitignore"), "/.wtree.yml\n/backend/\n/api/\n")
	assertFileBytes(t, filepath.Join(overrideTarget, "api", ".gitignore"), "/shared/\n/common/\n")
	assertIgnoredAndNoGitlink(t, overrideTarget, "api")
	assertIgnoredAndNoGitlink(t, filepath.Join(overrideTarget, "api"), "common")
	assertSourceBytesUnchanged(t, sourceIgnores)

	noChangeTarget := filepath.Join(t.TempDir(), "no-change")
	noChange := testutil.RunCommand(t, cli.Execute,
		"create", "--project", root.Path, "feature/no-change", "--data-dir", data, "--path", noChangeTarget)
	if noChange.Err != nil || noChange.Stderr != "" {
		t.Fatalf("already-protected create = %#v", noChange)
	}
	assertCreateNoIgnoreChangesOutput(t, noChange.Stdout, "feature/no-change", noChangeTarget)
	assertFileBytes(t, filepath.Join(noChangeTarget, ".gitignore"), "/.wtree.yml\n/backend/\n")
	assertFileBytes(t, filepath.Join(noChangeTarget, "backend", ".gitignore"), "/shared/\n")
	assertIgnoredAndNoGitlink(t, noChangeTarget, "backend")
	assertIgnoredAndNoGitlink(t, filepath.Join(noChangeTarget, "backend"), "shared")
}

func TestAutomaticNestedIgnoreDryRunsAreTotallyNonMutating(t *testing.T) {
	root, backend, _ := threeLevelIgnoreFixture(t)
	data := filepath.Join(t.TempDir(), "data")
	beforeInit := snapshotPaths(t, root.Path, backend.Path, data)

	init := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data, "--dry-run", "--json")
	var initJSON struct {
		DryRun        bool `json:"dryRun"`
		IgnoreUpdates []struct {
			AddedRules []string `json:"addedRules"`
		} `json:"ignoreUpdates"`
	}
	if init.Err != nil || init.Stderr != "" || json.Unmarshal([]byte(init.Stdout), &initJSON) != nil || !initJSON.DryRun || len(initJSON.IgnoreUpdates) != 2 {
		t.Fatalf("init dry-run = %#v json=%#v", init, initJSON)
	}
	assertSnapshotUnchanged(t, beforeInit, root.Path, backend.Path, data)

	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init after dry-run = %#v", result)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	beforeCreate := snapshotPaths(t, root.Path, backend.Path, data, target)
	create := testutil.RunCommand(t, cli.Execute,
		"create", "--project", root.Path, "feature/dry-run", "--data-dir", data, "--path", target,
		"--mount", "backend=api", "--mount", "shared=common", "--dry-run")
	if create.Err != nil || create.Stderr != "" || !containsAll(create.Stdout,
		"Automatic ignore protection (execution will ensure):", "/api/", "/common/", "No changes made. Dry run performs no mutation.") {
		t.Fatalf("create dry-run = %#v", create)
	}
	assertSnapshotUnchanged(t, beforeCreate, root.Path, backend.Path, data, target)
}

func TestAutomaticIgnorePublicSurfaceHasNoManualCommand(t *testing.T) {
	for _, arguments := range [][]string{{"--help"}, {"--how-to"}, {"init", "--help"}, {"create", "--help"}, {"create", "--how-to"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil || result.Stderr != "" || strings.Contains(result.Stdout, "add-ignore") || strings.Contains(result.Stdout, "--add-ignore") {
			t.Fatalf("public help %v = %#v", arguments, result)
		}
	}

	for _, path := range []string{
		"../../README.md",
		"../../docs/INSTALL.md",
		"../../docs/TROUBLESHOOTING.md",
		"../../tutorial/README.md",
		"../../docs/ai/README.md",
		"../../docs/spec/wtree.traceability.md",
	} {
		contents := string(mustReadFile(t, path))
		if strings.Contains(contents, "wtree add-ignore") || strings.Contains(contents, "--add-ignore") {
			t.Fatalf("current public document %s advertises removed manual ignore management", path)
		}
	}
	for _, path := range []string{
		"../../docs/spec/wtree.spec.md",
		"../../docs/spec/wtree.traceability.md",
		"../../docs/spec/portable-manifest-clone.md",
		"../../docs/spec/portable-manifest-v2-base-repository-format.md",
	} {
		contents := string(mustReadFile(t, path))
		if strings.Contains(contents, "wtree add-ignore") || strings.Contains(contents, "--add-ignore") {
			t.Fatalf("current non-superseded specification %s advertises removed manual ignore management", path)
		}
	}
	automaticSpec := string(mustReadFile(t, "../../docs/spec/automatic-nested-mount-ignore-protection.md"))
	if !containsAll(automaticSpec, "automatic", "not part of the supported command surface") || strings.Contains(automaticSpec, "[--add-ignore]") {
		t.Fatalf("current automatic-protection specification does not record the automatic-only contract")
	}
	cloneSpec := string(mustReadFile(t, "../../docs/spec/portable-manifest-clone.md"))
	if !containsAll(cloneSpec, "Before retrying clone, users must add and commit", "ordinary repository workflow") || strings.Contains(strings.ToLower(cloneSpec), "diagnostic tells") || strings.Contains(strings.ToLower(cloneSpec), "diagnostic must tell") {
		t.Fatalf("clone specification does not state the ordinary committed-rule retry workflow without a false diagnostic claim")
	}
	plan := string(mustReadFile(t, "../../docs/plans/automatic-nested-mount-ignore-protection.md"))
	if !containsAll(plan, "automatic", "add-ignore") || strings.Contains(plan, "[--add-ignore]") {
		t.Fatalf("current implementation plan does not record the automatic-only contract")
	}
}

func TestAutomaticIgnoreLeavesUnrelatedCommandSurfaceUnchanged(t *testing.T) {
	for _, command := range []string{"clone", "checkout", "import", "remove", "delete", "doctor"} {
		result := testutil.RunCommand(t, cli.Execute, command, "--help")
		if result.Err != nil || result.Stderr != "" || strings.Contains(result.Stdout, "add-ignore") || strings.Contains(result.Stdout, "--add-ignore") {
			t.Fatalf("%s help = %#v", command, result)
		}
	}
}

func TestAutomaticIgnorePreflightErrorContractsThroughPublicCLI(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("validation json=%t", jsonOutput), func(t *testing.T) {
			root, _, _ := threeLevelIgnoreFixture(t)
			data := t.TempDir()
			if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("init = %#v", result)
			}
			arguments := []string{"create", "--project", root.Path, "feature/invalid", "--data-dir", data, "--path", filepath.Join(t.TempDir(), "workspace"), "--mount", "backend=../escape"}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			assertCLIErrorContract(t, testutil.RunCommand(t, cli.Execute, arguments...), 5, "validation", jsonOutput, "")
		})

		t.Run(fmt.Sprintf("conflict json=%t", jsonOutput), func(t *testing.T) {
			root := testutil.NewPushedGitRepository(t)
			root.CommitFile("root.txt", "root\n", "root")
			data := t.TempDir()
			if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("first init = %#v", result)
			}
			arguments := []string{"init", root.Path, "--data-dir", data}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			assertCLIErrorContract(t, testutil.RunCommand(t, cli.Execute, arguments...), 8, "conflict", jsonOutput, "")
		})

		t.Run(fmt.Sprintf("internal json=%t", jsonOutput), func(t *testing.T) {
			root := testutil.NewPushedGitRepository(t)
			root.CommitFile("root.txt", "root\n", "root")
			data := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(data, []byte("occupied\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			arguments := []string{"init", root.Path, "--data-dir", data}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			assertCLIErrorContract(t, testutil.RunCommand(t, cli.Execute, arguments...), 1, "internal", jsonOutput, "")
		})
	}
}

func TestAutomaticIgnoreRollbackErrorContractsThroughPublicCLI(t *testing.T) {
	for _, test := range []struct {
		name            string
		failDelete      bool
		wantExit        int
		wantCode        string
		wantHumanStderr string
	}{
		{name: "clean rollback", wantExit: 6, wantCode: "git", wantHumanStderr: "Rollback complete."},
		{name: "rollback incomplete", failDelete: true, wantExit: 9, wantCode: "rollback_incomplete", wantHumanStderr: "Removed .gitignore files with clean rollback:"},
	} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s json=%t", test.name, jsonOutput), func(t *testing.T) {
				root, backend, _ := threeLevelIgnoreFixture(t)
				data, target := t.TempDir(), filepath.Join(t.TempDir(), "rollback")
				if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
					t.Fatalf("init = %#v", result)
				}
				realGit, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				shimDir := t.TempDir()
				canonicalBackend, err := filepath.EvalSymlinks(backend.Path)
				if err != nil {
					t.Fatal(err)
				}
				buildAutomaticIgnoreGitFailureHelper(t, shimDir, realGit, canonicalBackend, test.failDelete)
				t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				arguments := []string{"create", "--project", root.Path, "feature/rollback", "--data-dir", data, "--path", target}
				if jsonOutput {
					arguments = append(arguments, "--json")
				}
				assertCLIErrorContract(t, testutil.RunCommand(t, cli.Execute, arguments...), test.wantExit, test.wantCode, jsonOutput, test.wantHumanStderr)
			})
		}
	}
}

// TestAutomaticIgnoreIneffectiveRuleRetryThroughPublicCLI covers the §11.7
// retry boundary where init's exact generated rule was later negated in the
// committed source. A create must neither trust that visible rule nor create
// the child before its parent protection verifies; after clean rollback, only
// correcting the committed facts permits a retry.
func TestAutomaticIgnoreIneffectiveRuleRetryThroughPublicCLI(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			root, _, _ := threeLevelIgnoreFixture(t)
			data := t.TempDir()
			if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("init = %#v", result)
			}

			generated := "/.wtree.yml\n/backend/\n"
			assertFileBytes(t, filepath.Join(root.Path, ".gitignore"), generated)
			root.CommitFile(".gitignore", generated+"!/backend/\n", "negate generated backend protection")
			assertFileBytes(t, filepath.Join(root.Path, ".gitignore"), generated+"!/backend/\n")
			if count := exactLineCount(string(mustReadFile(t, filepath.Join(root.Path, ".gitignore"))), "/backend/"); count != 1 {
				t.Fatalf("generated backend rule count = %d, want one", count)
			}

			target := filepath.Join(t.TempDir(), "workspace")
			arguments := []string{"create", "--project", root.Path, "feature/ineffective", "--data-dir", data, "--path", target}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			first := testutil.RunCommand(t, cli.Execute, arguments...)
			assertCLIErrorContract(t, first, 8, "conflict", jsonOutput, "")
			assertCreateRejectedBeforeChildAndCleanRollback(t, root.Path, target, "feature/ineffective")

			retry := testutil.RunCommand(t, cli.Execute, arguments...)
			assertCLIErrorContract(t, retry, 8, "conflict", jsonOutput, "")
			assertCreateRejectedBeforeChildAndCleanRollback(t, root.Path, target, "feature/ineffective")

			root.CommitFile(".gitignore", generated, "restore effective backend protection")
			success := testutil.RunCommand(t, cli.Execute, arguments...)
			if success.Err != nil || success.Stderr != "" {
				t.Fatalf("create after correcting facts = %#v", success)
			}
			if jsonOutput {
				var result struct {
					RootPath string `json:"rootPath"`
				}
				if json.Unmarshal([]byte(success.Stdout), &result) != nil {
					t.Fatalf("decode corrected retry JSON = %#v", success)
				}
				assertSameExistingPath(t, result.RootPath, target)
			} else {
				assertCreateChangedIgnoreOutput(t, success.Stdout, "feature/ineffective", target, []ignoreOutputChange{{
					path: filepath.Join(target, "backend", ".gitignore"), rules: []string{"/shared/"},
				}})
			}
			assertFileBytes(t, filepath.Join(target, ".gitignore"), generated)
			if count := exactLineCount(string(mustReadFile(t, filepath.Join(target, ".gitignore"))), "/backend/"); count != 1 {
				t.Fatalf("retry rule count = %d, want one", count)
			}
			if _, err := os.Stat(filepath.Join(target, "backend", ".git")); err != nil {
				t.Fatalf("corrected retry did not create verified child: %v", err)
			}
			assertIgnoredAndNoGitlink(t, target, "backend")
		})
	}
}

type ignoreOutputChange struct {
	path  string
	rules []string
}

func assertInitChangedIgnoreOutput(t *testing.T, output, manifestPath string, changedPaths []string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "Initialized ") || lines[0] == "Initialized " || !strings.HasPrefix(lines[1], "Portable manifest: ") || !strings.HasPrefix(lines[2], "Changed .gitignore: ") || lines[3] != "Review and commit project.wtree.yml and .gitignore changes; wtree did not stage, commit, or push them." {
		t.Fatalf("init output shape = %q", output)
	}
	assertSameExistingPath(t, strings.TrimPrefix(lines[1], "Portable manifest: "), manifestPath)
	actual := strings.Split(strings.TrimPrefix(lines[2], "Changed .gitignore: "), ", ")
	if len(actual) != len(changedPaths) {
		t.Fatalf("changed init paths = %q, want %q", actual, changedPaths)
	}
	for index := range changedPaths {
		assertSameExistingPath(t, actual[index], changedPaths[index])
	}
}

func assertCreateChangedIgnoreOutput(t *testing.T, output, workspace, target string, changes []ignoreOutputChange) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != len(changes)+4 || lines[0] != "Created workspace: "+workspace || !strings.HasPrefix(lines[1], "Target: ") || lines[2] != "Changed .gitignore files:" || lines[len(lines)-1] != "Review and commit .gitignore changes; wtree did not stage or commit them." {
		t.Fatalf("create output shape = %q", output)
	}
	assertSameExistingPath(t, strings.TrimPrefix(lines[1], "Target: "), target)
	for index, change := range changes {
		line := lines[index+3]
		suffix := " (" + strings.Join(change.rules, ", ") + ")"
		if !strings.HasPrefix(line, "  ") || !strings.HasSuffix(line, suffix) {
			t.Fatalf("changed ignore line %d = %q, want path plus %q", index, line, suffix)
		}
		assertSameExistingPath(t, strings.TrimSuffix(strings.TrimPrefix(line, "  "), suffix), change.path)
	}
}

func assertCreateNoIgnoreChangesOutput(t *testing.T, output, workspace, target string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "Created workspace: "+workspace || !strings.HasPrefix(lines[1], "Target: ") || lines[2] != "Every nested mount was already protected." {
		t.Fatalf("no-change create output shape = %q", output)
	}
	assertSameExistingPath(t, strings.TrimPrefix(lines[1], "Target: "), target)
}

func assertSameExistingPath(t *testing.T, actual, expected string) {
	t.Helper()
	actualInfo, actualErr := os.Stat(actual)
	expectedInfo, expectedErr := os.Stat(expected)
	if actualErr != nil || expectedErr != nil || !os.SameFile(actualInfo, expectedInfo) {
		t.Fatalf("emitted path %q is not the same filesystem object as %q: actual=%v expected=%v", actual, expected, actualErr, expectedErr)
	}
}

func assertCreateRejectedBeforeChildAndCleanRollback(t *testing.T, source, target, branch string) {
	t.Helper()
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed create retained parent or child checkout at %q: %v", target, err)
	}
	if output, err := runFixtureGitResult(source, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil || len(output) != 0 {
		t.Fatalf("failed create retained branch %q: output=%q err=%v", branch, output, err)
	}
}

func exactLineCount(contents, want string) int {
	count := 0
	for _, line := range strings.Split(contents, "\n") {
		if line == want {
			count++
		}
	}
	return count
}

func assertCLIErrorContract(t *testing.T, result testutil.Result, wantExit int, wantCode string, jsonOutput bool, wantHumanStderr string) {
	t.Helper()
	if result.Err == nil || cli.ExitCode(result.Err) != wantExit {
		t.Fatalf("error result = %#v, want exit %d", result, wantExit)
	}
	if jsonOutput {
		var envelope struct {
			Success bool `json:"success"`
			Error   struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if result.Stderr != "" || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Success || envelope.Error.Code != wantCode || strings.Count(result.Stdout, "\n") != 1 {
			t.Fatalf("JSON error result = %#v envelope=%#v, want code %q and empty stderr", result, envelope, wantCode)
		}
		return
	}
	if result.Stdout != "" || strings.Contains(result.Stderr, `"code":`) || (wantHumanStderr != "" && !strings.Contains(result.Stderr, wantHumanStderr)) {
		t.Fatalf("human error result = %#v, want non-JSON output and stderr containing %q", result, wantHumanStderr)
	}
}

// buildAutomaticIgnoreGitFailureHelper is a platform-native test fixture. It
// fails backend worktree creation and, when requested, root-branch cleanup to
// exercise both clean and incomplete rollback without production hooks.
func buildAutomaticIgnoreGitFailureHelper(t *testing.T, directory, realGit, failedRepository string, failDelete bool) {
	t.Helper()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source := filepath.Join(directory, "main.go")
	program := fmt.Sprintf(`package main
import (
  "fmt"
  "os"
  "os/exec"
  "path/filepath"
)
func main() {
  args := os.Args[1:]
  repo, rest := "", args
  if len(args) >= 2 && args[0] == "-C" { repo, rest = args[1], args[2:] }
  resolved, _ := filepath.EvalSymlinks(repo)
  if resolved == %q && len(rest) >= 2 && (rest[0] == "worktree" && rest[1] == "add") {
    fmt.Fprintln(os.Stderr, "injected Git failure")
    os.Exit(1)
  }
  if %t && len(rest) >= 2 && rest[0] == "branch" && rest[1] == "-D" {
    fmt.Fprintln(os.Stderr, "injected Git cleanup failure")
    os.Exit(1)
  }
  command := exec.Command(%q, args...)
  command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
  if err := command.Run(); err != nil {
    if exit, ok := err.(*exec.ExitError); ok { os.Exit(exit.ExitCode()) }
    fmt.Fprintln(os.Stderr, err)
    os.Exit(1)
  }
}
`, failedRepository, failDelete, realGit)
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", filepath.Join(directory, name), source).CombinedOutput(); err != nil {
		t.Fatalf("build automatic-ignore Git fixture: %v\n%s", err, output)
	}
}

func threeLevelIgnoreFixture(t *testing.T) (testutil.PushedGitRepository, testutil.PushedGitRepository, testutil.PushedGitRepository) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("shared.txt", "shared\n", "shared")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	sharedPath := filepath.Join(backend.Path, "shared")
	if err := os.Rename(shared.Path, sharedPath); err != nil {
		t.Fatal(err)
	}
	shared.Path = sharedPath
	return root, backend, shared
}

func assertIgnoredAndNoGitlink(t *testing.T, parent, mount string) {
	t.Helper()
	output := runFixtureGit(t, parent, "check-ignore", "--no-index", "--", mount+"/")
	if strings.TrimSpace(string(output)) == "" {
		t.Fatalf("mount %q is not ignored in %q", mount, parent)
	}
	reset := func() { _, _ = runFixtureGitResult(parent, "reset", "--", ".") }
	t.Cleanup(reset)
	runFixtureGit(t, parent, "add", ".")
	index := runFixtureGit(t, parent, "ls-files", "--stage", "-z")
	for _, entry := range bytes.Split(index, []byte{0}) {
		if bytes.HasPrefix(entry, []byte("160000 ")) {
			t.Fatalf("git add . staged nested repository as gitlink in %q: %q", parent, entry)
		}
	}
	reset()
}

func assertNoStagedChanges(t *testing.T, repository string) {
	t.Helper()
	if output := runFixtureGit(t, repository, "diff", "--cached", "--name-only"); len(output) != 0 {
		t.Fatalf("wtree staged changes in %q: %q", repository, output)
	}
}

type pathSnapshot struct {
	exists bool
	mode   os.FileMode
	bytes  []byte
	mtime  int64
}

func snapshotPaths(t *testing.T, paths ...string) map[string]pathSnapshot {
	t.Helper()
	result := make(map[string]pathSnapshot, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			result[path] = pathSnapshot{}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		value := pathSnapshot{exists: true, mode: info.Mode(), mtime: info.ModTime().UnixNano()}
		if !info.IsDir() {
			value.bytes = mustReadFile(t, path)
		}
		result[path] = value
	}
	return result
}

func assertSnapshotUnchanged(t *testing.T, before map[string]pathSnapshot, paths ...string) {
	t.Helper()
	after := snapshotPaths(t, paths...)
	for path, want := range before {
		got := after[path]
		if got.exists != want.exists || got.mode != want.mode || got.mtime != want.mtime || !bytes.Equal(got.bytes, want.bytes) {
			t.Fatalf("dry-run changed %q: before=%#v after=%#v", path, want, got)
		}
	}
}

func assertSourceBytesUnchanged(t *testing.T, expected map[string][]byte) {
	t.Helper()
	for path, want := range expected {
		assertFileBytes(t, path, string(want))
	}
}

func assertFileBytes(t *testing.T, path, want string) {
	t.Helper()
	if got := mustReadFile(t, path); string(got) != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func runFixtureGit(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	output, err := runFixtureGitResult(directory, arguments...)
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", directory, strings.Join(arguments, " "), err, output)
	}
	return output
}

func runFixtureGitResult(directory string, arguments ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	return command.CombinedOutput()
}
