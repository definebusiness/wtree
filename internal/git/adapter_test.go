package git_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestAdapterReadsHermeticRepositoryFacts(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme.txt", "initial\n", "initial")

	adapter := git.NewAdapter("git")
	context := context.Background()
	commonDir, err := adapter.CommonGitDir(context, repository.Path)
	if err != nil {
		t.Fatalf("CommonGitDir() error = %v", err)
	}
	if !filepath.IsAbs(commonDir) {
		t.Errorf("common Git dir = %q, want absolute", commonDir)
	}
	if _, err := adapter.TopLevel(context, repository.Path); err != nil {
		t.Fatalf("TopLevel() error = %v", err)
	}
	if branch, detached, err := adapter.CurrentBranch(context, repository.Path); err != nil || detached || branch == "" {
		t.Fatalf("CurrentBranch() = (%q, %t, %v), want attached branch", branch, detached, err)
	}
	if head, err := adapter.Head(context, repository.Path); err != nil || head == "" {
		t.Fatalf("Head() = (%q, %v)", head, err)
	}
	if status, err := adapter.Status(context, repository.Path); err != nil || len(status.Entries) != 0 {
		t.Fatalf("Status() = (%#v, %v), want clean", status, err)
	}
	if clean, err := adapter.IsClean(context, repository.Path); err != nil || !clean {
		t.Fatalf("IsClean() = %t, %v; want true", clean, err)
	}
}

