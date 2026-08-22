package pathutil_test

import (
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/pathutil"
)

func FuzzResolveMount(f *testing.F) {
	for _, seed := range []string{".", "api", "../escape", `..\\escape`, "απि"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, mount string) {
		kind := pathutil.ChildMount
		if mount == "." {
			kind = pathutil.TopLevelMount
		}
		_, _ = pathutil.ResolveMount("/workspace", "/workspace", mount, kind)
	})
}

func FuzzNormalizeMount(f *testing.F) {
	for _, seed := range []string{"api", `services\api`, "services/../api", "./api", "../escape", "line\nbreak", "世界"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, mount string) {
		normalized, err := pathutil.NormalizeMount(mount, pathutil.ChildMount)
		if err != nil {
			return
		}
		if normalized == "" || strings.ContainsAny(normalized, "\\\r\n\x00") {
			t.Fatalf("NormalizeMount(%q) returned unsafe %q", mount, normalized)
		}
		again, err := pathutil.NormalizeMount(normalized, pathutil.ChildMount)
		if err != nil || again != normalized {
			t.Fatalf("NormalizeMount(%q) is not idempotent: %q, %v", normalized, again, err)
		}
	})
}

func FuzzStorageName(f *testing.F) {
	for _, seed := range []string{"feature/login", "A B", "απि", "../escape", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		first, second := pathutil.StorageName(name), pathutil.StorageName(name)
		if first != second || first == "" {
			t.Fatalf("StorageName(%q) = %q, %q", name, first, second)
		}
	})
}
