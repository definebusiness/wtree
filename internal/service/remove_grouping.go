package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// removalGroupingInventory is the operation-local ownership boundary for the
// ordinary directories that contain top-level worktrees. The workspace-state
// schema intentionally does not persist directory receipts, so removal may
// delete only the exact directories captured under the mutation lock and only
// after their contents are proven to be declared topology entries.
type removalGroupingInventory struct {
	root         string
	repositories map[string]RemovalRepository
	worktreeDirs map[string][]string
	receipts     map[string]groupingReceipt
}

func validateRemovalGroupingPaths(value RemovalPlan) ([]string, error) {
	unique := map[string]struct{}{}
	for _, repository := range value.Repositories {
		if repository.ParentID != "" {
			continue
		}
		directories, err := workspaceGroupingDirectories(value.RootPath, value.RootPath, repository.Path, true)
		if err != nil {
			return nil, err
		}
		for _, directory := range directories {
			unique[filepath.Clean(directory)] = struct{}{}
		}
	}
	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(left, right int) bool {
		leftDepth, rightDepth := removalPathDepth(paths[left]), removalPathDepth(paths[right])
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return paths[left] > paths[right]
	})
	if err := validateRemovalGroupingLayout(value, paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func removalPathDepth(path string) int {
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) {
		return 0
	}
	return len(strings.Split(cleaned, string(filepath.Separator)))
}

func validateRemovalGroupingLayout(value RemovalPlan, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	rootParent, err := filepath.EvalSymlinks(filepath.Dir(value.RootPath))
	if err != nil {
		return fmt.Errorf("resolve logical-root parent: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(value.RootPath)
	if err != nil {
		return fmt.Errorf("resolve logical root: %w", err)
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect grouping directory %q: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("grouping directory %q must be a real directory", path)
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !workspacePathWithin(rootParent, canonical) || !workspacePathWithin(canonicalRoot, canonical) {
			return fmt.Errorf("grouping directory %q escapes logical workspace root", path)
		}
	}
	return nil
}

func captureRemovalGroupingInventory(value RemovalPlan) (*removalGroupingInventory, error) {
	if err := validateRemovalGroupingLayout(value, value.groupingPaths); err != nil {
		return nil, err
	}
	inventory := &removalGroupingInventory{
		root:         filepath.Clean(value.RootPath),
		repositories: make(map[string]RemovalRepository, len(value.Repositories)),
		worktreeDirs: make(map[string][]string, len(value.Repositories)),
		receipts:     make(map[string]groupingReceipt),
	}
	for _, repository := range value.Repositories {
		inventory.repositories[repository.ID] = repository
	}
	for _, repository := range value.Repositories {
		parent := inventory.root
		if repository.ParentID != "" {
			parent = inventory.repositories[repository.ParentID].Path
		}
		directories, err := workspaceGroupingDirectories(inventory.root, parent, repository.Path, repository.ParentID == "")
		if err != nil {
			return nil, err
		}
		inventory.worktreeDirs[repository.ID] = append([]string(nil), directories...)
		for _, path := range directories {
			path = filepath.Clean(path)
			if _, captured := inventory.receipts[path]; captured {
				continue
			}
			info, err := os.Lstat(path)
			if err != nil {
				return nil, fmt.Errorf("capture grouping directory %q: %w", path, err)
			}
			if err := validateRemovalGroupingDirectory(inventory.root, parent, path, info); err != nil {
				return nil, err
			}
			inventory.receipts[path] = groupingReceipt{path: path, info: info}
		}
	}
	return inventory, nil
}

func validateRemovalDirectoryReceipt(receipt groupingReceipt) error {
	info, err := os.Lstat(receipt.path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(receipt.info, info) {
		return fmt.Errorf("grouping directory %q changed after locked preflight", receipt.path)
	}
	return nil
}

func validateRemovalGroupingDirectory(root, parent, directory string, info os.FileInfo) error {
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("grouping directory %q must be a real directory", directory)
	}
	canonical, err := filepath.EvalSymlinks(directory)
	canonicalParent, parentErr := filepath.EvalSymlinks(parent)
	canonicalRootParent, rootErr := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil || parentErr != nil || rootErr != nil || !workspacePathWithin(canonicalParent, canonical) || !workspacePathWithin(canonicalRootParent, canonical) {
		return fmt.Errorf("grouping directory %q escapes its owner", directory)
	}
	return nil
}

func (inventory *removalGroupingInventory) validateForWorktree(repository RemovalRepository) error {
	if inventory == nil {
		return nil
	}
	if err := inventory.validateAllReceipts(); err != nil {
		return err
	}
	parent := inventory.root
	if repository.ParentID != "" {
		parent = inventory.repositories[repository.ParentID].Path
	}
	for _, directory := range inventory.worktreeDirs[repository.ID] {
		receipt, found := inventory.receipts[filepath.Clean(directory)]
		if !found {
			return fmt.Errorf("grouping directory %q has no locked receipt", directory)
		}
		if err := validateRemovalDirectoryReceipt(receipt); err != nil {
			return err
		}
		if err := validateRemovalGroupingDirectory(inventory.root, parent, directory, receipt.info); err != nil {
			return err
		}
	}
	return nil
}

func (inventory *removalGroupingInventory) validateAllReceipts() error {
	paths := make([]string, 0, len(inventory.receipts))
	for path := range inventory.receipts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := validateRemovalDirectoryReceipt(inventory.receipts[path]); err != nil {
			return err
		}
	}
	return nil
}

func (inventory *removalGroupingInventory) invalidateBelow(path string) {
	if inventory == nil {
		return
	}
	for directory := range inventory.receipts {
		if workspacePathWithin(path, directory) {
			delete(inventory.receipts, directory)
		}
	}
}

func (inventory *removalGroupingInventory) ensureForWorktree(repository RemovalRepository) error {
	if inventory == nil {
		return nil
	}
	parent := inventory.root
	if repository.ParentID != "" {
		parent = inventory.repositories[repository.ParentID].Path
	}
	directories, err := workspaceGroupingDirectories(inventory.root, parent, repository.Path, repository.ParentID == "")
	if err != nil {
		return err
	}
	for _, directory := range directories {
		directory = filepath.Clean(directory)
		if receipt, ok := inventory.receipts[directory]; ok {
			if err := validateRemovalDirectoryReceipt(receipt); err != nil {
				return err
			}
			continue
		}
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			if err := os.Mkdir(directory, 0o700); err != nil {
				return fmt.Errorf("recreate rollback grouping directory %q: %w", directory, err)
			}
			info, err = os.Lstat(directory)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("rollback grouping directory %q is unsafe: %w", directory, err)
		}
		if err := validateRemovalGroupingDirectory(inventory.root, parent, directory, info); err != nil {
			return err
		}
		inventory.receipts[directory] = groupingReceipt{path: directory, info: info}
	}
	return nil
}
