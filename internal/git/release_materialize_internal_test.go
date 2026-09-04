package git

import "testing"

func TestReleaseAuthenticationEnvironmentPreservesGitOwnedChannelsAndDisablesPrompts(t *testing.T) {
	canary := "release-secret-canary"
	environment := authenticatedEnvironment([]string{
		"PATH=/bin", "SSH_AUTH_SOCK=/tmp/agent.sock", "GIT_ASKPASS=/tmp/askpass",
		"ASKPASS_REQUIRED_SECRET=" + canary, "GIT_CONFIG_GLOBAL=/tmp/gitconfig",
		"GIT_TERMINAL_PROMPT=1", "HOME=/tmp/home",
	})
	values := map[string]string{}
	for _, item := range environment {
		key, value, _ := splitEnvironment(item)
		values[key] = value
	}
	for key, want := range map[string]string{"SSH_AUTH_SOCK": "/tmp/agent.sock", "GIT_ASKPASS": "/tmp/askpass", "ASKPASS_REQUIRED_SECRET": canary, "GIT_CONFIG_GLOBAL": "/tmp/gitconfig", "HOME": "/tmp/home", "GIT_TERMINAL_PROMPT": "0"} {
		if values[key] != want {
			t.Fatalf("%s = %q, want %q", key, values[key], want)
		}
	}
}

func splitEnvironment(value string) (string, string, bool) {
	for index, character := range value {
		if character == '=' {
			return value[:index], value[index+1:], true
		}
	}
	return value, "", false
}
