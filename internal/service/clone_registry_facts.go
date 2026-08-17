package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/definebusiness/wtree/internal/store"
)

// CloneWorkspaceFact is one immutable workspace-state observation belonging
// to the same stable registry generation.
type CloneWorkspaceFact struct {
	ProjectID string               `json:"projectId"`
	FileName  string               `json:"fileName"`
	SHA256    string               `json:"sha256"`
	State     store.WorkspaceState `json:"state"`
}

type CloneRegistrySnapshot struct {
	Registry         store.Registry       `json:"registry"`
	RegistrySHA256   string               `json:"registrySha256"`
	GenerationSHA256 string               `json:"generationSha256"`
	Workspaces       []CloneWorkspaceFact `json:"workspaces"`
}

type CloneRegistryFactsReader interface {
	Read(string) (CloneRegistrySnapshot, error)
}

type cloneRegistryFileSystem interface {
	ReadDir(string) ([]os.DirEntry, error)
	Lstat(string) (os.FileInfo, error)
	Open(string) (ManifestSourceFile, error)
}

type osCloneRegistryFileSystem struct{}

func (osCloneRegistryFileSystem) ReadDir(path string) ([]os.DirEntry, error)   { return os.ReadDir(path) }
func (osCloneRegistryFileSystem) Lstat(path string) (os.FileInfo, error)       { return os.Lstat(path) }
func (osCloneRegistryFileSystem) Open(path string) (ManifestSourceFile, error) { return os.Open(path) }

type stableCloneRegistryFactsReader struct {
	fs               cloneRegistryFileSystem
	beforeRevalidate func()
}

func newCloneRegistryFactsReader() CloneRegistryFactsReader {
	return stableCloneRegistryFactsReader{fs: osCloneRegistryFileSystem{}}
}

type cloneWorkspaceCapture struct {
	fact CloneWorkspaceFact
	path string
	raw  []byte
}

func (reader stableCloneRegistryFactsReader) Read(dataDir string) (CloneRegistrySnapshot, error) {
	registryPath := filepath.Join(dataDir, "registry.json")
	registryRaw, registryAbsent, err := reader.readOptionalRegular(registryPath)
	if err != nil {
		return CloneRegistrySnapshot{}, err
	}
	registry := store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{}}
	registryDigest := "absent"
	if !registryAbsent {
		registry, err = store.DecodeRegistry(registryRaw)
		if err != nil {
			return CloneRegistrySnapshot{}, err
		}
		registryDigest = bytesSHA256(registryRaw)
	}

	projectIDs := make([]string, 0, len(registry.Projects))
	for projectID := range registry.Projects {
		projectIDs = append(projectIDs, projectID)
	}
	sort.Strings(projectIDs)
	directoryEntries := make(map[string][]string, len(projectIDs))
	var captures []cloneWorkspaceCapture
	for _, projectID := range projectIDs {
		directory := WorkspaceStateDirectory(dataDir, projectID)
		names, err := reader.regularWorkspaceNames(directory)
		if err != nil {
			return CloneRegistrySnapshot{}, fmt.Errorf("read registered workspaces for project %q: %w", projectID, err)
		}
		directoryEntries[projectID] = names
		for _, name := range names {
			path := filepath.Join(directory, name)
			raw, absent, err := reader.readOptionalRegular(path)
			if err != nil {
				return CloneRegistrySnapshot{}, fmt.Errorf("read registered workspace %q for project %q: %w", name, projectID, err)
			}
			if absent {
				return CloneRegistrySnapshot{}, NewError(ErrorConflict, fmt.Errorf("registered workspace %q for project %q changed during capture", name, projectID))
			}
			state, err := store.DecodeWorkspace(raw)
			if err != nil {
				return CloneRegistrySnapshot{}, fmt.Errorf("decode registered workspace %q for project %q: %w", name, projectID, err)
			}
			captures = append(captures, cloneWorkspaceCapture{fact: CloneWorkspaceFact{ProjectID: projectID, FileName: name, SHA256: bytesSHA256(raw), State: state}, path: path, raw: raw})
		}
	}

	// Re-read the complete registry/workspace set and directory membership.
	// A plan never combines paths or identities from different generations.
	if reader.beforeRevalidate != nil {
		reader.beforeRevalidate()
	}
	registryAgain, absentAgain, err := reader.readOptionalRegular(registryPath)
	if err != nil || absentAgain != registryAbsent || !bytes.Equal(registryRaw, registryAgain) {
		return CloneRegistrySnapshot{}, NewError(ErrorConflict, fmt.Errorf("project registry changed during clone planning"))
	}
	for _, projectID := range projectIDs {
		names, err := reader.regularWorkspaceNames(WorkspaceStateDirectory(dataDir, projectID))
		if err != nil || !equalStrings(names, directoryEntries[projectID]) {
			return CloneRegistrySnapshot{}, NewError(ErrorConflict, fmt.Errorf("registered workspaces for project %q changed during clone planning", projectID))
		}
	}
	for _, capture := range captures {
		raw, absent, err := reader.readOptionalRegular(capture.path)
		if err != nil || absent || !bytes.Equal(raw, capture.raw) {
			return CloneRegistrySnapshot{}, NewError(ErrorConflict, fmt.Errorf("registered workspace %q for project %q changed during clone planning", capture.fact.FileName, capture.fact.ProjectID))
		}
	}

	generation := sha256.New()
	_, _ = generation.Write(registryRaw)
	workspaces := make([]CloneWorkspaceFact, 0, len(captures))
	for _, capture := range captures {
		_, _ = fmt.Fprintf(generation, "\x00%s\x00%s\x00", capture.fact.ProjectID, capture.fact.FileName)
		_, _ = generation.Write(capture.raw)
		workspaces = append(workspaces, capture.fact)
	}
	return CloneRegistrySnapshot{Registry: registry, RegistrySHA256: registryDigest, GenerationSHA256: hex.EncodeToString(generation.Sum(nil)), Workspaces: workspaces}, nil
}

func (reader stableCloneRegistryFactsReader) readOptionalRegular(path string) ([]byte, bool, error) {
	info, err := reader.fs.Lstat(path)
	if os.IsNotExist(err) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%q must be a regular non-symlink file", path)
	}
	file, err := reader.fs.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, false, NewError(ErrorConflict, fmt.Errorf("%q changed while it was opened", path))
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, false, err
	}
	after, err := file.Stat()
	if err != nil || after.Size() != opened.Size() || after.Mode() != opened.Mode() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, false, NewError(ErrorConflict, fmt.Errorf("%q changed while it was read", path))
	}
	return data, false, nil
}

func (reader stableCloneRegistryFactsReader) regularWorkspaceNames(directory string) ([]string, error) {
	entries, err := reader.fs.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.Type().IsRegular() == false && entry.Type() != 0 {
			return nil, fmt.Errorf("workspace state %q must be a regular non-symlink file", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func bytesSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
