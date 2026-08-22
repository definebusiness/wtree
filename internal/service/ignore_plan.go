package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/definebusiness/wtree/internal/fsutil"
	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/pathutil"
)

// IgnoreRequirement describes one child mount that must be protected by its
// immediate parent's root .gitignore. Mount is normalized by Plan and never
// used as a filesystem path.
type IgnoreRequirement struct {
	ParentRepositoryID string
	ChildRepositoryID  string
	ParentPath         string
	Mount              string

	// LocalConfig adds the root local-configuration rule to the same owning
	// file plan as nested mounts. The existing working-tree check is supplied
	// by the init caller because this is a file rule, not a mount rule.
	LocalConfig      bool
	AlreadyProtected bool
}

// IgnoreFileSnapshot is the immutable non-following observation used to
// reject a changed target before it is replaced.
type IgnoreFileSnapshot struct {
	Path       string
	Exists     bool
	Bytes      []byte
	Mode       os.FileMode
	parentInfo os.FileInfo
	info       os.FileInfo
}

// IgnoreFilePlan is the complete, immutable intended generation for one root
// .gitignore. Files includes unchanged plans too, so callers can verify every
// direct-child requirement without reconstructing filesystem facts.
type IgnoreFilePlan struct {
	ParentRepositoryID string
	ParentPath         string
	Path               string
	Snapshot           IgnoreFileSnapshot
	NewBytes           []byte
	AddedRules         []string
	Changed            bool
}

// IgnorePlan contains deterministic per-parent file plans.
type IgnorePlan struct {
	Files []IgnoreFilePlan
}

// IgnoreFileUpdate is the public progress record for one actually changed or
// still-pending target. It deliberately excludes snapshots and file bytes.
type IgnoreFileUpdate struct {
	ParentRepositoryID string
	Path               string
	AddedRules         []string
}

// IgnoreApplyResult preserves monotonic source-file progress: Changed remains
// meaningful even when a later replacement fails, while Remaining gives a
// fresh retry its exact outstanding targets.
type IgnoreApplyResult struct {
	Changed   []IgnoreFileUpdate
	Remaining []IgnoreFileUpdate
}

// IgnorePlanner owns read-only direct-child ignore planning.
type IgnorePlanner struct {
	inspector git.WorkingTreeIgnoreInspector
}

func NewIgnorePlanner(inspector git.WorkingTreeIgnoreInspector) *IgnorePlanner {
	return &IgnorePlanner{inspector: inspector}
}

// Plan validates, snapshots, and coalesces the requirements without changing
// a target. It always uses the immediate parent checkout for both Git evidence
// and the owning root .gitignore.
func (p *IgnorePlanner) Plan(ctx context.Context, requirements []IgnoreRequirement) (IgnorePlan, error) {
	if p == nil || p.inspector == nil {
		return IgnorePlan{}, NewError(ErrorInternal, errors.New("ignore planner requires a working-tree ignore inspector"))
	}
	groups, err := normalizeIgnoreRequirements(requirements)
	if err != nil {
		return IgnorePlan{}, NewError(ErrorValidation, err)
	}
	files := make([]IgnoreFilePlan, 0, len(groups))
	for _, group := range groups {
		info, err := os.Stat(group.parentPath)
		if err != nil {
			return IgnorePlan{}, NewError(ErrorValidation, fmt.Errorf("inspect parent checkout %q: %w", group.parentPath, err))
		}
		if !info.IsDir() {
			return IgnorePlan{}, NewError(ErrorValidation, fmt.Errorf("parent checkout %q must be a directory", group.parentPath))
		}
		if err := pathutil.CheckPotentialWithin(group.parentPath, group.path); err != nil {
			return IgnorePlan{}, NewError(ErrorValidation, fmt.Errorf("ignore target %q escapes parent checkout %q: %w", group.path, group.parentPath, err))
		}
		snapshot, err := captureIgnoreFile(group.path)
		if err != nil {
			return IgnorePlan{}, NewError(ErrorValidation, err)
		}
		file := IgnoreFilePlan{ParentRepositoryID: group.parentID, ParentPath: group.parentPath, Path: group.path, Snapshot: snapshot, NewBytes: append([]byte(nil), snapshot.Bytes...)}
		for _, requirement := range group.requirements {
			rule := "/.wtree.yml"
			if requirement.LocalConfig {
				if requirement.AlreadyProtected {
					continue
				}
			} else {
				evidence, inspectErr := p.inspector.InspectWorkingTreeIgnore(ctx, group.parentPath, requirement.Mount)
				if inspectErr != nil {
					return IgnorePlan{}, NewError(ErrorGit, fmt.Errorf("inspect mount %q for repository %q: %w", requirement.Mount, requirement.ChildRepositoryID, inspectErr))
				}
				var ruleErr error
				rule, ruleErr = NestedDirectoryRule(requirement.Mount)
				if ruleErr != nil {
					return IgnorePlan{}, NewError(ErrorValidation, fmt.Errorf("repository %q mount: %w", requirement.ChildRepositoryID, ruleErr))
				}
				if evidence.Qualifies(group.parentPath) {
					continue
				}
			}
			if ignoreFileHasExactLine(snapshot.Bytes, rule) {
				return IgnorePlan{}, NewError(ErrorConflict, fmt.Errorf("ignore-rule conflict for mount %q of repository %q: %q is present in %s but Git reports the mount visible", requirement.Mount, requirement.ChildRepositoryID, rule, group.path))
			}
			file.AddedRules = append(file.AddedRules, rule)
		}
		file.NewBytes = appendIgnoreRules(snapshot.Bytes, file.AddedRules)
		file.Changed = len(file.AddedRules) != 0
		files = append(files, file)
	}
	return IgnorePlan{Files: files}, nil
}

