package git_test

import (
	"testing"

	"github.com/definebusiness/wtree/internal/git"
)

func FuzzParseStatus(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("?? file\x00"), []byte("R  destination\x00source\x00"), []byte(" M path with space\x00")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) { _, _ = git.ParseStatus(input) })
}

func FuzzParseWorktreeList(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("worktree /tmp/a\nHEAD deadbeef\n\n"), []byte("worktree /tmp/a\ndetached\n\n")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) { _, _ = git.ParseWorktreeList(input) })
}
