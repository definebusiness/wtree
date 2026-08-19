package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
)

type ignoreInspectorStub struct {
	evidence map[string]git.WorkingTreeIgnoreEvidence
	err      error
}

func (s ignoreInspectorStub) InspectWorkingTreeIgnore(_ context.Context, parent, mount string) (git.WorkingTreeIgnoreEvidence, error) {
	if s.err != nil {
		return git.WorkingTreeIgnoreEvidence{}, s.err
	}
	return s.evidence[parent+"\x00"+mount], nil
}

func TestIgnorePlannerCoalescesDirectChildrenInParentFirstOrder(t *testing.T) {
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	for _, path := range []string{backend, filepath.Join(root, "other")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	planner := NewIgnorePlanner(ignoreInspectorStub{})
	plan, err := planner.Plan(context.Background(), []IgnoreRequirement{
		{ParentRepositoryID: "root", ChildRepositoryID: "backend", ParentPath: root, Mount: "backend"},
		{ParentRepositoryID: "backend", ChildRepositoryID: "shared", ParentPath: backend, Mount: "shared"},
		{ParentRepositoryID: "root", ChildRepositoryID: "z-child", ParentPath: root, Mount: "z"},
		{ParentRepositoryID: "root", ChildRepositoryID: "a-child", ParentPath: root, Mount: "a"},
		{ParentRepositoryID: "root", ChildRepositoryID: "other", ParentPath: root, Mount: "other"},
		{ParentRepositoryID: "other", ChildRepositoryID: "other-child", ParentPath: filepath.Join(root, "other"), Mount: "child"},
	})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if got, want := plan.Files[0].ParentRepositoryID, "root"; got != want {
		t.Fatalf("first parent = %q, want %q", got, want)
	}
	if got, want := plan.Files[0].AddedRules, []string{"/a/", "/backend/", "/other/", "/z/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root rules = %#v, want %#v", got, want)
	}
	if got, want := []string{plan.Files[0].ParentRepositoryID, plan.Files[1].ParentRepositoryID, plan.Files[2].ParentRepositoryID}, []string{"root", "backend", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("file order = %v, want %v", got, want)
	}
	for _, file := range plan.Files {
		if !file.Changed || file.Path != filepath.Join(file.ParentPath, ".gitignore") {
			t.Fatalf("planned file = %#v", file)
		}
	}
}

func TestIgnorePlannerPreservesBytesAndSuppressesQualifiedRule(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, ".gitignore")
	before := []byte("one\r\ntwo\r\n")
	if err := os.WriteFile(path, before, 0o640); err != nil {
		t.Fatal(err)
	}
	deeper := filepath.Join(parent, "nested", ".gitignore")
	if err := os.MkdirAll(filepath.Dir(deeper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deeper, []byte("/child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := NewIgnorePlanner(ignoreInspectorStub{evidence: map[string]git.WorkingTreeIgnoreEvidence{
		parent + "\x00nested/child": {Ignored: true, Source: deeper, Pattern: "/child/", Path: "nested/child"},
	}})
	plan, err := planner.Plan(context.Background(), []IgnoreRequirement{
		{ParentRepositoryID: "root", ChildRepositoryID: "visible", ParentPath: parent, Mount: "visible"},
		{ParentRepositoryID: "root", ChildRepositoryID: "covered", ParentPath: parent, Mount: "nested/child"},
	})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if got, want := plan.Files[0].AddedRules, []string{"/visible/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %#v, want %#v", got, want)
	}
	if got, want := string(plan.Files[0].NewBytes), "one\r\ntwo\r\n/visible/\r\n"; got != want {
		t.Fatalf("new bytes = %q, want %q", got, want)
	}
	if got := plan.Files[0].Snapshot.Mode; got != 0o640 {
		t.Fatalf("mode = %o, want 0640", got)
	}
	if _, err := NewIgnoreApplier().Apply(context.Background(), plan); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "one\r\ntwo\r\n/visible/\r\n" {
		t.Fatalf("applied bytes = %q, %v", after, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("applied mode = %v, %v; want 0640", info.Mode(), err)
	}
}

