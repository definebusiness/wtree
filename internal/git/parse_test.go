package git_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
)

func TestParseCheckIgnoreEvidenceAndQualificationRejections(t *testing.T) {
	evidence, err := git.ParseCheckIgnoreEvidence([]byte("nested/.gitignore\x0012\x00/child/\x00nested/child/\x00"))
	if err != nil || !evidence.Ignored || evidence.Negated || evidence.Source != "nested/.gitignore" || evidence.Line != 12 || evidence.Pattern != "/child/" || evidence.Path != "nested/child/" {
		t.Fatalf("ParseCheckIgnoreEvidence() = %#v, %v", evidence, err)
	}

	for _, output := range [][]byte{
		[]byte("nested/.gitignore\x0012\x00!/child/\x00nested/child/\x00"),
		[]byte(".git/info/exclude\x001\x00/child/\x00child/\x00"),
		[]byte("/outside/.gitignore\x001\x00/child/\x00child/\x00"),
	} {
		evidence, err := git.ParseCheckIgnoreEvidence(output)
		if err != nil {
			t.Fatalf("ParseCheckIgnoreEvidence(%q) error = %v", output, err)
		}
		if evidence.Qualifies(t.TempDir()) {
			t.Fatalf("evidence %#v qualifies outside or negated rule", evidence)
		}
	}
	for _, output := range [][]byte{nil, []byte("no NUL\n"), []byte(".gitignore\x00not-a-line\x00/child/\x00child/\x00"), []byte(".gitignore\x001\x00/child\x00child\x00extra")} {
		if _, err := git.ParseCheckIgnoreEvidence(output); err == nil {
			t.Fatalf("ParseCheckIgnoreEvidence(%q) error = nil", output)
		}
	}
}

func TestWorkingTreeIgnoreEvidenceRequiresSourceToGovernMount(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "unrelated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "unrelated", ".gitignore"), []byte("/child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := git.ParseCheckIgnoreEvidence([]byte("unrelated/.gitignore\x001\x00/child/\x00nested/child/\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Qualifies(parent) {
		t.Fatalf("evidence %#v qualifies despite a non-governing source", evidence)
	}
}

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
