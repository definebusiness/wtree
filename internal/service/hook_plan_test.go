package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hookPlanTestPath(parts ...string) string {
	return filepath.Join(append([]string{os.TempDir(), "wtree-hook-plan"}, parts...)...)
}

func TestHookPlanDefensivelyProjectsExecutionFacts(t *testing.T) {
	source, target := hookPlanTestPath("source"), hookPlanTestPath("target")
	input := hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "root", WorkspaceID: "default", WorkspaceName: "Default", SourceLogicalRoot: source, TargetLogicalRoot: target, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "setup", Repository: "root", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: "setup", ResolvedExecutable: filepath.Join(target, "setup"), Availability: "available", Arguments: []string{"one"}, Timeout: time.Second}}}
	plan, err := newHookPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	entries := plan.Entries()
	entries[0].Arguments[0] = "changed"
	if plan.Entries()[0].Arguments[0] != "one" || plan.Digest() == "" {
		t.Fatal("plan projection mutated")
	}
}

func TestHookPlanTopologyAndExecutableProjectionMatrix(t *testing.T) {
	tests := []struct {
		name, sourceRoot, targetRoot, sourceRepository, targetRepository, executable, resolved, availability string
	}{
		{"plain-root-absolute", hookPlanTestPath("src", "project"), hookPlanTestPath("dst", "project"), hookPlanTestPath("src", "project"), hookPlanTestPath("dst", "project"), hookPlanTestPath("dst", "project", ".wtree", "hooks", "setup"), hookPlanTestPath("dst", "project", ".wtree", "hooks", "setup"), "available"},
		{"sibling-nondot-base", hookPlanTestPath("src", "forest", "base-repository"), hookPlanTestPath("dst", "forest", "base-repository"), hookPlanTestPath("src", "forest", "base-repository"), hookPlanTestPath("dst", "forest", "base-repository"), "hooks/setup", hookPlanTestPath("dst", "forest", "base-repository", "hooks", "setup"), "available"},
		{"three-level-nested", hookPlanTestPath("src", "base"), hookPlanTestPath("dst", "base"), hookPlanTestPath("src", "base", "a", "b", "c"), hookPlanTestPath("dst", "base", "a", "b", "c"), "scripts/setup", hookPlanTestPath("dst", "base", "a", "b", "c", "scripts", "setup"), "available"},
		{"mount-override", hookPlanTestPath("src", "base"), hookPlanTestPath("dst", "base"), hookPlanTestPath("src", "base", "components", "tooling"), hookPlanTestPath("dst", "base", "mounted", "tooling"), "hooks/setup", hookPlanTestPath("dst", "base", "mounted", "tooling", "hooks", "setup"), "available"},
		{"bare-path-deferred", hookPlanTestPath("src", "base"), hookPlanTestPath("dst", "base"), hookPlanTestPath("src", "base"), hookPlanTestPath("dst", "base"), "tool", "", "deferred"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "base", WorkspaceID: "workspace", WorkspaceName: "Workspace", SourceLogicalRoot: test.sourceRoot, TargetLogicalRoot: test.targetRoot, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("workspace"), Entries: []hookPlanInputEntry{{ID: "setup", Repository: "base", SourceRepository: test.sourceRepository, TargetRepository: test.targetRepository, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: test.executable, ResolvedExecutable: test.resolved, Availability: test.availability, Timeout: time.Minute}}})
			if err != nil {
				t.Fatal(err)
			}
			entry := plan.Entries()[0]
			if entry.WorkingDirectory != test.targetRepository || entry.ConfiguredExecutable != test.executable || entry.ResolvedExecutable != test.resolved || entry.Availability != test.availability || entry.Arguments == nil {
				t.Fatalf("entry=%#v", entry)
			}
			data, err := json.Marshal(plan)
			if err != nil || strings.Contains(string(data), "sourceBytes") || strings.Contains(string(data), "workspaceState") || strings.Contains(string(data), "environment") {
				t.Fatalf("plan JSON=%s %v", data, err)
			}
		})
	}
}