func TestIgnorePlannerPreservesAllSupportedLineEndings(t *testing.T) {
	for _, test := range []struct {
		name   string
		before []byte
		want   []byte
	}{
		{name: "empty", before: nil, want: []byte("/child/\n")},
		{name: "lf", before: []byte("one\ntwo\n"), want: []byte("one\ntwo\n/child/\n")},
		{name: "crlf", before: []byte("one\r\ntwo\r\n"), want: []byte("one\r\ntwo\r\n/child/\r\n")},
		{name: "mixed", before: []byte("one\r\ntwo\n"), want: []byte("one\r\ntwo\n/child/\n")},
		{name: "unterminated", before: []byte("one\ntwo"), want: []byte("one\ntwo\n/child/\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, ".gitignore")
			if err := os.WriteFile(path, test.before, 0o640); err != nil {
				t.Fatal(err)
			}
			plan, err := NewIgnorePlanner(ignoreInspectorStub{}).Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "child", ParentPath: parent, Mount: "child"}})
			if err != nil {
				t.Fatal(err)
			}
			if got := plan.Files[0].NewBytes; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("planned bytes = %q, want %q", got, test.want)
			}
			if _, err := NewIgnoreApplier().Apply(context.Background(), plan); err != nil {
				t.Fatalf("Apply() = %v", err)
			}
			if got, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("applied bytes = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestIgnorePlannerRejectsVisibleExactLineAndUnsafeOrEscapingTarget(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, ".gitignore")
	if err := os.WriteFile(path, []byte("/child/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planner := NewIgnorePlanner(ignoreInspectorStub{})
	_, err := planner.Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "child", ParentPath: parent, Mount: "child"}})
	assertIgnoreErrorKind(t, err, ErrorConflict)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
		t.Fatal(err)
	}
	_, err = planner.Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "child", ParentPath: parent, Mount: "child"}})
	assertIgnoreErrorKind(t, err, ErrorValidation)
}

func TestIgnorePlannerRejectsStaleExistenceModeAndTypeWithoutOverwriting(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{name: "appeared", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("other\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mode", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "type", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, ".gitignore")
			if test.name != "appeared" {
				if err := os.WriteFile(path, []byte("/child/\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			planner := NewIgnorePlanner(ignoreInspectorStub{})
			plan, err := planner.Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "next", ParentPath: parent, Mount: "next"}})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, path)
			_, err = NewIgnoreApplier().Apply(context.Background(), plan)
			assertIgnoreErrorKind(t, err, ErrorConflict)
			info, statErr := os.Lstat(path)
			if statErr != nil || (test.name == "type" && !info.IsDir()) {
				t.Fatalf("target after stale apply = %v, %v", info, statErr)
			}
		})
	}
}

func TestIgnoreApplierRejectsRetargetedMissingParentBeforeReplacement(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err := NewIgnorePlanner(ignoreInspectorStub{}).Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "child", ParentPath: parent, Mount: "child"}})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "moved-parent")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	_, err = NewIgnoreApplier().Apply(context.Background(), plan)
	assertIgnoreErrorKind(t, err, ErrorConflict)
	for _, path := range []string{filepath.Join(outside, ".gitignore"), filepath.Join(moved, ".gitignore")} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("retargeted apply wrote %q: %v", path, statErr)
		}
	}
}

func TestIgnoreApplierClassifiesFinalBoundaryStaleTypeAndReadFailuresAsConflict(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{name: "type", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreadable", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Chmod(path, 0o000); err != nil {
				t.Fatal(err)
			}
			if _, err := os.ReadFile(path); err == nil {
				t.Skip("runtime permits reading mode-000 files")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, ".gitignore")
			if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			plan, err := NewIgnorePlanner(ignoreInspectorStub{}).Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "child", ParentPath: parent, Mount: "child"}})
			if err != nil {
				t.Fatal(err)
			}
			applier := NewIgnoreApplierWith(func(file IgnoreFilePlan, beforeReplace func() error) (bool, error) {
				test.mutate(t, file.Path)
				return false, beforeReplace()
			})
			_, err = applier.Apply(context.Background(), plan)
			assertIgnoreErrorKind(t, err, ErrorConflict)
		})
	}
}

