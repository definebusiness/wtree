package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/transaction"
)

// workspaceGrouping owns only the ordinary directories it creates between a
// logical workspace root (or immediate parent checkout) and a checkout. Git
// worktree add must never be allowed to create those paths through a symlink.
type workspaceGrouping struct {
	root       string
	boundary   string
	created    map[string][]groupingReceipt
	receipts   map[string]groupingReceipt
	filesystem workspaceFilesystem
}

type groupingReceipt struct {
	path      string
	info      os.FileInfo
	authority workspaceDirectoryAuthority
}

// workspaceDirectoryAuthority retains a live directory identity just long
// enough to bridge creation and the Git operation that will populate it. On
// Unix, keeping the descriptor open prevents a deleted directory's inode from
// being reused as a convincing replacement during that boundary.
type workspaceDirectoryAuthority interface {
	matches(os.FileInfo) bool
	close() error
}

type workspaceDirectoryAuthorityFunc struct {
	matchFunc func(os.FileInfo) bool
	closeFunc func() error
}

func (authority workspaceDirectoryAuthorityFunc) matches(info os.FileInfo) bool {
	return authority.matchFunc(info)
}

func (authority workspaceDirectoryAuthorityFunc) close() error {
	return authority.closeFunc()
}

type workspaceFilesystem struct {
	lstat           func(string) (os.FileInfo, error)
	evalSymlinks    func(string) (string, error)
	mkdir           func(string, os.FileMode) error
	remove          func(string) error
	retainDirectory func(string) (workspaceDirectoryAuthority, error)
}

func newWorkspaceFilesystem() workspaceFilesystem {
	return workspaceFilesystem{lstat: os.Lstat, evalSymlinks: filepath.EvalSymlinks, mkdir: os.Mkdir, remove: os.Remove, retainDirectory: retainWorkspaceDirectory}
}

func newWorkspaceGrouping(root string, filesystem workspaceFilesystem) *workspaceGrouping {
	defaults := newWorkspaceFilesystem()
	if filesystem.lstat == nil {
		filesystem.lstat = defaults.lstat
	}
	if filesystem.evalSymlinks == nil {
		filesystem.evalSymlinks = defaults.evalSymlinks
	}
	if filesystem.mkdir == nil {
		filesystem.mkdir = defaults.mkdir
	}
	if filesystem.remove == nil {
		filesystem.remove = defaults.remove
	}
	if filesystem.retainDirectory == nil {
		filesystem.retainDirectory = defaults.retainDirectory
	}
	return &workspaceGrouping{root: filepath.Clean(root), created: make(map[string][]groupingReceipt), receipts: make(map[string]groupingReceipt), filesystem: filesystem}
}

func (g *workspaceGrouping) step(repository plan.RepositoryPlan, parent string) (transaction.Step, bool, error) {
	directories, err := workspaceGroupingDirectories(g.root, parent, repository.Path, repository.ParentID == "")
	if err != nil {
		return transaction.Step{}, false, err
	}
	rootGit := repository.ParentID == "" && filepath.Clean(repository.Path) == g.root
	if len(directories) == 0 && !rootGit {
		return transaction.Step{}, false, nil
	}
	return transaction.Step{
		Name: "prepare_grouping:" + repository.ID,
		Execute: func(context.Context) error {
			if err := g.prepare(repository.ID, parent, directories); err != nil {
				return err
			}
			return nil
		},
		Rollback:              func(context.Context) error { return g.rollback(repository.ID) },
		RollbackFailedExecute: func(context.Context) error { return g.rollback(repository.ID) },
	}, true, nil
}

func workspaceGroupingDirectories(root, parent, checkout string, topLevel bool) ([]string, error) {
	root, parent, checkout = filepath.Clean(root), filepath.Clean(parent), filepath.Clean(checkout)
	if topLevel {
		if !workspacePathWithin(filepath.Dir(root), checkout) {
			return nil, fmt.Errorf("top-level checkout %q escapes workspace parent", checkout)
		}
		relative, err := filepath.Rel(root, checkout)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return nil, fmt.Errorf("top-level checkout %q escapes workspace root", checkout)
		}
		if relative == "." {
			return nil, nil
		}
		components := strings.Split(relative, string(filepath.Separator))
		directories := []string{root}
		current := root
		for _, component := range components[:len(components)-1] {
			current = filepath.Join(current, component)
			directories = append(directories, current)
		}
		return directories, nil
	}
	if !workspacePathWithin(parent, checkout) {
		return nil, fmt.Errorf("child checkout %q escapes immediate parent %q", checkout, parent)
	}
	relative, err := filepath.Rel(parent, checkout)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, fmt.Errorf("child checkout %q is not below immediate parent %q", checkout, parent)
	}
	components := strings.Split(relative, string(filepath.Separator))
	current := parent
	directories := make([]string, 0, len(components)-1)
	for _, component := range components[:len(components)-1] {
		current = filepath.Join(current, component)
		directories = append(directories, current)
	}
	return directories, nil
}

