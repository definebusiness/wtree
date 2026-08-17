package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
)

const validPortableManifest = `version: 1
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme-shop
repositories:
  root:
    clone:
      remote: origin
      url: https://github.com/acme/acme-shop.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - 0123456789abcdef0123456789abcdef01234567
    parent: ""
    mount: .
    default_branch: main
`

func TestPortableManifestStrictDeterministicRoundTrip(t *testing.T) {
	input := strings.Replace(validPortableManifest, "  root:\n", "  zoo:\n", 1) + `  alpha:
    clone:
      remote: upstream
      url: git@example.test:group/alpha.git
    upstream:
      branch: develop
      remote: upstream
      merge: refs/heads/develop
    identity:
      initial_commits:
        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    parent: zoo
    mount: services/alpha
    default_branch: develop
`
	first, err := config.LoadPortableManifest([]byte(input))
	if err != nil {
		t.Fatalf("LoadPortableManifest() error = %v", err)
	}
	one, err := config.MarshalPortableManifest(first)
	if err != nil {
		t.Fatalf("MarshalPortableManifest() error = %v", err)
	}
	two, err := config.MarshalPortableManifest(first)
	if err != nil {
		t.Fatalf("second MarshalPortableManifest() error = %v", err)
	}
	if string(one) != string(two) {
		t.Fatalf("portable manifest encoding is not deterministic:\n%s\n---\n%s", one, two)
	}
	if !strings.HasPrefix(string(one), "version: 1\n") {
		t.Fatalf("manifest version is not canonical YAML:\n%s", one)
	}
	if strings.Index(string(one), "  alpha:\n") > strings.Index(string(one), "  zoo:\n") {
		t.Fatalf("repository map is not lexical:\n%s", one)
	}
	if _, err := config.LoadPortableManifest(append(one, []byte("---\nversion: 1\n")...)); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
	for _, input := range []string{
		"version: 2\n", strings.Replace(validPortableManifest, "version: 1", "version: 1\nunknown: true", 1),
		strings.Replace(validPortableManifest, "repositories:\n", "repositories: null\n", 1),
		strings.Replace(validPortableManifest, "  root:\n", "  root:\n  root:\n", 1),
		strings.Replace(validPortableManifest, "  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221", "  id: ../project", 1),
	} {
		if _, err := config.LoadPortableManifest([]byte(input)); err == nil {
			t.Fatalf("LoadPortableManifest(%q) error = nil", input)
		}
	}
}

func TestPortableManifestUsesItsOwnSchemaVersion(t *testing.T) {
	if config.PortableManifestVersion != 1 {
		t.Fatalf("PortableManifestVersion = %d, want 1", config.PortableManifestVersion)
	}
	manifest, err := config.LoadPortableManifest([]byte(validPortableManifest))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != config.PortableManifestVersion {
		t.Fatalf("portable manifest version = %d, want PortableManifestVersion %d", manifest.Version, config.PortableManifestVersion)
	}
}

