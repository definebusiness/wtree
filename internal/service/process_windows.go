//go:build windows

package service

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

func configureDirectProcess(command *exec.Cmd) {}

// taskkill is the Windows-supported process-tree primitive. It is invoked
// directly with a fixed argument array and only after a context cancellation.
func terminateDirectProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return errors.New("direct process has not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F").Run(); err == nil {
		return nil
	}
	return command.Process.Kill()
}
