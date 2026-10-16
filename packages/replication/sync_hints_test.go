package replication

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func TestSyncHintsExpireAndStayProjectScoped(t *testing.T) {
	c := &Client{}
	hints := &contracts.RelaySyncHints{Policy: contracts.RelayContentPolicy{SchemaVersion: 1, Direct: true, Temporary: true, TTLSeconds: 604800}}
	if err := c.rememberHints("one", hints); err != nil {
		t.Fatal(err)
	}
	hints.Policy.TTLSeconds = 0 // Caller cannot change the cached value.
	if value, ok := c.syncHint("one"); !ok || value.Policy.TTLSeconds != 604800 {
		t.Fatal(value, ok)
	}
	if _, ok := c.syncHint("other"); ok {
		t.Fatal("cross-project hint")
	}
	ctx := context.Background()
	fetches := 0
	fetch := func(context.Context, string) (contracts.RelayContentPolicy, error) {
		fetches++
		return contracts.RelayContentPolicy{}, errors.New("offline")
	}
	if _, err := c.contentPolicy(ctx, "one", fetch); err != nil || fetches != 0 {
		t.Fatal("fresh policy refetched", err)
	}
	c.hintsMu.Lock()
	entry := c.hints["one"]
	entry.at = time.Now().Add(-91 * time.Second)
	c.hints["one"] = entry
	c.hintsMu.Unlock()
	if _, ok := c.syncHint("one"); ok {
		t.Fatal("expired hints suppressed new work")
	}
	if _, err := c.contentPolicy(ctx, "one", fetch); err == nil || fetches != 1 {
		t.Fatal("expired policy hid outage")
	}
	if err := c.rememberHints("one", hints); err != nil {
		t.Fatal(err)
	}
	if err := c.rememberHints("one", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.contentPolicy(ctx, "one", fetch); err == nil || fetches != 2 {
		t.Fatal("missing hints retained stale policy")
	}
	if err := c.rememberHints("one", hints); err != nil {
		t.Fatal(err)
	}
	if err := c.rememberHints("one", &contracts.RelaySyncHints{}); err == nil {
		t.Fatal("malformed hint accepted")
	}
	if _, ok := c.syncHint("one"); ok {
		t.Fatal("malformed response left earlier hints active")
	}
}