func TestPortableManifestRejectsInvalidGraphAndFields(t *testing.T) {
	tests := []struct {
		name string
		edit string
	}{
		{"zero roots", "    parent: root\n"},
		{"multiple roots", "  other:\n    clone:\n      remote: origin\n      url: https://example.test/other.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    parent: \"\"\n    mount: .\n    default_branch: main\n"},
		{"missing parent", "    parent: missing\n"},
		{"cycle", "  first:\n    clone:\n      remote: origin\n      url: https://example.test/first.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    parent: second\n    mount: first\n    default_branch: main\n  second:\n    clone:\n      remote: origin\n      url: https://example.test/second.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n    parent: first\n    mount: second\n    default_branch: main\n"},
		{"unsafe repository id", "  ../root:\n"},
		{"unsafe child mount", "    mount: ../escape\n"},
		{"root mount", "    mount: root\n"},
		{"mount collision", "  child:\n    clone:\n      remote: origin\n      url: https://example.test/child.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    parent: root\n    mount: .git\n    default_branch: main\n  sibling:\n    clone:\n      remote: origin\n      url: https://example.test/sibling.git\n    upstream:\n      branch: main\n      remote: origin\n      merge: refs/heads/main\n    identity:\n      initial_commits:\n        - bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n    parent: root\n    mount: .git\n    default_branch: main\n"},
		{"malformed merge ref", "      merge: main\n"},
		{"remote mismatch", "      remote: upstream\n"},
		{"invalid remote", "      remote: --upload-pack\n"},
		{"branch mismatch", "      branch: remote-main\n"},
		{"abbreviated commit", "        - 0123456\n"},
		{"empty commits", "      initial_commits: []\n"},
		{"unsorted commits", "        - ffffffffffffffffffffffffffffffffffffffff\n        - 0123456789abcdef0123456789abcdef01234567\n"},
		{"null commits", "      initial_commits: null\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validPortableManifest
			switch test.name {
			case "zero roots", "missing parent":
				input = strings.Replace(input, "    parent: \"\"\n", test.edit, 1)
			case "multiple roots", "cycle", "mount collision":
				input += test.edit
			case "unsafe repository id":
				input = strings.Replace(input, "  root:\n", test.edit, 1)
			case "unsafe child mount", "root mount":
				input = strings.Replace(input, "    mount: .\n", test.edit, 1)
			case "malformed merge ref", "remote mismatch":
				input = strings.Replace(input, "      merge: refs/heads/main\n", test.edit, 1)
			case "invalid remote":
				input = strings.Replace(input, "      remote: origin\n      merge", "      remote: --upload-pack\n      merge", 1)
			case "branch mismatch":
				input = strings.Replace(input, "      branch: main\n", test.edit, 1)
			case "abbreviated commit":
				input = strings.Replace(input, "        - 0123456789abcdef0123456789abcdef01234567\n", test.edit, 1)
			case "empty commits", "null commits":
				input = strings.Replace(input, "      initial_commits:\n        - 0123456789abcdef0123456789abcdef01234567\n", test.edit, 1)
			case "unsorted commits":
				input = strings.Replace(input, "        - 0123456789abcdef0123456789abcdef01234567\n", test.edit, 1)
			}
			if _, err := config.LoadPortableManifest([]byte(input)); err == nil {
				t.Fatal("LoadPortableManifest() error = nil")
			}
		})
	}
}

func TestValidateCloneURLClasses(t *testing.T) {
	for _, input := range []string{
		"https://example.test/org/repo.git", "http://example.test/org/repo.git", "HTTPS://example.test/org/repo.git", "hTtP://example.test/org/repo.git", "ssh://git@example.test/org/repo.git",
		"git@example.test:org/repo.git", "example.test:org/repo.git", "/srv/git/repo.git", "/srv/git/my repo.git", `C:\\git\\repo.git`, "file:///srv/git/repo.git",
	} {
		if err := config.ValidateCloneURL(input); err != nil {
			t.Errorf("ValidateCloneURL(%q) error = %v", input, err)
		}
	}
	for _, input := range []string{
		"https://token:secret@example.test/repo.git", "HTTPS://token:secret@example.test/repo.git", "hTtPs://example.test/repo?credentialcanary", "https://example.test/repo?ghp_credentialcanary", "https://example.test/repo?bearer_token_material", "https://example.test/repo?%67hp_credentialcanary", "https://example.test/repo?raw&download", "https://example.test/repo?auth=secret", "https://example.test/repo?sig=secret", "https://example.test/repo?X-Amz-Signature=secret", "https://example.test/repo?X-Goog-Signature=secret", "https://example.test/repo?download=1", "ftp://example.test/repo.git", "git://example.test/repo.git", "s3://bucket/repo.git", "relative/repo.git", "file:relative",
		"-c core.hooksPath=/tmp", "git@example.test:repo;touch /tmp/pwn", "/srv/repo&&touch", "example.test:repo&&touch", "https://example.test/repo\nsecret", "ssh://git:secret@example.test/repo.git",
	} {
		err := config.ValidateCloneURL(input)
		if err == nil {
			t.Errorf("ValidateCloneURL(%q) error = nil", input)
			continue
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "credentialcanary") || strings.Contains(err.Error(), "material") {
			t.Errorf("ValidateCloneURL(%q) leaked a credential: %v", input, err)
		}
	}
}

