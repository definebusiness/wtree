package fsutil

import (
	"errors"
	"os"
	"testing"
)

func TestPrivatePathNotExistRejectsRetainedDirectoryAuthority(t *testing.T) {
	anchor := t.TempDir()
	if path, err := OpenPrivatePath(anchor, []string{"missing"}, "record", false); err == nil || !PrivatePathNotExist(err) {
		if path != nil {
			_ = path.Close()
		}
		t.Fatalf("initial private-path absence = %v, want recognized absence", err)
	}
	leaf, err := OpenPrivatePath(anchor, []string{"present"}, "record", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.ReadFile(); err == nil || !PrivatePathNotExist(err) {
		_ = leaf.Close()
		t.Fatalf("authoritative leaf absence = %v, want recognized absence", err)
	}
	if err := leaf.Close(); err != nil {
		t.Fatal(err)
	}
	if PrivatePathNotExist(errors.Join(errPrivateDirectoryAuthority, os.ErrNotExist)) {
		t.Fatal("retained directory authority failure was classified as absence")
	}
	if PrivatePathNotExist(os.ErrNotExist) {
		t.Fatal("unmarked platform not-found was classified as authoritative absence")
	}
	if !PrivatePathNotExist(markPrivatePathNotExist(os.ErrNotExist)) {
		t.Fatal("positively marked private-path absence was not classified")
	}
}
