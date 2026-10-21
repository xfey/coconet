package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
)

// The child's stdin is a lease: parent failure closes the pipe and the Runtime
// resumes using the unchanged (or successfully activated) current pointer.
func startInstallationGuard(ctx context.Context, executable, root string, stderr io.Writer) (func() error, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable, "_install-guard", root)
	lease, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		lease.Close()
		return nil, err
	}
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Cancellation requests an orderly release, never SIGKILLs an active drain.
	command.Cancel = func() error { return lease.Close() }
	if err := command.Start(); err != nil {
		lease.Close()
		return nil, err
	}
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(output).Decode(&ready); err != nil || !ready.Ready {
		lease.Close()
		waitErr := errors.Join(err, command.Wait())
		if waitErr == nil {
			waitErr = errors.New("Runtime did not confirm readiness")
		}
		return nil, fmt.Errorf("Runtime update preparation did not complete; installation remains unchanged: %w", waitErr)
	}
	return func() error { _ = lease.Close(); return command.Wait() }, nil
}
