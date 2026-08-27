//go:build !windows

package service

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureDirectProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateDirectProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return errors.New("direct process has not started")
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
