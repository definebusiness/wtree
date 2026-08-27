package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConfiguredRefObservation is the current commit advertised for one explicit
// configured remote branch. It never consults the remote symbolic HEAD.
type ConfiguredRefObservation struct {
	Remote    string
	RemoteRef string
	Commit    string
}

// ConfiguredRefFetch records the remote-tracking generation before and after
// fetching exactly one configured remote branch. It never changes a local
// branch or checkout worktree. A populated value can accompany an error when
// the tracking mutation preceded a process failure or late cancellation.
type ConfiguredRefFetch struct {
	Remote               string
	RemoteRef            string
	PreviousRemoteCommit string
	ActualRemoteCommit   string
}

// FastForwardReceipt identifies the only generation an inverse may restore.
// It is deliberately value-only so an operation journal can own it later.
type FastForwardReceipt struct {
	Branch    string
	OldCommit string
	NewCommit string
}

// fastForwardBeforeRefUpdate is a package-private test seam that runs after
// preflight and immediately before the generation-guarded ref update.
var fastForwardBeforeRefUpdate func()

// fastForwardAfterRefUpdate and fastForwardMaterialize are controlled test
// seams for the transition window. Production uses a non-reset two-tree merge
// so Git refuses tracked and untracked collisions instead of overwriting them.
var fastForwardAfterRefUpdate func()
var fastForwardMaterialize = func(adapter *Adapter, ctx context.Context, repository, fromCommit, toCommit string) error {
	return adapter.materializeAttachedWorktree(ctx, repository, fromCommit, toCommit)
}

// configuredRefAfterFetch is a package-private test seam for the process
// return window after Git may have updated the selected tracking ref. The
// production value is nil.
var configuredRefAfterFetch func() error

// ObserveConfiguredRef reads the configured remote URL and asks only for the
// named branch ref. It is an observation operation and uses optional locks.
func (a *Adapter) ObserveConfiguredRef(ctx context.Context, repository, remote, remoteRef string) (ConfiguredRefObservation, error) {
	if err := validateConfiguredRef(remote, remoteRef); err != nil {
		return ConfiguredRefObservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ConfiguredRefObservation{}, err
	}
	url, err := a.valueFact(ctx, repository, "remote", "get-url", "--", remote)
	if err != nil {
		return ConfiguredRefObservation{}, err
	}
	commit, err := a.AdvertisedCommit(ctx, url, remoteRef)
	if err != nil {
		return ConfiguredRefObservation{}, err
	}
	return ConfiguredRefObservation{Remote: remote, RemoteRef: remoteRef, Commit: commit}, nil
}

// IsAncestor observes reachability between two already-local commit objects.
// It neither fetches nor changes refs, so update dry-run can refuse a remote
// advertised commit that is not locally verifiable without broadening Git.
func (a *Adapter) IsAncestor(ctx context.Context, repository, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, errors.New("ancestor and descendant commits are required")
	}
	if _, err := a.runFact(ctx, repository, "merge-base", "--is-ancestor", ancestor, descendant); err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && (gitError.ExitCode == 1 || gitError.ExitCode == 128) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FetchConfiguredRef force-updates exactly the selected private remote-tracking
