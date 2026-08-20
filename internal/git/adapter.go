package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxStderr = 8192

// Git is the complete Git boundary used by application code.
type Git interface {
	CommonGitDir(context.Context, string) (string, error)
	TopLevel(context.Context, string) (string, error)
	Head(context.Context, string) (string, error)
	ValidBranchName(context.Context, string, string) (bool, error)
	CurrentBranch(context.Context, string) (string, bool, error)
	ResolveRef(context.Context, string, string) (string, error)
	ListWorktrees(context.Context, string) ([]Worktree, error)
	Status(context.Context, string) (Status, error)
	StatusIncludingIgnored(context.Context, string) (Status, error)
	IsClean(context.Context, string) (bool, error)
	InspectWorkingTreeIgnore(context.Context, string, string) (WorkingTreeIgnoreEvidence, error)
	IsIgnoredAt(context.Context, string, string, string) (bool, error)
	IsIgnoredWorkingTree(context.Context, string, string) (bool, error)
	BranchExists(context.Context, string, string) (bool, error)
	BranchMerged(context.Context, string, string) (bool, error)
	BranchCheckedOut(context.Context, string, string) (bool, error)
	CreateBranch(context.Context, string, string, string) error
	DeleteBranch(context.Context, string, string, bool) error
	AddWorktree(context.Context, string, string, string) error
	RemoveWorktree(context.Context, string, string, bool) error
	WorktreePrune(context.Context, string) error
	WorktreeRepair(context.Context, string) error
	HasSubmodules(context.Context, string) (bool, error)
	AheadBehind(context.Context, string) (ahead, behind int, upstream bool, err error)
	Upstream(context.Context, string) (Upstream, error)
	PublishedUpstream(context.Context, string) (Upstream, error)
	PublishedRepositoryFacts(context.Context, string) (PublishedRepositoryFacts, error)
	AdvertisedCommit(context.Context, string, string) (string, error)
	InitialCommits(context.Context, string, string) ([]string, error)
	ContainsCommits(context.Context, string, []string) (bool, error)
	TrackedFile(context.Context, string, string, string) ([]byte, error)
	Clone(context.Context, string, string, string) error
	FetchTrackingBranch(context.Context, string, string, string) error
	CheckoutTrackingBranch(context.Context, string, string, string, string) (string, error)
}

// Adapter invokes Git only through locale-neutral, non-interactive subprocesses.
type Adapter struct {
	binary string
	env    []string
}

// NewAdapter constructs an adapter using binary, or git when binary is empty.
func NewAdapter(binary string) *Adapter {
	return NewAdapterWithEnv(binary, os.Environ())
}

// NewAdapterWithEnv creates an adapter with a sanitized injected environment.
func NewAdapterWithEnv(binary string, environment []string) *Adapter {
	if binary == "" {
		binary = "git"
	}
	return &Adapter{binary: binary, env: sanitizedEnvironment(environment)}
}

// Error preserves actionable command context without unbounded stderr output.
type Error struct {
	Command    []string
	Repository string
	ExitCode   int
	Stderr     string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git -C %q %s failed with exit status %d: %s", e.Repository, strings.Join(e.Command, " "), e.ExitCode, e.Stderr)
}