func TestAdapterChecksCommittedGitignoreAtRequestedRef(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile(".gitignore", "/api/\n/services/generated/\n", "ignore mounts")
	adapter := git.NewAdapter("git")
	ctx := context.Background()

	for _, path := range []string{"api", "services/generated"} {
		ignored, err := adapter.IsIgnoredAt(ctx, repository.Path, "HEAD", path)
		if err != nil || !ignored {
			t.Fatalf("IsIgnoredAt(%q) = %t, %v; want true, nil", path, ignored, err)
		}
	}
	ignored, err := adapter.IsIgnoredAt(ctx, repository.Path, "HEAD", "other")
	if err != nil || ignored {
		t.Fatalf("IsIgnoredAt(other) = %t, %v; want false, nil", ignored, err)
	}

	if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte("/other/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored, err = adapter.IsIgnoredAt(ctx, repository.Path, "HEAD", "other")
	if err != nil || ignored {
		t.Fatalf("IsIgnoredAt(other) with uncommitted rule = %t, %v; want false, nil", ignored, err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, ".git", "info", "exclude"), []byte("/other/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored, err = adapter.IsIgnoredAt(ctx, repository.Path, "HEAD", "other")
	if err != nil || ignored {
		t.Fatalf("IsIgnoredAt(other) with local exclude = %t, %v; want false, nil", ignored, err)
	}
}

func TestAdapterWorkingTreeIgnoreUsesGitSemanticsAndExcludesInfoRules(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile(".gitignore", "/literal\\[name\\]/\n", "ignore literal mount")
	adapter := git.NewAdapter("git")
	ignored, err := adapter.IsIgnoredWorkingTree(context.Background(), repository.Path, "literal[name]")
	if err != nil || !ignored {
		t.Fatalf("IsIgnoredWorkingTree(literal) = %t, %v", ignored, err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, ".git", "info", "exclude"), []byte("/excluded/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignored, err = adapter.IsIgnoredWorkingTree(context.Background(), repository.Path, "excluded")
	if err != nil || ignored {
		t.Fatalf("IsIgnoredWorkingTree(info/exclude) = %t, %v", ignored, err)
	}
}

func TestAdapterInspectsAndQualifiesWorkingTreeGitignoreEvidence(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")
	adapter := git.NewAdapter("git")

	if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte("/root-child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := adapter.InspectWorkingTreeIgnore(context.Background(), repository.Path, "root-child")
	if err != nil || !evidence.Ignored || evidence.Negated || !evidence.Qualifies(repository.Path) {
		t.Fatalf("root evidence = %#v, %v; want qualifying ignored .gitignore", evidence, err)
	}

	if err := os.MkdirAll(filepath.Join(repository.Path, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, "nested", ".gitignore"), []byte("/child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err = adapter.InspectWorkingTreeIgnore(context.Background(), repository.Path, "nested/child")
	if err != nil || !evidence.Ignored || !evidence.Qualifies(repository.Path) {
		t.Fatalf("deeper evidence = %#v, %v; want qualifying in-parent .gitignore", evidence, err)
	}

	if err := os.WriteFile(filepath.Join(repository.Path, ".git", "info", "exclude"), []byte("/info-only/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err = adapter.InspectWorkingTreeIgnore(context.Background(), repository.Path, "info-only")
	if err != nil || !evidence.Ignored || evidence.Qualifies(repository.Path) {
		t.Fatalf("info/exclude evidence = %#v, %v; want non-qualifying ignored result", evidence, err)
	}

	evidence, err = git.NewAdapter(filepath.Join(t.TempDir(), "missing-git")).InspectWorkingTreeIgnore(context.Background(), repository.Path, "root-child")
	if err == nil || evidence.Qualifies(repository.Path) {
		t.Fatalf("Git failure evidence = %#v, %v; want non-qualifying error", evidence, err)
	}
}

func TestAdapterWorkingTreeIgnoreRejectsConfiguredExcludeInsideCheckout(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")

	exclude := filepath.Join(repository.Path, "nested", ".gitignore")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte("/child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "config", "core.excludesFile", "nested/.gitignore")

	evidence, err := git.NewAdapter("git").InspectWorkingTreeIgnore(context.Background(), repository.Path, "nested/child")
	if err != nil || !evidence.Ignored || !evidence.ConfiguredExclude || evidence.Qualifies(repository.Path) {
		t.Fatalf("configured-exclude evidence = %#v, %v; want non-qualifying ignored evidence", evidence, err)
	}
}

func TestAdapterWorkingTreeIgnoreCapturesGitWinningDirectoryNegation(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")
	if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte("child/*\n!child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	adapter := git.NewAdapter("git")
	evidence, err := adapter.InspectWorkingTreeIgnore(context.Background(), repository.Path, "child")
	if err != nil || !evidence.Ignored || evidence.Pattern != "child/*" || evidence.Qualifies(repository.Path) {
		t.Fatalf("absent child evidence = %#v, %v; want recorded but non-qualifying non-directory evidence", evidence, err)
	}

	if err := os.Mkdir(filepath.Join(repository.Path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	evidence, err = adapter.InspectWorkingTreeIgnore(context.Background(), repository.Path, "child")
	if err != nil || evidence.Ignored || !evidence.Negated || evidence.Pattern != "!child/" || evidence.Qualifies(repository.Path) {
		t.Fatalf("negation evidence = %#v, %v; want real non-qualifying negation", evidence, err)
	}
}

func TestAdapterWorkingTreeIgnoreDoesNotQualifyAbsentDirectoryBeforeNegationBecomesEffective(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")
	if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte("*\n!child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	adapter := git.NewAdapter("git")
	evidence, err := adapter.InspectWorkingTreeIgnore(context.Background(), repository.Path, "child")
	if err != nil || !evidence.Ignored || evidence.Pattern != "*" || evidence.Qualifies(repository.Path) {
		t.Fatalf("absent child evidence = %#v, %v; want recorded but non-qualifying broad evidence", evidence, err)
	}

	if err := os.Mkdir(filepath.Join(repository.Path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	evidence, err = adapter.InspectWorkingTreeIgnore(context.Background(), repository.Path, "child")
	if err != nil || evidence.Ignored || !evidence.Negated || evidence.Pattern != "!child/" || evidence.Qualifies(repository.Path) {
		t.Fatalf("created child evidence = %#v, %v; want Git's effective non-qualifying negation", evidence, err)
	}
}

func TestAdapterWorkingTreeIgnoreQualifiesBroadPatternForExistingDirectory(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")
	if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository.Path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}

	evidence, err := git.NewAdapter("git").InspectWorkingTreeIgnore(context.Background(), repository.Path, "child")
	if err != nil || !evidence.Ignored || evidence.Negated || evidence.Pattern != "*" || !evidence.DirectoryObserved || !evidence.Qualifies(repository.Path) {
		t.Fatalf("existing child evidence = %#v, %v; want qualifying Git-effective broad pattern", evidence, err)
	}
}

func TestAdapterWorkingTreeIgnoreQualifiesBroadPatternForExistingNestedDirectory(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")
	if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository.Path, "nested", "child"), 0o700); err != nil {
		t.Fatal(err)
	}

	evidence, err := git.NewAdapter("git").InspectWorkingTreeIgnore(context.Background(), repository.Path, "nested/child")
	if err != nil || !evidence.Ignored || evidence.Negated || evidence.Pattern != "*" || !evidence.DirectoryObserved || !evidence.Qualifies(repository.Path) {
		t.Fatalf("existing nested child evidence = %#v, %v; want qualifying Git-effective broad pattern", evidence, err)
	}
}

func TestAdapterWorkingTreeIgnoreDoesNotQualifyBroadPatternWhenDirectoryMovesDuringGitProbe(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")
	if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte("*\n!child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(repository.Path, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}

	checkIgnoreCalls := filepath.Join(t.TempDir(), "check-ignore-calls")
	lsFilesCalls := filepath.Join(t.TempDir(), "ls-files-calls")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := fmt.Sprintf(`#!/bin/sh
for argument in "$@"; do
	if [ "$argument" = check-ignore ]; then
		calls=0
		if [ -f %q ]; then calls=$(cat %q); fi
		calls=$((calls + 1))
		printf '%%s' "$calls" > %q
		if [ "$calls" -eq 1 ]; then
			mv %q %q
			git "$@"
			status=$?
			mv %q %q
			exit "$status"
		fi
		break
	fi
	if [ "$argument" = ls-files ]; then
		printf '1' > %q
		break
	fi
done
exec git "$@"
`, checkIgnoreCalls, checkIgnoreCalls, checkIgnoreCalls, child, child+".saved", child+".saved", child, lsFilesCalls)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	evidence, err := git.NewAdapter(wrapper).InspectWorkingTreeIgnore(context.Background(), repository.Path, "child")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Qualifies(repository.Path) {
		t.Fatalf("moved-directory evidence = %#v; want no protection so generated-rule planning is required", evidence)
	}
	calls, err := os.ReadFile(checkIgnoreCalls)
	if err != nil || string(calls) != "1" {
		t.Fatalf("check-ignore calls = %q, %v; want one first probe", calls, err)
	}
	if calls, err := os.ReadFile(lsFilesCalls); err != nil || string(calls) != "1" {
		t.Fatalf("ls-files calls = %q, %v; want Git-owned directory validation after first-probe restoration", calls, err)
	}

	evidence, err = git.NewAdapter("git").InspectWorkingTreeIgnore(context.Background(), repository.Path, "child")
	if err != nil || evidence.Ignored || !evidence.Negated || evidence.Pattern != "!child/" || evidence.Qualifies(repository.Path) {
		t.Fatalf("restored child evidence = %#v, %v; want Git's winning non-qualifying directory negation", evidence, err)
	}
}

func TestAdapterWorkingTreeIgnoreDecodesNULDelimitedUnicodeSource(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "initial\n", "initial")
	sourceDirectory := "deeper:12:\t\u4e16\u754c"
	if err := os.MkdirAll(filepath.Join(repository.Path, sourceDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, sourceDirectory, ".gitignore"), []byte("/child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mount := sourceDirectory + "/child"

	evidence, err := git.NewAdapter("git").InspectWorkingTreeIgnore(context.Background(), repository.Path, mount)
	if err != nil || !evidence.Ignored || evidence.Source != sourceDirectory+"/.gitignore" || !evidence.Qualifies(repository.Path) {
		t.Fatalf("NUL-delimited evidence = %#v, %v; want qualifying Unicode source", evidence, err)
	}
}

func TestAdapterCanonicalizesCommonGitDirFromSymlinkedCheckout(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme.txt", "initial\n", "initial")
	link := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(repository.Path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	commonDir, err := git.NewAdapter("git").CommonGitDir(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if commonDir != canonical || !filepath.IsAbs(commonDir) {
		t.Errorf("CommonGitDir() = %q, want canonical absolute %q", commonDir, canonical)
	}
}

func TestAdapterIgnoresHostileGlobalConfiguration(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme.txt", "initial\n", "initial")
	hostileConfig := filepath.Join(t.TempDir(), "hostile.gitconfig")
	if err := os.WriteFile(hostileConfig, []byte("[core]\n\tbare = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "GIT_CONFIG_GLOBAL="+hostileConfig, "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	adapter := git.NewAdapterWithEnv("git", environment)

	if _, err := adapter.Status(context.Background(), repository.Path); err != nil {
		t.Fatalf("Status() honored hostile global configuration: %v", err)
	}
}

func TestAdapterCoversWorktreeDirtyDetachedAndMissingRefFacts(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	worktreePath := filepath.Join(t.TempDir(), "worktree with space")
	repository.Run(t, "branch", "feature")
	repository.Run(t, "worktree", "add", worktreePath, "feature")

	adapter := git.NewAdapter("git")
	context := context.Background()
	commonDir, err := adapter.CommonGitDir(context, repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	worktreeCommonDir, err := adapter.CommonGitDir(context, worktreePath)
	if err != nil || worktreeCommonDir != commonDir {
		t.Fatalf("worktree common dir = %q, %v; want %q", worktreeCommonDir, err, commonDir)
	}
	if checkedOut, err := adapter.BranchCheckedOut(context, repository.Path, "feature"); err != nil || !checkedOut {
		t.Fatalf("BranchCheckedOut() = %t, %v; want true", checkedOut, err)
	}

	if err := os.WriteFile(filepath.Join(repository.Path, "tracked.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "add", "tracked.txt")
	if err := os.WriteFile(filepath.Join(repository.Path, "tracked.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := adapter.Status(context, repository.Path)
	if err != nil || !status.Staged || !status.Modified || !status.Untracked {
		t.Fatalf("Status() = %#v, %v; want staged/modified/untracked", status, err)
	}
	if _, err := adapter.ResolveRef(context, repository.Path, "refs/heads/not-a-ref"); err == nil {
		t.Fatal("ResolveRef() error = nil, want invalid ref error")
	}

	repository.Run(t, "checkout", "--detach", "HEAD")
	if branch, detached, err := adapter.CurrentBranch(context, repository.Path); err != nil || !detached || branch != "" {
		t.Fatalf("CurrentBranch() = (%q, %t, %v), want detached", branch, detached, err)
	}
}

func TestAdapterReportsOptionalUpstreamAheadBehind(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	adapter := git.NewAdapter("git")

	if ahead, behind, upstream, err := adapter.AheadBehind(context.Background(), repository.Path); err != nil || upstream || ahead != 0 || behind != 0 {
		t.Fatalf("AheadBehind without upstream = (%d, %d, %t, %v)", ahead, behind, upstream, err)
	}
	repository.Run(t, "branch", "upstream/main")
	repository.Run(t, "config", "branch.main.remote", ".")
	repository.Run(t, "config", "branch.main.merge", "refs/heads/upstream/main")
	repository.CommitFile("ahead.txt", "ahead\n", "ahead")
	ahead, behind, upstream, err := adapter.AheadBehind(context.Background(), repository.Path)
	if err != nil || !upstream || ahead != 1 || behind != 0 {
		t.Fatalf("AheadBehind with upstream = (%d, %d, %t, %v), want (1, 0, true, nil)", ahead, behind, upstream, err)
	}
}

func TestAdapterReportsWhetherBranchIsMergedIntoSourceHEAD(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	repository.Run(t, "branch", "merged")
	repository.Run(t, "branch", "unmerged")
	worktree := filepath.Join(t.TempDir(), "unmerged")
	repository.Run(t, "worktree", "add", worktree, "unmerged")
	testutil.GitRepository{Path: worktree}.CommitFile("unmerged.txt", "change\n", "change")
	adapter := git.NewAdapter("git")
	if merged, err := adapter.BranchMerged(context.Background(), repository.Path, "merged"); err != nil || !merged {
		t.Fatalf("BranchMerged merged = %t, %v", merged, err)
	}
	if merged, err := adapter.BranchMerged(context.Background(), repository.Path, "unmerged"); err != nil || merged {
		t.Fatalf("BranchMerged unmerged = %t, %v", merged, err)
	}
}

func TestAdapterStatusUsesOptionalLocksAndLeavesIndexMetadataUntouched(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	tracked := filepath.Join(repository.Path, "tracked.txt")
	if err := os.Chtimes(tracked, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(repository.Path, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	observed := filepath.Join(t.TempDir(), "optional-locks")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$GIT_OPTIONAL_LOCKS\" > %q\nexec git \"$@\"\n", observed)), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git.NewAdapter(wrapper).Status(context.Background(), repository.Path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("status changed index metadata: before=%#v after=%#v", before, after)
	}
	value, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "0" {
		t.Fatalf("GIT_OPTIONAL_LOCKS = %q, want 0", value)
	}
}
