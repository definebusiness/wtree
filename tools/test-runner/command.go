package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// runOwnedCommand creates a runner-owned process boundary. Cancellation is
// deliberately handled here rather than by exec.CommandContext so POSIX can
// terminate the owned process group (including test-created descendants) while
// never addressing processes outside that group.
func runOwnedCommand(ctx context.Context, name string, args ...string) commandResult {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return commandResult{ErrorOutput: []byte(err.Error()), Elapsed: time.Since(started), ExitCode: 1}
	}
	command := exec.Command(name, args...)
	configureOwnedCommand(command)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return commandResult{ErrorOutput: []byte(err.Error()), Elapsed: time.Since(started), ExitCode: 1}
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var err error
	select {
	case err = <-wait:
	case <-ctx.Done():
		terminateOwnedCommand(command)
		err = <-wait
	}
	result := commandResult{Output: stdout.Bytes(), ErrorOutput: stderr.Bytes(), Elapsed: time.Since(started)}
	if err == nil {
		return result
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
	} else {
		result.ExitCode = 1
		result.ErrorOutput = append(result.ErrorOutput, []byte("\n"+err.Error())...)
	}
	return result
}
