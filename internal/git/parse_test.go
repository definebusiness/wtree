package git_test

import (
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
)

func TestParseWorktreeListPorcelain(t *testing.T) {
	input := "worktree /tmp/repo with space\nHEAD 012345\nbranch refs/heads/main\n\nworktree /tmp/detached\nHEAD abcdef\ndetached\n\n"
	got, err := git.ParseWorktreeList([]byte(input))
	if err != nil {
		t.Fatalf("ParseWorktreeList() error = %v", err)
	}
	want := []git.Worktree{
		{Path: "/tmp/repo with space", Head: "012345", Branch: "main"},
		{Path: "/tmp/detached", Head: "abcdef", Detached: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseWorktreeList() = %#v, want %#v", got, want)
	}
}

func TestParseStatusPorcelain(t *testing.T) {
	status, err := git.ParseStatus([]byte("M  staged.txt\x00 M modified.txt\x00?? untracked file.txt\x00"))
	if err != nil {
		t.Fatalf("ParseStatus() error = %v", err)
	}
	if !status.Staged || !status.Modified || !status.Untracked {
		t.Errorf("status = %#v, want staged, modified, and untracked", status)
	}
	if got, want := len(status.Entries), 3; got != want {
		t.Errorf("entry count = %d, want %d", got, want)
	}
}

func TestParseStatusPorcelainNULPreservesSpacesAndUnicodePaths(t *testing.T) {
	status, err := git.ParseStatus([]byte("?? api space/δοκιμή\x00 M modified file.txt\x00"))
	if err != nil {
		t.Fatalf("ParseStatus() error = %v", err)
	}
	if len(status.Entries) != 2 || status.Entries[0].Path != "api space/δοκιμή" || status.Entries[1].Path != "modified file.txt" || !status.Untracked || !status.Modified {
		t.Fatalf("status = %#v", status)
	}
}

func TestParseStatusPorcelainNULPreservesBothRenamePathOrders(t *testing.T) {
	status, err := git.ParseStatus([]byte("R  destination\x00source\x00 R destination 2\x00source 2\x00"))
	if err != nil {
		t.Fatalf("ParseStatus() error = %v", err)
	}
	if len(status.Entries) != 2 || status.Entries[0].Path != "destination" || status.Entries[0].OriginalPath != "source" || status.Entries[1].Path != "destination 2" || status.Entries[1].OriginalPath != "source 2" {
		t.Fatalf("status = %#v", status)
	}
}

func TestParseWorktreeListRejectsMalformedRecords(t *testing.T) {
	if _, err := git.ParseWorktreeList([]byte("HEAD abc\n\n")); err == nil {
		t.Fatal("ParseWorktreeList() error = nil, want malformed record rejection")
	}
}
