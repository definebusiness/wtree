// Package discovery finds independent Git repositories inside a project tree.
package discovery

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gitadapter "github.com/definebusiness/wtree/internal/git"
)

type Repository struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	ParentID string `json:"parentId"`
	Mount    string `json:"mount"`
}

// Discover walks an explicit logical-root boundary deterministically, ignoring
// Git metadata and requested directory names.
func Discover(root string, ignores []string) ([]Repository, error) {
	return DiscoverContext(context.Background(), root, ignores)
}

// DiscoverContext is Discover with cancellation support. It never promotes an
// enclosing Git checkout: root is the complete discovery boundary.
func DiscoverContext(ctx context.Context, root string, ignores []string) ([]Repository, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize discovery path: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		if path != root && entry.IsDir() && ShouldIgnorePath(relative, entry.Name(), ignores) {
			return filepath.SkipDir
		}
		if entry.IsDir() && isGit(path) {
			hasSubmodules, err := hasSubmodules(ctx, path)
			if err != nil {
				return err
			}
			if hasSubmodules {
				return fmt.Errorf("submodules are unsupported; remove or convert .gitmodules before initializing")
			}
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%q contains no Git repositories", root)
	}
	sort.Slice(paths, func(left, right int) bool {
		leftDepth := pathDepth(root, paths[left])
		rightDepth := pathDepth(root, paths[right])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return paths[left] < paths[right]
	})
	ids := repositoryIDs(root, paths, isGit(root))
	repositories := make([]Repository, 0, len(paths))
	for _, path := range paths {
		id := ids[path]
		parent := ""
		mount := ""
		for i := len(repositories) - 1; i >= 0; i-- {
			if contains(repositories[i].Path, path) {
				parent = repositories[i].ID
				relative, _ := filepath.Rel(repositories[i].Path, path)
				mount = filepath.ToSlash(relative)
				break
			}
		}
		if parent == "" {
			relative, _ := filepath.Rel(root, path)
			mount = filepath.ToSlash(relative)
			if path == root {
				mount = "."
			}
		}
		repositories = append(repositories, Repository{ID: id, Path: path, ParentID: parent, Mount: mount})
	}
	sortRepositories(repositories)
	return repositories, nil
}

func sortRepositories(repositories []Repository) {
	byID := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		byID[repository.ID] = repository
	}
	depths := make(map[string]int, len(repositories))
	var depth func(string) int
	depth = func(id string) int {
		if value, found := depths[id]; found {
			return value
		}
		repository := byID[id]
		if repository.ParentID == "" {
			depths[id] = 0
			return 0
		}
		value := depth(repository.ParentID) + 1
		depths[id] = value
		return value
	}
	sort.Slice(repositories, func(left, right int) bool {
		leftDepth, rightDepth := depth(repositories[left].ID), depth(repositories[right].ID)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return repositories[left].ID < repositories[right].ID
	})
}

func hasSubmodules(ctx context.Context, path string) (bool, error) {
	_, err := os.Stat(filepath.Join(path, ".gitmodules"))
	if err == nil {
		return true, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("check submodule configuration in %q: %w", path, err)
	}
	hasGitlink, err := gitadapter.NewAdapter("git").HasSubmodules(ctx, path)
	if err != nil {
		return false, fmt.Errorf("inspect Git submodule metadata in %q: %w", path, err)
	}
	return hasGitlink, nil
}

var defaultIgnores = []string{
	"node_modules", "vendor", ".venv", "target", "build",
	"node_modules/**", "vendor/**", ".venv/**", "target/**", "build/**",
}

func repositoryIDs(root string, paths []string, rootIsRepository bool) map[string]string {
	counts := map[string]int{}
	if rootIsRepository {
		counts["root"] = 1
	}
	bases := make(map[string]string, len(paths))
	for _, path := range paths {
		if rootIsRepository && path == root {
			continue
		}
		base := slug(filepath.Base(path))
		if base == "" {
			base = "repo"
		}
		bases[path] = base
		counts[base]++
	}
	ids := make(map[string]string, len(paths))
	for _, path := range paths {
		if rootIsRepository && path == root {
			ids[path] = "root"
			continue
		}
		base := bases[path]
		if counts[base] == 1 {
			ids[path] = base
			continue
		}
		relative, _ := filepath.Rel(root, path)
		sum := sha256.Sum256([]byte(filepath.ToSlash(relative)))
		ids[path] = fmt.Sprintf("%s-%x", base, sum[:6])
	}
	return ids
}

func pathDepth(root, path string) int {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." {
		return 0
	}
	return strings.Count(filepath.ToSlash(relative), "/") + 1
}

// ShouldIgnorePath applies the built-in and configured discovery ignore
// patterns to one path relative to a logical-root boundary. It is shared by
// initialization discovery and import observation so an ignored checkout is
// never treated as an unknown repository by only one entry point.
func ShouldIgnorePath(relative, name string, ignores []string) bool {
	patterns := map[string]bool{".git": true}
	for _, value := range defaultIgnores {
		patterns[value] = true
	}
	for _, value := range ignores {
		patterns[value] = true
	}
	if patterns[name] || patterns[filepath.ToSlash(relative)] {
		return true
	}
	for pattern := range patterns {
		pattern = filepath.ToSlash(pattern)
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			relative = filepath.ToSlash(relative)
			if relative == prefix || strings.HasPrefix(relative, prefix+"/") {
				return true
			}
		}
		if matched, err := filepath.Match(pattern, filepath.ToSlash(relative)); err == nil && matched {
			return true
		}
	}
	return false
}
func isGit(path string) bool {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return true
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return false
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir: ") {
		return false
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(path, target)
	}
	targetInfo, err := os.Stat(target)
	return err == nil && targetInfo.IsDir()
}
func contains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
func slug(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