func TestIgnoreApplierRejectsStaleSnapshotsAndReportsPartialProgress(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	planner := NewIgnorePlanner(ignoreInspectorStub{})
	plan, err := planner.Plan(context.Background(), []IgnoreRequirement{
		{ParentRepositoryID: "first", ChildRepositoryID: "a", ParentPath: first, Mount: "a"},
		{ParentRepositoryID: "second", ChildRepositoryID: "b", ParentPath: second, Mount: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Files[0].Path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := NewIgnoreApplier().Apply(context.Background(), plan)
	assertIgnoreErrorKind(t, err, ErrorConflict)
	if len(result.Changed) != 0 || len(result.Remaining) != 2 {
		t.Fatalf("stale result = %#v", result)
	}

	plan, err = planner.Plan(context.Background(), []IgnoreRequirement{
		{ParentRepositoryID: "first", ChildRepositoryID: "a", ParentPath: first, Mount: "a"},
		{ParentRepositoryID: "second", ChildRepositoryID: "b", ParentPath: second, Mount: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	applier := NewIgnoreApplierWith(func(file IgnoreFilePlan, beforeReplace func() error) (bool, error) {
		if file.ParentRepositoryID == "second" {
			return false, errors.New("injected replacement failure")
		}
		return applyIgnoreFile(file, beforeReplace)
	})
	result, err = applier.Apply(context.Background(), plan)
	assertIgnoreErrorKind(t, err, ErrorInternal)
	if got, want := result.Changed, []IgnoreFileUpdate{{ParentRepositoryID: "first", Path: filepath.Join(first, ".gitignore"), AddedRules: []string{"/a/"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %#v, want %#v", got, want)
	}
	if got, want := result.Remaining, []IgnoreFileUpdate{{ParentRepositoryID: "second", Path: filepath.Join(second, ".gitignore"), AddedRules: []string{"/b/"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining = %#v, want %#v", got, want)
	}

	plan, err = planner.Plan(context.Background(), []IgnoreRequirement{
		{ParentRepositoryID: "first", ChildRepositoryID: "c", ParentPath: first, Mount: "c"},
		{ParentRepositoryID: "second", ChildRepositoryID: "d", ParentPath: second, Mount: "d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	applier = NewIgnoreApplierWith(func(file IgnoreFilePlan, beforeReplace func() error) (bool, error) {
		if file.ParentRepositoryID == "second" {
			replaced, err := applyIgnoreFile(file, beforeReplace)
			if err != nil {
				return replaced, err
			}
			return replaced, errors.New("injected post-replacement failure")
		}
		return applyIgnoreFile(file, beforeReplace)
	})
	result, err = applier.Apply(context.Background(), plan)
	assertIgnoreErrorKind(t, err, ErrorInternal)
	if got, want := result.Changed, []IgnoreFileUpdate{
		{ParentRepositoryID: "first", Path: filepath.Join(first, ".gitignore"), AddedRules: []string{"/c/"}},
		{ParentRepositoryID: "second", Path: filepath.Join(second, ".gitignore"), AddedRules: []string{"/d/"}},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("post-replacement changed = %#v, want %#v", got, want)
	}
	if len(result.Remaining) != 0 {
		t.Fatalf("post-replacement remaining = %#v, want empty", result.Remaining)
	}
	if got, readErr := os.ReadFile(filepath.Join(second, ".gitignore")); readErr != nil || string(got) != "/d/\n" {
		t.Fatalf("post-replacement target = %q, %v; want complete new file", got, readErr)
	}

	retryPlanner := NewIgnorePlanner(ignoreInspectorStub{evidence: map[string]git.WorkingTreeIgnoreEvidence{
		first + "\x00a": {Ignored: true, Source: filepath.Join(first, ".gitignore"), Pattern: "/a/", Path: "a"},
	}})
	retry, err := retryPlanner.Plan(context.Background(), []IgnoreRequirement{
		{ParentRepositoryID: "first", ChildRepositoryID: "a", ParentPath: first, Mount: "a"},
		{ParentRepositoryID: "second", ChildRepositoryID: "b", ParentPath: second, Mount: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Files[0].AddedRules) != 0 || !retry.Files[1].Changed {
		t.Fatalf("retry plan = %#v", retry.Files)
	}
	if _, err := NewIgnoreApplier().Apply(context.Background(), retry); err != nil {
		t.Fatalf("retry Apply() = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(first, ".gitignore")); err != nil || string(got) != "changed\n/a/\n/c/\n" {
		t.Fatalf("first retry file = %q, %v", got, err)
	}
}

func TestIgnoreApplierCancellationRetainsCompletedTargetsAndLeavesLaterTargetsUntouched(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := NewIgnorePlanner(ignoreInspectorStub{}).Plan(context.Background(), []IgnoreRequirement{
		{ParentRepositoryID: "first", ChildRepositoryID: "a", ParentPath: first, Mount: "a"},
		{ParentRepositoryID: "second", ChildRepositoryID: "b", ParentPath: second, Mount: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	applier := NewIgnoreApplierWith(func(file IgnoreFilePlan, beforeReplace func() error) (bool, error) {
		replaced, err := applyIgnoreFile(file, beforeReplace)
		if file.ParentRepositoryID == "first" && err == nil {
			cancel()
		}
		return replaced, err
	})
	result, err := applier.Apply(ctx, plan)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply() error = %v, want context cancellation", err)
	}
	if !strings.Contains(err.Error(), "source ignore progress: changed files") || !strings.Contains(err.Error(), "remaining targets") {
		t.Fatalf("Apply() cancellation diagnostic = %q, want retained progress", err)
	}
	if got, want := result.Changed, []IgnoreFileUpdate{{ParentRepositoryID: "first", Path: filepath.Join(first, ".gitignore"), AddedRules: []string{"/a/"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %#v, want %#v", got, want)
	}
	if got, want := result.Remaining, []IgnoreFileUpdate{{ParentRepositoryID: "second", Path: filepath.Join(second, ".gitignore"), AddedRules: []string{"/b/"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining = %#v, want %#v", got, want)
	}
	if got, readErr := os.ReadFile(filepath.Join(first, ".gitignore")); readErr != nil || string(got) != "/a/\n" {
		t.Fatalf("completed target = %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(second, ".gitignore")); !os.IsNotExist(statErr) {
		t.Fatalf("later target was replaced after cancellation: %v", statErr)
	}
}

func assertIgnoreErrorKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
