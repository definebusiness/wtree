package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/pathutil"
)

const validPortableManifest = `version: 2
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme-shop
  base_repository: root
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
	input := strings.Replace(strings.Replace(validPortableManifest, "  base_repository: root\n", "  base_repository: zoo\n", 1), "  root:\n", "  zoo:\n", 1) + `  alpha:
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
	if !strings.HasPrefix(string(one), "version: 2\n") {
		t.Fatalf("manifest version is not canonical YAML:\n%s", one)
	}
	if strings.Index(string(one), "  alpha:\n") > strings.Index(string(one), "  zoo:\n") {
		t.Fatalf("repository map is not lexical:\n%s", one)
	}
	if _, err := config.LoadPortableManifest(append(one, []byte("---\nversion: 1\n")...)); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
	for _, input := range []string{
		"version: 3\n", strings.Replace(validPortableManifest, "version: 2", "version: 2\nunknown: true", 1),
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
	if config.PortableManifestVersion != 2 {
		t.Fatalf("PortableManifestVersion = %d, want 2", config.PortableManifestVersion)
	}
	manifest, err := config.LoadPortableManifest([]byte(validPortableManifest))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != config.PortableManifestVersion {
		t.Fatalf("portable manifest version = %d, want PortableManifestVersion %d", manifest.Version, config.PortableManifestVersion)
	}
}

func TestPortableProjectJSONUsesBaseRepositoryWireName(t *testing.T) {
	encoded, err := json.Marshal(config.PortableProject{ID: "project", Name: "Project", BaseRepository: "root"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"project","name":"Project","base_repository":"root"}`
	if string(encoded) != want {
		t.Fatalf("PortableProject JSON = %s, want %s", encoded, want)
	}
}

func TestPortableManifestV2CanonicalFixturePreservesExactBytes(t *testing.T) {
	const fixture = `version: 2
project:
    id: acme-shop
    name: Acme Shop
    base_repository: root
repositories:
    api:
        clone:
            remote: origin
            url: https://example.test/acme/api.git
        upstream:
            branch: main
            remote: origin
            merge: refs/heads/main
        identity:
            initial_commits:
                - 0123456789abcdef0123456789abcdef01234567
                - ffffffffffffffffffffffffffffffffffffffff
        parent: root
        mount: services/api
        default_branch: main
    root:
        clone:
            remote: origin
            url: https://example.test/acme/root.git
        upstream:
            branch: main
            remote: origin
            merge: refs/heads/main
        identity:
            initial_commits:
                - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        parent: ""
        mount: .
        default_branch: main
`
	manifest, err := config.LoadPortableManifest([]byte(fixture))
	if err != nil {
		t.Fatalf("LoadPortableManifest() error = %v", err)
	}
	if manifest.Project.BaseRepository != "root" {
		t.Fatalf("base repository = %q, want root", manifest.Project.BaseRepository)
	}
	first, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatalf("first MarshalPortableManifest() error = %v", err)
	}
	second, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatalf("second MarshalPortableManifest() error = %v", err)
	}
	if string(first) != fixture || string(second) != fixture {
		t.Fatalf("canonical portable manifest bytes =\n%s\nsecond marshal =\n%s\nwant fixture =\n%s", first, second, fixture)
	}
}

func TestPortableManifestV2CanonicalizesInitialCommitOrder(t *testing.T) {
	manifest, err := config.LoadPortableManifest([]byte(validPortableManifest))
	if err != nil {
		t.Fatalf("LoadPortableManifest() error = %v", err)
	}
	root := manifest.Repositories["root"]
	manifest.Repositories["root"] = config.PortableRepository{
		Clone:         root.Clone,
		Upstream:      root.Upstream,
		Identity:      config.RepositoryIdentity{InitialCommits: []string{"ffffffffffffffffffffffffffffffffffffffff", "0123456789abcdef0123456789abcdef01234567"}},
		Parent:        root.Parent,
		Mount:         root.Mount,
		DefaultBranch: root.DefaultBranch,
	}

	encoded, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatalf("MarshalPortableManifest() error = %v", err)
	}
	decoded, err := config.LoadPortableManifest(encoded)
	if err != nil {
		t.Fatalf("LoadPortableManifest() of canonical bytes error = %v", err)
	}
	if got, want := decoded.Repositories["root"].Identity.InitialCommits, []string{"0123456789abcdef0123456789abcdef01234567", "ffffffffffffffffffffffffffffffffffffffff"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("canonical initial commits = %q, want %q", got, want)
	}
}

func TestPortableManifestV2AcceptsForestAndCanonicalizesRepositoryMapOrder(t *testing.T) {
	forest := strings.Replace(validPortableManifest, "    mount: .\n", "    mount: api\n", 1) + `  client:
    clone:
      remote: origin
      url: https://example.test/acme/client.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    parent: ""
    mount: web
    default_branch: main
  api:
    clone:
      remote: origin
      url: https://example.test/acme/api.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - cccccccccccccccccccccccccccccccccccccccc
    parent: root
    mount: grouped/api
    default_branch: main
`
	manifest, err := config.LoadPortableManifest([]byte(forest))
	if err != nil {
		t.Fatalf("LoadPortableManifest() error = %v", err)
	}
	if manifest.Project.BaseRepository != "root" || manifest.Repositories["client"].Parent != "" {
		t.Fatalf("forest topology = %#v", manifest)
	}
	first, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	reordered := manifest
	reordered.Repositories = map[string]config.PortableRepository{
		"root": manifest.Repositories["root"], "api": manifest.Repositories["api"], "client": manifest.Repositories["client"],
	}
	second, err := config.MarshalPortableManifest(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical bytes changed with repository map order:\n%s\n---\n%s", first, second)
	}
	if decoded, err := config.LoadPortableManifest(first); err != nil || decoded.Repositories["root"].Mount != "api" || decoded.Repositories["client"].Mount != "web" {
		t.Fatalf("canonical forest round-trip = %#v, %v; want api/web top-level mounts", decoded, err)
	}
}

func TestPortableManifestV2RejectsGitAdministrationMounts(t *testing.T) {
	for _, mount := range []string{".git", "services/.git/hooks", ".git/project"} {
		t.Run(mount, func(t *testing.T) {
			input := strings.Replace(validPortableManifest, "    mount: .\n", "    mount: "+mount+"\n", 1)
			if _, err := config.LoadPortableManifest([]byte(input)); err == nil || !strings.Contains(err.Error(), "Git administration") {
				t.Fatalf("LoadPortableManifest() error = %v, want Git administration rejection", err)
			}
		})
	}
}

func TestPortableManifestV2RejectsCaseFoldedMountAliasesDeterministically(t *testing.T) {
	base, err := config.LoadPortableManifest([]byte(validPortableManifest))
	if err != nil {
		t.Fatal(err)
	}
	root := base.Repositories["root"]
	for _, test := range []struct {
		name  string
		build func(bool) map[string]config.PortableRepository
		want  string
	}{
		{
			name: "top-level aliases",
			build: func(reverse bool) map[string]config.PortableRepository {
				first, second := root, root
				first.Mount, second.Mount = "api", "API"
				if reverse {
					return map[string]config.PortableRepository{"other": second, "root": first}
				}
				return map[string]config.PortableRepository{"root": first, "other": second}
			},
			want: `repository mount "other" conflicts with "root"`,
		},
		{
			name: "same-parent child aliases",
			build: func(reverse bool) map[string]config.PortableRepository {
				parent, first, second := root, root, root
				parent.Mount = "project"
				first.Parent, first.Mount = "root", "api"
				second.Parent, second.Mount = "root", "API"
				if reverse {
					return map[string]config.PortableRepository{"child-b": second, "root": parent, "child-a": first}
				}
				return map[string]config.PortableRepository{"root": parent, "child-a": first, "child-b": second}
			},
			want: `repository mount "child-a" conflicts with "child-b"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				manifest := base
				manifest.Repositories = test.build(reverse)
				if err := manifest.Validate(); err == nil || err.Error() != test.want {
					t.Fatalf("Validate() error = %v, want %q", err, test.want)
				}
			}
		})
	}
}

