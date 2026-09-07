package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
)

const localV3Hooks = `version: 3
project:
  id: hook-project
  name: Hook Project
  base_repository: root
logical_root: .
repositories:
  root:
    source: .
    parent: ""
    mount: .
    default_branch: main
  api:
    source: api
    parent: root
    mount: api
    default_branch: main
worktrees:
  root: /tmp/worktrees
discovery:
  ignore: []
manifest:
  path: project.wtree.yml
  source: https://example.test/project.wtree.yml
hooks:
  post-create:
    - id: base
      command: [setup]
    - id: api
      repository: api
      command: [.wtree-hooks/setup, --fast]
      timeout: 2m
`

const portableV3Hooks = `version: 3
project:
  id: hook-project
  name: Hook Project
  base_repository: root
repositories:
  root:
    clone:
      remote: origin
      url: https://example.test/project.git
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
  api:
    clone:
      remote: origin
      url: https://example.test/api.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    parent: root
    mount: api
    default_branch: main
hooks:
  post-clone:
    - id: clone-root
      command: [setup]
shared_hooks:
  post-create:
    - id: shared-api
      repository: api
      command: [.wtree-hooks/setup]
      timeout: 90s
`

func TestHookV3SchemasDefaultCompareAndMarshal(t *testing.T) {
	local, err := config.LoadProject([]byte(localV3Hooks))
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	if local.Version != config.ProjectConfigVersion3 {
		t.Fatalf("local version = %d", local.Version)
	}
	if got := local.Hooks["post-create"][0]; got.Repository != "" || got.Timeout != 0 {
		t.Fatalf("decoded defaults must remain represented as omitted: %#v", got)
	}
	canonical, err := config.CanonicalHookEvent("post-create", local.Hooks["post-create"], local.Project.BaseRepository)
	if err != nil {
		t.Fatal(err)
	}
	if canonical[0].Repository != "root" || canonical[0].Timeout != time.Minute {
		t.Fatalf("canonical defaults = %#v", canonical[0])
	}
	encoded, err := config.MarshalProject(local)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "repository: root") || !strings.Contains(string(encoded), "timeout: 1m0s") {
		t.Fatalf("local canonical encoding did not materialize defaults:\n%s", encoded)
	}
	decoded, err := config.LoadProject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if equal, err := config.HookEventsEqual("post-create", local.Hooks["post-create"], decoded.Hooks["post-create"], "root"); err != nil || !equal {
		t.Fatalf("local hook round trip equality = %v, %v", equal, err)
	}

	portable, err := config.LoadPortableManifest([]byte(portableV3Hooks))
	if err != nil {
		t.Fatalf("LoadPortableManifest() error = %v", err)
	}
	encoded, err = config.MarshalPortableManifest(portable)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "hooks:\n") || !strings.Contains(string(encoded), "shared_hooks:\n") {
		t.Fatalf("v3 hooks absent from canonical encoding:\n%s", encoded)
	}
	if _, err := config.LoadPortableManifest(encoded); err != nil {
		t.Fatalf("portable v3 round trip: %v", err)
	}
}

