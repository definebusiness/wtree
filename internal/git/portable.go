package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ErrRemoteRefNotFound reports an advertised remote reference that does not
// exist. It lets planning distinguish a valid remote with a missing branch
// from a failed transport or authentication command.
var ErrRemoteRefNotFound = errors.New("remote reference not found")

// Upstream is the complete configured tracking fact for the current branch.
// Merge is always the full remote reference (for example refs/heads/main).
type Upstream struct {
	LocalBranch string
	Remote      string
	Merge       string
	FetchURL    string
}

// PublishedRepositoryFacts is one stable authoring observation. All values
// are re-read before it is returned, so callers never combine separate Git
// generations while publishing a portable manifest.
type PublishedRepositoryFacts struct {
	Upstream Upstream
	Head     string
	Roots    []string
}

// publishedFactsBeforeRevalidation is a package-private controlled test seam.
// It is deliberately invoked only after the first complete observation and
// before its complete revalidation; production leaves it nil.
var publishedFactsBeforeRevalidation func()

func (a *Adapter) PublishedRepositoryFacts(ctx context.Context, repository string) (PublishedRepositoryFacts, error) {
	first, err := a.PublishedUpstream(ctx, repository)
	if err != nil {
		return PublishedRepositoryFacts{}, err
	}
	head, err := a.Head(ctx, repository)
	if err != nil {
		return PublishedRepositoryFacts{}, err
	}
	roots, err := a.InitialCommits(ctx, repository, head)
	if err != nil {
		return PublishedRepositoryFacts{}, err
	}
	if publishedFactsBeforeRevalidation != nil {
		publishedFactsBeforeRevalidation()
	}
	second, err := a.PublishedUpstream(ctx, repository)
	if err != nil {
		return PublishedRepositoryFacts{}, err
	}
	secondHead, err := a.Head(ctx, repository)
	if err != nil {
		return PublishedRepositoryFacts{}, err
	}
	secondRoots, err := a.InitialCommits(ctx, repository, secondHead)
	if err != nil {
		return PublishedRepositoryFacts{}, err
	}
	if first != second || head != secondHead || !sameObjectIDs(roots, secondRoots) {
		return PublishedRepositoryFacts{}, fmt.Errorf("published repository facts changed during capture")
	}
	return PublishedRepositoryFacts{Upstream: first, Head: head, Roots: roots}, nil
}

func sameObjectIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// Upstream obtains the current attached branch's unambiguous configured
// upstream and that remote's fetch URL. It never assumes a remote named
// origin, and rejects incomplete or malformed configuration.
func (a *Adapter) Upstream(ctx context.Context, repository string) (Upstream, error) {
	branch, detached, err := a.CurrentBranch(ctx, repository)
	if err != nil {
		return Upstream{}, err
	}
	if detached {
		return Upstream{}, fmt.Errorf("discover upstream for %q: HEAD is detached", repository)
	}
	remote, err := a.singleConfig(ctx, repository, "branch."+branch+".remote")
	if err != nil {
		return Upstream{}, fmt.Errorf("discover upstream remote for branch %q: %w", branch, err)
	}
	if remote == "." || strings.HasPrefix(remote, "-") {
		return Upstream{}, fmt.Errorf("discover upstream remote for branch %q: invalid remote", branch)
	}
	merge, err := a.singleConfig(ctx, repository, "branch."+branch+".merge")
	if err != nil {
		return Upstream{}, fmt.Errorf("discover upstream merge for branch %q: %w", branch, err)
	}
	if !strings.HasPrefix(merge, "refs/heads/") || len(strings.TrimPrefix(merge, "refs/heads/")) == 0 {
		return Upstream{}, fmt.Errorf("discover upstream merge for branch %q: invalid merge ref", branch)
	}
	// get-url without --push returns the configured fetch URL.
	fetchURL, err := a.valueFact(ctx, repository, "remote", "get-url", "--", remote)
	if err != nil {
		return Upstream{}, fmt.Errorf("discover fetch URL for remote %q: %w", remote, err)
	}
	if fetchURL == "" {
		return Upstream{}, fmt.Errorf("discover fetch URL for remote %q: empty URL", remote)
	}
	return Upstream{LocalBranch: branch, Remote: remote, Merge: merge, FetchURL: fetchURL}, nil
}

