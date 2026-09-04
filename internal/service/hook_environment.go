package service

import (
	"errors"
	"strings"
)

type HookEnvironmentPolicy string

const (
	HookEnvironmentLocal    HookEnvironmentPolicy = "local"
	HookEnvironmentPortable HookEnvironmentPolicy = "portable"
)

func buildHookEnvironment(policy HookEnvironmentPolicy, windows bool, inherited []string, plan HookPlan, entryIndex int) ([]string, error) {
	if entryIndex < 0 || entryIndex >= len(plan.authority.entries) || policy != HookEnvironmentLocal && policy != HookEnvironmentPortable {
		return nil, errors.New("invalid hook environment")
	}
	values := map[string]string{}
	names := map[string]string{}
	for _, entry := range inherited {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !validHookEnvironmentName(name) || strings.ContainsRune(value, '\x00') {
			return nil, errors.New("malformed environment")
		}
		key := name
		if windows {
			key = strings.ToUpper(name)
		}
		if policy == HookEnvironmentPortable && !allowedPortableEnvironment(name, windows) {
			continue
		}
		values[key] = value
		names[key] = name
	}
	for key := range values {
		if strings.HasPrefix(key, "WTREE_") || !windows && strings.HasPrefix(names[key], "WTREE_") {
			delete(values, key)
			delete(names, key)
		}
	}
	out := make([]string, 0, len(values)+15)
	for key := range values {
		out = append(out, names[key]+"="+values[key])
	}
	sortStrings(out)
	e := plan.authority.entries[entryIndex]
	reserved := [][2]string{{"WTREE_HOOK", plan.authority.event}, {"WTREE_OPERATION", plan.Operation}, {"WTREE_PROJECT_ID", plan.authority.projectID}, {"WTREE_PROJECT_NAME", plan.authority.projectName}, {"WTREE_BASE_REPOSITORY_ID", plan.authority.baseRepository}, {"WTREE_WORKSPACE_ID", plan.authority.workspaceID}, {"WTREE_WORKSPACE_NAME", plan.authority.workspaceName}, {"WTREE_SOURCE_LOGICAL_ROOT", plan.authority.sourceRoot}, {"WTREE_TARGET_LOGICAL_ROOT", plan.authority.targetRoot}, {"WTREE_REPOSITORY_ID", e.Repository}, {"WTREE_SOURCE_REPOSITORY", e.SourceRepository}, {"WTREE_TARGET_REPOSITORY", e.TargetRepository}, {"WTREE_BRANCH", e.Branch}, {"WTREE_HEAD", e.Head}}
	for _, v := range reserved {
		out = append(out, v[0]+"="+v[1])
	}
	if plan.authority.releaseName != "" {
		out = append(out, "WTREE_RELEASE_NAME="+plan.authority.releaseName)
	}
	return out, nil
}
func validHookEnvironmentName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "=\x00\r\n")
}
func allowedPortableEnvironment(name string, windows bool) bool {
	key := name
	if windows {
		key = strings.ToUpper(key)
	}
	return key == "PATH" || key == "LANG" || key == "LC_ALL" || strings.HasPrefix(key, "LC_") || key == "TMPDIR" || key == "TMP" || key == "TEMP" || windows && (key == "PATHEXT" || key == "SYSTEMROOT" || key == "WINDIR" || key == "COMSPEC")
}
func sortStrings(v []string) {
	for i := range v {
		for j := i + 1; j < len(v); j++ {
			if v[j] < v[i] {
				v[i], v[j] = v[j], v[i]
			}
		}
	}
}
