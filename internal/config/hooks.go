package config

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// ProjectConfigVersion3 is the hook-capable local configuration schema.
	// ProjectConfigVersion intentionally remains v2 because it is the default
	// emitted by init and clone when no local-only hook data exists.
	ProjectConfigVersion3 = 3
	// PortableManifestVersion3 is the hook-capable portable manifest schema.
	// PortableManifestVersion intentionally remains v2 for existing writers.
	PortableManifestVersion3 = 3

	HookEventPostCreate = "post-create"
	HookEventPostClone  = "post-clone"
	// HookEventPostRelease is a trusted, local-only action that runs after a
	// release lock has been published. It is intentionally not portable.
	HookEventPostRelease = "post-release"
	HookDefaultTimeout   = time.Minute
	HookMaximumTimeout   = 24 * time.Hour
)

// Hook is one ordered direct-process declaration. Timeout and Repository keep
// their zero values when omitted on the wire; canonical helpers apply defaults
// without mutating the decoded declaration.
type Hook struct {
	ID         string        `yaml:"id" json:"id"`
	Repository string        `yaml:"repository,omitempty" json:"repository,omitempty"`
	Command    []string      `yaml:"command" json:"command"`
	Timeout    time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// HookEvents maps event names to their ordered hook declarations.
type HookEvents map[string][]Hook

// CanonicalHookEvent validates an ordered event and returns a deep-copied
// defaulted representation suitable for equality and fingerprints.
func CanonicalHookEvent(event string, hooks []Hook, baseRepository string) ([]Hook, error) {
	if err := validateHookEvent(event, hooks, baseRepository, hookSourceAny); err != nil {
		return nil, err
	}
	canonical := make([]Hook, len(hooks))
	for index, hook := range hooks {
		canonical[index] = hook
		canonical[index].Command = append([]string(nil), hook.Command...)
		if canonical[index].Repository == "" {
			canonical[index].Repository = baseRepository
		}
		if canonical[index].Timeout == 0 {
			canonical[index].Timeout = HookDefaultTimeout
		}
	}
	return canonical, nil
}

// HookEventsEqual compares one event after applying its repository and timeout
// defaults. Declaration order deliberately remains significant.
func HookEventsEqual(event string, left, right []Hook, baseRepository string) (bool, error) {
	leftCanonical, err := CanonicalHookEvent(event, left, baseRepository)
	if err != nil {
		return false, err
	}
	rightCanonical, err := CanonicalHookEvent(event, right, baseRepository)
	if err != nil {
		return false, err
	}
	if len(leftCanonical) != len(rightCanonical) {
		return false, nil
	}
	for index := range leftCanonical {
		leftHook, rightHook := leftCanonical[index], rightCanonical[index]
		if leftHook.ID != rightHook.ID || leftHook.Repository != rightHook.Repository || leftHook.Timeout != rightHook.Timeout || len(leftHook.Command) != len(rightHook.Command) {
			return false, nil
		}
		for commandIndex := range leftHook.Command {
			if leftHook.Command[commandIndex] != rightHook.Command[commandIndex] {
				return false, nil
			}
		}
	}
	return true, nil
}

type hookSource uint8

const (
	hookSourceLocal hookSource = iota
	hookSourcePortable
	hookSourceShared
	hookSourceAny
)

func validateHookEvents(events HookEvents, baseRepository string, source hookSource) error {
	for event, hooks := range events {
		if err := validateHookEvent(event, hooks, baseRepository, source); err != nil {
			return err
		}
	}
	return nil
}

func validateHookEvent(event string, hooks []Hook, baseRepository string, source hookSource) error {
	if err := ValidatePortableID(baseRepository); err != nil {
		return fmt.Errorf("hook base repository: %w", err)
	}
	if !hookEventAllowed(event, source) {
		return fmt.Errorf("hook event %q is not supported for this source", event)
	}
	if len(hooks) == 0 {
		return fmt.Errorf("hook event %q must contain at least one declaration", event)
	}
	ids := make(map[string]struct{}, len(hooks))
	for index, hook := range hooks {
		if err := ValidatePortableID(hook.ID); err != nil {
			return fmt.Errorf("hook event %q declaration %d ID: %w", event, index, err)
		}
		if _, exists := ids[hook.ID]; exists {
			return fmt.Errorf("hook event %q contains duplicate hook ID %q", event, hook.ID)
		}
		ids[hook.ID] = struct{}{}
		if hook.Repository != "" {
			if err := ValidatePortableID(hook.Repository); err != nil {
				return fmt.Errorf("hook %q repository: %w", hook.ID, err)
			}
		}
		if len(hook.Command) == 0 {
			return fmt.Errorf("hook %q command must be non-empty", hook.ID)
		}
		for commandIndex, element := range hook.Command {
			if element == "" && commandIndex == 0 {
				return fmt.Errorf("hook %q executable is required", hook.ID)
			}
			if strings.ContainsAny(element, "\x00\r\n") {
				return fmt.Errorf("hook %q command element %d must not contain controls", hook.ID, commandIndex)
			}
			if source == hookSourcePortable || source == hookSourceShared {
				if err := validatePortableHookCommandElement(commandIndex, element); err != nil {
					return fmt.Errorf("hook %q command element %d: %w", hook.ID, commandIndex, err)
				}
			}
		}
		if hook.Timeout < 0 || hook.Timeout > HookMaximumTimeout {
			return fmt.Errorf("hook %q timeout must be positive and no greater than %s", hook.ID, HookMaximumTimeout)
		}
	}
	return nil
}

func hookEventAllowed(event string, source hookSource) bool {
	switch source {
	case hookSourceLocal:
		return event == HookEventPostCreate || event == HookEventPostRelease
	case hookSourceShared:
		return event == HookEventPostCreate
	case hookSourcePortable:
		return event == HookEventPostClone
	case hookSourceAny:
		return event == HookEventPostCreate || event == HookEventPostClone || event == HookEventPostRelease
	default:
		return false
	}
}

// validateExplicitHookTimeouts distinguishes an omitted timeout (which later
// defaults) from an explicitly supplied zero timeout (which is invalid).
func validateExplicitHookTimeouts(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Content) == 0 {
		return nil
	}
	root := document.Content[0]
	for _, section := range []string{"hooks", "shared_hooks"} {
		events := mappingValue(root, section)
		if events == nil || events.Kind != yaml.MappingNode {
			continue
		}
		for eventIndex := 0; eventIndex+1 < len(events.Content); eventIndex += 2 {
			event, declarations := events.Content[eventIndex].Value, events.Content[eventIndex+1]
			if declarations.Kind != yaml.SequenceNode {
				continue
			}
			for _, declaration := range declarations.Content {
				timeout := mappingValue(declaration, "timeout")
				if timeout == nil {
					continue
				}
				duration, err := time.ParseDuration(timeout.Value)
				if err != nil || duration <= 0 || duration > HookMaximumTimeout {
					return fmt.Errorf("hook event %q timeout must be positive and no greater than %s", event, HookMaximumTimeout)
				}
			}
		}
	}
	return nil
}

