package replication

import (
	"context"
	"testing"

	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestLibraryPinsReplicateBootstrapAndProtectExactContent(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, author, lt := replica(t, server, "alice", "a")
	right, colleague, rt := replica(t, server, "bob", "b")
	left.Remote, right.Remote = &checkpointTransport{localTransport: lt}, &checkpointTransport{localTransport: rt}
	v1, err := author.Capture(ctx, source("native", "保存这段重试说明，以后可以参考。"))
	if err != nil {
		t.Fatal(err)
	}
	state, _ := left.Store.State("project")
	c := workgraph.Commit{Schema: 1, ID: "start", ProjectID: "project", Actor: left.Writer(), Base: state.Revision, RunID: "start", At: "2026-10-20T00:00:00Z", Stage: &workgraph.StageDecision{Source: v1, Action: "start", NewNodeID: "work", Text: &workgraph.Text{Objective: "完成重试说明"}, Reason: "fixture"}}
	if err := left.Publish(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := left.Store.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	if err := right.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	request := localdag.LibraryRequest{RequestID: "collect-colleague", NodeID: "work", Source: v1}
	if _, err := colleague.Collect(ctx, "project", request); err != nil {
		t.Fatal(err)
	}
	if err := right.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	page, _ := server.Checkpoint("project")
	bad := *page.Checkpoint
	bad.Anchor.Commit.Codec = "coconet-json-v5"
	if _, err := decodeReceipt("project", bad.Anchor); err == nil {
		t.Fatal("pin accepted as legacy commit")
	}
	bad = *page.Checkpoint
	bad.Checkpoint.Codec = "coconet-state-json-v3"
	if _, _, err := decodeCheckpoint("project", bad); err == nil {
		t.Fatal("pin accepted as legacy checkpoint")
	}
	if err := left.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	input := source("native", "保存这段重试说明，以后可以参考。")
	more := source("native", "继续验证并发恢复。").Entries[0]
	more.Turn.Ordinal, more.Turn.TurnID = 1, "second-turn"
	input.Entries = append(input.Entries, more)
	v2, err := author.Capture(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = left.Store.State("project")
	c.ID, c.RunID, c.Base = "continue", "continue", state.Revision
	c.Stage = &workgraph.StageDecision{Source: v2, SessionRevision: state.Sessions[v1.SessionID].Revision, Action: "continue", Reason: "same work"}
	if err := left.Publish(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := left.Store.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	if err := left.SyncContents(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := left.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	fresh, reader, ft := replica(t, server, "carol", "c")
	fresh.Remote = &checkpointTransport{localTransport: ft}
	if err := fresh.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ = fresh.Store.State("project")
	if state.Nodes["work"].Source != v2 || state.LibrarySources["work"].Source != v1 {
		t.Fatal("bootstrap substituted the saved version")
	}
	read, err := reader.ReadSelectedNode(ctx, "project", "work", v1, true)
	if err != nil || read.Source != v1 || read.NativePath == "" {
		t.Fatal("peer could not materialize the pinned version", read, err)
	}
	if _, err := reader.Uncollect(ctx, "project", localdag.LibraryRequest{RequestID: "remove-colleague", NodeID: "work", Source: v2}); err == nil {
		t.Fatal("stale removal accepted")
	}
	if _, err := reader.Uncollect(ctx, "project", localdag.LibraryRequest{RequestID: "remove-colleague-fixed", NodeID: "work", Source: v1}); err != nil {
		t.Fatal(err)
	}
	if err := left.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ = left.Store.State("project")
	if len(state.Library) != 0 || state.Nodes["work"].Source != v2 || state.Nodes["work"].State != "current" {
		t.Fatal("peer removal changed author work")
	}
}
