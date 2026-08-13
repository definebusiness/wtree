// Package git is the sole subprocess adapter for Git facts and operations.
package git

import (
	"bytes"
	"fmt"
	"strings"
)

// Worktree is one record emitted by git worktree list --porcelain.
type Worktree struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
}

// Status is the parsed subset of git status --porcelain used by safety checks.
type Status struct {
	Entries   []StatusEntry
	Staged    bool
	Modified  bool
	Untracked bool
}

// StatusEntry represents a porcelain-v1 status line.
type StatusEntry struct {
	Index        byte
	Worktree     byte
	Path         string
	OriginalPath string
	Untracked    bool
}

// ParseWorktreeList parses git worktree list --porcelain output.
func ParseWorktreeList(output []byte) ([]Worktree, error) {
	blocks := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n\n")
	worktrees := make([]Worktree, 0, len(blocks))
	for _, block := range blocks {
		if block == "" {
			continue
		}
		var worktree Worktree
		for _, line := range strings.Split(block, "\n") {
			key, value, hasValue := strings.Cut(line, " ")
			switch key {
			case "worktree":
				if !hasValue || value == "" {
					return nil, fmt.Errorf("malformed worktree record")
				}
				worktree.Path = value
			case "HEAD":
				worktree.Head = value
			case "branch":
				worktree.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "detached":
				worktree.Detached = true
			case "bare":
				worktree.Bare = true
			}
		}
		if worktree.Path == "" {
			return nil, fmt.Errorf("worktree record has no path")
		}
		if worktree.Detached && worktree.Branch != "" {
			return nil, fmt.Errorf("worktree %q is both detached and on branch %q", worktree.Path, worktree.Branch)
		}
		worktrees = append(worktrees, worktree)
	}
	return worktrees, nil
}

// ParseStatus parses NUL-delimited porcelain-v1 output. NUL delimiters are
// required because valid checkout paths can contain whitespace, newlines, and
// non-ASCII characters without Git's quoted-path encoding.
func ParseStatus(output []byte) (Status, error) {
	var status Status
	records := bytes.Split(output, []byte{0})
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) == 0 {
			continue
		}
		if len(record) < 3 || record[2] != ' ' {
			return Status{}, fmt.Errorf("malformed status record %q", record)
		}
		entry := StatusEntry{Index: record[0], Worktree: record[1], Path: string(record[3:])}
		if entry.Index == '?' && entry.Worktree == '?' {
			entry.Untracked = true
			status.Untracked = true
		} else {
			status.Staged = status.Staged || entry.Index != ' '
			status.Modified = status.Modified || entry.Worktree != ' '
		}
		if entry.Index == 'R' || entry.Index == 'C' || entry.Worktree == 'R' || entry.Worktree == 'C' {
			index++
			if index >= len(records) || len(records[index]) == 0 {
				return Status{}, fmt.Errorf("rename/copy status record missing original path")
			}
			entry.OriginalPath = string(records[index])
		}
		status.Entries = append(status.Entries, entry)
	}
	return status, nil
}
