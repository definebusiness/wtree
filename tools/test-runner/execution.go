package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type changedExecutor interface {
	Run(context.Context, string, []string, []string) error
}

type processChangedExecutor struct{}

func (processChangedExecutor) Run(ctx context.Context, name string, args []string, environment []string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), environment...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func executeChangedActions(ctx context.Context, executor changedExecutor, actions []ChangedAction, timeout string, hostArch string) error {
	temporary, err := os.MkdirTemp("", "wtree-changed-cross.")
	if err != nil {
		return fmt.Errorf("create changed cross-compile directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	for _, action := range actions {
		var name string
		var args, environment []string
		switch action.Kind {
		case "docs":
			name, args = "bash", []string{"scripts/docs-check.sh"}
		case "harness":
			name, args = "bash", []string{"scripts/local-test-targets_test.sh"}
		case "test":
			name, args = "go", []string{"test", "-short=false", "-count=1", "-timeout=" + timeout, action.Package}
		case "cross-compile":
			name, args = "go", []string{"test", "-c", "-o", filepath.Join(temporary, filepath.Base(action.Package)+"-"+action.Platform), action.Package}
			environment = []string{"GOOS=" + action.Platform, "GOARCH=" + hostArch}
		default:
			return fmt.Errorf("unsupported changed action %q", action.Kind)
		}
		if err := executor.Run(ctx, name, args, environment); err != nil {
			return fmt.Errorf("changed %s %s failed: %w", action.Kind, action.Package, err)
		}
	}
	return nil
}

// ChangedAction is a deterministic, non-secret execution instruction derived
// solely from ChangeSelection. Make consumes its stable sentinel rendering;
// tests exercise this source of truth without shell parsing.
type ChangedAction struct {
	Kind     string
	Package  string
	Platform string
}

func changedExecutionPlan(selection ChangeSelection, hostOS, hostArch string) ([]ChangedAction, error) {
	if hostOS == "" || hostArch == "" {
		return nil, fmt.Errorf("changed execution requires host platform")
	}
	known := map[string]bool{"darwin": true, "linux": true, "windows": true, "freebsd": true}
	seen := map[string]bool{}
	add := func(action ChangedAction, actions *[]ChangedAction) {
		key := action.Kind + "\x00" + action.Package + "\x00" + action.Platform
		if !seen[key] {
			seen[key] = true
			*actions = append(*actions, action)
		}
	}
	var actions []ChangedAction
	if selection.Documentation {
		add(ChangedAction{Kind: "docs"}, &actions)
	}
	if selection.Harness {
		add(ChangedAction{Kind: "harness"}, &actions)
	}
	packages := append([]string(nil), selection.Packages...)
	sort.Strings(packages)
	for _, pkg := range packages {
		if !strings.HasPrefix(pkg, "./") && !strings.Contains(pkg, "/") {
			return nil, fmt.Errorf("unsafe changed package %q", pkg)
		}
		add(ChangedAction{Kind: "test", Package: pkg}, &actions)
	}
	for _, platform := range selection.Platforms {
		if !known[platform] {
			return nil, fmt.Errorf("unsupported changed platform %q", platform)
		}
		if platform == hostOS {
			continue
		}
		for _, pkg := range packages {
			add(ChangedAction{Kind: "cross-compile", Package: pkg, Platform: platform}, &actions)
		}
	}
	return actions, nil
}

func hostChangedExecutionPlan(selection ChangeSelection) ([]ChangedAction, error) {
	return changedExecutionPlan(selection, runtime.GOOS, runtime.GOARCH)
}
