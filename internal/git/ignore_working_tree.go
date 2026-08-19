package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/definebusiness/wtree/internal/pathutil"
)

// WorkingTreeIgnoreEvidence is Git's complete winning-pattern observation for
// one directory. A zero value means Git found no matching ignore pattern.
type WorkingTreeIgnoreEvidence struct {
	Ignored bool
	Negated bool
	Source  string
	Line    int
	Pattern string
	Path    string

	// DirectoryObserved reports that Git enumerated this mount as an ignored
	// directory in the working tree. It is required before a non-directory
	// pattern can qualify, because a future directory can activate a later
	// negation that Git cannot apply while the directory is absent.
	DirectoryObserved bool

	// ConfiguredExclude reports that the winning source is configured through
	// core.excludesFile rather than discovered through the .gitignore hierarchy.
	ConfiguredExclude bool
}

// WorkingTreeIgnoreInspector is deliberately narrower than Git so existing
// consumers of committed-ref inspection do not acquire a working-tree API.
type WorkingTreeIgnoreInspector interface {
	InspectWorkingTreeIgnore(context.Context, string, string) (WorkingTreeIgnoreEvidence, error)
}

// InspectWorkingTreeIgnore observes Git's winning ignore pattern for a
// normalized non-root mount in a checkout. --no-index lets it inspect paths
// independently of whether the child is tracked. The exact query does not
// consult the filesystem first, so Git alone selects its semantics and can
// report a winning directory negation. When the exact query has no match, a
// directory-form fallback lets an explicit directory rule protect a future
// mount. A broad non-directory positive must additionally be corroborated by
// Git's ignored-directory walk, because a later directory negation can depend
// on the mount's actual presence.
func (a *Adapter) InspectWorkingTreeIgnore(ctx context.Context, repository, mount string) (WorkingTreeIgnoreEvidence, error) {
	normalized, err := pathutil.NormalizeMount(mount, false)
	if err != nil {
		return WorkingTreeIgnoreEvidence{}, err
	}
	if normalized != mount {
		return WorkingTreeIgnoreEvidence{}, fmt.Errorf("mount %q is not normalized", mount)
	}
	evidence, matched, err := a.checkWorkingTreeIgnore(ctx, repository, mount)
	if err != nil {
		return WorkingTreeIgnoreEvidence{}, err
	}
	if !matched {
		evidence, matched, err = a.checkWorkingTreeIgnore(ctx, repository, mount+"/")
		if err != nil {
			return WorkingTreeIgnoreEvidence{}, err
		}
		if !matched {
			return WorkingTreeIgnoreEvidence{}, nil
		}
	}
	configured, err := a.configuredExcludesFile(ctx, repository)
	if err != nil {
		return WorkingTreeIgnoreEvidence{}, err
	}
	evidence.ConfiguredExclude = configured != "" && sameIgnoreSource(repository, evidence.Source, configured)
	if evidence.Ignored && !evidence.Negated && !strings.HasSuffix(evidence.Pattern, "/") {
		evidence.DirectoryObserved, err = a.ignoredWorkingTreeDirectory(ctx, repository, mount)
		if err != nil {
			return WorkingTreeIgnoreEvidence{}, err
		}
	}
	return evidence, nil
}

