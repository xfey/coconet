package inference

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Waiting distinguishes unavailable prerequisites from failed model results.
// Schedulers retain the task and back off without spending its result retries.
type Waiting struct {
	Kind  string
	Cause error
}

func (e *Waiting) Error() string { return e.Kind + ": " + e.Cause.Error() }
func (e *Waiting) Unwrap() error { return e.Cause }

func ConfigurationError(err error) error {
	if _, ok := RetryDelay(err); ok {
		return err
	}
	return &Waiting{Kind: "configuration_required", Cause: err}
}

func RetryDelay(err error) (time.Duration, bool) {
	var waiting *Waiting
	if errors.As(err, &waiting) {
		if waiting.Kind == "transient_network" || waiting.Kind == "rate_limited" {
			return time.Minute, true
		}
		return 5 * time.Minute, true
	}
	var network net.Error
	if errors.As(err, &network) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return time.Minute, true
	}
	var executable *exec.Error
	var path *os.PathError
	if errors.As(err, &executable) || errors.As(err, &path) {
		return 5 * time.Minute, true
	}
	return 0, false
}

func providerFailure(classification string, cause error) error {
	code, _, _ := strings.Cut(classification, ":")
	switch code {
	case "authentication_required", "quota_exhausted", "rate_limited", "provider_configuration_rejected", "thinking_configuration_rejected", "context_capacity_exceeded", "transient_network":
		return &Waiting{Kind: code, Cause: cause}
	}
	return cause
}
