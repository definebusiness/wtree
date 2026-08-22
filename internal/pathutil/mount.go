// Package pathutil centralizes portable path and mount safety rules.
package pathutil

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MountKind makes the ownership of a mount explicit. Top-level mounts are
// relative to the logical root while child mounts are relative to their
// immediate declared parent.
type MountKind uint8

const (
	TopLevelMount MountKind = iota
	ChildMount
)

// ResolveMount derives one repository path and rejects lexical or canonical
// escapes from the logical root.
func ResolveMount(logicalRoot, parentPath, mount string, kind MountKind) (string, error) {
	normalized, err := NormalizeMount(mount, kind)
	if err != nil {
		return "", err
	}
	rootPath, err := filepath.Abs(logicalRoot)
	if err != nil {
		return "", fmt.Errorf("make logical root absolute: %w", err)
	}
	rootPath = filepath.Clean(rootPath)
	if kind == TopLevelMount {
		candidate := rootPath
		if normalized != "." {
			candidate = filepath.Join(rootPath, filepath.FromSlash(normalized))
		}
		if err := CheckPotentialWithin(rootPath, candidate); err != nil {
			return "", err
		}
		return candidate, nil
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
	if err := CheckPotentialWithin(parentPath, candidate); err != nil {
		return "", fmt.Errorf("resolved child mount must remain within its immediate parent: %w", err)
	}
	return candidate, nil
}

// CheckPotentialWithin compares canonical paths while allowing a planned
// target (or workspace root) not to exist yet. Existing intermediate symlinks
// are always resolved before the containment decision.
func CheckPotentialWithin(root, target string) error {
	canonicalRoot, err := CanonicalPotentialPath(root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	canonicalTarget, err := CanonicalPotentialPath(target)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	if !within(canonicalRoot, canonicalTarget) {
		return fmt.Errorf("resolved target %q escapes workspace root", target)
	}
	return nil
}

// CanonicalPotentialPath resolves every existing component while retaining a
// planned non-existent suffix. It is safe for preflight comparisons.
func CanonicalPotentialPath(value string) (string, error) {
	return canonicalPotential(value)
}

// ValidateMount verifies the portable grammar for the declared mount owner.
func ValidateMount(mount string, kind MountKind) error {
	_, err := NormalizeMount(mount, kind)
	return err
}

// NormalizeMount returns the one portable, slash-separated representation of
// a repository mount. Callers must retain this value for both path placement
// and any Git-ignore rule derived from the mount.
func NormalizeMount(mount string, kind MountKind) (string, error) {
	if !utf8.ValidString(mount) {
		return "", fmt.Errorf("repository mount contains invalid UTF-8")
	}
	if strings.ContainsAny(mount, "\r\n\x00") {
		return "", fmt.Errorf("repository mount contains a line break or NUL")
	}
	if kind == TopLevelMount && mount == "." {
		return ".", nil
	}
	if mount == "" {
		return "", fmt.Errorf("repository mount is required")
	}
	if strings.Contains(mount, `\`) || strings.HasPrefix(mount, "/") || hasWindowsVolume(mount) {
		return "", fmt.Errorf("repository mount %q must be relative", mount)
	}
	cleaned := path.Clean(mount)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("repository mount %q escapes its parent", mount)
	}
	if cleaned != mount {
		return "", fmt.Errorf("repository mount %q must be clean and canonical", mount)
	}
	components := strings.Split(cleaned, "/")
	for _, component := range components {
		if strings.EqualFold(component, ".git") {
			return "", fmt.Errorf("repository mount %q enters forbidden Git administration path", mount)
		}
		if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") || strings.ContainsAny(component, `<>:"|?*`) || strings.IndexFunc(component, unicode.IsControl) >= 0 || reservedDeviceComponent(component) {
			return "", fmt.Errorf("repository mount %q has a platform-unsafe component", mount)
		}
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

func reservedDeviceComponent(component string) bool {
	base := strings.ToUpper(strings.Split(component, ".")[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

// CaseFoldedPathEqual reports whether two clean paths alias on a
// case-insensitive filesystem. It deliberately applies the comparison on
// every platform so preflight safety does not depend on the host filesystem.
func CaseFoldedPathEqual(left, right string) bool {
	leftComponents, rightComponents := caseFoldedPathComponents(left), caseFoldedPathComponents(right)
	if len(leftComponents) != len(rightComponents) {
		return false
	}
	for index := range leftComponents {
		if !strings.EqualFold(leftComponents[index], rightComponents[index]) {
			return false
		}
	}
	return true
}

// CaseFoldedPathOverlap reports equality or containment under the same
// cross-platform case-folding rule used for repository placement preflight.
func CaseFoldedPathOverlap(left, right string) bool {
	leftComponents, rightComponents := caseFoldedPathComponents(left), caseFoldedPathComponents(right)
	if len(leftComponents) > len(rightComponents) {
		leftComponents, rightComponents = rightComponents, leftComponents
	}
	for index := range leftComponents {
		if !strings.EqualFold(leftComponents[index], rightComponents[index]) {
			return false
		}
	}
	return true
}

func caseFoldedPathComponents(value string) []string {
	cleaned := path.Clean(filepath.ToSlash(value))
	if cleaned == "." {
		return nil
	}
	return strings.Split(cleaned, "/")
}

func within(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