// PublishedUpstream verifies that the current local HEAD is exactly the
// advertised commit of its configured upstream. It rejects a locally ahead,
// behind, diverged, deleted, or otherwise unpublished branch before an init
// caller can author a moving-branch manifest.
func (a *Adapter) PublishedUpstream(ctx context.Context, repository string) (Upstream, error) {
	upstream, err := a.Upstream(ctx, repository)
	if err != nil {
		return Upstream{}, err
	}
	head, err := a.Head(ctx, repository)
	if err != nil {
		return Upstream{}, err
	}
	advertised, err := a.AdvertisedCommit(ctx, upstream.FetchURL, upstream.Merge)
	if err != nil {
		return Upstream{}, fmt.Errorf("verify published upstream for branch %q: %w", upstream.LocalBranch, err)
	}
	if head != advertised {
		return Upstream{}, fmt.Errorf("verify published upstream for branch %q: local HEAD does not equal advertised upstream", upstream.LocalBranch)
	}
	return upstream, nil
}

func (a *Adapter) singleConfig(ctx context.Context, repository, key string) (string, error) {
	output, err := a.runFact(ctx, repository, "config", "--get-all", key)
	if err != nil {
		return "", err
	}
	values := strings.Fields(string(output))
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("expected exactly one configured value")
	}
	return values[0], nil
}

// AdvertisedCommit resolves exactly one full commit advertised for ref without
// cloning. url is a repository clone transport; manifest-source URLs are
// deliberately outside this Git boundary.
func (a *Adapter) AdvertisedCommit(ctx context.Context, url, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !strings.HasPrefix(ref, "refs/heads/") || strings.TrimPrefix(ref, "refs/heads/") == "" {
		return "", fmt.Errorf("resolve advertised commit: invalid remote branch ref")
	}
	output, err := a.runRemoteFact(ctx, "ls-remote", "--refs", "--", url, ref)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "", fmt.Errorf("%w: %s", ErrRemoteRefNotFound, ref)
	}
	if len(lines) != 1 {
		return "", fmt.Errorf("parse advertised remote ref: expected one result")
	}
	commit, actualRef, found := strings.Cut(lines[0], "\t")
	if !found || actualRef != ref || !fullObjectID(commit) {
		return "", fmt.Errorf("parse advertised remote ref")
	}
	return commit, nil
}

// InitialCommits reports all root commits reachable from ref in lexical order.
func (a *Adapter) InitialCommits(ctx context.Context, repository, ref string) ([]string, error) {
	output, err := a.runFact(ctx, repository, "rev-list", "--max-parents=0", ref)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for _, value := range strings.Fields(string(output)) {
		if !fullObjectID(value) {
			return nil, fmt.Errorf("parse initial commits")
		}
		seen[value] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("discover initial commits: no roots")
	}
	commits := make([]string, 0, len(seen))
	for commit := range seen {
		commits = append(commits, commit)
	}
	sort.Strings(commits)
	return commits, nil
}

