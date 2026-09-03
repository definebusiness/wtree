//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package main

import "os/exec"

func configureOwnedCommand(command *exec.Cmd) {}

func adoptOwnedCommand(*exec.Cmd) error { return nil }

func releaseOwnedCommand(*exec.Cmd) {}

func terminateOwnedCommand(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