func (a *Adapter) CommonGitDir(ctx context.Context, repository string) (string, error) {
	value, err := a.valueFact(ctx, repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("make common Git directory absolute: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("canonicalize common Git directory: %w", err)
	}
	return canonical, nil
}
func (a *Adapter) TopLevel(ctx context.Context, repository string) (string, error) {
	return a.valueFact(ctx, repository, "rev-parse", "--show-toplevel")
}
func (a *Adapter) Head(ctx context.Context, repository string) (string, error) {
	return a.valueFact(ctx, repository, "rev-parse", "--verify", "HEAD")
}

// ValidBranchName delegates the complete ref grammar to Git. The command is
// repository-scoped so it uses the same hardened subprocess boundary as every
// other Git fact.
func (a *Adapter) ValidBranchName(ctx context.Context, repository, branch string) (bool, error) {
	command := exec.CommandContext(ctx, a.binary, "check-ref-format", "--branch", branch)
	command.Env = a.factEnvironment()
	_, err := command.Output()
	if err == nil {
		if _, err := a.runFact(ctx, repository, "rev-parse", "--is-inside-work-tree"); err != nil {
			return false, err
		}
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 128 {
		return false, nil
	}
	return false, &Error{Command: []string{"check-ref-format", "--branch", branch}, ExitCode: exitCode(err), Stderr: boundedStderr(exitStderr(err))}
}

func exitCode(err error) int {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 1
}

func exitStderr(err error) string {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return string(exitError.Stderr)
	}
	return ""
}
func (a *Adapter) ResolveRef(ctx context.Context, repository, ref string) (string, error) {
	return a.valueFact(ctx, repository, "rev-parse", "--verify", ref+"^{commit}")
}
func (a *Adapter) ListWorktrees(ctx context.Context, repository string) ([]Worktree, error) {
	output, err := a.runFact(ctx, repository, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(output)
}
func (a *Adapter) Status(ctx context.Context, repository string) (Status, error) {
	output, err := a.runFact(ctx, repository, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(output)
}

// StatusIncludingIgnored reports all checkout dirt, including ignored
// untracked files. Transaction cleanup uses it before force-removing a
// worktree so an unrelated ignored file is never discarded as clean state.
func (a *Adapter) StatusIncludingIgnored(ctx context.Context, repository string) (Status, error) {
	output, err := a.runFact(ctx, repository, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(output)
}
func (a *Adapter) IsClean(ctx context.Context, repository string) (bool, error) {
	status, err := a.Status(ctx, repository)
	if err != nil {
		return false, err
	}
	return len(status.Entries) == 0, nil
}

// IsIgnoredAt reports whether path is ignored by a committed .gitignore in
// ref. It deliberately excludes uncommitted rules and repository-local or
// global excludes because those rules will not protect a newly added worktree.
func (a *Adapter) IsIgnoredAt(ctx context.Context, repository, ref, path string) (bool, error) {
	commit, err := a.ResolveRef(ctx, repository, ref)
	if err != nil {
		return false, err
	}
	worktree, err := os.MkdirTemp("", "wtree-ignore-")
	if err != nil {
		return false, fmt.Errorf("create temporary ignore worktree: %w", err)
	}
	defer os.RemoveAll(worktree)

	mount := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	directories := []string{"."}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(mount)))
	if parent != "." {
		current := ""
		for _, component := range strings.Split(parent, "/") {
			current = filepath.ToSlash(filepath.Join(current, component))
			directories = append(directories, current)
		}
	}
	for _, directory := range directories {
		ignorePath := filepath.ToSlash(filepath.Join(directory, ".gitignore"))
		if directory == "." {
			ignorePath = ".gitignore"
		}
		contents, showErr := a.runFact(ctx, repository, "show", commit+":"+ignorePath)
		if showErr == nil {
			destination := filepath.Join(worktree, filepath.FromSlash(ignorePath))
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return false, fmt.Errorf("prepare temporary ignore worktree: %w", err)
			}
			if err := os.WriteFile(destination, contents, 0o600); err != nil {
				return false, fmt.Errorf("write temporary ignore rules: %w", err)
			}
		} else if !isMissingObject(showErr) {
			return false, showErr
		}
	}

	// A trailing slash makes check-ignore evaluate the mount as a directory,
	// including the common anchored rule /mount/ before the directory exists.
	mount += "/"
	output, err := a.runFact(ctx, repository, "--work-tree="+worktree, "check-ignore", "-v", "--no-index", "--", mount)
	if err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	metadata, _, found := strings.Cut(strings.TrimSpace(string(output)), "\t")
	if !found {
		return false, fmt.Errorf("parse check-ignore output")
	}
	source, parseErr := checkIgnoreSource(metadata)
	if parseErr != nil {
		return false, fmt.Errorf("parse check-ignore source")
	}
	return isRootGitIgnoreSource(source, worktree), nil
}

// IsIgnoredWorkingTree reports only a rule sourced from the repository's
// .gitignore; info/global excludes never qualify authoring output.
func (a *Adapter) IsIgnoredWorkingTree(ctx context.Context, repository, path string) (bool, error) {
	output, err := a.runFact(ctx, repository, "check-ignore", "-v", "--no-index", "--", filepath.ToSlash(path)+"/")
	if err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	metadata, _, found := strings.Cut(strings.TrimSpace(string(output)), "\t")
	if !found {
		return false, fmt.Errorf("parse check-ignore output")
	}
	source, parseErr := checkIgnoreSource(metadata)
	if parseErr != nil {
		return false, fmt.Errorf("parse check-ignore output")
	}
	root, rootErr := a.TopLevel(ctx, repository)
	if rootErr != nil {
		return false, rootErr
	}
	return isRootGitIgnoreSource(source, root), nil
}

func checkIgnoreSource(metadata string) (string, error) {
	for index := 0; index < len(metadata); index++ {
		if metadata[index] != ':' {
			continue
		}
		end := index + 1
		for end < len(metadata) && metadata[end] >= '0' && metadata[end] <= '9' {
			end++
		}
		if end > index+1 && end < len(metadata) && metadata[end] == ':' {
			return metadata[:index], nil
		}
	}
	return "", fmt.Errorf("missing ignore source")
}

func isRootGitIgnoreSource(source, root string) bool {
	source = filepath.Clean(filepath.FromSlash(source))
	if source == ".gitignore" {
		return true
	}
	if !filepath.IsAbs(source) {
		return false
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return false
	}
	return canonicalSource == filepath.Join(canonicalRoot, ".gitignore")
}

func isMissingObject(err error) bool {
	var gitError *Error
	return errors.As(err, &gitError) && gitError.ExitCode == 128
}
func (a *Adapter) CreateBranch(ctx context.Context, repository, branch, base string) error {
	_, err := a.run(ctx, repository, "branch", branch, base)
	return err
}
func (a *Adapter) DeleteBranch(ctx context.Context, repository, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := a.run(ctx, repository, "branch", flag, branch)
	return err
}
func (a *Adapter) AddWorktree(ctx context.Context, repository, path, branch string) error {
	_, err := a.run(ctx, repository, "worktree", "add", path, branch)
	return err
}
func (a *Adapter) RemoveWorktree(ctx context.Context, repository, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := a.run(ctx, repository, args...)
	return err
}
func (a *Adapter) WorktreePrune(ctx context.Context, repository string) error {
	_, err := a.run(ctx, repository, "worktree", "prune")
	return err
}
func (a *Adapter) WorktreeRepair(ctx context.Context, repository string) error {
	_, err := a.run(ctx, repository, "worktree", "repair")
	return err
}
func (a *Adapter) HasSubmodules(ctx context.Context, repository string) (bool, error) {
	output, err := a.runFact(ctx, repository, "ls-files", "--stage", "-z")
	if err != nil {
		return false, err
	}
	for _, entry := range strings.Split(string(output), "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return true, nil
		}
	}
	return false, nil
}

