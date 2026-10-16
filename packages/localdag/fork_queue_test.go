package localdag

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestForkBaselineQueuesOfflineSurvivesRestartAndPreservesLaterWork(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	now := time.Now().UTC()
	calls := 0
	var firstID string
	remote := publisherFunc(func(_ context.Context, commit workgraph.Commit) error {
		calls++
		if firstID == "" {
			firstID = commit.ID
		} else if firstID != commit.ID {
			t.Fatal("network failure changed the queued commit identity")
		}
		return errors.New("Relay offline")
	})
	e := &Engine{Store: store, Actor: "reader", Publisher: remote, Now: func() time.Time { return now }}
	baseline := nativeObservation(nativeTurn("u", "user", "先完善重试提示。", 0), nativeTurn("a", "assistant", "已经完成。", 1))
	if _, err := e.QueueInheritedObservation(ctx, baseline); err == nil {
		t.Fatal("ordinary work was accepted as a Fork baseline")
	}
	baseline.Inherited = true
	source, err := e.QueueInheritedObservation(ctx, baseline)
	if err != nil || source.Version != 1 || calls != 0 {
		t.Fatal("local queue contacted Relay", source, calls, err)
	}
	again, err := e.QueueInheritedObservation(ctx, baseline)
	if err != nil || again != source || calls != 0 {
		t.Fatal("replaying the queued baseline was not local and idempotent", again, err)
	}
	changed := nativeObservation(nativeTurn("u", "user", "先完善重试提示。", 0), nativeTurn("a", "assistant", "改写后的历史", 1))
	changed.Inherited = true
	if _, err := e.QueueInheritedObservation(ctx, changed); err == nil {
		t.Fatal("different baseline reused an existing queued source")
	}
	resumed := nativeObservation(append(baseline.Normalized.Turns, nativeTurn("next", "user", "接下来检查并发问题。", 2))...)
	if _, err := e.CaptureObservation(ctx, resumed); !errors.Is(err, ErrCapturePending) {
		t.Fatal("later work bypassed the unpublished inherited boundary", err)
	}
	// A long outage must not silently pause registration of an already-usable
	// native child. Backoff remains capped and survives a Runtime restart.
	for i := 0; i < 10; i++ {
		if err := e.Flush(ctx, "project"); err == nil {
			t.Fatal("offline publication reported success")
		}
		tasks, _ := store.Tasks("project")
		if len(tasks) != 1 || tasks[0].Status != "publish_pending" || tasks[0].Error == "" {
			t.Fatal("long outage paused or lost the baseline", tasks)
		}
		next, err := time.Parse(time.RFC3339Nano, tasks[0].NextAt)
		if err != nil || !next.After(now) || next.Sub(now) > 256*time.Second {
			t.Fatal("invalid retry backoff", tasks[0], err)
		}
		if err := e.Flush(ctx, "project"); err != nil || calls != i+1 {
			t.Fatal("retry ignored its next-at boundary", calls, err)
		}
		now = next.Add(time.Second)
	}
	store.Close()
	store, err = graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	e = &Engine{Store: store, Actor: "reader", Publisher: remote, Now: func() time.Time { return now }}
	if err := e.RecoverLocal(ctx, "project"); err != nil || calls != 10 {
		t.Fatal("restart attempted network registration", calls, err)
	}
	if again, err := e.QueueInheritedObservation(ctx, baseline); err != nil || again != source {
		t.Fatal("restart lost baseline idempotency", err)
	}
	// Simulate an accepted commit whose response was lost. The durable receipt
	// finalizes it without a second baseline or a model call.
	e.Publisher = publisherFunc(func(_ context.Context, commit workgraph.Commit) error {
		if commit.ID != firstID {
			t.Fatal("restart changed publication identity")
		}
		if _, err := store.Apply(commit, true); err != nil {
			t.Fatal(err)
		}
		return errors.New("response lost after acceptance")
	})
	if err := e.Flush(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	tasks, _ := store.Tasks("project")
	if state.Revision != 1 || state.Sessions[source.SessionID].ConsumedTurns != 2 || len(tasks) != 0 || len(state.Nodes) != 0 {
		t.Fatal("inherited baseline was duplicated or queued as new work", state, tasks)
	}
	e.Publisher = nil
	next, err := e.CaptureObservation(ctx, resumed)
	if err != nil || next.Version != 2 {
		t.Fatal("resumed work did not follow the inherited baseline", next, err)
	}
	state, _ = store.State("project")
	entries, err := readEntries(store, state.Snapshots[next.Key()], 0, 3)
	if err != nil || len(entries) != 3 || !entries[0].Inherited || !entries[1].Inherited || entries[2].Inherited {
		t.Fatal("later dialogue attribution changed", entries, err)
	}
	input, err := prepareStage(store, state, next, false)
	if err != nil || input.Audit.From != 2 || input.Anchors["e1"] || !input.Anchors["e3"] {
		t.Fatal("inherited history entered the new-work evidence range", input, err)
	}
	replay, err := store.Replay("project")
	if err != nil || workgraph.Digest(replay) != workgraph.Digest(state) {
		t.Fatal("state differs from deterministic replay", err)
	}
}
