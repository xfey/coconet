package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/localdag"
)

// A new replica must not report an empty graph before its first Relay sync.
// Existing replicas remain readable offline. Reads wait on the background sync
// rather than starting competing downloads or spending inference quota.
type localDAGDiscoverySync struct {
	mu      sync.Mutex
	results map[string]error
	changed chan struct{}
}

func newLocalDAGDiscoverySync() *localDAGDiscoverySync {
	return &localDAGDiscoverySync{results: map[string]error{}, changed: make(chan struct{})}
}

func (s *localDAGDiscoverySync) report(project string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[project] = err
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *localDAGDiscoverySync) wait(ctx context.Context, engine *localdag.Engine, project string) error {
	if engine.Publisher == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		// Read the notification before state so a completed sync cannot be lost
		// between checking the store and waiting for the next notification.
		s.mu.Lock()
		failure, settled := s.results[project]
		changed := s.changed
		s.mu.Unlock()
		state, err := engine.Store.State(project)
		if err != nil {
			return err
		}
		if state.Revision > 0 {
			return nil
		}
		if settled {
			if failure != nil {
				return fmt.Errorf("initial Relay synchronization failed; retry after checking Runtime health: %w", failure)
			}
			return nil // The Relay has confirmed an empty project.
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("initial Relay synchronization is still pending; retry the project query: %w", ctx.Err())
		case <-changed:
		}
	}
}
