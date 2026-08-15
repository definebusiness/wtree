package pathutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/pathutil"
)

func TestResolveMountKeepsNestedRepositoriesInsideWorkspace(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	root, err := pathutil.ResolveMount(workspaceRoot, "", ".", true)
	if err != nil {
		t.Fatalf("resolve root mount: %v", err)
	}
	backend, err := pathutil.ResolveMount(workspaceRoot, root, "api", false)
	if err != nil {
		t.Fatalf("resolve backend mount: %v", err)
	}
	shared, err := pathutil.ResolveMount(workspaceRoot, backend, "common", false)
	if err != nil {
		t.Fatalf("resolve shared mount: %v", err)
	}
	if got, want := shared, filepath.Join(workspaceRoot, "api", "common"); got != want {
		t.Errorf("resolved path = %q, want %q", got, want)
	}
}

func TestResolveMountRejectsUnsafeMounts(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, test := range []struct {
		name  string
		mount string
		root  bool
	}{
		{name: "empty child", mount: "", root: false},
		{name: "dot child", mount: ".", root: false},
		{name: "parent escape", mount: "../outside", root: false},
		{name: "windows parent escape", mount: `..\outside`, root: false},
		{name: "absolute", mount: "/outside", root: false},
		{name: "windows absolute", mount: `C:\outside`, root: false},
		{name: "root relocation", mount: "api", root: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pathutil.ResolveMount(workspaceRoot, workspaceRoot, test.mount, test.root); err == nil {
				t.Fatal("ResolveMount() error = nil, want unsafe mount rejection")
			}
		})
	}
}

func TestCheckResolvedWithinRejectsSymlinkEscape(t *testing.T) {
	workspaceRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(workspaceRoot, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := pathutil.CheckResolvedWithin(workspaceRoot, link); err == nil {
		t.Fatal("CheckResolvedWithin() error = nil, want symlink escape rejection")
	}
}
