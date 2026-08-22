package service

import (
	"testing"
)

func TestInitIgnoreRuleEscapesSupportedLiteralGitMetacharacters(t *testing.T) {
	got, err := NestedDirectoryRule("space #bang! [bracket] slash")
	if err != nil {
		t.Fatal(err)
	}
	want := "/space\\ \\#bang\\!\\ \\[bracket\\]\\ slash/"
	if got != want {
		t.Fatalf("NestedDirectoryRule() = %q, want %q", got, want)
	}
}
