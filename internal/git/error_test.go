package git

import (
	"strings"
	"testing"
)

func TestBoundedStderrLimitsDiagnosticSize(t *testing.T) {
	input := strings.Repeat("x", maxStderr+1)
	if got := boundedStderr(input); len(got) != maxStderr {
		t.Errorf("boundedStderr length = %d, want %d", len(got), maxStderr)
	}
}

func TestSanitizedEnvironmentForcesLocaleAndNoninteractiveGit(t *testing.T) {
	values := make(map[string]string)
	for _, item := range sanitizedEnvironment([]string{"PATH=/bin", "LC_ALL=hostile", "HOME=/hostile", "GIT_CONFIG_GLOBAL=/hostile"}) {
		parts := strings.SplitN(item, "=", 2)
		values[parts[0]] = parts[1]
	}
	if values["LC_ALL"] != "C" || values["LANG"] != "C" || values["GIT_TERMINAL_PROMPT"] != "0" || values["GIT_CONFIG_GLOBAL"] == "/hostile" {
		t.Errorf("sanitized environment = %#v", values)
	}
}
