package service

import (
	"fmt"
	"strings"

	"github.com/definebusiness/wtree/internal/pathutil"
)

// NestedDirectoryRule returns the one anchored literal Git-ignore rule for an
// already-normalized non-root mount. Keeping normalization outside this
// function ensures placement and protection use the same mount value.
func NestedDirectoryRule(mount string) (string, error) {
	normalized, err := pathutil.NormalizeMount(mount, false)
	if err != nil {
		return "", err
	}
	if mount != normalized {
		return "", fmt.Errorf("mount %q is not normalized", mount)
	}

	var rule strings.Builder
	rule.WriteByte('/')
	for _, character := range mount {
		switch character {
		case '\\', '#', '!', '*', '?', '[', ']', ' ', '\t':
			rule.WriteByte('\\')
		}
		rule.WriteRune(character)
	}
	rule.WriteByte('/')
	return rule.String(), nil
}