type ignoreRequirementGroup struct {
	parentID     string
	parentPath   string
	path         string
	depth        int
	requirements []IgnoreRequirement
}

func normalizeIgnoreRequirements(requirements []IgnoreRequirement) ([]ignoreRequirementGroup, error) {
	groupsByParent := make(map[string]*ignoreRequirementGroup)
	childParents := make(map[string]string, len(requirements))
	seen := make(map[string]IgnoreRequirement, len(requirements))
	for _, requirement := range requirements {
		if requirement.ParentRepositoryID == "" || (!requirement.LocalConfig && (requirement.ChildRepositoryID == "" || requirement.ParentRepositoryID == requirement.ChildRepositoryID)) {
			return nil, fmt.Errorf("ignore requirement must name distinct parent and child repositories")
		}
		parentPath, err := filepath.Abs(requirement.ParentPath)
		if err != nil {
			return nil, fmt.Errorf("make parent checkout %q absolute: %w", requirement.ParentPath, err)
		}
		mount := requirement.Mount
		if requirement.LocalConfig {
			if requirement.ParentRepositoryID != requirement.ChildRepositoryID || requirement.Mount != "" {
				return nil, fmt.Errorf("local configuration ignore requirement must use its owning repository")
			}
		} else {
			var err error
			mount, err = pathutil.NormalizeMount(requirement.Mount, pathutil.ChildMount)
			if err != nil {
				return nil, fmt.Errorf("repository %q mount: %w", requirement.ChildRepositoryID, err)
			}
		}
		requirement.ParentPath, requirement.Mount = filepath.Clean(parentPath), mount
		if !requirement.LocalConfig {
			if previous, found := childParents[requirement.ChildRepositoryID]; found && previous != requirement.ParentRepositoryID {
				return nil, fmt.Errorf("repository %q has conflicting immediate parents %q and %q", requirement.ChildRepositoryID, previous, requirement.ParentRepositoryID)
			}
			childParents[requirement.ChildRepositoryID] = requirement.ParentRepositoryID
		}
		key := requirement.ParentRepositoryID + "\x00" + requirement.ChildRepositoryID
		if previous, found := seen[key]; found {
			if previous.ParentPath != requirement.ParentPath || previous.Mount != requirement.Mount {
				return nil, fmt.Errorf("repository %q has conflicting ignore requirements", requirement.ChildRepositoryID)
			}
			continue
		}
		seen[key] = requirement
		group := groupsByParent[requirement.ParentRepositoryID]
		if group == nil {
			group = &ignoreRequirementGroup{parentID: requirement.ParentRepositoryID, parentPath: requirement.ParentPath, path: filepath.Join(requirement.ParentPath, ".gitignore")}
			groupsByParent[group.parentID] = group
		} else if group.parentPath != requirement.ParentPath {
			return nil, fmt.Errorf("parent repository %q has conflicting checkout paths", requirement.ParentRepositoryID)
		}
		group.requirements = append(group.requirements, requirement)
	}
	for _, group := range groupsByParent {
		depth, err := ignoreParentDepth(group.parentID, childParents)
		if err != nil {
			return nil, err
		}
		group.depth = depth
		sort.Slice(group.requirements, func(left, right int) bool {
			if group.requirements[left].LocalConfig != group.requirements[right].LocalConfig {
				return group.requirements[left].LocalConfig
			}
			return group.requirements[left].ChildRepositoryID < group.requirements[right].ChildRepositoryID
		})
	}
	groups := make([]ignoreRequirementGroup, 0, len(groupsByParent))
	for _, group := range groupsByParent {
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(left, right int) bool {
		if groups[left].depth != groups[right].depth {
			return groups[left].depth < groups[right].depth
		}
		if groups[left].parentID != groups[right].parentID {
			return groups[left].parentID < groups[right].parentID
		}
		return groups[left].path < groups[right].path
	})
	return groups, nil
}

