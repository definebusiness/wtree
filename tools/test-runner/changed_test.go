package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSelectPathsBuildsProductionAndTestClosures(t *testing.T) {
	graph := syntheticGraph()
	selection, err := selectPaths("/repo", graph, []string{"internal/core/core.go", "internal/consumer/consumer_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example/consumer", "example/core", "example/dependent"}
	if !reflect.DeepEqual(selection.Packages, want) {
		t.Fatalf("packages = %q, want %q", selection.Packages, want)
	}
}

func TestSelectPathsKeepsPackageLocalTestsOutOfProductionReverseClosure(t *testing.T) {
	selection, err := selectPaths("/repo", syntheticGraph(), []string{"internal/consumer/consumer_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"example/consumer"}; !reflect.DeepEqual(selection.Packages, want) {
		t.Fatalf("test-only selection = %q, want %q", selection.Packages, want)
	}
	selection, err = selectPaths("/repo", syntheticGraph(), []string{"internal/core/core.go"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"example/consumer", "example/core", "example/dependent"}; !reflect.DeepEqual(selection.Packages, want) {
		t.Fatalf("production selection = %q, want %q", selection.Packages, want)
	}
}

func TestSelectPathsSelectsAllTestutilConsumersAndHarnesses(t *testing.T) {
	selection, err := selectPaths("/repo", syntheticGraph(), []string{
		"internal/testutil/git.go", "Makefile", "scripts/ci-helper_test.sh", ".github/workflows/test.yml", "internal/core/core_windows_test.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./tools/test-runner", "example/consumer", "example/core", "example/testutil"}
	if !reflect.DeepEqual(selection.Packages, want) || !selection.Harness || !reflect.DeepEqual(selection.Platforms, []string{"windows"}) {
		t.Fatalf("selection = %#v, want packages=%q harness/windows", selection, want)
	}
}

func TestChangedPathFailuresAreClosed(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("D\x00internal/core/core.go\x00"),
		[]byte("R100\x00old.go\x00new.go\x00"),
		[]byte("M\x00"),
	} {
		if _, err := nameStatusPaths(input); err == nil {
			t.Fatalf("name-status %q unexpectedly succeeded", input)
		}
	}
	if _, err := selectPaths("/repo", syntheticGraph(), []string{"go.mod"}); err == nil {
		t.Fatal("module boundary unexpectedly selected")
	}
	for _, path := range []string{"README.txt", "unknown/config.bin", "/outside/README.md", `C:\outside\README.md`, "C:/outside/README.md", "../outside/README.md", "internal/core/../../outside/README.md"} {
		if _, err := selectPaths("/repo", syntheticGraph(), []string{path}); err == nil {
			t.Fatalf("unsafe or ambiguous path %q unexpectedly selected", path)
		}
	}
}

func TestSelectPathsClassifiesMarkdownEverywhereAsDocumentationOnly(t *testing.T) {
	for _, path := range []string{
		"README.md",
		"AGENTS.md",
		"docs/guide.md",
		"tutorial/getting-started.md",
		"scripts/ci-helper.md",
		"internal/core/README.md",
		"docs/UPPER.MD",
	} {
		t.Run(path, func(t *testing.T) {
			selection, err := selectPaths("/repo", syntheticGraph(), []string{path})
			if err != nil {
				t.Fatal(err)
			}
			wantSelection := ChangeSelection{Documentation: true}
			if !reflect.DeepEqual(selection, wantSelection) {
				t.Fatalf("selection = %#v, want %#v", selection, wantSelection)
			}
			plan, err := changedExecutionPlan(selection, "darwin", "arm64")
			if err != nil {
				t.Fatal(err)
			}
			wantPlan := []ChangedAction{{Kind: "docs"}}
			if !reflect.DeepEqual(plan, wantPlan) {
				t.Fatalf("plan = %#v, want %#v", plan, wantPlan)
			}
		})
	}
}

func TestSelectPathsCombinesMarkdownWithHarnessAndPackageActions(t *testing.T) {
	tests := []struct {
		name      string
		paths     []string
		selection ChangeSelection
		plan      []ChangedAction
	}{
		{
			name:      "documentation and script",
			paths:     []string{"README.md", "scripts/ci-helper_test.sh", "tutorial/guide.md"},
			selection: ChangeSelection{Packages: []string{"./tools/test-runner"}, Harness: true, Documentation: true},
			plan:      []ChangedAction{{Kind: "docs"}, {Kind: "harness"}, {Kind: "test", Package: "./tools/test-runner"}},
		},
		{
			name:      "documentation and package test",
			paths:     []string{"internal/core/README.md", "internal/core/core_test.go", "AGENTS.md"},
			selection: ChangeSelection{Packages: []string{"example/core"}, Documentation: true},
			plan:      []ChangedAction{{Kind: "docs"}, {Kind: "test", Package: "example/core"}},
		},
		{
			name:      "documentation script and package test",
			paths:     []string{"scripts/notes.md", "scripts/check.sh", "internal/core/core_test.go", "docs/guide.md"},
			selection: ChangeSelection{Packages: []string{"./tools/test-runner", "example/core"}, Harness: true, Documentation: true},
			plan:      []ChangedAction{{Kind: "docs"}, {Kind: "harness"}, {Kind: "test", Package: "./tools/test-runner"}, {Kind: "test", Package: "example/core"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, err := selectPaths("/repo", syntheticGraph(), test.paths)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(selection, test.selection) {
				t.Fatalf("selection = %#v, want %#v", selection, test.selection)
			}
			plan, err := changedExecutionPlan(selection, "darwin", "arm64")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan, test.plan) {
				t.Fatalf("plan = %#v, want %#v", plan, test.plan)
			}
		})
	}
}

func TestSelectChangedIncludesCommittedStagedUnstagedAndUntracked(t *testing.T) {
	commands := changedFake{results: map[string]commandResult{
		"git rev-parse --verify base^{commit}":        {Output: []byte("deadbeef\n")},
		"git rev-parse --show-toplevel":               {Output: []byte("/repo\n")},
		"git diff --name-status -z base...HEAD":       {Output: []byte("M\x00internal/core/core.go\x00")},
		"git diff --name-status -z":                   {Output: []byte("M\x00internal/consumer/consumer_test.go\x00")},
		"git diff --cached --name-status -z":          {Output: []byte("M\x00internal/testutil/git.go\x00")},
		"git ls-files --others --exclude-standard -z": {Output: []byte("tools/test-runner/new_test.go\x00")},
		"go list -json ./...":                         {Output: graphJSON(t, syntheticGraph())},
	}}
	selection, err := selectChanged(context.Background(), commands, "base")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./tools/test-runner", "example/consumer", "example/core", "example/dependent", "example/testutil"}
	if !reflect.DeepEqual(selection.Packages, want) {
		t.Fatalf("packages = %q, want %q", selection.Packages, want)
	}
}

func TestSelectChangedRequiresValidBaseAndGraph(t *testing.T) {
	if _, err := selectChanged(context.Background(), changedFake{}, ""); err == nil {
		t.Fatal("missing base unexpectedly succeeded")
	}
	commands := changedFake{results: map[string]commandResult{"git rev-parse --verify missing^{commit}": {ExitCode: 1}}}
	if _, err := selectChanged(context.Background(), commands, "missing"); err == nil {
		t.Fatal("invalid base unexpectedly succeeded")
	}
}

type changedFake struct{ results map[string]commandResult }

func (fake changedFake) Run(_ context.Context, name string, args ...string) commandResult {
	key := name + " " + strings.Join(args, " ")
	result, ok := fake.results[key]
	if !ok {
		return commandResult{ExitCode: 1, ErrorOutput: []byte(fmt.Sprintf("unexpected command %s", key))}
	}
	return result
}

func syntheticGraph() []PackageGraph {
	return []PackageGraph{
		{ImportPath: "example/core", Dir: "/repo/internal/core"},
		{ImportPath: "example/consumer", Dir: "/repo/internal/consumer", Imports: []string{"example/core"}, TestImports: []string{"example/testutil"}},
		{ImportPath: "example/dependent", Dir: "/repo/internal/dependent", Imports: []string{"example/consumer"}},
		{ImportPath: "example/testutil", Dir: "/repo/internal/testutil"},
		{ImportPath: "./tools/test-runner", Dir: "/repo/tools/test-runner"},
	}
}

func graphJSON(t *testing.T, graph []PackageGraph) []byte {
	t.Helper()
	var data strings.Builder
	for _, entry := range graph {
		data.WriteString(fmt.Sprintf(`{"ImportPath":%q,"Dir":%q,"Imports":%q,"TestImports":%q,"XTestImports":%q}`+"\n", entry.ImportPath, entry.Dir, entry.Imports, entry.TestImports, entry.XTestImports))
	}
	return []byte(data.String())
}