func TestHookPlanRejectsInvalidCombinationAndMutableFacts(t *testing.T) {
	source, target := hookPlanTestPath("source"), hookPlanTestPath("target")
	base := hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "base", WorkspaceID: "workspace", WorkspaceName: "Workspace", SourceLogicalRoot: source, TargetLogicalRoot: target, Entries: []hookPlanInputEntry{{ID: "setup", Repository: "base", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: "setup", ResolvedExecutable: filepath.Join(target, "setup"), Availability: "available", Timeout: time.Second}}}
	for _, mutate := range []func(*hookPlanInput){
		func(v *hookPlanInput) { v.Event = "post-clone" },
		func(v *hookPlanInput) { v.Entries[0].Availability, v.Entries[0].ResolvedExecutable = "available", "" },
		func(v *hookPlanInput) { v.Entries[0].TargetRepository = hookPlanTestPath("outside") },
		func(v *hookPlanInput) { v.Entries[0].ID = "../unsafe" },
	} {
		input := base
		input.Entries = append([]hookPlanInputEntry(nil), base.Entries...)
		mutate(&input)
		if _, err := newHookPlan(input); err == nil {
			t.Fatal("accepted invalid plan")
		}
	}
}
func TestHookEnvironmentReplacesReservedValues(t *testing.T) {
	source, target := hookPlanTestPath("s"), hookPlanTestPath("t")
	plan, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "p", ProjectName: "P", BaseRepository: "r", WorkspaceID: "w", WorkspaceName: "W", SourceLogicalRoot: source, TargetLogicalRoot: target, SourceBytes: []byte("s"), WorkspaceStateBytes: []byte("w"), Entries: []hookPlanInputEntry{{ID: "h", Repository: "r", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: "x", ResolvedExecutable: filepath.Join(target, "x"), Availability: "available", Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := buildHookEnvironment(HookEnvironmentLocal, false, []string{"WTREE_PROJECT_ID=bad", "A=one", "A=two"}, plan, 0)
	if err != nil || strings.Join(env, "\n") == "" || strings.Contains(strings.Join(env, "\n"), "bad") {
		t.Fatalf("env=%v %v", env, err)
	}
}

func TestHookEnvironmentPortableAllowlistExcludesSecrets(t *testing.T) {
	source, target := hookPlanTestPath("s"), hookPlanTestPath("t")
	plan, err := newHookPlan(hookPlanInput{Operation: "clone", Source: "portable", Event: "post-clone", Policy: "requires-run-hooks", ProjectID: "p", ProjectName: "P", BaseRepository: "r", WorkspaceID: "default", WorkspaceName: "Default", SourceLogicalRoot: source, TargetLogicalRoot: target, SourceBytes: []byte("s"), WorkspaceStateBytes: []byte("w"), Entries: []hookPlanInputEntry{{ID: "h", Repository: "r", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: "x", ResolvedExecutable: filepath.Join(target, "x"), Availability: "available", Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := buildHookEnvironment(HookEnvironmentPortable, false, []string{"PATH=/bin", "LC_ALL=C", "HOME=/secret", "GIT_CONFIG=x", "TOKEN=secret"}, plan, 0)
	if err != nil || !strings.Contains(strings.Join(env, "\n"), "PATH=/bin") || strings.Contains(strings.Join(env, "\n"), "HOME=") || strings.Contains(strings.Join(env, "\n"), "TOKEN=") {
		t.Fatalf("env=%v %v", env, err)
	}
}

func TestHookEnvironmentPolicyMatrix(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	tests := []struct {
		name         string
		policy       HookEnvironmentPolicy
		windows      bool
		inherited    []string
		want, absent []string
	}{
		{"local-last-wins", HookEnvironmentLocal, false, []string{"A=one", "A=two", "HOME=/home", "WTREE_HOOK=forged"}, []string{"A=two", "HOME=/home", "WTREE_HOOK=post-create"}, []string{"A=one", "forged"}},
		{"portable-posix", HookEnvironmentPortable, false, []string{"PATH=/bin", "LANG=C", "LC_MESSAGES=C", "TMPDIR=/tmp", "PATHEXT=.EXE", "HOME=/secret", "WTREE_PROJECT_ID=bad"}, []string{"PATH=/bin", "LANG=C", "LC_MESSAGES=C", "TMPDIR=/tmp", "WTREE_PROJECT_ID=project"}, []string{"PATHEXT=.EXE", "HOME=/secret", "bad"}},
		{"portable-windows-case", HookEnvironmentPortable, true, []string{"path=one", "PATH=two", "pAtHeXt=.PY", "windir=C:\\Windows", "TOKEN=secret", "wtree_hook=bad"}, []string{"PATH=two", "pAtHeXt=.PY", "windir=C:\\Windows", "WTREE_HOOK=post-create"}, []string{"path=one", "TOKEN=secret", "bad"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment, err := buildHookEnvironment(test.policy, test.windows, test.inherited, plan, 0)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(environment, "\n")
			for _, want := range test.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("environment %q lacks %q", joined, want)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(joined, absent) {
					t.Fatalf("environment %q leaks %q", joined, absent)
				}
			}
			if got := environment[len(environment)-14:]; strings.Join(got, "\n") != strings.Join([]string{"WTREE_HOOK=post-create", "WTREE_OPERATION=create", "WTREE_PROJECT_ID=project", "WTREE_PROJECT_NAME=Project", "WTREE_BASE_REPOSITORY_ID=root", "WTREE_WORKSPACE_ID=default", "WTREE_WORKSPACE_NAME=Default", "WTREE_SOURCE_LOGICAL_ROOT=" + plan.authority.sourceRoot, "WTREE_TARGET_LOGICAL_ROOT=" + plan.authority.targetRoot, "WTREE_REPOSITORY_ID=root", "WTREE_SOURCE_REPOSITORY=" + plan.authority.entries[0].SourceRepository, "WTREE_TARGET_REPOSITORY=" + plan.authority.entries[0].TargetRepository, "WTREE_BRANCH=main", "WTREE_HEAD=" + strings.Repeat("a", 40)}, "\n") {
				t.Fatalf("reserved=%v", got)
			}
		})
	}
	for _, inherited := range [][]string{{"broken"}, {"A\nB=value"}, {"A=bad\x00value"}} {
		if _, err := buildHookEnvironment(HookEnvironmentLocal, false, inherited, plan, 0); err == nil {
			t.Fatalf("accepted malformed environment %q", inherited)
		}
	}
}
