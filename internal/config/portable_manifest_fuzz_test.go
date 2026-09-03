package config_test

import (
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/pathutil"
)

// Fuzz covers both existing local v1 decoders and the portable contract in a
// single target so `go test -fuzz=Fuzz` remains an unambiguous, repeatable
// command on every supported Go release.
func Fuzz(f *testing.F) {
	for _, seed := range []struct {
		kind  byte
		input string
	}{
		{0, "version: 1\n"},
		{0, "version: 1\nproject:\n  id: p\nrepositories:\n  root:\n    source: .\n    mount: .\n"},
		{0, localV3Hooks},
		{1, validPortableManifest},
		{1, portableV3Hooks},
		{1, "version: 1\nrepositories: null\n"},
		{2, "https://example.test/repo.git"},
		{2, "HTTPS://example.test/repo.git"},
		{2, "hTtPs://example.test/repo.git?credential-canary"},
		{2, "git@example.test:repo.git"},
		{2, "ftp://example.test/repo.git"},
		{2, "git://example.test/repo.git"},
		{2, "s3://bucket/repo.git"},
		{2, "../escape"},
		{2, "refs/heads/main"},
		{2, "https://token:secret@example.test/repo.git"},
		{2, "main\nnext"},
	} {
		f.Add(seed.kind, seed.input)
	}
	f.Fuzz(func(t *testing.T, kind byte, input string) {
		switch kind % 3 {
		case 0:
			_, _ = config.LoadProject([]byte(input))
			_, _ = config.LoadGlobal([]byte(input))
		case 1:
			_, _ = config.LoadPortableManifest([]byte(input))
		case 2:
			if err := config.ValidateCloneURL(input); strings.Contains(input, "credential-canary") && err != nil && strings.Contains(err.Error(), "credential-canary") {
				t.Fatalf("clone URL validation leaked a credential-shaped value: %v", err)
			}
			_ = config.ValidateBranchName(input)
			_ = config.ValidateMergeRef(input)
			_ = pathutil.ValidateMount(input, pathutil.ChildMount)
		}
	})
}