func ignoreParentDepth(id string, parents map[string]string) (int, error) {
	depth, seen := 0, map[string]bool{}
	for id != "" {
		if seen[id] {
			return 0, fmt.Errorf("ignore requirements contain a repository cycle at %q", id)
		}
		seen[id] = true
		parent, found := parents[id]
		if !found {
			return depth, nil
		}
		depth++
		id = parent
	}
	return depth, nil
}

func captureIgnoreFile(path string) (IgnoreFileSnapshot, error) {
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return IgnoreFileSnapshot{}, fmt.Errorf("inspect ignore parent %q: %w", parent, err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return IgnoreFileSnapshot{}, fmt.Errorf("ignore parent %q must be a directory and not a symlink", parent)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return IgnoreFileSnapshot{Path: path, Mode: 0o644, parentInfo: parentInfo}, nil
	}
	if err != nil {
		return IgnoreFileSnapshot{}, fmt.Errorf("inspect ignore target %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return IgnoreFileSnapshot{}, fmt.Errorf("ignore target %q must be a regular non-symlink file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return IgnoreFileSnapshot{}, fmt.Errorf("read ignore target %q: %w", path, err)
	}
	return IgnoreFileSnapshot{Path: path, Exists: true, Bytes: append([]byte(nil), data...), Mode: info.Mode().Perm(), parentInfo: parentInfo, info: info}, nil
}

func ignoreFileHasExactLine(data []byte, rule string) bool {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if string(line) == rule {
			return true
		}
	}
	return false
}

func appendIgnoreRules(data []byte, rules []string) []byte {
	result := append([]byte(nil), data...)
	if len(rules) == 0 {
		return result
	}
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) && !bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) {
		newline = "\r\n"
	}
	if len(result) != 0 && result[len(result)-1] != '\n' {
		result = append(result, newline...)
	}
	for _, rule := range rules {
		result = append(result, rule...)
		result = append(result, newline...)
	}
	return result
}

// IgnoreFileWriter is a narrow application seam. It must invoke
// beforeReplace immediately before the atomic replacement boundary and report
// whether a complete replacement occurred before any returned error.
type IgnoreFileWriter func(IgnoreFilePlan, func() error) (bool, error)

// IgnoreApplier owns compare-and-replace application of an immutable plan.
type IgnoreApplier struct{ write IgnoreFileWriter }

func NewIgnoreApplier() *IgnoreApplier { return NewIgnoreApplierWith(applyIgnoreFile) }

func NewIgnoreApplierWith(writer IgnoreFileWriter) *IgnoreApplier {
	if writer == nil {
		writer = applyIgnoreFile
	}
	return &IgnoreApplier{write: writer}
}