// ignoredWorkingTreeDirectory asks Git to enumerate ignored directories from
// its working-tree walk. Unlike a separate Lstat, the result is a Git-owned
// observation that the mount existed as an ignored directory when Git
// evaluated its ignore rules. Git may collapse an ignored multi-component
// mount to an ignored ancestor in this output; that ancestor establishes the
// requested mount is inside an ignored directory.
func (a *Adapter) ignoredWorkingTreeDirectory(ctx context.Context, repository, mount string) (bool, error) {
	output, err := a.runFact(ctx, repository, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z", "--", mount)
	if err != nil {
		return false, err
	}
	if len(output) == 0 {
		return false, nil
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 2 || len(fields[len(fields)-1]) != 0 {
		return false, fmt.Errorf("malformed ignored-directory output")
	}
	want := mount + "/"
	allowAncestor := strings.Contains(mount, "/")
	for _, field := range fields[:len(fields)-1] {
		directory := string(field)
		if directory == want {
			return true, nil
		}
		if allowAncestor && strings.HasSuffix(directory, "/") {
			ancestor := strings.TrimSuffix(directory, "/")
			if ancestor != "" && strings.HasPrefix(mount, ancestor+"/") {
				return true, nil
			}
		}
	}
	return false, nil
}

func (a *Adapter) checkWorkingTreeIgnore(ctx context.Context, repository, path string) (WorkingTreeIgnoreEvidence, bool, error) {
	output, err := a.runFactInput(ctx, repository, append([]byte(path), 0), "check-ignore", "-v", "-z", "--stdin", "--no-index")
	if err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 1 {
			return WorkingTreeIgnoreEvidence{}, false, nil
		}
		return WorkingTreeIgnoreEvidence{}, false, err
	}
	evidence, err := ParseCheckIgnoreEvidence(output)
	if err != nil {
		return WorkingTreeIgnoreEvidence{}, false, err
	}
	return evidence, true, nil
}

func (a *Adapter) configuredExcludesFile(ctx context.Context, repository string) (string, error) {
	output, err := a.runFact(ctx, repository, "config", "--null", "--path", "--get", "core.excludesFile")
	if err != nil {
		var gitError *Error
		if errors.As(err, &gitError) && gitError.ExitCode == 1 {
			return "", nil
		}
		return "", err
	}
	if len(output) == 0 || output[len(output)-1] != 0 || bytes.IndexByte(output[:len(output)-1], 0) >= 0 {
		return "", fmt.Errorf("parse core.excludesFile")
	}
	return string(output[:len(output)-1]), nil
}

// ParseCheckIgnoreEvidence parses the locale-neutral verbose check-ignore
// record: source<NUL>line<NUL>pattern<NUL>path<NUL>. It uses Git's NUL output
// so source and path names containing colons, tabs, or newlines remain exact.
func ParseCheckIgnoreEvidence(output []byte) (WorkingTreeIgnoreEvidence, error) {
	fields := bytes.Split(output, []byte{0})
	if len(fields) != 5 || len(fields[4]) != 0 || len(fields[0]) == 0 || len(fields[1]) == 0 || len(fields[2]) == 0 || len(fields[3]) == 0 {
		return WorkingTreeIgnoreEvidence{}, fmt.Errorf("malformed check-ignore output")
	}
	line, err := strconv.Atoi(string(fields[1]))
	if err != nil || line <= 0 {
		return WorkingTreeIgnoreEvidence{}, fmt.Errorf("malformed check-ignore line")
	}
	pattern := string(fields[2])
	return WorkingTreeIgnoreEvidence{
		Ignored: !strings.HasPrefix(pattern, "!"),
		Negated: strings.HasPrefix(pattern, "!"),
		Source:  string(fields[0]),
		Line:    line,
		Pattern: pattern,
		Path:    string(fields[3]),
	}, nil
}

// Qualifies reports whether this effective winning pattern originates from a
// .gitignore file in the immediate parent checkout. info/exclude, global
// excludes, outside files, malformed paths, and winning negations do not. A
// non-directory pattern also needs Git-owned ignored-directory evidence.
func (e WorkingTreeIgnoreEvidence) Qualifies(parent string) bool {
	if !e.Ignored || e.Negated || e.ConfiguredExclude || (!strings.HasSuffix(e.Pattern, "/") && !e.DirectoryObserved) || filepath.Base(filepath.FromSlash(e.Source)) != ".gitignore" {
		return false
	}
	root, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false
	}
	resolved, err := resolveIgnoreSource(root, e.Source)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	if filepath.Base(resolved) != ".gitignore" {
		return false
	}
	query, err := pathutil.NormalizeMount(strings.TrimSuffix(e.Path, "/"), false)
	if err != nil {
		return false
	}
	sourceDirectory := filepath.ToSlash(filepath.Dir(relative))
	return sourceDirectory == "." || query == sourceDirectory || strings.HasPrefix(query, sourceDirectory+"/")
}

func sameIgnoreSource(parent, left, right string) bool {
	root, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false
	}
	leftResolved, leftErr := resolveIgnoreSource(root, left)
	rightResolved, rightErr := resolveIgnoreSource(root, right)
	return leftErr == nil && rightErr == nil && leftResolved == rightResolved
}

func resolveIgnoreSource(root, source string) (string, error) {
	path := filepath.FromSlash(source)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.EvalSymlinks(path)
}

var _ WorkingTreeIgnoreInspector = (*Adapter)(nil)
