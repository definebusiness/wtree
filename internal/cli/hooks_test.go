package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestHooksCommandsRenderVersionedResultsAndKeepJSONSeparateOnErrors(t *testing.T) {
	project, data := testutil.NewPushedGitRepository(t), t.TempDir()
	project.CommitFile("README.md", "hooks\n", "seed")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	configPath := filepath.Join(project.Path, ".wtree.yml")
	local, err := config.ReadProjectFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	local.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(local.Manifest.Source)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := config.LoadPortableManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = config.PortableManifestVersion3
	manifestData, err = config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.Manifest.Source, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}

	listed := testutil.RunCommand(t, cli.Execute, "hooks", "list", "--project", project.Path, "--data-dir", data, "--json")
	var list service.HookListResult
	if listed.Err != nil || listed.Stderr != "" || json.Unmarshal([]byte(listed.Stdout), &list) != nil || list.Version != 1 || list.Operation != "hooks-list" || len(list.Groups) != 3 {
		t.Fatalf("hooks list JSON = %#v, decoded=%#v", listed, list)
	}
	if list.Status != "completed" || list.Groups[0].Source != "portable" || list.Groups[1].Source != "shared" || list.Groups[2].Source != "local" || list.Groups[2].Events[0].Hooks[0].Repository != "root" || list.Groups[2].Events[0].Hooks[0].Timeout != "1m0s" || list.Groups[2].Events[0].Hooks[0].ExecutionPolicy != "automatic-post-create-unless-bypassed" || list.Groups[2].Events[0].Hooks[0].Command[0] != "setup" {
		t.Fatalf("hooks list contract = %#v", list)
	}
	for _, group := range list.Groups {
		if group.Events == nil {
			t.Fatalf("JSON emitted null events for %s", group.Source)
		}
		for _, event := range group.Events {
			if event.Hooks == nil {
				t.Fatalf("JSON emitted null hooks for %s", event.Event)
			}
		}
	}
	humanList := testutil.RunCommand(t, cli.Execute, "hooks", "list", "--project", project.Path, "--data-dir", data)
	wantList := "Hooks:\nSource: portable\n  (none)\nSource: shared\n  (none)\nSource: local\n  Event: post-create comparison=shared:missing\n    setup repository=root timeout=1m0s policy=automatic-post-create-unless-bypassed command=[\"setup\"]\n"
	if humanList.Err != nil || humanList.Stderr != "" || humanList.Stdout != wantList {
		t.Fatalf("hooks list human = %#v, want %q", humanList, wantList)
	}

	humanShare := testutil.RunCommand(t, cli.Execute, "hooks", "share", "post-create", "--project", project.Path, "--data-dir", data)
	wantShare := "Hooks share complete (changed=true)\n  added: post-create\n  replaced: none\n  unchanged: none\n  skipped: none\n  conflicting: none\n"
	if humanShare.Err != nil || humanShare.Stderr != "" || humanShare.Stdout != wantShare {
		t.Fatalf("hooks share human = %#v, want %q", humanShare, wantShare)
	}
	shared := testutil.RunCommand(t, cli.Execute, "hooks", "share", "post-create", "--project", project.Path, "--data-dir", data, "--json")
	var share service.HookMutationResult
	if shared.Err != nil || shared.Stderr != "" || json.Unmarshal([]byte(shared.Stdout), &share) != nil || share.Changed || share.Operation != "hooks-share" || share.Status != "completed" || share.Unchanged == nil || share.Added == nil || share.Replaced == nil || share.Skipped == nil || share.Conflicting == nil || strings.Join(share.Unchanged, ",") != "post-create" {
		t.Fatalf("hooks share JSON = %#v, decoded=%#v", shared, share)
	}

	local.Hooks = nil
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	installed := testutil.RunCommand(t, cli.Execute, "hooks", "install", "--project", project.Path, "--data-dir", data)
	if installed.Err != nil || installed.Stderr != "" || !strings.Contains(installed.Stdout, "Hooks install complete (changed=true)") || !strings.Contains(installed.Stdout, "  added: post-create") {
		t.Fatalf("hooks install human = %#v", installed)
	}

	invalid := testutil.RunCommand(t, cli.Execute, "hooks", "share", "post-clone", "--project", project.Path, "--data-dir", data, "--json")
	if invalid.Err == nil || invalid.Stderr != "" || !strings.Contains(invalid.Stdout, `"success":false`) || strings.Contains(invalid.Stdout, `"operation":"hooks-share"`) {
		t.Fatalf("hooks share invalid JSON = %#v", invalid)
	}
}

func TestHooksCommandArgumentAndFlagShape(t *testing.T) {
	for _, arguments := range [][]string{
		{"hooks", "list", "unexpected"},
		{"hooks", "share"},
		{"hooks", "share", "post-create", "extra"},
		{"hooks", "install", "--force", "--missing"},
		{"hooks", "list", "--force"},
	} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 || result.Stderr != "" {
			t.Fatalf("%v = %#v, want invalid arguments", arguments, result)
		}
	}
}
