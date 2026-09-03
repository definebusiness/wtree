//go:build !windows

package service

import "os/exec"

// configureDirectProcess creates a dedicated process group on Unix. Keeping
// the command reference lets Kill(-pgid) remain authoritative after the leader
// has exited while an inherited-output descendant is still alive.
type hookProcessTree struct{ command *exec.Cmd }

func configureHookProcess(command *exec.Cmd) { configureDirectProcess(command) }

func beginHookProcessTree(command *exec.Cmd) (hookProcessTree, error) {
	return hookProcessTree{command: command}, nil
}

func (tree hookProcessTree) Terminate() error { return stopDirectProcess(tree.command) }
func (hookProcessTree) Close() error          { return nil }
