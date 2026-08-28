//go:build windows

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
)

func TestWindowsIgnorePlanApplyRejectsAliasReplacementAndPreservesGenerations(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, ".gitignore")
	initial := []byte("/existing/\n")
	if err := os.WriteFile(target, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(parent, "unrelated")
	if err := os.WriteFile(unrelated, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	alias := alternateWindowsSpelling(parent)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	aliasParentInfo, err := os.Lstat(alias)
	if err != nil || !os.SameFile(parentInfo, aliasParentInfo) {
		t.Fatalf("parent alias identity = %v, %v", aliasParentInfo, err)
	}
	planner := NewIgnorePlanner(ignoreInspectorStub{evidence: map[string]git.WorkingTreeIgnoreEvidence{}})
	plan, err := planner.Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "child", ParentPath: alias, Mount: "child"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Snapshot.parentInfo == nil || !os.SameFile(plan.Files[0].Snapshot.parentInfo, parentInfo) {
		t.Fatalf("planned alias parent identity = %#v", plan.Files)
	}
	if _, err := NewIgnoreApplier().Apply(context.Background(), plan); err != nil {
		t.Fatalf("Apply() through alias = %v", err)
	}
	want := []byte("/existing/\n/child/\n")
	if got, err := os.ReadFile(target); err != nil || string(got) != string(want) {
		t.Fatalf("canonical target after alias apply = %q, %v; want %q", got, err, want)
	}

	stale, err := planner.Plan(context.Background(), []IgnoreRequirement{{ParentRepositoryID: "root", ChildRepositoryID: "next", ParentPath: alias, Mount: "next"}})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "expected-generation")
	attacker := []byte("attacker generation\n")
	applier := NewIgnoreApplierWith(func(file IgnoreFilePlan, beforeReplace func() error) (bool, error) {
		if err := os.Rename(file.Path, moved); err != nil {
			return false, err
		}
		if err := os.WriteFile(file.Path, attacker, 0o600); err != nil {
			return false, err
		}
		return false, beforeReplace()
	})
	if _, err := applier.Apply(context.Background(), stale); err == nil || !hasIgnoreErrorKind(err, ErrorConflict) {
		t.Fatalf("Apply() stale alias replacement error = %v, want conflict", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(attacker) {
		t.Fatalf("replacement generation = %q, %v; want %q", got, err, attacker)
	}
	if got, err := os.ReadFile(moved); err != nil || string(got) != string(want) {
		t.Fatalf("prior generation = %q, %v; want %q", got, err, want)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "preserve" {
		t.Fatalf("unrelated generation = %q, %v", got, err)
	}
}

func alternateWindowsSpelling(path string) string {
	volume := filepath.VolumeName(path)
	if len(volume) >= 2 && volume[1] == ':' {
		first := volume[:1]
		if first == strings.ToLower(first) {
			first = strings.ToUpper(first)
		} else {
			first = strings.ToLower(first)
		}
		return first + path[1:]
	}
	return filepath.ToSlash(path)
}

func hasIgnoreErrorKind(err error, want ErrorKind) bool {
	value := &Error{}
	return errors.As(err, &value) && value.Kind == want
}
