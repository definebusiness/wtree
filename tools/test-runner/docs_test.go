package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDocsFindsEveryInlineLinkAndSupportsAngles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target one.md"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"[a](missing.md) [b](target one.md)", "[a](target one.md) [b](missing.md) [c](target one.md)", "[a](target one.md) [b](missing.md)"} {
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkDocs(root); err == nil {
			t.Fatal("missing link accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("[x](<target one.md#part>) ![i](target one.md) [web](https://example.com)"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkDocs(root); err != nil {
		t.Fatal(err)
	}
}