func TestPortableManifestV2RejectsInvalidBaseTopologyWithoutMutation(t *testing.T) {
	const child = `  child:
    clone:
      remote: origin
      url: https://example.test/acme/child.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    parent: root
    mount: services/child
    default_branch: main
`
	const otherRoot = `  other:
    clone:
      remote: origin
      url: https://example.test/acme/other.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    parent: ""
    mount: .
    default_branch: main
`
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "version 1",
			input: strings.Replace(validPortableManifest, "version: 2", "version: 1", 1),
			want:  "logical-root manifest format version 2 is required",
		},
		{
			name:  "version 3",
			input: strings.Replace(validPortableManifest, "version: 2", "version: 3", 1),
			want:  "logical-root manifest format version 2 is required",
		},
		{
			name:  "missing base",
			input: strings.Replace(validPortableManifest, "  base_repository: root\n", "", 1),
			want:  "project base repository",
		},
		{
			name:  "invalid base ID",
			input: strings.Replace(validPortableManifest, "base_repository: root", "base_repository: ../root", 1),
			want:  "project base repository",
		},
		{
			name:  "unknown base",
			input: strings.Replace(validPortableManifest, "base_repository: root", "base_repository: unknown", 1),
			want:  "is not declared",
		},
		{
			name:  "non-root base",
			input: strings.Replace(validPortableManifest, "base_repository: root", "base_repository: child", 1) + child,
			want:  "must be top-level",
		},
		{
			name:  "multiple roots",
			input: validPortableManifest + otherRoot,
			want:  "sole top-level",
		},
		{
			name:  "single component root mount",
			input: strings.Replace(validPortableManifest, "    mount: .\n", "    mount: project\n", 1),
			want:  "",
		},
		{
			name:  "dot plus single-component top-level sibling",
			input: validPortableManifest + strings.Replace(otherRoot, "    mount: .\n", "    mount: api\n", 1),
			want:  "sole top-level",
		},
		{
			name:  "dot plus grouped top-level sibling",
			input: validPortableManifest + strings.Replace(otherRoot, "    mount: .\n", "    mount: services/peer\n", 1),
			want:  "sole top-level",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.input)
			before := append([]byte(nil), input...)
			_, err := config.LoadPortableManifest(input)
			if test.want == "" {
				if err != nil {
					t.Fatalf("LoadPortableManifest() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("LoadPortableManifest() error = nil")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadPortableManifest() error = %q, want substring %q", err, test.want)
			}
			if string(input) != string(before) {
				t.Fatal("LoadPortableManifest() mutated its input")
			}
		})
	}
}