// ref. FetchTrackingBranch deliberately has no remote-HEAD fallback.
func (a *Adapter) FetchConfiguredRef(ctx context.Context, repository, remote, remoteRef string) (ConfiguredRefFetch, error) {
	if err := validateConfiguredRef(remote, remoteRef); err != nil {
		return ConfiguredRefFetch{}, err
	}
	if err := ctx.Err(); err != nil {
		return ConfiguredRefFetch{}, err
	}
	previous, err := a.remoteTrackingCommit(ctx, repository, remote, remoteRef)
	if err != nil {
		return ConfiguredRefFetch{}, err
	}
	fetchErr := a.FetchTrackingBranch(ctx, repository, remote, remoteRef)
	if fetchErr == nil && configuredRefAfterFetch != nil {
		fetchErr = configuredRefAfterFetch()
	}
	// Fetch may update the local tracking ref before its process reports an
	// error or the caller observes cancellation. Recapture that one local ref
	// under a short cancellation-independent context so the caller receives
	// ownership evidence alongside the original error.
	recaptureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	actual, recaptureErr := a.remoteTrackingCommit(recaptureCtx, repository, remote, remoteRef)
	cancel()
	if recaptureErr != nil {
		return ConfiguredRefFetch{}, errors.Join(fetchErr, fmt.Errorf("recapture configured ref after fetch: %w", recaptureErr))
	}
	if actual == "" {
		return ConfiguredRefFetch{}, errors.Join(fetchErr, errors.New("fetch configured ref: selected remote-tracking ref is absent after fetch"))
	}
	receipt := ConfiguredRefFetch{Remote: remote, RemoteRef: remoteRef, PreviousRemoteCommit: previous, ActualRemoteCommit: actual}
	if fetchErr != nil {
		return receipt, fetchErr
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// RestoreConfiguredRef compares the selected remote-tracking ref with the
// fetched generation before restoring its prior value (or deleting a ref that
// this operation created). This is deliberately local-only: no remote HEAD,
// hooks, branch configuration, or checkout state is touched.
func (a *Adapter) RestoreConfiguredRef(ctx context.Context, repository string, receipt ConfiguredRefFetch) error {
	if err := validateConfiguredRef(receipt.Remote, receipt.RemoteRef); err != nil || !fullObjectID(receipt.ActualRemoteCommit) || (receipt.PreviousRemoteCommit != "" && !fullObjectID(receipt.PreviousRemoteCommit)) {
		return errors.New("configured ref restore receipt is invalid")
	}
	ref := "refs/remotes/" + receipt.Remote + "/" + strings.TrimPrefix(receipt.RemoteRef, "refs/heads/")
	if receipt.PreviousRemoteCommit == "" {
		_, err := a.run(ctx, repository, "update-ref", "-d", ref, receipt.ActualRemoteCommit)
		return err
	}
	_, err := a.run(ctx, repository, "update-ref", ref, receipt.PreviousRemoteCommit, receipt.ActualRemoteCommit)
	return err
}

func (a *Adapter) remoteTrackingCommit(ctx context.Context, repository, remote, remoteRef string) (string, error) {
	branch := strings.TrimPrefix(remoteRef, "refs/heads/")
	commit, err := a.ResolveRef(ctx, repository, "refs/remotes/"+remote+"/"+branch)
	if err == nil {
		return commit, nil
	}
	var gitError *Error
	if errors.As(err, &gitError) && gitError.ExitCode == 128 {
		return "", nil
	}
	return "", err
}

// FastForward advances only the currently attached clean branch. It refuses
// stale/non-descendant generations and suppresses hooks for the operation.
func (a *Adapter) FastForward(ctx context.Context, repository, branch, oldCommit, newCommit string) (FastForwardReceipt, error) {
	if err := validateFastForward(branch, oldCommit, newCommit); err != nil {
		return FastForwardReceipt{}, err
	}
	if err := a.assertOwnedBranchGeneration(ctx, repository, branch, oldCommit); err != nil {
		return FastForwardReceipt{}, err
	}
	if err := a.assertAncestor(ctx, repository, oldCommit, newCommit); err != nil {
		return FastForwardReceipt{}, err
	}
	if err := a.transitionAttachedBranch(ctx, repository, branch, oldCommit, newCommit); err != nil {
		return FastForwardReceipt{}, fmt.Errorf("fast-forward transition: %w", err)
	}
	return FastForwardReceipt{Branch: branch, OldCommit: oldCommit, NewCommit: newCommit}, nil
}

// RestoreFastForward reverses a recorded fast-forward only while the branch,
// HEAD, and clean checkout still exactly match the owned new generation.
func (a *Adapter) RestoreFastForward(ctx context.Context, repository string, receipt FastForwardReceipt) error {
	if err := validateFastForward(receipt.Branch, receipt.OldCommit, receipt.NewCommit); err != nil {
		return err
	}
	if err := a.assertOwnedBranchGeneration(ctx, repository, receipt.Branch, receipt.NewCommit); err != nil {
		return fmt.Errorf("restore fast-forward ownership: %w", err)
	}
	if err := a.transitionAttachedBranch(ctx, repository, receipt.Branch, receipt.NewCommit, receipt.OldCommit); err != nil {
		return fmt.Errorf("restore fast-forward transition: %w", err)
	}
	return nil
}

// transitionAttachedBranch applies one expected-generation branch transition.
// Filesystem materialization uses Git's non-reset two-tree merge and is
// attempted only while the original index/worktree generation is still exact.
// Failure cleanup may restore only the ref, and only while both the ref and
// source filesystem/index generation remain owned. It never rewrites files.
func (a *Adapter) transitionAttachedBranch(ctx context.Context, repository, branch, fromCommit, toCommit string) error {
	if fastForwardBeforeRefUpdate != nil {
		fastForwardBeforeRefUpdate()
	}
	if err := a.updateBranchRef(ctx, repository, branch, toCommit, fromCommit); err != nil {
		return err
	}
	if fastForwardAfterRefUpdate != nil {
		fastForwardAfterRefUpdate()
	}
	if err := a.assertAttachedBranchHead(ctx, repository, branch, toCommit); err != nil {
		return a.cleanupFailedTransition(repository, branch, fromCommit, toCommit, fmt.Errorf("pre-materialization ownership lost: %w", err))
	}
	matchesSource, err := a.worktreeMatchesCommit(ctx, repository, fromCommit)
	if err != nil {
		return a.cleanupFailedTransition(repository, branch, fromCommit, toCommit, fmt.Errorf("verify source generation: %w", err))
	}
	if !matchesSource {
		return a.cleanupFailedTransition(repository, branch, fromCommit, toCommit, errors.New("source index/worktree generation changed before materialization"))
	}
	if err := a.assertIgnoredPathsDoNotCollide(ctx, repository, toCommit); err != nil {
		return a.cleanupFailedTransition(repository, branch, fromCommit, toCommit, fmt.Errorf("verify ignored target collisions: %w", err))
	}
	if err := fastForwardMaterialize(a, ctx, repository, fromCommit, toCommit); err != nil {
		return a.cleanupFailedTransition(repository, branch, fromCommit, toCommit, fmt.Errorf("materialize attached worktree: %w", err))
	}
	if err := a.assertOwnedBranchGeneration(ctx, repository, branch, toCommit); err != nil {
		return fmt.Errorf("post-materialization ownership lost without destructive reconciliation: %w", err)
	}
	return nil
}

func (a *Adapter) cleanupFailedTransition(repository, branch, fromCommit, toCommit string, transitionErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.assertAttachedBranchHead(cleanupCtx, repository, branch, toCommit); err != nil {
		return fmt.Errorf("%w; ref cleanup refused after ownership loss: %v", transitionErr, err)
	}
	matchesSource, err := a.worktreeMatchesCommit(cleanupCtx, repository, fromCommit)
	if err != nil {
		return fmt.Errorf("%w; source generation cleanup check failed: %v", transitionErr, err)
	}
	if !matchesSource {
		return fmt.Errorf("%w; ref cleanup refused because filesystem/index source ownership was lost", transitionErr)
	}
	if err := a.updateBranchRef(cleanupCtx, repository, branch, fromCommit, toCommit); err != nil {
		return fmt.Errorf("%w; ref cleanup compare-and-swap failed: %v", transitionErr, err)
	}
	return transitionErr
}

func (a *Adapter) updateBranchRef(ctx context.Context, repository, branch, nextCommit, expectedCommit string) error {
	_, err := a.run(ctx, repository, "update-ref", "refs/heads/"+branch, nextCommit, expectedCommit)
	return err
}

func (a *Adapter) materializeAttachedWorktree(ctx context.Context, repository, fromCommit, toCommit string) error {
	matchesSource, err := a.worktreeMatchesCommit(ctx, repository, fromCommit)
	if err != nil {
		return fmt.Errorf("verify source immediately before materialization: %w", err)
	}
	if !matchesSource {
		return errors.New("source index/worktree generation changed immediately before materialization")
	}
	if err := a.assertIgnoredPathsDoNotCollide(ctx, repository, toCommit); err != nil {
		return fmt.Errorf("verify ignored target collisions immediately before materialization: %w", err)
	}
	_, err = a.run(ctx, repository, "read-tree", "-m", "-u", fromCommit, toCommit)
	return err
}

func (a *Adapter) assertAttachedBranchHead(ctx context.Context, repository, branch, expectedHead string) error {
	current, detached, err := a.CurrentBranch(ctx, repository)
	if err != nil {
		return err
	}
	if detached || current != branch {
		return fmt.Errorf("expected attached branch %q", branch)
	}
	head, err := a.Head(ctx, repository)
	if err != nil {
		return err
	}
	if head != expectedHead {
		return fmt.Errorf("expected HEAD %q, got %q", expectedHead, head)
	}
	return nil
}

func (a *Adapter) worktreeMatchesCommit(ctx context.Context, repository, commit string) (bool, error) {
	for _, args := range [][]string{{"diff-index", "--quiet", commit, "--"}, {"diff-files", "--quiet"}} {
		if _, err := a.runFact(ctx, repository, args...); err != nil {
			var gitError *Error
			if errors.As(err, &gitError) && gitError.ExitCode == 1 {
				return false, nil
			}
			return false, err
		}
	}
	others, err := a.runFact(ctx, repository, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return false, err
	}
	if len(others) != 0 {
		return false, nil
	}
	return true, nil
}

// assertIgnoredPathsDoNotCollide permits deliberately ignored local state
// (notably .wtree.yml and nested checkout mounts) to survive a transition.
// It still rejects every ignored path that the target tree would need to
// materialize over or through.  The same check runs before and at the final
// materialization boundary, so a newly-created ignored collision is never
// overwritten during the ref-owned window.
func (a *Adapter) assertIgnoredPathsDoNotCollide(ctx context.Context, repository, targetCommit string) error {
	ignored, err := a.runFact(ctx, repository, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	if len(ignored) == 0 {
		return nil
	}
	target, err := a.runFact(ctx, repository, "ls-tree", "-r", "-z", "--name-only", targetCommit)
	if err != nil {
		return err
	}
	for _, ignoredPath := range nulPaths(ignored) {
		for _, targetPath := range nulPaths(target) {
			if overlappingCheckoutPath(ignoredPath, targetPath) {
				return fmt.Errorf("ignored path %q collides with target path %q", ignoredPath, targetPath)
			}
		}
	}
	return nil
}

func nulPaths(value []byte) []string {
	records := strings.Split(string(value), "\x00")
	paths := make([]string, 0, len(records))
	for _, record := range records {
		if record != "" {
			paths = append(paths, record)
		}
	}
	return paths
}

func overlappingCheckoutPath(left, right string) bool {
	return checkoutPathPrefix(left, right) || checkoutPathPrefix(right, left)
}

func checkoutPathPrefix(prefix, path string) bool {
	return len(prefix) <= len(path) && strings.EqualFold(prefix, path[:len(prefix)]) && (len(prefix) == len(path) || path[len(prefix)] == '/')
}

func validateConfiguredRef(remote, remoteRef string) error {
	branch := strings.TrimPrefix(remoteRef, "refs/heads/")
	if remote == "" || strings.HasPrefix(remote, "-") || branch == remoteRef || branch == "" || strings.ContainsAny(remote, "\x00\r\n") || strings.ContainsAny(branch, "\x00\r\n") {
		return errors.New("configured ref has invalid remote or remote branch")
	}
	return nil
}

func validateFastForward(branch, oldCommit, newCommit string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, "\x00\r\n") || !fullObjectID(oldCommit) || !fullObjectID(newCommit) || oldCommit == newCommit {
		return errors.New("fast-forward has invalid branch or commit generation")
	}
	return nil
}

func (a *Adapter) assertOwnedBranchGeneration(ctx context.Context, repository, branch, expectedHead string) error {
	if err := a.assertAttachedBranchHead(ctx, repository, branch, expectedHead); err != nil {
		return err
	}
	matches, err := a.worktreeMatchesCommit(ctx, repository, expectedHead)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("worktree is not clean")
	}
	return nil
}

func (a *Adapter) assertAncestor(ctx context.Context, repository, ancestor, descendant string) error {
	_, err := a.runFact(ctx, repository, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return nil
	}
	var gitError *Error
	if errors.As(err, &gitError) && gitError.ExitCode == 1 {
		return fmt.Errorf("%q is not an ancestor of %q", ancestor, descendant)
	}
	return err
}
