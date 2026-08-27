package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/definebusiness/wtree/internal/pathutil"
)

type committedIgnoreFile struct {
	directory string
	contents  []byte
}

// CommittedIgnoreInspector is the observational committed-ref ignore boundary
// used by doctor for an existing nested checkout. It is deliberately narrower
// than Git so acquisition callers keep their existing absent-mount contract.
type CommittedIgnoreInspector interface {
	InspectCommittedIgnore(context.Context, string, string, string) (bool, error)
}

// InspectCommittedIgnore reports whether path is ignored by the winning committed
// .gitignore rule at ref. It reads ignore sources from Git objects and asks
// Git itself to evaluate their patterns against the already-observed checkout.
// It creates no checkout, index, lock, temporary file, directory, object, or
// ref, and it never accepts info, global, or working-tree ignore sources as
// committed evidence.
func (a *Adapter) InspectCommittedIgnore(ctx context.Context, repository, ref, path string) (bool, error) {
	mount, err := pathutil.NormalizeMount(filepath.ToSlash(path), pathutil.ChildMount)
	if err != nil || mount != filepath.ToSlash(path) {
		return false, fmt.Errorf("inspect committed ignore: invalid mount")
	}
	commit, err := a.ResolveRef(ctx, repository, ref)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	files := make([]committedIgnoreFile, 0, strings.Count(mount, "/")+1)
	for _, directory := range committedIgnoreDirectories(mount) {
		if directory != "." {
			ignored, evaluateErr := a.evaluateCommittedIgnore(ctx, repository, files, directory)
			if evaluateErr != nil {
				return false, evaluateErr
			}
			if ignored {
				// Git does not inspect a per-directory ignore file below an
				// excluded directory, so a descendant negation cannot revive it.
				continue
			}
		}
		ignorePath := ".gitignore"
		if directory != "." {
			ignorePath = directory + "/.gitignore"
		}
		contents, showErr := a.runFact(ctx, repository, "show", commit+":"+ignorePath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		if showErr != nil {
			if isMissingObject(showErr) {
				continue
			}
			return false, showErr
		}
		files = append(files, committedIgnoreFile{directory: directory, contents: append([]byte(nil), contents...)})
	}
	return a.evaluateCommittedIgnore(ctx, repository, files, mount)
}

func committedIgnoreDirectories(mount string) []string {
	directories := []string{"."}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(mount)))
	if parent == "." {
		return directories
	}
	current := ""
	for _, component := range strings.Split(parent, "/") {
		current = strings.TrimPrefix(current+"/"+component, "/")
		directories = append(directories, current)
	}
	return directories
}

func (a *Adapter) evaluateCommittedIgnore(ctx context.Context, repository string, files []committedIgnoreFile, query string) (bool, error) {
	ignored := false
	for _, file := range files {
		relative := query
		if file.directory != "." {
			prefix := file.directory + "/"
			if !strings.HasPrefix(query, prefix) {
				continue
			}
			relative = strings.TrimPrefix(query, prefix)
		}
		seed := committedIgnoreLiteralRule(relative)
		if !ignored {
			seed = "!" + seed
		}
		input := make([]byte, 0, len(seed)+1+len(file.contents))
		input = append(input, seed...)
		input = append(input, '\n')
		input = append(input, file.contents...)
		evaluationDirectory := repository
		if file.directory != "." {
			evaluationDirectory = filepath.Join(repository, filepath.FromSlash(file.directory))
		}
		output, err := a.runFactInput(ctx, repository, input,
			"--work-tree="+evaluationDirectory,
			"-c", "core.excludesFile=/dev/stdin",
			"check-ignore", "-v", "--no-index", "--", relative+"/",
		)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		if err != nil {
			var gitError *Error
			if errors.As(err, &gitError) && gitError.ExitCode == 1 {
				ignored = false
				continue
			}
			return false, err
		}
		metadata, _, found := bytes.Cut(bytes.TrimSpace(output), []byte{'\t'})
		if !found {
			return false, fmt.Errorf("parse committed check-ignore output")
		}
		source, parseErr := checkIgnoreSource(string(metadata))
		if parseErr != nil {
			return false, fmt.Errorf("parse committed check-ignore source")
		}
		if filepath.ToSlash(source) == "/dev/stdin" {
			ignored = true
			continue
		}
		// A higher-precedence working-tree or repository-local source may be
		// reported by check-ignore. Re-evaluate with only the committed rules
		// supplied as command-line exclude input so that source cannot qualify.
		isolated, isolatedErr := a.runFactInput(ctx, repository, input,
			"--work-tree="+evaluationDirectory,
			"ls-files", "--others", "--ignored", "--directory", "--no-empty-directory",
			"--exclude-from=/dev/stdin", "--", relative+"/",
		)
		if isolatedErr != nil {
			return false, isolatedErr
		}
		ignored = len(bytes.TrimSpace(isolated)) != 0
	}
	return ignored, nil
}

var _ CommittedIgnoreInspector = (*Adapter)(nil)

func committedIgnoreLiteralRule(mount string) string {
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
	return rule.String()
}
