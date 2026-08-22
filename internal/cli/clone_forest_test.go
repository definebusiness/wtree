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
}
