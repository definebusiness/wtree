package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
)

func TestRenderClonePlanForestStatesVerificationOwners(t *testing.T) {
	plan := service.ClonePlan{
		Source:         service.ClonePlanSource{Value: "project.wtree.yml"},
		Destination:    service.CloneDestinationFacts{Path: "/tmp/logical-root"},
		LogicalRoot:    "/tmp/logical-root",
		BaseRepository: "base",
		Project:        config.PortableProject{ID: "forest-project", Name: "forest", BaseRepository: "base"},
		Repositories: []service.ClonePlanRepository{
			{ID: "base", Mount: "services/base", Verification: service.CloneVerification{TrackedManifestExact: true}},
			{ID: "sibling", Mount: "web", Verification: service.CloneVerification{}},
			{ID: "child", Parent: "base", Mount: "packages/child", Verification: service.CloneVerification{CommittedParentIgnore: true}},
		},
		Hooks: []service.HookPlanEntry{{Source: "portable", Event: "post-clone", ID: "setup", Repository: "base", WorkingDirectory: "/tmp/logical-root/services/base", ConfiguredExecutable: "hooks/setup", Availability: "deferred", Timeout: "1m0s", ExecutionPolicy: "requires-run-hooks"}, {Source: "shared", Event: "post-create", ID: "inert", Repository: "base", WorkingDirectory: "/tmp/logical-root/services/base", ConfiguredExecutable: "hooks/shared", Availability: "deferred", Timeout: "1m0s", ExecutionPolicy: "inert"}},
	}
	var output bytes.Buffer
	if err := renderClonePlan(&output, plan); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "Logical root: /tmp/logical-root") || !strings.Contains(text, "Base repository: base") {
		t.Fatalf("missing forest topology in render: %s", text)
	}
	if strings.Count(text, "tracked manifest") != 1 || !strings.Contains(text, "child\n    remote:") {
		t.Fatalf("verification owner render = %s", text)
	}
	if !strings.Contains(text, "Hooks:\n  portable/setup (post-clone)") || !strings.Contains(text, "policy: inert") || strings.Contains(text, "Resolved executable") {
		t.Fatalf("hook dry-run render = %s", text)
	}
}
