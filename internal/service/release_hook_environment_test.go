package service

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostReleaseEnvironmentAuthoritativelyBindsReleaseAndHead(t *testing.T) {
	root := t.TempDir()
	head := strings.Repeat("a", 40)
	plan, err := newHookPlan(hookPlanInput{Operation: "release-lock", Source: "local", Event: "post-release", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "root", WorkspaceID: "workspace", WorkspaceName: "workspace", SourceLogicalRoot: root, TargetLogicalRoot: root, ReleaseName: "v1.2.3", SourceBytes: []byte("config"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "tag", Repository: "api", SourceRepository: root, TargetRepository: filepath.Join(root, "api"), Head: head, ConfiguredExecutable: "tagger", ResolvedExecutable: filepath.Join(root, "tagger"), Availability: "available", Timeout: time.Minute}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := buildHookEnvironment(HookEnvironmentLocal, false, []string{"PATH=/bin", "WTREE_HEAD=forged", "WTREE_RELEASE_NAME=forged"}, plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, expected := range []string{"\nWTREE_HOOK=post-release\n", "\nWTREE_OPERATION=release-lock\n", "\nWTREE_RELEASE_NAME=v1.2.3\n", "\nWTREE_HEAD=" + head + "\n"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("environment missing %q: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "forged") {
		t.Fatalf("reserved environment leaked: %s", joined)
	}
}
