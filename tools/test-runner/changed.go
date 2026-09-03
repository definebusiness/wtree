package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// PackageGraph is the small, testable subset of `go list -json` used for a
// changed-area closure. It intentionally has no product-package knowledge.
type PackageGraph struct {
	ImportPath   string
	Dir          string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

type ChangeSelection struct {
	Packages      []string
	Platforms     []string
	Harness       bool
	Documentation bool
}

func selectChanged(ctx context.Context, commands commander, base string) (ChangeSelection, error) {
	if strings.TrimSpace(base) == "" {
		return ChangeSelection{}, fmt.Errorf("BASE_REF is required and must resolve to a commit")
	}
	if result := commands.Run(ctx, "git", "rev-parse", "--verify", base+"^{commit}"); result.ExitCode != 0 {
		return ChangeSelection{}, fmt.Errorf("BASE_REF %q does not resolve to a commit", base)
	}
	rootResult := commands.Run(ctx, "git", "rev-parse", "--show-toplevel")
	if rootResult.ExitCode != 0 || strings.TrimSpace(string(rootResult.Output)) == "" {
		return ChangeSelection{}, fmt.Errorf("cannot determine repository root for changed-area selection")
	}
	root := strings.TrimSpace(string(rootResult.Output))
	var paths []string
	for _, args := range [][]string{
		{"diff", "--name-status", "-z", base + "...HEAD"},
		{"diff", "--name-status", "-z"},
		{"diff", "--cached", "--name-status", "-z"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	} {
		result := commands.Run(ctx, "git", args...)
		if result.ExitCode != 0 {
			return ChangeSelection{}, fmt.Errorf("changed-area git %s failed", strings.Join(args[:1], " "))
		}
		var current []string
		var err error
		if args[0] == "ls-files" {
			current = nulPaths(result.Output)
		} else {
			current, err = nameStatusPaths(result.Output)
			if err != nil {
				return ChangeSelection{}, err
			}
		}
		paths = append(paths, current...)
	}
	if len(paths) == 0 {
		return ChangeSelection{}, fmt.Errorf("changed-area selection is empty; make a change or choose a different BASE_REF")
	}
	graphResult := commands.Run(ctx, "go", "list", "-json", "./...")
	if graphResult.ExitCode != 0 {
		return ChangeSelection{}, fmt.Errorf("cannot read in-repository dependency graph")
	}
	graph, err := parsePackageGraph(graphResult.Output)
	if err != nil {
		return ChangeSelection{}, err
	}
	return selectPaths(root, graph, paths)
}

func nulPaths(data []byte) []string {
	var paths []string
	for _, value := range bytes.Split(data, []byte{0}) {
		if path := string(value); path != "" {
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	return paths
}

func nameStatusPaths(data []byte) ([]string, error) {
	fields := nulPaths(data)
	var paths []string
	for len(fields) > 0 {
		status := fields[0]
		fields = fields[1:]
		if status == "" {
			continue
		}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			return nil, fmt.Errorf("changed-area selection fails closed for renamed or copied paths (%s)", status)
		}
		if len(fields) == 0 {
			return nil, fmt.Errorf("malformed changed-area name-status output")
		}
		if strings.HasPrefix(status, "D") {
			return nil, fmt.Errorf("changed-area selection fails closed for deleted path %q", fields[0])
		}
		paths = append(paths, fields[0])
		fields = fields[1:]
	}
	return paths, nil
}

func parsePackageGraph(data []byte) ([]PackageGraph, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var graph []PackageGraph
	for {
		var entry PackageGraph
		if err := decoder.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("cannot decode in-repository dependency graph: %w", err)
		}
		if entry.ImportPath == "" || entry.Dir == "" {
			return nil, fmt.Errorf("ambiguous package in dependency graph")
		}
		graph = append(graph, entry)
	}
	if len(graph) == 0 {
		return nil, fmt.Errorf("in-repository dependency graph is empty")
	}
	return graph, nil
}

func selectPaths(root string, graph []PackageGraph, paths []string) (ChangeSelection, error) {
	selected := make(map[string]bool)
	productionSeeds := make(map[string]bool)
	platforms := make(map[string]bool)
	harness := false
	documentation := false
	for _, changedPath := range paths {
		path, err := repositoryRelativePath(changedPath)
		if err != nil {
			return ChangeSelection{}, err
		}
		switch {
		case path == "go.mod" || path == "go.sum":
			return ChangeSelection{}, fmt.Errorf("changed-area selection fails closed for module boundary %q", path)
		case strings.EqualFold(filepath.Ext(path), ".md"):
			documentation = true
			continue
		case path == "Makefile" || strings.HasPrefix(path, "tools/test-runner/") || strings.HasPrefix(path, "scripts/") || strings.HasPrefix(path, ".github/workflows/"):
			harness = true
			selected["./tools/test-runner"] = true
			continue
		}
		owner, err := ownerForPath(root, graph, path)
		if err != nil {
			return ChangeSelection{}, err
		}
		selected[owner.ImportPath] = true
		if isTestutilPackage(owner.ImportPath) {
			for _, candidate := range graph {
				if imports(candidate.TestImports, owner.ImportPath) || imports(candidate.XTestImports, owner.ImportPath) {
					selected[candidate.ImportPath] = true
				}
			}
		}
		// A package-local test change verifies its owning package only. It is
		// not a production API change, so reverse dependencies must not be
		// widened merely because they import the owner.
		if !strings.HasSuffix(path, "_test.go") && !isTestutilPackage(owner.ImportPath) {
			productionSeeds[owner.ImportPath] = true
		}
		if platform := platformForPath(path); platform != "" {
			platforms[platform] = true
		}
	}
	if len(selected) == 0 && !documentation {
		return ChangeSelection{}, fmt.Errorf("changed-area selection has no Go package (documentation-only changes require make fmt-check)")
	}
	// Production package changes exercise their in-repository reverse closure.
	queue := make([]string, 0, len(productionSeeds))
	for pkg := range productionSeeds {
		queue = append(queue, pkg)
	}
	visited := make(map[string]bool)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
		}
		visited[current] = true
		for _, candidate := range graph {
			if imports(candidate.Imports, current) {
				selected[candidate.ImportPath] = true
				queue = append(queue, candidate.ImportPath)
			}
		}
	}
	selection := ChangeSelection{Harness: harness, Documentation: documentation}
	for pkg := range selected {
		selection.Packages = append(selection.Packages, pkg)
	}
	for platform := range platforms {
		selection.Platforms = append(selection.Platforms, platform)
	}
	sort.Strings(selection.Packages)
	sort.Strings(selection.Platforms)
	return selection, nil
}