func (g *workspaceGrouping) prepare(id, parent string, directories []string) error {
	var err error
	directories, err = g.withMissingRootAncestors(directories)
	if err != nil {
		return err
	}
	if err := g.validateReceipt(parent); err != nil {
		return err
	}
	for _, directory := range directories {
		info, err := g.filesystem.lstat(directory)
		if os.IsNotExist(err) {
			if err := g.filesystem.mkdir(directory, 0o700); err != nil {
				if os.IsExist(err) {
					if err := g.validateParent(parent, directory); err != nil {
						return err
					}
					continue
				}
				return fmt.Errorf("create grouping directory %q: %w", directory, err)
			}
			info, err = g.filesystem.lstat(directory)
			if err != nil {
				return fmt.Errorf("inspect created grouping directory %q: %w", directory, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("grouping directory %q must be a real directory without symlinks", directory)
			}
			authority, retainErr := g.filesystem.retainDirectory(directory)
			if retainErr != nil {
				return fmt.Errorf("retain created grouping directory %q identity: %w", directory, retainErr)
			}
			if authority == nil || !authority.matches(info) {
				if authority != nil {
					_ = authority.close()
				}
				return fmt.Errorf("retain created grouping directory %q identity", directory)
			}
			receipt := groupingReceipt{path: directory, info: info, authority: authority}
			g.created[id] = append(g.created[id], receipt)
			g.receipts[filepath.Clean(directory)] = receipt
			if err := g.validateParent(parent, directory); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect grouping directory %q: %w", directory, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("grouping directory %q must be a real directory without symlinks", directory)
		}
		if err := g.validateParent(parent, directory); err != nil {
			return err
		}
		if err := g.validateReceipt(directory); err != nil {
			return err
		}
	}
	return nil
}

// withMissingRootAncestors extends the first top-level grouping step back to
// the nearest existing real directory. This is required for the default
// <worktree-root>/<project>/<workspace> layout, whose project directory need
// not exist yet. Every added component receives the same ownership receipt and
// reverse rollback as ordinary grouping directories.
func (g *workspaceGrouping) withMissingRootAncestors(directories []string) ([]string, error) {
	if g.boundary != "" {
		return directories, g.validateReceipt(g.boundary)
	}
	cursor := filepath.Dir(g.root)
	missing := make([]string, 0, 2)
	for {
		info, err := g.filesystem.lstat(cursor)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, fmt.Errorf("workspace grouping boundary %q must be a real directory without symlinks", cursor)
			}
			if _, err := g.filesystem.evalSymlinks(cursor); err != nil {
				return nil, fmt.Errorf("resolve workspace grouping boundary %q: %w", cursor, err)
			}
			g.boundary = filepath.Clean(cursor)
			g.receipts[g.boundary] = groupingReceipt{path: cursor, info: info}
			break
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect workspace grouping ancestor %q: %w", cursor, err)
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return nil, fmt.Errorf("workspace grouping path %q has no existing directory boundary", g.root)
		}
		missing = append(missing, cursor)
		cursor = parent
	}
	for left, right := 0, len(missing)-1; left < right; left, right = left+1, right-1 {
		missing[left], missing[right] = missing[right], missing[left]
	}
	return append(missing, directories...), nil
}