func TestHTTPValidatorsRejectLoneTrailingQueryDelimiter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(string) error
		want     string
	}{
		{
			name:     "clone URL",
			input:    "https://example.test/repo.git?",
			validate: config.ValidateCloneURL,
			want:     "invalid clone URL",
		},
		{
			name:     "manifest source",
			input:    "https://example.test/project.wtree.yml?",
			validate: config.ValidateManifestSource,
			want:     "invalid manifest source",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate(test.input)
			if err == nil {
				t.Fatalf("validation of %q returned nil", test.input)
			}
			if err.Error() != test.want {
				t.Fatalf("validation error = %q, want generic %q", err, test.want)
			}
			if strings.Contains(err.Error(), test.input) {
				t.Fatalf("validation error leaked input %q: %v", test.input, err)
			}
		})
	}
}

func TestHTTPValidatorsRedactMixedCaseCredentials(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(string) error
		want     string
	}{
		{
			name:     "clone URL userinfo",
			input:    "hTtPs://token:secret@example.test/repo.git",
			validate: config.ValidateCloneURL,
			want:     "invalid clone URL",
		},
		{
			name:     "clone URL query",
			input:    "hTtPs://example.test/repo.git?credentialcanary",
			validate: config.ValidateCloneURL,
			want:     "invalid clone URL",
		},
		{
			name:     "manifest source userinfo",
			input:    "hTtPs://token:secret@example.test/project.wtree.yml",
			validate: config.ValidateManifestSource,
			want:     "invalid manifest source",
		},
		{
			name:     "manifest source query",
			input:    "hTtPs://example.test/project.wtree.yml?credentialcanary",
			validate: config.ValidateManifestSource,
			want:     "invalid manifest source",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate(test.input)
			if err == nil {
				t.Fatalf("validation of %q returned nil", test.input)
			}
			if err.Error() != test.want {
				t.Fatalf("validation error = %q, want generic %q", err, test.want)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "credentialcanary") {
				t.Fatalf("validation error leaked input %q: %v", test.input, err)
			}
		})
	}
}

func TestMarshalPortableManifestSortsInitialCommits(t *testing.T) {
	manifest, err := config.LoadPortableManifest([]byte(validPortableManifest))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Repositories["root"] = config.PortableRepository{
		Clone:         manifest.Repositories["root"].Clone,
		Upstream:      manifest.Repositories["root"].Upstream,
		Identity:      config.RepositoryIdentity{InitialCommits: []string{"ffffffffffffffffffffffffffffffffffffffff", "0123456789abcdef0123456789abcdef01234567"}},
		Parent:        "",
		Mount:         ".",
		DefaultBranch: "main",
	}
	encoded, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatalf("MarshalPortableManifest() error = %v", err)
	}
	if strings.Index(string(encoded), "0123456789abcdef0123456789abcdef01234567") > strings.Index(string(encoded), "ffffffffffffffffffffffffffffffffffffffff") {
		t.Fatalf("initial commits are not sorted:\n%s", encoded)
	}
}

func TestLocalProjectManifestCompatibility(t *testing.T) {
	old := []byte("version: 1\nproject:\n  id: p1\n  name: product\nrepositories:\n  root:\n    source: .\n    mount: .\n")
	loaded, err := config.LoadProject(old)
	if err != nil {
		t.Fatalf("LoadProject(old) error = %v", err)
	}
	if loaded.Manifest.Path != "" || loaded.Manifest.Source != "" {
		t.Fatalf("old config gained manifest metadata: %#v", loaded.Manifest)
	}
	if string(old) != "version: 1\nproject:\n  id: p1\n  name: product\nrepositories:\n  root:\n    source: .\n    mount: .\n" {
		t.Fatal("LoadProject rewrote its input")
	}
	path := filepath.Join(t.TempDir(), ".wtree.yml")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadProjectFile(path); err != nil {
		t.Fatalf("ReadProjectFile(old) error = %v", err)
	}
	readBack, err := os.ReadFile(path)
	if err != nil || string(readBack) != string(old) {
		t.Fatalf("ReadProjectFile changed old v1 data: %q, %v", readBack, err)
	}
	loaded.Manifest = config.ManifestMetadata{Path: "project.wtree.yml", Source: "/projects/product/project.wtree.yml"}
	encoded, err := config.MarshalProject(loaded)
	if err != nil {
		t.Fatalf("MarshalProject() error = %v", err)
	}
	roundTrip, err := config.LoadProject(encoded)
	if err != nil || roundTrip.Manifest != loaded.Manifest || roundTrip.Version != config.Version {
		t.Fatalf("manifest metadata round trip = %#v, %v", roundTrip.Manifest, err)
	}
	if _, err := config.LoadProject([]byte("version: 1\nmanifest:\n  path: project.wtree.yml\n  source: https://token:secret@example.test/project.wtree.yml\n")); err == nil {
		t.Fatal("LoadProject() accepted credential-bearing manifest metadata")
	}
	if _, err := config.LoadProject([]byte("version: 2\n")); err == nil {
		t.Fatal("LoadProject() accepted newer local version")
	}
	if _, err := config.LoadProject([]byte("version: 1\nmanifest:\n  path: alternate.yml\n  source: /projects/product/project.wtree.yml\n")); err == nil {
		t.Fatal("LoadProject() accepted an alternate manifest path")
	}
	plain, err := config.MarshalProject(config.ProjectConfig{Version: config.Version, Project: config.Project{ID: "p1", Name: "product"}, Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: "."}}})
	if err != nil || strings.Contains(string(plain), "manifest:") {
		t.Fatalf("old v1 serialization unexpectedly contains manifest metadata: %s, %v", plain, err)
	}
}

