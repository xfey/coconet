package replication

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

func (t *checkpointTransport) Receipt(_ context.Context, p, id string) (contracts.RelayCommitEvidence, error) {
	return t.s.Receipt(p, id)
}

type offlineHistoryTransport struct{ *checkpointTransport }

func (t offlineHistoryTransport) Append(context.Context, string, contracts.RelayCommit) (contracts.RelayReceipt, error) {
	return contracts.RelayReceipt{}, errors.New("offline")
}

func TestCompactedHistoryRecoversOfflinePendingAndLostReceipts(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint("receipt_lost_", lost), func(t *testing.T) {
			ctx := context.Background()
			server, err := relay.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			left, producer, lt := replica(t, server, "a", "left")
			left.Remote = &checkpointTransport{localTransport: lt}
			right, worker, rt := replica(t, server, "b", "right")
			remote := &checkpointTransport{localTransport: rt}
			right.Remote = remote
			if _, err := producer.Capture(ctx, source("first", "先看看目前的进度。")); err != nil {
				t.Fatal(err)
			}
			if err := right.Sync(ctx, "project"); err != nil {
				t.Fatal(err)
			}
			if lost {
				rt.loseReceipt = true
			} else {
				right.Remote = offlineHistoryTransport{remote}
			}
			if _, err := worker.Capture(ctx, source("pending", "补充一下错误提示，先不要发布。")); err == nil {
				t.Fatal("publication should remain pending")
			}
			pending, err := right.Store.Pending("project")
			if err != nil || len(pending) != 1 {
				t.Fatal(pending, err)
			}
			native := pending[0].Snapshot.Artifact
			if err := left.Sync(ctx, "project"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 15; i++ {
				if _, err := producer.Capture(ctx, source(fmt.Sprintf("branch_%d", i), "理解，再检查一下这个改动。")); err != nil {
					t.Fatal(err)
				}
			}
			if err := left.PublishCheckpoint(ctx, "project"); err != nil {
				t.Fatal(err)
			}
			if n, err := server.CompactHistory(ctx, 0, 1024); err != nil || n < 15 {
				t.Fatal(n, err)
			}
			right.Remote = remote
			before, _ := right.Store.State("project")
			remote.broken = true
			if err := right.Sync(ctx, "project"); err == nil {
				t.Fatal("corrupted checkpoint accepted")
			}
			after, _ := right.Store.State("project")
			if workgraph.Digest(before) != workgraph.Digest(after) {
				t.Fatal("bad checkpoint changed state")
			}
			remote.broken = false
			if err := right.Sync(ctx, "project"); err != nil {
				t.Fatal(err)
			}
			want, _ := left.Store.State("project")
			actual, _ := right.Store.State("project")
			if workgraph.Digest(actual) != workgraph.Digest(want) {
				t.Fatal("replicas diverged")
			}
			if _, err := right.Store.Object(native); err != nil {
				t.Fatal("local original was lost", err)
			}
			remaining, _ := right.Store.Pending("project")
			if lost && len(remaining) != 0 || !lost && len(remaining) != 1 {
				t.Fatal("pending work was lost or duplicated", remaining)
			}
			if lost {
				if ok, err := right.Store.HasCommit(pending[0]); err != nil || !ok {
					t.Fatal("lost receipt was not reconciled", err)
				}
			}
			replayed, err := right.Store.Replay("project")
			if err != nil || workgraph.Digest(replayed) != workgraph.Digest(actual) {
				t.Fatal("baseline replay failed", err)
			}
			if rt.gets != 0 {
				t.Fatal("metadata recovery fetched raw sessions")
			}
			if err := worker.Recover(ctx, "project"); err != nil {
				t.Fatal("outbox failed to resume", err)
			}
			remaining, _ = right.Store.Pending("project")
			if len(remaining) != 0 {
				t.Fatal("pending work did not finish")
			}
			final, _ := right.Store.State("project")
			wantRevision := actual.Revision
			if !lost {
				wantRevision++
			}
			if final.Revision != wantRevision {
				t.Fatal("lost receipt was executed again", final.Revision, wantRevision)
			}
		})
	}
}
