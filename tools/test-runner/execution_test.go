package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExecuteChangedActionsOrdersAndStopsOnFailure(t *testing.T) {
	actions := []ChangedAction{{Kind: "docs"}, {Kind: "harness"}, {Kind: "test", Package: "example/a"}, {Kind: "cross-compile", Package: "example/a", Platform: "windows"}}
	executor := &fakeActionExecutor{failAt: -1}
	if err := executeChangedActions(context.Background(), executor, actions, "5m", "arm64"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(executor.calls, "|"); !strings.Contains(got, "bash scripts/docs-check.sh") || !strings.Contains(got, "bash scripts/local-test-targets_test.sh") || !strings.Contains(got, "go test -short=false -count=1 -timeout=5m example/a") || !strings.Contains(got, "go test -c -o") || !strings.Contains(got, "GOOS=windows,GOARCH=arm64") {
		t.Fatalf("calls=%q", got)
	}
	for _, failAt := range []int{0, 1, 2, 3} {
		executor := &fakeActionExecutor{failAt: failAt}
		if err := executeChangedActions(context.Background(), executor, actions, "5m", "arm64"); err == nil || len(executor.calls) != failAt+1 {
			t.Fatalf("failAt=%d err=%v calls=%v", failAt, err, executor.calls)
		}
	}
}

type fakeActionExecutor struct {
	calls  []string
	failAt int
}

func (fake *fakeActionExecutor) Run(_ context.Context, name string, args []string, environment []string) error {
	fake.calls = append(fake.calls, name+" "+strings.Join(args, " ")+" "+strings.Join(environment, ","))
	if len(fake.calls)-1 == fake.failAt {
		return errors.New("controlled failure")
	}
	return nil
}

func TestChangedExecutionPlanConsumesSelectionOnceAndOrdersActions(t *testing.T) {
	plan, err := changedExecutionPlan(ChangeSelection{Packages: []string{"example/b", "example/a", "example/a"}, Harness: true, Documentation: true, Platforms: []string{"darwin", "windows", "windows"}}, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	want := []ChangedAction{{Kind: "docs"}, {Kind: "harness"}, {Kind: "test", Package: "example/a"}, {Kind: "test", Package: "example/b"}, {Kind: "cross-compile", Package: "example/a", Platform: "windows"}, {Kind: "cross-compile", Package: "example/b", Platform: "windows"}}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan=%#v want=%#v", plan, want)
	}
}

func TestChangedExecutionPlanFailsClosed(t *testing.T) {
	for _, selection := range []ChangeSelection{{Packages: []string{"unsafe"}}, {Platforms: []string{"plan9"}}} {
		if _, err := changedExecutionPlan(selection, "darwin", "arm64"); err == nil || strings.Contains(err.Error(), "HOME") {
			t.Fatalf("error=%v", err)
		}
	}
}
