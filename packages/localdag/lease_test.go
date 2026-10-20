package localdag

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func TestLeaseTemporaryRenewalFailureKeepsValidExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	renewed := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		keepProcessingLease(ctx, cancel, time.Now().Add(time.Second), time.Now, 10*time.Millisecond, 50*time.Millisecond, func(context.Context) (time.Time, error) {
			if attempts.Add(1) == 1 {
				return time.Time{}, errors.New("temporary outage")
			}
			if attempts.Load() == 2 {
				close(renewed)
			}
			return time.Now().Add(time.Second), nil
		})
	}()
	select {
	case <-ctx.Done():
		t.Fatal("single failed renewal canceled valid claim")
	case <-renewed:
	case <-time.After(2 * time.Second):
		t.Fatal("renewal did not recover")
	}
	cancel()
	<-finished
}

func TestLeaseExpiryAndConfirmedLossCancelModel(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "revoked"}[lost], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				keepProcessingLease(ctx, cancel, time.Now().Add(80*time.Millisecond), time.Now, 10*time.Millisecond, 20*time.Millisecond, func(context.Context) (time.Time, error) {
					if lost {
						return time.Time{}, contracts.ErrProcessingStale
					}
					return time.Time{}, errors.New("offline")
				})
			}()
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("lost lease stayed active")
			}
			<-finished
		})
	}
}
