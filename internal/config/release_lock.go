package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ReleaseLockVersion is the independently versioned, tracked revision overlay.
const ReleaseLockVersion = 1

// ReleaseLock deliberately contains only immutable revision composition facts.
// Repository topology and sources remain owned by the portable manifest.
type ReleaseLock struct {
	Version      int                              `yaml:"version" json:"version"`
	Project      ReleaseLockProject               `yaml:"project" json:"project"`
	Release      ReleaseLockRelease               `yaml:"release" json:"release"`
	Repositories map[string]ReleaseLockRepository `yaml:"repositories" json:"repositories"`
}
type ReleaseLockProject struct {
	ID             string `yaml:"id" json:"id"`
	ManifestSHA256 string `yaml:"manifest_sha256" json:"manifestSha256"`
}
type ReleaseLockRelease struct {
	Name string `yaml:"name" json:"name"`
}
type ReleaseLockRepository struct {
	Revision string `yaml:"revision" json:"revision"`
}

// ReleaseManifestSHA256 binds a lock to the literal tracked manifest bytes.
func ReleaseManifestSHA256(manifest []byte) string {
	digest := sha256.Sum256(manifest)
	return hex.EncodeToString(digest[:])
}

// LoadReleaseLock strictly decodes one lock document without filesystem I/O.
func LoadReleaseLock(data []byte) (ReleaseLock, error) {
	if !utf8.Valid(data) {
		return ReleaseLock{}, fmt.Errorf("release lock must be valid UTF-8")
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return ReleaseLock{}, err
	}
	if err := validateReleaseLockYAML(&node); err != nil {
		return ReleaseLock{}, err
	}
	var value ReleaseLock
	if err := strictYAML(data, &value); err != nil {
		return ReleaseLock{}, err
	}
	if err := requireReleaseLockFields(&node); err != nil {
		return ReleaseLock{}, err
	}
	if err := value.Validate(); err != nil {
		return ReleaseLock{}, err
	}
	return value, nil
}

func validateReleaseLockYAML(node *yaml.Node) error {
	if node == nil {
		return fmt.Errorf("release lock is required")
	}
	if node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return fmt.Errorf("release lock aliases and merge keys are not supported")
	}
	for _, child := range node.Content {
		if err := validateReleaseLockYAML(child); err != nil {
			return err
		}
	}
	return nil
}
func requireReleaseLockFields(document *yaml.Node) error {
	if document == nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("release lock is required")
	}
	root := document.Content[0]
	for _, field := range []string{"version", "project", "release", "repositories"} {
		if missingOrNull(mappingValue(root, field)) {
			return fmt.Errorf("release lock field %q is required", field)
		}
	}
	project, release := mappingValue(root, "project"), mappingValue(root, "release")
	for _, field := range []string{"id", "manifest_sha256"} {
		if missingOrNull(mappingValue(project, field)) {
			return fmt.Errorf("release lock project field %q is required", field)
		}
	}
	if missingOrNull(mappingValue(release, "name")) {
		return fmt.Errorf("release lock release field %q is required", "name")
	}
	return nil
}
func missingOrNull(node *yaml.Node) bool { return node == nil || node.Tag == "!!null" }

// Validate checks syntax local to a lock. ValidateFor binds it to an exact
// portable manifest and its non-base repository set.
func (lock ReleaseLock) Validate() error {
	if lock.Version != ReleaseLockVersion {
		return fmt.Errorf("unsupported release lock version %d", lock.Version)
	}
	if err := ValidatePortableID(lock.Project.ID); err != nil {
		return fmt.Errorf("release lock project ID: %w", err)
	}
	if !lowerHex(lock.Project.ManifestSHA256, 64) {
		return fmt.Errorf("release lock manifest_sha256 must be a lowercase SHA-256 digest")
	}
	if lock.Release.Name == "" || containsControl(lock.Release.Name) {
		return fmt.Errorf("release lock name is required and must not contain control characters")
	}
	for id, repository := range lock.Repositories {
		if err := ValidatePortableID(id); err != nil {
			return fmt.Errorf("release lock repository ID %q: %w", id, err)
		}
		if !releaseRevision(repository.Revision) {
			return fmt.Errorf("release lock repository %q revision must be a full lowercase 40- or 64-character object ID", id)
		}
	}
	return nil
}
func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func releaseRevision(value string) bool { return lowerHex(value, 40) || lowerHex(value, 64) }

func (lock ReleaseLock) ValidateFor(projectID string, manifest []byte, nonBaseIDs []string) error {
	if err := lock.Validate(); err != nil {
		return err
	}
	if lock.Project.ID != projectID {
		return fmt.Errorf("release lock project ID does not match portable manifest")
	}
	if lock.Project.ManifestSHA256 != ReleaseManifestSHA256(manifest) {
		return fmt.Errorf("release lock manifest SHA-256 does not match exact portable manifest bytes")
	}
	expected := append([]string(nil), nonBaseIDs...)
	sort.Strings(expected)
	if len(expected) != len(lock.Repositories) {
		return fmt.Errorf("release lock repository set does not match portable manifest")
	}
	for index, id := range expected {
		if index > 0 && id == expected[index-1] {
			return fmt.Errorf("portable manifest non-base repository IDs are duplicated")
		}
		if _, ok := lock.Repositories[id]; !ok {
			return fmt.Errorf("release lock repository set does not match portable manifest")
		}
	}
	return nil
}

// MarshalReleaseLock returns deterministic UTF-8 YAML. yaml.v3 sorts string
// map keys; the intermediate ordered map keeps this contract explicit.
func MarshalReleaseLock(lock ReleaseLock) ([]byte, error) {
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(lock.Repositories))
	for id := range lock.Repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	add := func(key string, value *yaml.Node) { root.Content = append(root.Content, scalar(key), value) }
	add("version", scalarInt(lock.Version))
	project := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	project.Content = append(project.Content, scalar("id"), scalar(lock.Project.ID), scalar("manifest_sha256"), scalar(lock.Project.ManifestSHA256))
	add("project", project)
	release := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	release.Content = append(release.Content, scalar("name"), scalar(lock.Release.Name))
	add("release", release)
	repositories := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, id := range ids {
		item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		item.Content = append(item.Content, scalar("revision"), scalar(lock.Repositories[id].Revision))
		repositories.Content = append(repositories.Content, scalar(id), item)
	}
	add("repositories", repositories)
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return []byte(strings.ReplaceAll(out.String(), "\r\n", "\n")), nil
}
func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
func scalarInt(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)}
}
