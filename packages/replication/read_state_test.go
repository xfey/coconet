package replication

import (
	"context"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

type brokenStateTail struct{ *checkpointTransport }

func (r brokenStateTail) Since(ctx context.Context, p string, after int64) (contracts.RelayPage, error) {
	page, err := r.checkpointTransport.Since(ctx, p, after)
	if len(page.Entries) > 0 {
		page.Entries[0].Commit.StateSHA256 = workgraph.Digest("wrong result")
	}
	return page, err
}

func TestReadStateReplaysMetadataAndRejectsCorruptCheckpointsOrTail(t *testing.T) {
	storage, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	client, engine, transport := replica(t, storage, "a", "device")
	remote := &checkpointTransport{localTransport: transport}
	client.Remote = remote
	ctx := context.Background()
	if _, err := engine.Capture(ctx, source("first", "理解，先整理基础行为。")); err != nil {
		t.Fatal(err)
	}
	if err := client.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Capture(ctx, source("second", "继续补充协作场景。")); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(ctx, remote, "project")
	if err != nil {
		t.Fatal(err)
	}
	accepted, _ := client.Store.State("project")
	if workgraph.Digest(state) != workgraph.Digest(accepted) {
		t.Fatal("metadata replay differs from local replica")
	}
	if _, err := ReadState(ctx, brokenStateTail{remote}, "project"); err == nil {
		t.Fatal("corrupt tail accepted")
	}
	remote.broken = true
	if _, err := ReadState(ctx, remote, "project"); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
}
