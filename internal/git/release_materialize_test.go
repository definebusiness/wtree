package git_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestReleaseFetchAuthenticationChannelsReachGitAndSecretsDoNotEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	directory := t.TempDir()
	capture := filepath.Join(directory, "environment")
	binary := filepath.Join(directory, "git")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nenv > \"$WTREE_CAPTURE\"\necho helper-diagnostic-$ASKPASS_REQUIRED_SECRET >&2\nexit 9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	canary := "credential-canary-987"
	adapter := git.NewAdapterWithEnv(binary, []string{"PATH=" + os.Getenv("PATH"), "WTREE_CAPTURE=" + capture, "SSH_AUTH_SOCK=/tmp/release-agent", "GIT_ASKPASS=/tmp/askpass", "ASKPASS_REQUIRED_SECRET=" + canary, "HOME=/tmp/release-home", "GIT_CONFIG_GLOBAL=/tmp/release-gitconfig", "GIT_TERMINAL_PROMPT=1"})
	err := adapter.FetchAdvertisedRefs(context.Background(), directory, "origin")
	if err == nil {
		t.Fatal("FetchAdvertisedRefs error = nil")
	}
	if text := err.Error(); strings.Contains(text, canary) || strings.Contains(text, "helper-diagnostic") || strings.Contains(text, "user:password") {
		t.Fatalf("authentication error leaked secret: %s", text)
	}
	data, readErr := os.ReadFile(capture)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{"SSH_AUTH_SOCK=/tmp/release-agent", "GIT_ASKPASS=/tmp/askpass", "ASKPASS_REQUIRED_SECRET=" + canary, "HOME=/tmp/release-home", "GIT_CONFIG_GLOBAL=/tmp/release-gitconfig", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("Git environment lacks %q: %s", want, data)
		}
	}
}

func TestReleaseFetchAdvertisedRefsChecksOutTagOnlyCommitDetached(t *testing.T) {
	source := testutil.NewGitRepository(t)
	source.CommitFile("release.txt", "tag only\n", "release")
	revision := mustHead(t, source)
	source.Run(t, "tag", "release-v1", revision)
	remote := testutil.NewBareGitRemote(t)
	source.Run(t, "push", remote, "refs/tags/release-v1")
	destination := filepath.Join(t.TempDir(), "checkout")
	adapter := git.NewAdapter("git")
	if err := adapter.Clone(context.Background(), remote, destination, "release"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FetchAdvertisedRefs(context.Background(), destination, "release"); err != nil {
		t.Fatal(err)
	}
	head, err := adapter.CheckoutDetached(context.Background(), destination, revision)
	if err != nil || head != revision {
		t.Fatalf("CheckoutDetached() = %q, %v", head, err)
	}
	if _, detached, err := adapter.CurrentBranch(context.Background(), destination); err != nil || !detached {
		t.Fatalf("detached = %t, %v", detached, err)
	}
}

func TestReleaseCheckoutRejectsUnavailableObjectWithoutDirectFetch(t *testing.T) {
	source := testutil.NewGitRepository(t)
	source.CommitFile("release.txt", "published\n", "release")
	remote := testutil.NewBareGitRemote(t)
	source.Run(t, "push", remote, "HEAD:refs/heads/main")
	destination := filepath.Join(t.TempDir(), "checkout")
	adapter := git.NewAdapter("git")
	if err := adapter.Clone(context.Background(), remote, destination, "release"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FetchAdvertisedRefs(context.Background(), destination, "release"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CheckoutDetached(context.Background(), destination, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("CheckoutDetached unavailable object error = nil")
	}
}
