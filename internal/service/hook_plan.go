package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/definebusiness/wtree/internal/config"
)

const HookPlanVersion = 1

type HookPlanEntry struct {
	Source               string   `json:"source"`
	Event                string   `json:"event"`
	ID                   string   `json:"id"`
	Repository           string   `json:"repository"`
	WorkingDirectory     string   `json:"workingDirectory"`
	ConfiguredExecutable string   `json:"configuredExecutable"`
	ResolvedExecutable   string   `json:"resolvedExecutable,omitempty"`
	Availability         string   `json:"availability"`
	Arguments            []string `json:"arguments"`
	Timeout              string   `json:"timeout"`
	ExecutionPolicy      string   `json:"executionPolicy"`
}
type HookPlan struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	entries   []HookPlanEntry
	authority hookPlanAuthority
}

func (p HookPlan) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Version   int             `json:"version"`
		Operation string          `json:"operation"`
		Entries   []HookPlanEntry `json:"entries"`
	}{Version: p.Version, Operation: p.Operation, Entries: p.Entries()})
}

type hookPlanAuthority struct {
	sourceSHA, workspaceSHA, digest                                                                                   string
	entries                                                                                                           []hookPlanInputEntry
	projectID, projectName, baseRepository, workspaceID, workspaceName, sourceRoot, targetRoot, source, event, policy string
}
type hookPlanInput struct {
	Operation, Source, Event, Policy, ProjectID, ProjectName, BaseRepository, WorkspaceID, WorkspaceName, SourceLogicalRoot, TargetLogicalRoot string
	SourceBytes, WorkspaceStateBytes                                                                                                           []byte
	Entries                                                                                                                                    []hookPlanInputEntry
}
type hookPlanInputEntry struct {
	ID, Repository, SourceRepository, TargetRepository, Branch, Head, ConfiguredExecutable, ResolvedExecutable, Availability string
	Arguments                                                                                                                []string
	Timeout                                                                                                                  time.Duration
}

func newHookPlan(in hookPlanInput) (HookPlan, error) {
	if !validHookPlanCombination(in) || len(in.Entries) == 0 || !safeHookPlanID(in.ProjectID) || !safeHookPlanID(in.BaseRepository) || !safeHookPlanID(in.WorkspaceID) || !validHookPlanName(in.ProjectName) || !validHookPlanName(in.WorkspaceName) || !absolute(in.SourceLogicalRoot) || !absolute(in.TargetLogicalRoot) {
		return HookPlan{}, errors.New("invalid hook plan")
	}
	entries := make([]HookPlanEntry, len(in.Entries))
	seen := map[string]bool{}
	for i, e := range in.Entries {
		if !safeHookPlanID(e.ID) || seen[e.ID] || !safeHookPlanID(e.Repository) || !absolute(e.SourceRepository) || !absolute(e.TargetRepository) || e.ConfiguredExecutable == "" || !absolute(e.ResolvedExecutable) && e.ResolvedExecutable != "" || !oneHook(e.Availability, "available", "deferred") || e.Availability == "available" && e.ResolvedExecutable == "" || e.Availability == "deferred" && e.ResolvedExecutable != "" || e.Timeout <= 0 || e.Timeout > 24*time.Hour || !head(e.Head) || !validHookPlanText(e.Branch) || !underRoot(in.SourceLogicalRoot, e.SourceRepository) || !underRoot(in.TargetLogicalRoot, e.TargetRepository) {
			return HookPlan{}, errors.New("invalid hook plan entry")
		}
		seen[e.ID] = true
		arguments := make([]string, len(e.Arguments))
		copy(arguments, e.Arguments)
		entries[i] = HookPlanEntry{Source: in.Source, Event: in.Event, ID: e.ID, Repository: e.Repository, WorkingDirectory: e.TargetRepository, ConfiguredExecutable: e.ConfiguredExecutable, ResolvedExecutable: e.ResolvedExecutable, Availability: e.Availability, Arguments: arguments, Timeout: e.Timeout.String(), ExecutionPolicy: in.Policy}
	}
	public := HookPlan{Version: HookPlanVersion, Operation: in.Operation, entries: entries}
	// Portable clone records intentionally bind configured authority, not the
	// transient availability or physical resolver result observed after core
	// publication. This lets an unavailable/canceled first observation create
	// a durable retry record while a later retry can still enforce its current
	// resolved executable facts.
	canonicalInput := in
	if in.Operation == "clone" && in.Source == "portable" {
		canonicalInput.Entries = cloneHookPlanInputEntries(in.Entries)
		for i := range canonicalInput.Entries {
			canonicalInput.Entries[i].Availability = "deferred"
			canonicalInput.Entries[i].ResolvedExecutable = ""
		}
	}
	canonical, err := json.Marshal(struct{ I hookPlanInput }{canonicalInput})
	if err != nil {
		return HookPlan{}, err
	}
	public.authority = hookPlanAuthority{sourceSHA: digest(in.SourceBytes), workspaceSHA: digest(in.WorkspaceStateBytes), digest: digest(canonical), entries: cloneHookPlanInputEntries(in.Entries), projectID: in.ProjectID, projectName: in.ProjectName, baseRepository: in.BaseRepository, workspaceID: in.WorkspaceID, workspaceName: in.WorkspaceName, sourceRoot: in.SourceLogicalRoot, targetRoot: in.TargetLogicalRoot, source: in.Source, event: in.Event, policy: in.Policy}
	return public, nil
}
func validHookPlanCombination(in hookPlanInput) bool {
	return in.Operation == "create" && in.Source == "local" && in.Event == "post-create" && in.Policy == "automatic" ||
		in.Operation == "clone" && in.Source == "portable" && in.Event == "post-clone" && in.Policy == "requires-run-hooks"
}
func (p HookPlan) Entries() []HookPlanEntry {
	out := make([]HookPlanEntry, len(p.entries))
	copy(out, p.entries)
	for i := range out {
		out[i].Arguments = append([]string{}, out[i].Arguments...)
	}
	return out
}
func (p HookPlan) Digest() string               { return p.authority.digest }
func (p HookPlan) SourceSHA256() string         { return p.authority.sourceSHA }
func (p HookPlan) WorkspaceStateSHA256() string { return p.authority.workspaceSHA }
func digest(b []byte) string                    { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func oneHook(s string, v ...string) bool {
	for _, x := range v {
		if s == x {
			return true
		}
	}
	return false
}
func safeHookPlanID(s string) bool {
	return config.ValidatePortableID(s) == nil
}
func validHookPlanName(s string) bool { return s != "" && len(s) <= 256 && validHookPlanText(s) }
func validHookPlanText(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
func absolute(s string) bool { return filepath.IsAbs(s) && filepath.Clean(s) == s }
func underRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && len(rel) >= 0 && (rel == "." || len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}
func head(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func cloneHookPlanInputEntries(v []hookPlanInputEntry) []hookPlanInputEntry {
	o := append([]hookPlanInputEntry(nil), v...)
	for i := range o {
		o[i].Arguments = append([]string(nil), o[i].Arguments...)
	}
	return o
}