// Apply replaces changed targets in plan order. Earlier completed writes are
// intentionally retained when a later target fails; no rollback can erase
// source-checkout progress that a retry can safely reuse.
func (a *IgnoreApplier) Apply(ctx context.Context, plan IgnorePlan) (IgnoreApplyResult, error) {
	if a == nil || a.write == nil {
		return IgnoreApplyResult{}, NewError(ErrorInternal, errors.New("ignore applier requires a file writer"))
	}
	changed := changedIgnoreFiles(plan.Files)
	result := IgnoreApplyResult{Remaining: updatesForIgnoreFiles(changed)}
	for index, file := range changed {
		if err := ctx.Err(); err != nil {
			return result, wrapIgnoreProgress(result, err)
		}
		beforeReplace := func() error { return revalidateIgnoreSnapshot(file.Snapshot) }
		if err := beforeReplace(); err != nil {
			return result, NewError(ErrorConflict, fmt.Errorf("ignore target %q changed after planning; %w", file.Path, wrapIgnoreProgress(result, err)))
		}
		replaced, err := a.write(file, beforeReplace)
		if err != nil {
			if errors.Is(err, errIgnoreSnapshotChanged) {
				return result, NewError(ErrorConflict, fmt.Errorf("ignore target %q changed after planning; %w", file.Path, wrapIgnoreProgress(result, err)))
			}
			if replaced {
				update := updateForIgnoreFile(file)
				result.Changed = append(result.Changed, update)
				result.Remaining = updatesForIgnoreFiles(changed[index+1:])
			}
			return result, wrapIgnoreProgress(result, NewError(ErrorInternal, fmt.Errorf("replace ignore target %q: %w", file.Path, err)))
		}
		update := updateForIgnoreFile(file)
		result.Changed = append(result.Changed, update)
		result.Remaining = updatesForIgnoreFiles(changed[index+1:])
	}
	return result, nil
}

func ignoreProgressDiagnostic(result IgnoreApplyResult) string {
	return fmt.Sprintf("source ignore progress: changed files %v; remaining targets %v; retry will not duplicate completed rules", result.Changed, result.Remaining)
}

func wrapIgnoreProgress(result IgnoreApplyResult, cause error) error {
	if len(result.Changed) == 0 {
		return cause
	}
	var wrapped *ignoreProgressError
	if errors.As(cause, &wrapped) {
		return cause
	}
	return &ignoreProgressError{result: result, cause: cause}
}

// ignoreProgressError marks an error whose retained source progress is already
// rendered, so later init failure handling can verify that progress without
// duplicating its retry diagnostic.
type ignoreProgressError struct {
	result IgnoreApplyResult
	cause  error
}

func (e *ignoreProgressError) Error() string {
	return fmt.Sprintf("%s: %v", ignoreProgressDiagnostic(e.result), e.cause)
}

func (e *ignoreProgressError) Unwrap() error { return e.cause }

func changedIgnoreFiles(files []IgnoreFilePlan) []IgnoreFilePlan {
	changed := make([]IgnoreFilePlan, 0, len(files))
	for _, file := range files {
		if file.Changed {
			changed = append(changed, file)
		}
	}
	return changed
}

func updateForIgnoreFile(file IgnoreFilePlan) IgnoreFileUpdate {
	return IgnoreFileUpdate{ParentRepositoryID: file.ParentRepositoryID, Path: file.Path, AddedRules: append([]string(nil), file.AddedRules...)}
}

func updatesForIgnoreFiles(files []IgnoreFilePlan) []IgnoreFileUpdate {
	updates := make([]IgnoreFileUpdate, 0, len(files))
	for _, file := range files {
		updates = append(updates, updateForIgnoreFile(file))
	}
	return updates
}

var errIgnoreSnapshotChanged = errors.New("ignore file snapshot changed")

func revalidateIgnoreSnapshot(expected IgnoreFileSnapshot) error {
	current, err := captureIgnoreFile(expected.Path)
	if err != nil {
		return fmt.Errorf("%w: %v", errIgnoreSnapshotChanged, err)
	}
	if expected.parentInfo == nil || current.parentInfo == nil || !os.SameFile(expected.parentInfo, current.parentInfo) || expected.Exists != current.Exists || expected.Mode != current.Mode || !bytes.Equal(expected.Bytes, current.Bytes) || (expected.Exists && (expected.info == nil || current.info == nil || !os.SameFile(expected.info, current.info))) {
		return errIgnoreSnapshotChanged
	}
	return nil
}

func applyIgnoreFile(file IgnoreFilePlan, beforeReplace func() error) (bool, error) {
	hook := func(step string) error {
		if step == "before-rename" {
			return beforeReplace()
		}
		return nil
	}
	if file.Snapshot.Exists {
		err := fsutil.WriteFileAtomicModeWithHook(file.Path, file.NewBytes, file.Snapshot.Mode, hook)
		return err == nil || fsutil.ReplacementCompleted(err), err
	}
	err := fsutil.WriteFileAtomicCreateModeWithHook(file.Path, file.NewBytes, 0o644, hook)
	return err == nil || fsutil.ReplacementCompleted(err), err
}
