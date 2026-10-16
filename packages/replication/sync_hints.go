package replication

import (
	"context"
	"errors"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

type syncHint struct {
	value contracts.RelaySyncHints
	at    time.Time
}

func validContentPolicy(p contracts.RelayContentPolicy) bool {
	return p.SchemaVersion == 1 && p.TTLSeconds >= 0 && p.Direct == p.Temporary && (p.Temporary || p.TTLSeconds == 0)
}

func (c *Client) rememberHints(project string, hints *contracts.RelaySyncHints) error {
	c.hintsMu.Lock()
	defer c.hintsMu.Unlock()
	if hints == nil {
		delete(c.hints, project)
		return nil
	}
	if !validContentPolicy(hints.Policy) {
		delete(c.hints, project)
		return errors.New("invalid relay synchronization hints")
	}
	if c.hints == nil {
		c.hints = map[string]syncHint{}
	}
	c.hints[project] = syncHint{value: *hints, at: time.Now()}
	return nil
}

func (c *Client) syncHint(project string) (contracts.RelaySyncHints, bool) {
	c.hintsMu.Lock()
	defer c.hintsMu.Unlock()
	h, ok := c.hints[project]
	// Covers the normal 60–66 second poll plus worker skew; hints never
	// suppress checks indefinitely when the graph worker has failed.
	return h.value, ok && time.Since(h.at) < 90*time.Second
}

func (c *Client) contentPolicy(ctx context.Context, project string, fetch func(context.Context, string) (contracts.RelayContentPolicy, error)) (contracts.RelayContentPolicy, error) {
	if h, ok := c.syncHint(project); ok {
		return h.Policy, nil
	}
	p, err := fetch(ctx, project)
	if err == nil && !validContentPolicy(p) {
		err = errors.New("invalid relay content policy")
	}
	return p, err
}
