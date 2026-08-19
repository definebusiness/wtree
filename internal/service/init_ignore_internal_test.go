package service

import (
	"testing"
)

func TestInitIgnoreRuleEscapesLiteralGitMetacharacters(t *testing.T) {
	got, err := NestedDirectoryRule("space #bang! [bracket] star* question? slash")
	if err != nil {
		t.Fatal(err)
	}
	want := "/space\\ \\#bang\\!\\ \\[bracket\\]\\ star\\*\\ question\\?\\ slash/"
	if got != want {
		t.Fatalf("NestedDirectoryRule() = %q, want %q", got, want)
	}
}