func TestPortableManifestV2NestedRepositoriesRequireImmediateParentRelativeMounts(t *testing.T) {
	const nested = `  child:
    clone:
      remote: origin
      url: https://example.test/acme/child.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    parent: root
    mount: services
    default_branch: main
  api:
    clone:
      remote: origin
      url: https://example.test/acme/api.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    parent: child
    mount: api
    default_branch: main
`
	valid := validPortableManifest + nested
	if _, err := config.LoadPortableManifest([]byte(valid)); err != nil {
		t.Fatalf("LoadPortableManifest() rejected valid immediate-parent nesting: %v", err)
	}
	for _, test := range []struct {
		name  string
		input string
	}{
		{"unknown immediate parent", strings.Replace(valid, "    parent: child\n", "    parent: missing\n", 1)},
		{"parent-relative mount escapes", strings.Replace(valid, "    mount: api\n", "    mount: ../api\n", 1)},
		{"parent-relative mount is absolute", strings.Replace(valid, "    mount: api\n", "    mount: /api\n", 1)},
		{"parent-relative mount is dot", strings.Replace(valid, "    mount: api\n", "    mount: .\n", 1)},
		{"cycle", strings.Replace(valid, "    parent: root\n    mount: services\n", "    parent: api\n    mount: services\n", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := config.LoadPortableManifest([]byte(test.input)); err == nil {
				t.Fatal("LoadPortableManifest() error = nil")
			}
		})
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
			case "unsafe child mount":
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

func TestLocalProjectConfigRequiresV2AndRoundTripsRequiredManifest(t *testing.T) {
	old := []byte("version: 1\nproject:\n  id: p1\n  name: product\nrepositories:\n  root:\n    source: .\n    mount: .\n")
	if _, err := config.LoadProject(old); err == nil || !strings.Contains(err.Error(), "reinitialization is required") {
		t.Fatalf("LoadProject(v1) error = %v, want reinitialization diagnostic", err)
	}
	path := filepath.Join(t.TempDir(), ".wtree.yml")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadProjectFile(path); err == nil || !strings.Contains(err.Error(), "reinitialization is required") {
		t.Fatalf("ReadProjectFile(v1) error = %v, want reinitialization diagnostic", err)
	}
	readBack, err := os.ReadFile(path)
	if err != nil || string(readBack) != string(old) {
		t.Fatalf("ReadProjectFile changed rejected v1 data: %q, %v", readBack, err)
	}
	loaded := config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "p1", Name: "product", BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: ".", DefaultBranch: "main"}}, Worktrees: config.Worktrees{Root: "/worktrees"}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: "/projects/product/project.wtree.yml"}}
	encoded, err := config.MarshalProject(loaded)
	if err != nil {
		t.Fatalf("MarshalProject() error = %v", err)
	}
	roundTrip, err := config.LoadProject(encoded)
	if err != nil || roundTrip.Manifest != loaded.Manifest || roundTrip.Version != config.ProjectConfigVersion {
		t.Fatalf("manifest metadata round trip = %#v, %v", roundTrip.Manifest, err)
	}
	if _, err := config.LoadProject([]byte("version: 2\n")); err == nil {
		t.Fatal("LoadProject() accepted incomplete local v2")
	}
	badManifest := loaded
	badManifest.Manifest.Path = "alternate.yml"
	if _, err := config.MarshalProject(badManifest); err == nil {
		t.Fatal("MarshalProject() accepted an alternate manifest path")
	}
	if !strings.Contains(string(encoded), "version: 2") || !strings.Contains(string(encoded), "logical_root: .") || !strings.Contains(string(encoded), "base_repository: root") {
		t.Fatalf("local v2 serialization omitted topology: %s", encoded)
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
	if err := config.ValidatePortableMount("with space/απि", pathutil.ChildMount); err != nil {
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
