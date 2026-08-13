package config

import (
	"fmt"
	"path/filepath"
)

type Paths struct {
	ConfigDir    string
	DataDir      string
	WorktreeRoot string
}

// ResolvePaths is deterministic and injectable; it never reads the process environment.
func ResolvePaths(goos, home string, environment map[string]string) (Paths, error) {
	override := environment["WTREE_DATA_HOME"]
	switch goos {
	case "linux":
		configHome := environment["XDG_CONFIG_HOME"]
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		dataHome := environment["XDG_DATA_HOME"]
		if dataHome == "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
		if override != "" {
			return Paths{ConfigDir: filepath.Join(configHome, "wtree"), DataDir: override, WorktreeRoot: filepath.Join(override, "worktrees")}, nil
		}
		return Paths{ConfigDir: filepath.Join(configHome, "wtree"), DataDir: filepath.Join(dataHome, "wtree"), WorktreeRoot: filepath.Join(dataHome, "wtree", "worktrees")}, nil
	case "darwin":
		if override != "" {
			return Paths{ConfigDir: filepath.Join(home, "Library", "Application Support", "wtree"), DataDir: override, WorktreeRoot: filepath.Join(override, "worktrees")}, nil
		}
		return Paths{ConfigDir: filepath.Join(home, "Library", "Application Support", "wtree"), DataDir: filepath.Join(home, "Library", "Application Support", "wtree"), WorktreeRoot: filepath.Join(home, "Library", "Application Support", "wtree", "worktrees")}, nil
	case "windows":
		configBase := environment["AppData"]
		if configBase == "" {
			return Paths{}, fmt.Errorf("AppData is required on Windows")
		}
		base := environment["LocalAppData"]
		if base == "" {
			return Paths{}, fmt.Errorf("LocalAppData is required on Windows")
		}
		if override != "" {
			return Paths{ConfigDir: filepath.Join(configBase, "wtree"), DataDir: override, WorktreeRoot: filepath.Join(override, "worktrees")}, nil
		}
		return Paths{ConfigDir: filepath.Join(configBase, "wtree"), DataDir: filepath.Join(base, "wtree"), WorktreeRoot: filepath.Join(base, "wtree", "worktrees")}, nil
	default:
		return Paths{}, fmt.Errorf("unsupported operating system %q", goos)
	}
}
