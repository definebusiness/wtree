//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os/exec"
	"syscall"
)

func configureOwnedCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateOwnedCommand(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	// The negative PID addresses only the process group created for this
	// command. ESRCH means the owned group has already exited.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