func (g *workspaceGrouping) revalidate(repository plan.RepositoryPlan, parent string) error {
	if _, err := g.withMissingRootAncestors(nil); err != nil {
		return err
	}
	for _, receipt := range g.created[repository.ID] {
		if err := g.validateParent(parent, receipt.path); err != nil {
			return err
		}
		if err := g.validateReceipt(receipt.path); err != nil {
			return err
		}
	}
	directories, err := workspaceGroupingDirectories(g.root, parent, repository.Path, repository.ParentID == "")
	if err != nil {
		return err
	}
	for _, directory := range directories {
		if err := g.validateParent(parent, directory); err != nil {
			return err
		}
		if err := g.validateReceipt(directory); err != nil {
			return err
		}
	}
	if repository.ParentID != "" {
		if err := g.validateReceipt(parent); err != nil {
			return err
		}
		if err := g.validateParent(parent, parent); err != nil {
			return err
		}
	}
	if info, err := g.filesystem.lstat(repository.Path); err == nil || !os.IsNotExist(err) {
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("checkout path %q is a symlink", repository.Path)
		}
		return fmt.Errorf("checkout path %q already exists", repository.Path)
	}
	return nil
}

func (g *workspaceGrouping) validateReceipt(directory string) error {
	if receipt, found := g.receipts[filepath.Clean(directory)]; found {
		info, err := g.filesystem.lstat(directory)
		if err != nil || !os.SameFile(receipt.info, info) || receipt.authority != nil && !receipt.authority.matches(info) {
			return fmt.Errorf("grouping directory %q changed after creation", directory)
		}
	}
	return nil
}

// releaseCreated releases identity descriptors after the corresponding
// add-worktree boundary. Rollback also calls this for every exit path.
func (g *workspaceGrouping) releaseCreated(id string) error {
	var result error
	for index := range g.created[id] {
		receipt := &g.created[id][index]
		if receipt.authority == nil {
			continue
		}
		if err := receipt.authority.close(); err != nil {
			result = errors.Join(result, fmt.Errorf("release grouping directory %q identity: %w", receipt.path, err))
		}
		receipt.authority = nil
		stored := g.receipts[filepath.Clean(receipt.path)]
		stored.authority = nil
		g.receipts[filepath.Clean(receipt.path)] = stored
	}
	return result
}

func (g *workspaceGrouping) recordWorktree(path string) error {
	info, err := g.filesystem.lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("capture created worktree %q: %w", path, err)
	}
	g.receipts[filepath.Clean(path)] = groupingReceipt{path: path, info: info}
	return nil
}

func (g *workspaceGrouping) validateParent(parent, directory string) error {
	info, err := g.filesystem.lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect grouping directory %q: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("grouping directory %q must be a real directory without symlinks", directory)
	}
	canonicalDirectory, err := g.filesystem.evalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("resolve grouping directory %q: %w", directory, err)
	}
	if g.boundary == "" {
		return fmt.Errorf("workspace grouping boundary is not established")
	}
	if err := g.validateReceipt(g.boundary); err != nil {
		return err
	}
	canonicalBoundary, err := g.filesystem.evalSymlinks(g.boundary)
	if err != nil || !workspacePathWithin(canonicalBoundary, canonicalDirectory) {
		return fmt.Errorf("grouping directory %q escapes workspace staging boundary", directory)
	}
	if !workspacePathWithin(directory, g.root) {
		canonicalRoot, err := g.filesystem.evalSymlinks(g.root)
		if err != nil || !workspacePathWithin(canonicalRoot, canonicalDirectory) {
			return fmt.Errorf("grouping directory %q escapes logical workspace root", directory)
		}
	}
	if filepath.Clean(parent) != g.root {
		canonicalParent, err := g.filesystem.evalSymlinks(parent)
		if err != nil || !workspacePathWithin(canonicalParent, canonicalDirectory) {
			return fmt.Errorf("grouping directory %q escapes immediate parent checkout", directory)
		}
	}
	return nil
}

func (g *workspaceGrouping) rollback(id string) (result error) {
	directories := g.created[id]
	defer func() {
		result = errors.Join(result, g.releaseCreated(id))
	}()
	for index := len(directories) - 1; index >= 0; index-- {
		receipt := directories[index]
		info, err := g.filesystem.lstat(receipt.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(receipt.info, info) {
			return fmt.Errorf("refuse to remove replaced grouping directory %q", receipt.path)
		}
		if err := g.filesystem.remove(receipt.path); err != nil {
			return fmt.Errorf("remove owned grouping directory %q: %w", receipt.path, err)
		}
	}
	return nil
}

func workspacePathWithin(base, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
