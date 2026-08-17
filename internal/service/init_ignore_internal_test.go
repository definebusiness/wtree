package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/discovery"
)

func TestInitIgnoreRuleEscapesLiteralGitMetacharacters(t *testing.T) {
	got := gitIgnoreRule("space #bang! [bracket] star* question? slash\\literal")
	want := "/space\\ \\#bang\\!\\ \\[bracket\\]\\ star\\*\\ question\\?\\ slash\\\\literal/"
	if got != want {
		t.Fatalf("gitIgnoreRule() = %q, want %q", got, want)
	}
}

func TestInitIgnoreWritesUseParentFirstThenRepositoryIDOrder(t *testing.T) {
	repositories := []discovery.Repository{
		{ID: "root"},
		{ID: "z-parent", ParentID: "root"},
		{ID: "a-parent", ParentID: "root"},
		{ID: "child", ParentID: "z-parent"},
	}
	writes := []fileWrite{
		{path: "/child", update: IgnoreUpdate{RepositoryID: "child"}},
		{path: "/z", update: IgnoreUpdate{RepositoryID: "z-parent"}},
		{path: "/root", update: IgnoreUpdate{RepositoryID: "root"}},
		{path: "/a", update: IgnoreUpdate{RepositoryID: "a-parent"}},
	}
	sortIgnoreWrites(writes, repositories)
	got := []string{writes[0].update.RepositoryID, writes[1].update.RepositoryID, writes[2].update.RepositoryID, writes[3].update.RepositoryID}
	want := []string{"root", "a-parent", "z-parent", "child"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("ignore write order = %v, want %v", got, want)
		}
	}
}

func TestInitIgnoreNewlinePreservesUnambiguousCRLFAndNoFinalNewline(t *testing.T) {
	if got := gitIgnoreNewline([]byte("one\r\ntwo\r\n")); got != "\r\n" {
		t.Fatalf("CRLF newline = %q", got)
	}
	if got := gitIgnoreNewline([]byte("one\r\ntwo\n")); got != "\n" {
		t.Fatalf("mixed newline = %q", got)
	}
	if hasGitIgnoreNewline([]byte("rule")) {
		t.Fatal("unterminated input reported a final newline")
	}
}

func TestInitIgnoreTargetRejectsNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"directory", "link"} {
		path := filepath.Join(directory, name)
		if name == "directory" {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Symlink(filepath.Join(directory, "missing"), path); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readOptionalFile(path); err == nil {
			t.Fatalf("readOptionalFile(%s) accepted a non-regular target", name)
		}
	}
}
