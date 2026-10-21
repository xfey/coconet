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
type installationLease struct {
	Finish  func() error
	Current bool
}

func startInstallationGuard(ctx context.Context, executable, root string, onlyIfNewer bool, stderr io.Writer) (installationLease, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return installationLease{}, err
	}
	args := []string{"_install-guard", root}
	if onlyIfNewer {
		args = append(args, "--if-newer")
	}
	command := exec.CommandContext(ctx, executable, args...)
	lease, err := command.StdinPipe()
	if err != nil {
		return installationLease{}, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		lease.Close()
		return installationLease{}, err
	}
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Cancellation requests an orderly release, never SIGKILLs an active drain.
	command.Cancel = func() error { return lease.Close() }
	if err := command.Start(); err != nil {
		lease.Close()
		return installationLease{}, err
	}
	var ready struct {
		Ready   bool `json:"ready"`
		Current bool `json:"current"`
	}
	if err := json.NewDecoder(output).Decode(&ready); err != nil || !ready.Ready {
		lease.Close()
		waitErr := errors.Join(err, command.Wait())
		if waitErr == nil {
			waitErr = errors.New("Runtime did not confirm readiness")
		}
		return installationLease{}, fmt.Errorf("Runtime update preparation did not complete; installation remains unchanged: %w", waitErr)
	}
	return installationLease{Finish: func() error { _ = lease.Close(); return command.Wait() }, Current: ready.Current}, nil
}
