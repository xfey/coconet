// Package subprocess bounds commands even when descendants retain output pipes.
package subprocess

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// Run starts an unstarted exec.Command (not CommandContext) with bounded pipe
// draining. Cancellation terminates its process group on supported Unix hosts.
// A wrapper that exits while a descendant keeps its pipes open fails explicitly.
func Run(ctx context.Context, command *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prepare(command)
	if command.WaitDelay == 0 {
		command.WaitDelay = time.Second
	}
	if err := command.Start(); err != nil {
		return err
	}
	finished, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			// Give a nested Runtime time to cancel its own command group and
			// persist the failed attempt before enforcing the deadline.
			terminate(command)
			timer := time.NewTimer(command.WaitDelay)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				stop(command)
			}
		case <-finished:
		}
	}()
	err := command.Wait()
	close(finished)
	<-watched
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		stop(command)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