// ContainsCommits verifies that every full immutable identity commit is
// reachable from the checked-out HEAD. It intentionally permits additional roots:
// portable verification guards against a plausible wrong repository without
// claiming to distinguish a legitimate fork that shares all recorded roots.
func (a *Adapter) ContainsCommits(ctx context.Context, repository string, commits []string) (bool, error) {
	if len(commits) == 0 {
		return false, fmt.Errorf("verify identity commits: empty commit set")
	}
	for _, commit := range commits {
		if !fullObjectID(commit) {
			return false, fmt.Errorf("verify identity commits: invalid commit")
		}
		if _, err := a.runFact(ctx, repository, "merge-base", "--is-ancestor", commit, "HEAD"); err != nil {
			var gitError *Error
			if errors.As(err, &gitError) && (gitError.ExitCode == 1 || gitError.ExitCode == 128) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// TrackedFile returns a file's exact bytes from commit rather than the working
// tree, binding manifest verification to the selected object.
func (a *Adapter) TrackedFile(ctx context.Context, repository, commit, path string) ([]byte, error) {
	if !fullObjectID(commit) || path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") {
		return nil, fmt.Errorf("read tracked file: invalid commit or path")
	}
	return a.runFact(ctx, repository, "show", commit+":"+path)
}

// Clone creates a destination with the requested remote name but does not
// select an implicit remote branch. FetchTrackingBranch and
// CheckoutTrackingBranch establish the manifest-selected branch afterwards.
func (a *Adapter) Clone(ctx context.Context, url, destination, remote string) error {
	if remote == "" || strings.HasPrefix(remote, "-") {
		return fmt.Errorf("clone repository: invalid remote name")
	}
	if _, err := a.runRemote(ctx, quiescentGitArgs("init", "--quiet", "--", destination)...); err != nil {
		return err
	}
	_, err := a.run(ctx, destination, quiescentGitArgs("remote", "add", "--", remote, url)...)
	return err
}

// FetchTrackingBranch obtains the manifest-selected remote branch at
// execution time. It deliberately fetches the named ref rather than an
// observed commit or the remote symbolic HEAD.
func (a *Adapter) FetchTrackingBranch(ctx context.Context, repository, remote, merge string) error {
	remoteBranch := strings.TrimPrefix(merge, "refs/heads/")
	if remote == "" || strings.HasPrefix(remote, "-") || remoteBranch == merge || remoteBranch == "" {
		return fmt.Errorf("fetch selected branch: invalid remote or merge ref")
	}
	_, err := a.run(ctx, repository, quiescentGitArgs("fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", remote, "+"+merge+":refs/remotes/"+remote+"/"+remoteBranch)...)
	return err
}

// CheckoutTrackingBranch creates the one manifest-selected local branch from
// the execution-time fetched remote-tracking ref and configures its declared
// upstream. Remote HEAD never selects or survives as a local branch.
func (a *Adapter) CheckoutTrackingBranch(ctx context.Context, repository, localBranch, remote, merge string) (string, error) {
	remoteBranch := strings.TrimPrefix(merge, "refs/heads/")
	if localBranch == "" || strings.HasPrefix(localBranch, "-") || remote == "" || strings.HasPrefix(remote, "-") || remoteBranch == merge || remoteBranch == "" {
		return "", fmt.Errorf("configure checkout: invalid branch, remote, or merge ref")
	}
	// Command-scope configuration takes precedence over repository, global, and
	// system config. os.DevNull makes hook suppression portable across POSIX and
	// Windows without a shell or user-controlled path.
	if _, err := a.run(ctx, repository, quiescentGitArgs("-c", "core.hooksPath="+os.DevNull, "checkout", "--no-recurse-submodules", "--no-guess", "-B", localBranch, "refs/remotes/"+remote+"/"+remoteBranch)...); err != nil {
		return "", err
	}
	if _, err := a.run(ctx, repository, "config", "branch."+localBranch+".remote", remote); err != nil {
		return "", err
	}
	if _, err := a.run(ctx, repository, "config", "branch."+localBranch+".merge", merge); err != nil {
		return "", err
	}
	selectedRef := "refs/heads/" + localBranch
	output, err := a.runFact(ctx, repository, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil {
		return "", err
	}
	branches := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	for _, branch := range branches {
		if branch == "" || branch == selectedRef {
			continue
		}
		if _, err := a.run(ctx, repository, "update-ref", "-d", branch); err != nil {
			return "", err
		}
	}
	if output, err = a.runFact(ctx, repository, "for-each-ref", "--format=%(refname)", "refs/heads"); err != nil || strings.TrimSpace(string(output)) != selectedRef {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("configure checkout: expected exactly local branch %q", localBranch)
	}
	head, err := a.Head(ctx, repository)
	if err != nil {
		return "", err
	}
	return head, nil
}

// quiescentGitArgs prevents Git's background maintenance from mutating a
// managed staging checkout between a command and its strict tree inventory.
// These are command-scoped overrides: no repository or user configuration is
// changed, and every clone/fetch/checkout call follows the same safe contract.
func quiescentGitArgs(args ...string) []string {
	return append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)
}

// runRemote performs a potentially mutating remote operation such as clone.
// It deliberately does not add read-only optional-lock semantics.
func (a *Adapter) runRemote(ctx context.Context, args ...string) ([]byte, error) {
	return a.runRemoteWithEnvironment(ctx, a.env, args...)
}

// runRemoteFact performs read-only remote discovery with the same optional
// lock protection used by repository-scoped facts.
func (a *Adapter) runRemoteFact(ctx context.Context, args ...string) ([]byte, error) {
	return a.runRemoteWithEnvironment(ctx, a.factEnvironment(), args...)
}

func (a *Adapter) runRemoteWithEnvironment(ctx context.Context, environment []string, args ...string) ([]byte, error) {
	command := append([]string(nil), args...)
	output, err := a.runWithEnvironment(ctx, "", environment, command...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return output, nil
}

func fullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

var urlUserInfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@[:space:]]+@`)
var urlQuery = regexp.MustCompile(`\?[^[:space:]'"]*`)

func redactGitText(value string) string {
	value = urlUserInfo.ReplaceAllString(value, "${1}[REDACTED]@")
	return urlQuery.ReplaceAllString(value, "?[REDACTED]")
}
