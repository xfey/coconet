package localdag

import (
	"context"
	"errors"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

// A renewal transport failure does not revoke an already granted lease.
// Only expiry or a confirmed loss cancels the in-flight model execution.
func keepProcessingLease(ctx context.Context, cancel context.CancelFunc, until time.Time, now func() time.Time, interval, requestTimeout time.Duration, renew func(context.Context) (time.Time, error)) {
	if !until.After(now()) {
		cancel()
		return
	}
	expiry := time.NewTimer(until.Sub(now()))
	defer expiry.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			cancel()
			return
		case <-ticker.C:
			remaining := until.Sub(now())
			if remaining <= 0 {
				cancel()
				return
			}
			attempt, stop := context.WithTimeout(ctx, min(requestTimeout, remaining))
			next, err := renew(attempt)
			stop()
			if errors.Is(err, contracts.ErrProcessingStale) || errors.Is(err, contracts.ErrProcessingPaused) {
				cancel()
				return
			}
			if err != nil {
				continue
			}
			if !next.After(now()) {
				cancel()
				return
			}
			until = next
			expiry.Reset(until.Sub(now()))
		}
	}
}
