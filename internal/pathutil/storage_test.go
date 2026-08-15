package pathutil_test

import (
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/pathutil"
)

func TestStorageNameIsDeterministicReadableAndCollisionSafe(t *testing.T) {
	fromSlash := pathutil.StorageName("feature/login")
	fromDash := pathutil.StorageName("feature-login")

	if fromSlash == fromDash {
		t.Fatalf("StorageName() collided: %q", fromSlash)
	}
	if !strings.HasPrefix(fromSlash, "feature-login-") {
		t.Errorf("StorageName(feature/login) = %q, want readable feature-login prefix", fromSlash)
	}
	if got := pathutil.StorageName("feature/login"); got != fromSlash {
		t.Errorf("StorageName() = %q, want deterministic %q", got, fromSlash)
	}
}

func TestStorageNamesRejectsDuplicateLogicalNames(t *testing.T) {
	_, err := pathutil.StorageNames([]string{"feature/login", "feature/login"})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("StorageNames() error = %v, want duplicate-name error", err)
	}
}

func TestStorageNamePreservesUnicodeReadability(t *testing.T) {
	if got := pathutil.StorageName("feature/登录"); !strings.HasPrefix(got, "feature-登录-") {
		t.Errorf("StorageName() = %q, want Unicode-readable prefix", got)
	}
}
