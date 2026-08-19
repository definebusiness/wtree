// Package pathutil centralizes portable path and mount safety rules.
package pathutil

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ResolveMount derives one repository path from its parent-relative mount and
// rejects lexical escapes from workspaceRoot.
func ResolveMount(workspaceRoot, parentPath, mount string, root bool) (string, error) {
	normalized, err := NormalizeMount(mount, root)
	if err != nil {
		return "", err
	}
	rootPath, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("make workspace root absolute: %w", err)
	}
	if root {
		return filepath.Clean(rootPath), nil
	}
	parentPath, err = filepath.Abs(parentPath)
	if err != nil {
		return "", fmt.Errorf("make parent path absolute: %w", err)
	}
	if !within(rootPath, parentPath) {
		return "", fmt.Errorf("parent path %q is outside workspace root", parentPath)
	}
	candidate := filepath.Join(parentPath, filepath.FromSlash(normalized))
	if !within(rootPath, candidate) {
		return "", fmt.Errorf("mount %q escapes workspace root", mount)
	}
	if err := CheckPotentialWithin(rootPath, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

// CheckPotentialWithin compares canonical paths while allowing a planned
// target (or workspace root) not to exist yet. Existing intermediate symlinks
// are always resolved before the containment decision.
func CheckPotentialWithin(root, target string) error {
	canonicalRoot, err := canonicalPotential(root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	canonicalTarget, err := canonicalPotential(target)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	if !within(canonicalRoot, canonicalTarget) {
		return fmt.Errorf("resolved target %q escapes workspace root", target)
	}
	return nil
}

// ValidateMount verifies that only the root may use the workspace-root mount.
func ValidateMount(mount string, root bool) error {
	_, err := NormalizeMount(mount, root)
	return err
}

// NormalizeMount returns the one portable, slash-separated representation of
// a repository mount. Callers must retain this value for both path placement
// and any Git-ignore rule derived from the mount.
func NormalizeMount(mount string, root bool) (string, error) {
	if !utf8.ValidString(mount) {
		return "", fmt.Errorf("repository mount contains invalid UTF-8")
	}
	if strings.ContainsAny(mount, "\r\n\x00") {
		return "", fmt.Errorf("repository mount contains a line break or NUL")
	}
	normalized := strings.ReplaceAll(mount, `\`, "/")
	if root {
		if mount != "." {
			return "", fmt.Errorf("root repository mount must be %q", ".")
		}
		return ".", nil
	}
	if mount == "" {
		return "", fmt.Errorf("repository mount is required")
	}
	if strings.HasPrefix(normalized, "/") || hasWindowsVolume(normalized) {
		return "", fmt.Errorf("repository mount %q must be relative", mount)
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("repository mount %q escapes its parent", mount)
	}
	return cleaned, nil
}

// CheckResolvedWithin rejects a target that leaves root after symlinks resolve.
// Both paths must already exist so their canonical locations can be compared.
func CheckResolvedWithin(root, target string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve workspace root symlinks: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve target symlinks: %w", err)
	}
	if !within(resolvedRoot, resolvedTarget) {
		return fmt.Errorf("resolved target %q escapes workspace root", target)
	}
	return nil
}

func canonicalPotential(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(abs)
	var trailing []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{resolved}, trailing...)...), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing path ancestor for %q", value)
		}
		trailing = append([]string{filepath.Base(current)}, trailing...)
		current = parent
	}
}

func hasWindowsVolume(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':'
}

func within(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
