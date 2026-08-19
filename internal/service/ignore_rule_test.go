package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestNestedDirectoryRuleIsLiteralAndAnchored(t *testing.T) {
	for _, test := range []struct {
		mount string
		want  string
	}{
		{mount: "with space", want: `/with\ space/`},
		{mount: "with\ttab", want: "/with\\\ttab/"},
		{mount: "unicode-世界", want: "/unicode-世界/"},
		{mount: "#hash", want: `/\#hash/`},
		{mount: "!bang", want: `/\!bang/`},
		{mount: "star*query?", want: `/star\*query\?/`},
		{mount: "[brackets]", want: `/\[brackets\]/`},
		{mount: "parent/child", want: "/parent/child/"},
	} {
		t.Run(test.mount, func(t *testing.T) {
			rule, err := service.NestedDirectoryRule(test.mount)
			if err != nil || rule != test.want {
				t.Fatalf("NestedDirectoryRule(%q) = %q, %v; want %q, nil", test.mount, rule, err, test.want)
			}

			repository := testutil.NewGitRepository(t)
			repository.CommitFile("tracked", "initial\n", "initial")
			if err := os.WriteFile(filepath.Join(repository.Path, ".gitignore"), []byte(rule+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ignored, err := git.NewAdapter("git").IsIgnoredWorkingTree(context.Background(), repository.Path, test.mount)
			if err != nil || !ignored {
				t.Fatalf("rule %q ignores %q = %t, %v; want true, nil", rule, test.mount, ignored, err)
			}
			if ignored, err := git.NewAdapter("git").IsIgnoredWorkingTree(context.Background(), repository.Path, "prefix-"+test.mount); err != nil || ignored {
				t.Fatalf("rule %q ignores sibling = %t, %v; want false, nil", rule, ignored, err)
			}
		})
	}
}

func TestNestedDirectoryRuleRequiresNormalizedNonRootMount(t *testing.T) {
	for _, mount := range []string{".", `dir\child`, "dir/../child", "line\nbreak"} {
		if _, err := service.NestedDirectoryRule(mount); err == nil {
			t.Fatalf("NestedDirectoryRule(%q) error = nil", mount)
		}
	}
}
