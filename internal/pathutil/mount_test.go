package pathutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/pathutil"
)

func TestNormalizeMountAcceptsCleanTopLevelAndChildForms(t *testing.T) {
	for _, test := range []struct {
		input string
		kind  pathutil.MountKind
		want  string
	}{
		{input: ".", kind: pathutil.TopLevelMount, want: "."},
		{input: "api", kind: pathutil.TopLevelMount, want: "api"},
		{input: "services/api", kind: pathutil.TopLevelMount, want: "services/api"},
		{input: "api", kind: pathutil.ChildMount, want: "api"},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := pathutil.NormalizeMount(test.input, test.kind)
			if err != nil || got != test.want {
				t.Fatalf("NormalizeMount(%q, %d) = %q, %v; want %q, nil", test.input, test.kind, got, err, test.want)
			}
		})
	}
}

func TestNormalizeMountRejectsUnrepresentableOrAmbiguousValues(t *testing.T) {
	for _, mount := range []string{"", ".", "/absolute", `C:\\absolute`, "../escape", "a/../../escape", "line\nbreak", "line\rbreak", "nul\x00byte", string([]byte{0xff}), `services\api`, "services/../api", "./services/api", ".git/config", "repo/.git/hooks"} {
		t.Run("mount", func(t *testing.T) {
			if _, err := pathutil.NormalizeMount(mount, pathutil.ChildMount); err == nil {
				t.Fatalf("NormalizeMount(%q, false) error = nil", mount)
			}
		})
	}
}

func TestNormalizeMountRequiresLiteralRootMarker(t *testing.T) {
	for _, mount := range []string{"", "./", "segment/..", "segment/../", `segment\\..`} {
		t.Run(mount, func(t *testing.T) {
			if _, err := pathutil.NormalizeMount(mount, pathutil.TopLevelMount); err == nil {
				t.Fatalf("NormalizeMount(%q, true) error = nil", mount)
			}
		})
	}

	got, err := pathutil.NormalizeMount(".", pathutil.TopLevelMount)
	if err != nil || got != "." {
		t.Fatalf("NormalizeMount(., true) = %q, %v; want ., nil", got, err)
	}
}

func TestResolveMountKeepsNestedRepositoriesInsideWorkspace(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	root, err := pathutil.ResolveMount(workspaceRoot, "", ".", pathutil.TopLevelMount)
	if err != nil {
		t.Fatalf("resolve root mount: %v", err)
	}
	backend, err := pathutil.ResolveMount(workspaceRoot, root, "api", pathutil.ChildMount)
	if err != nil {
		t.Fatalf("resolve backend mount: %v", err)
	}
	shared, err := pathutil.ResolveMount(workspaceRoot, backend, "common", pathutil.ChildMount)
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
		kind  pathutil.MountKind
	}{
		{name: "empty child", mount: "", kind: pathutil.ChildMount},
		{name: "dot child", mount: ".", kind: pathutil.ChildMount},
		{name: "parent escape", mount: "../outside", kind: pathutil.ChildMount},
		{name: "windows parent escape", mount: `..\outside`, kind: pathutil.ChildMount},
		{name: "absolute", mount: "/outside", kind: pathutil.ChildMount},
		{name: "windows absolute", mount: `C:\outside`, kind: pathutil.ChildMount},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pathutil.ResolveMount(workspaceRoot, workspaceRoot, test.mount, test.kind); err == nil {
				t.Fatal("ResolveMount() error = nil, want unsafe mount rejection")
			}
		})
	}
}

func TestValidateMountRejectsCanonicalAliasesAndGitAdministrationPaths(t *testing.T) {
	for _, mount := range []string{`services\api`, "services/../api", "./services/api", ".git", "nested/.git/config", ".git.", "nested/.git. ", "NUL.txt", "COM1", "component.", "component ", "component<unsafe"} {
		if err := pathutil.ValidateMount(mount, pathutil.ChildMount); err == nil {
			t.Errorf("ValidateMount(%q) error = nil, want rejection", mount)
		}
	}
	workspaceRoot := t.TempDir()
	resolved, err := pathutil.ResolveMount(workspaceRoot, workspaceRoot, "services/api", pathutil.ChildMount)
	if err != nil || resolved != filepath.Join(workspaceRoot, "services", "api") {
		t.Fatalf("ResolveMount(clean) = %q, %v", resolved, err)
	}
}

func TestResolveMountRejectsChildSymlinkOutsideImmediateParent(t *testing.T) {
	workspaceRoot := t.TempDir()
	parentPath := filepath.Join(workspaceRoot, "services", "base")
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspaceRoot, "grouping"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspaceRoot, "grouping"), filepath.Join(parentPath, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := pathutil.ResolveMount(workspaceRoot, parentPath, "link/child", pathutil.ChildMount); err == nil {
		t.Fatal("ResolveMount() error = nil, want immediate-parent containment rejection")
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

func TestCaseFoldedPathOverlapUsesComponentBoundaries(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        bool
	}{
		{left: "api", right: "API", want: true},
		{left: "services/API", right: "services/api/child", want: true},
		{left: "api/one", right: "API/two", want: false},
		{left: "api", right: "apricot", want: false},
		{left: "space 世界", right: "SPACE 世界", want: true},
	} {
		t.Run(test.left+"/"+test.right, func(t *testing.T) {
			if got := pathutil.CaseFoldedPathOverlap(test.left, test.right); got != test.want {
				t.Fatalf("CaseFoldedPathOverlap(%q, %q) = %t, want %t", test.left, test.right, got, test.want)
			}
		})
	}
}
