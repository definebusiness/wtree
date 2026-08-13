package pathutil_test

import (
	"testing"

	"github.com/marcel/wtree/internal/pathutil"
)

func FuzzResolveMount(f *testing.F) {
	for _, seed := range []string{".", "api", "../escape", `..\\escape`, "απि"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, mount string) {
		_, _ = pathutil.ResolveMount("/workspace", "/workspace", mount, mount == ".")
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
