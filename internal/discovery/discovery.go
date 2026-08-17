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

// Discover walks root deterministically, ignoring Git metadata and requested directory names.
func Discover(root string, ignores []string) ([]Repository, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize discovery path: %w", err)
	}
	root, err = repositoryRoot(root)
	if err != nil {
		return nil, err
	}
	if !isGit(root) {
		return nil, fmt.Errorf("%q is not a Git repository", root)
	}
	ignored := map[string]bool{".git": true}
	for _, value := range defaultIgnores {
		ignored[value] = true
	}
	for _, value := range ignores {
		ignored[value] = true
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		if path != root && entry.IsDir() && ignoredPath(relative, entry.Name(), ignored) {
			return filepath.SkipDir
		}
		if entry.IsDir() && isGit(path) {
			hasSubmodules, err := hasSubmodules(path)
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
	sort.Strings(paths)
	ids := repositoryIDs(root, paths)
	repositories := make([]Repository, 0, len(paths))
	for _, path := range paths {
		id := ids[path]
		parent := ""
		mount := "."
		if path != root {
			for i := len(repositories) - 1; i >= 0; i-- {
				if contains(repositories[i].Path, path) {
					parent = repositories[i].ID
					relative, _ := filepath.Rel(repositories[i].Path, path)
					mount = filepath.ToSlash(relative)
					break
				}
			}
		}
		repositories = append(repositories, Repository{ID: id, Path: path, ParentID: parent, Mount: mount})
	}
	return repositories, nil
}

func hasSubmodules(path string) (bool, error) {
	_, err := os.Stat(filepath.Join(path, ".gitmodules"))
	if err == nil {
		return true, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("check submodule configuration in %q: %w", path, err)
	}
	hasGitlink, err := gitadapter.NewAdapter("git").HasSubmodules(context.Background(), path)
	if err != nil {
		return false, fmt.Errorf("inspect Git submodule metadata in %q: %w", path, err)
	}
	return hasGitlink, nil
}

var defaultIgnores = []string{
	"node_modules", "vendor", ".venv", "target", "build",
	"node_modules/**", "vendor/**", ".venv/**", "target/**", "build/**",
}

func repositoryRoot(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	for {
		if isGit(path) {
			return path, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("%q is not inside a Git repository", path)
		}
		path = parent
	}
}

func repositoryIDs(root string, paths []string) map[string]string {
	counts := map[string]int{"root": 1}
	bases := make(map[string]string, len(paths))
	for _, path := range paths {
		if path == root {
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
		if path == root {
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
func ignoredPath(relative, name string, patterns map[string]bool) bool {
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