// AheadBehind returns counts relative to the current branch upstream. A
// branch without an upstream is reported as upstream=false, not an error.
func (a *Adapter) AheadBehind(ctx context.Context, repository string) (int, int, bool, error) {
	if _, err := a.valueFact(ctx, repository, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 128 {
			return 0, 0, false, nil
		}
		return 0, 0, false, err
	}
	output, err := a.runFact(ctx, repository, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return 0, 0, false, err
	}
	var ahead, behind int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(output)), "%d\t%d", &ahead, &behind); err != nil {
		return 0, 0, false, fmt.Errorf("parse ahead/behind: %w", err)
	}
	return ahead, behind, true, nil
}
func (a *Adapter) CurrentBranch(ctx context.Context, repository string) (string, bool, error) {
	output, err := a.runFact(ctx, repository, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 1 {
			return "", true, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(output)), false, nil
}
func (a *Adapter) BranchExists(ctx context.Context, repository, branch string) (bool, error) {
	_, err := a.runFact(ctx, repository, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// BranchMerged reports whether branch is reachable from the repository's
// current HEAD, the conservative default merge target for delete preflight.
func (a *Adapter) BranchMerged(ctx context.Context, repository, branch string) (bool, error) {
	_, err := a.runFact(ctx, repository, "merge-base", "--is-ancestor", "refs/heads/"+branch, "HEAD")
	if err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
func (a *Adapter) BranchCheckedOut(ctx context.Context, repository, branch string) (bool, error) {
	worktrees, err := a.ListWorktrees(ctx, repository)
	if err != nil {
		return false, err
	}
	for _, worktree := range worktrees {
		if worktree.Branch == branch {
			return true, nil
		}
	}
	return false, nil
}
func (a *Adapter) valueFact(ctx context.Context, repository string, args ...string) (string, error) {
	output, err := a.runFact(ctx, repository, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
func (a *Adapter) run(ctx context.Context, repository string, args ...string) ([]byte, error) {
	return a.runWithEnvironment(ctx, repository, a.env, args...)
}

func (a *Adapter) runFact(ctx context.Context, repository string, args ...string) ([]byte, error) {
	return a.runWithEnvironment(ctx, repository, a.factEnvironment(), args...)
}

func (a *Adapter) runFactInput(ctx context.Context, repository string, input []byte, args ...string) ([]byte, error) {
	return a.runWithEnvironmentInput(ctx, repository, a.factEnvironment(), input, args...)
}

func (a *Adapter) factEnvironment() []string {
	return append(append([]string(nil), a.env...), "GIT_OPTIONAL_LOCKS=0")
}

func (a *Adapter) runWithEnvironment(ctx context.Context, repository string, environment []string, args ...string) ([]byte, error) {
	return a.runWithEnvironmentInput(ctx, repository, environment, nil, args...)
}

func (a *Adapter) runWithEnvironmentInput(ctx context.Context, repository string, environment []string, input []byte, args ...string) ([]byte, error) {
	commandArgs := append([]string(nil), args...)
	if repository != "" {
		commandArgs = append([]string{"-C", repository}, commandArgs...)
	}
	command := exec.CommandContext(ctx, a.binary, commandArgs...)
	command.Env = environment
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	stderr := ""
	exitCode := -1
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitCode()
		stderr = string(exitError.Stderr)
	}
	return nil, &Error{Command: redactGitArguments(args), Repository: repository, ExitCode: exitCode, Stderr: redactGitText(boundedStderr(stderr))}
}

func redactGitArguments(args []string) []string {
	redacted := make([]string, len(args))
	for index, arg := range args {
		redacted[index] = redactGitText(arg)
	}
	return redacted
}

var _ Git = (*Adapter)(nil)

func sanitizedEnvironment(environment []string) []string {
	allowed := map[string]bool{"PATH": true, "SystemRoot": true, "WINDIR": true, "ComSpec": true, "TMP": true, "TEMP": true, "TMPDIR": true}
	values := make(map[string]string)
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if found && allowed[key] {
			values[key] = value
		}
	}
	values["HOME"] = ""
	values["XDG_CONFIG_HOME"] = ""
	values["GIT_CONFIG_GLOBAL"] = os.DevNull
	values["GIT_CONFIG_NOSYSTEM"] = "1"
	values["GIT_TERMINAL_PROMPT"] = "0"
	values["GIT_ASKPASS"] = ""
	values["GIT_ATTR_NOSYSTEM"] = "1"
	values["LC_ALL"] = "C"
	values["LANG"] = "C"
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func boundedStderr(stderr string) string {
	if len(stderr) > maxStderr {
		stderr = stderr[:maxStderr]
	}
	return strings.TrimSpace(stderr)
}