func repositoryRelativePath(changedPath string) (string, error) {
	path := filepath.ToSlash(changedPath)
	windowsAbsolute := len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") || windowsAbsolute {
		return "", fmt.Errorf("changed path %q is not repository-relative", changedPath)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", fmt.Errorf("changed path %q escapes the repository", changedPath)
		}
	}
	path = strings.TrimPrefix(path, "./")
	if path == "" {
		return "", fmt.Errorf("changed path %q is empty", changedPath)
	}
	return path, nil
}

func isTestutilPackage(importPath string) bool {
	return importPath == "github.com/definebusiness/wtree/internal/testutil" || strings.HasSuffix(importPath, "/testutil")
}

func ownerForPath(root string, graph []PackageGraph, path string) (PackageGraph, error) {
	abs := filepath.Clean(filepath.Join(root, filepath.FromSlash(path)))
	var owner *PackageGraph
	for index := range graph {
		dir := filepath.Clean(graph[index].Dir)
		if abs == dir || strings.HasPrefix(abs, dir+string(filepath.Separator)) {
			if owner != nil && len(dir) <= len(owner.Dir) {
				continue
			}
			owner = &graph[index]
		}
	}
	if owner == nil {
		return PackageGraph{}, fmt.Errorf("changed path %q does not have an unambiguous in-repository package owner", path)
	}
	return *owner, nil
}

func imports(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func platformForPath(path string) string {
	base := filepath.Base(path)
	for _, platform := range []string{"windows", "linux", "darwin", "freebsd"} {
		if strings.HasSuffix(base, "_"+platform+".go") || strings.HasSuffix(base, "_"+platform+"_test.go") {
			return platform
		}
	}
	return ""
}