func TestHookV2StrictAndV3Rejections(t *testing.T) {
	localV2 := strings.Replace(localV3Hooks, "version: 3", "version: 2", 1)
	portableV2 := strings.Replace(portableV3Hooks, "version: 3", "version: 2", 1)
	for name, input := range map[string]string{
		"local v2 hooks":         localV2,
		"local reserved":         strings.Replace(localV3Hooks, "post-create:", "post-update:", 1),
		"local wrong source":     strings.Replace(localV3Hooks, "post-create:", "post-clone:", 1),
		"local empty list":       strings.Replace(localV3Hooks, "    - id: base\n      command: [setup]\n    - id: api\n      repository: api\n      command: [.wtree-hooks/setup, --fast]\n      timeout: 2m", "[]", 1),
		"local duplicate id":     strings.Replace(localV3Hooks, "id: api", "id: base", 1),
		"local unknown repo":     strings.Replace(localV3Hooks, "repository: api", "repository: missing", 1),
		"local empty executable": strings.Replace(localV3Hooks, "command: [setup]", "command: ['']", 1),
		"local timeout":          strings.Replace(localV3Hooks, "timeout: 2m", "timeout: 25h", 1),
		"local zero timeout":     strings.Replace(localV3Hooks, "timeout: 2m", "timeout: 0s", 1),
	} {
		if _, err := config.LoadProject([]byte(input)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, input := range map[string]string{
		"portable v2 hooks":            portableV2,
		"portable wrong source":        strings.Replace(portableV3Hooks, "post-clone:", "post-create:", 1),
		"portable shared wrong source": strings.Replace(portableV3Hooks, "shared_hooks:\n  post-create:", "shared_hooks:\n  post-clone:", 1),
		"portable absolute":            strings.Replace(portableV3Hooks, "command: [setup]", "command: [/usr/bin/setup]", 1),
		"portable home":                strings.Replace(portableV3Hooks, "command: [setup]", "command: [~/setup]", 1),
		"portable URL":                 strings.Replace(portableV3Hooks, "command: [setup]", "command: [file:///tmp/setup]", 1),
		"portable control":             strings.Replace(portableV3Hooks, "command: [setup]", "command: [\"setup\\nnext\"]", 1),
	} {
		if _, err := config.LoadPortableManifest([]byte(input)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPostReleaseIsLocalV3Only(t *testing.T) {
	local := strings.Replace(localV3Hooks, "post-create:", "post-release:", 1)
	value, err := config.LoadProject([]byte(local))
	if err != nil || len(value.Hooks[config.HookEventPostRelease]) != 2 {
		t.Fatalf("local v3 post-release = %#v, %v", value.Hooks, err)
	}
	for name, input := range map[string]string{
		"local v2": strings.Replace(local, "version: 3", "version: 2", 1),
		"portable": strings.Replace(portableV3Hooks, "post-clone:", "post-release:", 1),
		"shared":   strings.Replace(portableV3Hooks, "shared_hooks:\n  post-create:", "shared_hooks:\n  post-release:", 1),
	} {
		if _, err := config.LoadProject([]byte(input)); name == "local v2" && err == nil {
			t.Errorf("%s accepted", name)
		} else if name != "local v2" {
			if _, portableErr := config.LoadPortableManifest([]byte(input)); portableErr == nil {
				t.Errorf("%s accepted", name)
			}
		}
	}
}

func TestLifecycleHookPublicContractMatrix(t *testing.T) {
	localV2 := strings.Split(localV3Hooks, "hooks:\n")[0]
	localV2 = strings.Replace(localV2, "version: 3", "version: 2", 1)
	portableV2 := strings.Split(portableV3Hooks, "hooks:\n")[0]
	portableV2 = strings.Replace(portableV2, "version: 3", "version: 2", 1)

	for _, test := range []struct {
		name     string
		local    bool
		input    string
		accepted bool
		event    string
		expected string
	}{
		{name: "local-v2-hook-free", local: true, input: localV2, accepted: true},
		{name: "portable-v2-hook-free", input: portableV2, accepted: true},
		{name: "local-v3-post-create", local: true, input: localV3Hooks, accepted: true, event: config.HookEventPostCreate, expected: "base"},
		{name: "local-v3-post-clone-rejected", local: true, input: strings.Replace(localV3Hooks, "post-create:", "post-clone:", 1)},
		{name: "portable-v3-post-clone", input: strings.Replace(portableV3Hooks, "shared_hooks:\n  post-create:\n    - id: shared-api\n      repository: api\n      command: [.wtree-hooks/setup]\n      timeout: 90s\n", "", 1), accepted: true, event: config.HookEventPostClone, expected: "clone-root"},
		{name: "portable-v3-shared-post-create", input: strings.Replace(portableV3Hooks, "hooks:\n  post-clone:\n    - id: clone-root\n      command: [setup]\n", "", 1), accepted: true, event: config.HookEventPostCreate, expected: "shared-api"},
		{name: "portable-v3-both-sources", input: portableV3Hooks, accepted: true, event: config.HookEventPostClone, expected: "clone-root"},
		{name: "portable-v3-direct-post-create-rejected", input: strings.Replace(portableV3Hooks, "hooks:\n  post-clone:", "hooks:\n  post-create:", 1)},
		{name: "portable-v3-shared-post-clone-rejected", input: strings.Replace(portableV3Hooks, "shared_hooks:\n  post-create:", "shared_hooks:\n  post-clone:", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.local {
				value, err := config.LoadProject([]byte(test.input))
				if (err == nil) != test.accepted {
					t.Fatalf("LoadProject() error = %v, accepted=%t", err, test.accepted)
				}
				if err == nil && test.event != "" && (len(value.Hooks[test.event]) == 0 || value.Hooks[test.event][0].ID != test.expected) {
					t.Fatalf("local decoded event %q = %#v", test.event, value.Hooks)
				}
				return
			}
			value, err := config.LoadPortableManifest([]byte(test.input))
			if (err == nil) != test.accepted {
				t.Fatalf("LoadPortableManifest() error = %v, accepted=%t", err, test.accepted)
			}
			if err == nil && test.event != "" {
				hooks := value.Hooks[test.event]
				if test.event == config.HookEventPostCreate {
					hooks = value.SharedHooks[test.event]
				}
				if len(hooks) == 0 || hooks[0].ID != test.expected {
					t.Fatalf("portable decoded event %q = %#v %#v", test.event, value.Hooks, value.SharedHooks)
				}
			}
		})
	}
}

func TestHookCanonicalEqualityOrderAndNoMutation(t *testing.T) {
	hooks := []config.Hook{{ID: "first", Command: []string{"setup"}}, {ID: "second", Repository: "api", Command: []string{"tool", "--flag"}, Timeout: 2 * time.Minute}}
	before := append([]config.Hook(nil), hooks...)
	before[0].Command = append([]string(nil), hooks[0].Command...)
	before[1].Command = append([]string(nil), hooks[1].Command...)
	canonical, err := config.CanonicalHookEvent("post-create", hooks, "root")
	if err != nil {
		t.Fatal(err)
	}
	if canonical[0].Repository != "root" || canonical[0].Timeout != time.Minute {
		t.Fatal("defaults were not applied")
	}
	if hooks[0].Repository != before[0].Repository || hooks[0].Timeout != before[0].Timeout || strings.Join(hooks[1].Command, ",") != strings.Join(before[1].Command, ",") {
		t.Fatal("canonicalization mutated caller input")
	}
	explicit := []config.Hook{{ID: "first", Repository: "root", Command: []string{"setup"}, Timeout: time.Minute}, hooks[1]}
	if equal, err := config.HookEventsEqual("post-create", hooks, explicit, "root"); err != nil || !equal {
		t.Fatalf("defaults equality = %v, %v", equal, err)
	}
	if equal, err := config.HookEventsEqual("post-create", hooks, []config.Hook{hooks[1], hooks[0]}, "root"); err != nil || equal {
		t.Fatalf("order equality = %v, %v", equal, err)
	}
	if equal, err := config.HookEventsEqual("post-clone", []config.Hook{{ID: "clone", Command: []string{"setup"}}}, []config.Hook{{ID: "clone", Command: []string{"setup"}}}, "root"); err != nil || !equal {
		t.Fatalf("portable event equality = %v, %v", equal, err)
	}
	presented := strings.Replace(localV3Hooks, "    - id: base\n      command: [setup]", "    - command: [setup]\n      id: base", 1)
	original, err := config.LoadProject([]byte(localV3Hooks))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := config.LoadProject([]byte(presented))
	if err != nil {
		t.Fatal(err)
	}
	if equal, err := config.HookEventsEqual("post-create", original.Hooks["post-create"], decoded.Hooks["post-create"], "root"); err != nil || !equal {
		t.Fatalf("YAML presentation equality = %v, %v", equal, err)
	}
}

func TestHookV2MarshalRejectsHooksWithoutBroadeningLegacyValidation(t *testing.T) {
	v2Input := strings.Replace(strings.Split(localV3Hooks, "hooks:\n")[0], "version: 3", "version: 2", 1)
	value, err := config.LoadProject([]byte(v2Input))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := config.MarshalProject(value)
	if err != nil {
		t.Fatal(err)
	}
	value.Hooks = config.HookEvents{"post-create": {{ID: "setup", Command: []string{"setup"}}}}
	if _, err := config.MarshalProject(value); err == nil {
		t.Fatal("MarshalProject() accepted v2 hooks that its decoder rejects")
	}
	value.Hooks = nil
	value.Project.BaseRepository = "prospective"
	prospective, err := config.MarshalProject(value)
	if err != nil {
		t.Fatalf("MarshalProject() rejected legacy prospective v2 topology: %v", err)
	}
	if string(baseline) == string(prospective) {
		t.Fatal("legacy prospective marshal did not preserve its input")
	}
}

func TestPortableHookCommandSyntaxIsElementAwareAndCrossPlatform(t *testing.T) {
	for name, command := range map[string]string{
		"unix executable":                 "[.wtree-hooks/setup, https://example.test/api, ../literal]",
		"windows executable":              "[.wtree-hooks\\setup.exe, https://example.test/api, ../literal]",
		"benign malformed URL":            "[setup, https://example.test/%zz]",
		"URL query at sign":               `[setup, "https://example.test?email=a@example.test"]`,
		"URL fragment at sign":            `[setup, "https://example.test#contact=a@example.test"]`,
		"malformed URL query at sign":     `[setup, "https://example.test?email=%zz@example.test"]`,
		"malformed URL fragment at sign":  `[setup, "https://example.test#contact=%zz@example.test"]`,
		"windows volume literal argument": "[setup, C:setup.exe, C:tools\\setup.exe]",
	} {
		t.Run(name, func(t *testing.T) {
			input := strings.Replace(portableV3Hooks, "command: [setup]", "command: "+command, 1)
			if _, err := config.LoadPortableManifest([]byte(input)); err != nil {
				t.Fatalf("LoadPortableManifest() error = %v", err)
			}
		})
	}
	for name, command := range map[string]string{
		"executable escape unix":         "[../setup]",
		"executable escape windows":      "[..\\setup.exe]",
		"executable absolute unix":       "[/usr/bin/setup]",
		"executable absolute windows":    "[C:\\tools\\setup.exe]",
		"executable drive relative":      "[C:setup.exe]",
		"executable drive relative path": "[C:tools\\setup.exe]",
		"executable home":                "[~/setup]",
		"executable tilde user unix":     "[~alice/setup]",
		"executable tilde user windows":  "[~alice\\setup.exe]",
		"literal absolute":               "[setup, /tmp/secret]",
		"literal home":                   "[setup, ~/secret]",
		"literal file URL":               "[setup, file:///tmp/secret]",
		"literal URL userinfo":           "[setup, https://token:secret@example.test/api]",
		"network URL userinfo":           "[setup, //token:secret@example.test/resource]",
		"malformed URL userinfo":         "[setup, https://token:secret@example.test/%zz]",
		"malformed file URL":             "[setup, file:///%zz]",
		"literal tab control":            "[\"setup\", \"bad\\targument\"]",
		"literal escape control":         "[\"setup\", \"bad\\u001bargument\"]",
	} {
		t.Run(name, func(t *testing.T) {
			input := strings.Replace(portableV3Hooks, "command: [setup]", "command: "+command, 1)
			_, err := config.LoadPortableManifest([]byte(input))
			if err == nil {
				t.Fatal("LoadPortableManifest() error = nil")
			}
			for _, literal := range []string{"token:secret", "C:setup.exe", `C:tools\setup.exe`} {
				if strings.Contains(command, literal) && strings.Contains(err.Error(), literal) {
					t.Fatalf("LoadPortableManifest() error leaks command literal: %v", err)
				}
			}
		})
	}
}