func validatePortableHookCommandElement(index int, value string) error {
	if containsControl(value) {
		return fmt.Errorf("must not contain controls")
	}
	if err := validatePortableLiteral(value); err != nil {
		return err
	}
	if index != 0 || value == "" {
		return nil
	}
	if hasWindowsPathVolume(value) {
		return fmt.Errorf("relative executable path must not be volume-qualified")
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	if path.Clean(normalized) != normalized || strings.HasPrefix(normalized, "../") || normalized == ".." {
		return fmt.Errorf("relative executable path must be clean and contained")
	}
	return nil
}

// validatePortableLiteral enforces restrictions common to every portable
// command element. Literal arguments intentionally do not get executable-path
// cleanliness or containment checks: they are passed verbatim to the process.
func validatePortableLiteral(value string) error {
	parsed, err := url.Parse(value)
	if (err == nil && parsed.User != nil) || hasURLUserinfo(value) {
		return fmt.Errorf("must not contain URL user information")
	}
	if (err == nil && strings.EqualFold(parsed.Scheme, "file")) || hasURLScheme(value, "file") {
		return fmt.Errorf("must not contain a file URL")
	}
	if isHomeRelativePath(value) || isPortableAbsolutePath(value) {
		return fmt.Errorf("must not be an absolute or home-relative path")
	}
	return nil
}

func isHomeRelativePath(value string) bool {
	return strings.HasPrefix(value, "~")
}

func isPortableAbsolutePath(value string) bool {
	return isAbsoluteLocalPath(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`)
}

// hasURLScheme deliberately does not rely on url.Parse succeeding: malformed
// file URLs remain prohibited just like valid ones.
func hasURLScheme(value, scheme string) bool {
	colon := strings.IndexByte(value, ':')
	return colon > 0 && strings.EqualFold(value[:colon], scheme)
}

// hasURLUserinfo detects an authority userinfo marker before a malformed URL
// reaches url.Parse. It never includes the input in its result or errors.
func hasURLUserinfo(value string) bool {
	authority := ""
	if strings.HasPrefix(value, "//") {
		authority = value[2:]
	} else {
		colon := strings.IndexByte(value, ':')
		if colon <= 0 || len(value) < colon+3 || value[colon+1:colon+3] != "//" {
			return false
		}
		authority = value[colon+3:]
	}
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	return strings.Contains(authority, "@")
}