func TestValidateManifestSource(t *testing.T) {
	for _, source := range []string{"/projects/acme/project.wtree.yml", "https://example.test/acme/project.wtree.yml", "HTTPS://example.test/acme/project.wtree.yml", "hTtP://example.test/acme/project.wtree.yml"} {
		if err := config.ValidateManifestSource(source); err != nil {
			t.Errorf("ValidateManifestSource(%q) error = %v", source, err)
		}
	}
	for _, source := range []string{"relative/project.wtree.yml", "file:///project.wtree.yml", "HTTPS://token:secret@example.test/project.wtree.yml", "hTtPs://example.test/project.wtree.yml?credentialcanary", "https://token:secret@example.test/project.wtree.yml", "https://example.test/project.wtree.yml?ghp_credentialcanary", "https://example.test/project.wtree.yml?bearer_token_material", "https://example.test/project.wtree.yml?%67hp_credentialcanary", "https://example.test/project.wtree.yml?raw&download", "https://example.test/project.wtree.yml?auth=secret", "https://example.test/project.wtree.yml?sig=secret", "https://example.test/project.wtree.yml?X-Amz-Signature=secret", "https://example.test/project.wtree.yml?X-Goog-Signature=secret", "https://example.test/project.wtree.yml?download=1", "https://example.test/project.wtree.yml\nsecret"} {
		err := config.ValidateManifestSource(source)
		if err == nil {
			t.Errorf("ValidateManifestSource(%q) error = nil", source)
		} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "credentialcanary") || strings.Contains(err.Error(), "material") {
			t.Errorf("ValidateManifestSource(%q) leaked a credential: %v", source, err)
		}
	}
}

func TestPortableManifestRejectsNonPortableMountLiterals(t *testing.T) {
	if err := config.ValidatePortableMount("with space/απि", false); err != nil {
		t.Fatalf("ValidatePortableMount() rejected a safe portable mount: %v", err)
	}
	for _, mount := range []string{"services//api", "services/./api", "services/../api", `services\api`, "services:api", "services/api?", "con", "NUL.txt", "lPt9.log", "service./api", "service /api"} {
		input := validPortableManifest + `  child:
    clone:
      remote: origin
      url: https://example.test/child.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    parent: root
    mount: ` + mount + `
    default_branch: main
`
		if _, err := config.LoadPortableManifest([]byte(input)); err == nil {
			t.Errorf("LoadPortableManifest() accepted non-portable mount %q", mount)
		}
	}
}

func TestValidateBranchNameMatchesPortableGitRefRules(t *testing.T) {
	for _, branch := range []string{"main", "topic/name", "release-1.0"} {
		if err := config.ValidateBranchName(branch); err != nil {
			t.Errorf("ValidateBranchName(%q) error = %v", branch, err)
		}
	}
	for _, branch := range []string{".hidden", "topic//name", "topic/.hidden", "topic/name.lock", "@"} {
		if err := config.ValidateBranchName(branch); err == nil {
			t.Errorf("ValidateBranchName(%q) error = nil", branch)
		}
	}
}
