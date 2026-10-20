package replication

import (
	"context"
	"fmt"
	"testing"

	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestEditableRewindReplicatesAndBootstraps(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, producer, lt := replica(t, server, "alice", "left")
	right, supplier, rt := replica(t, server, "bob", "right")
	left.Remote = &checkpointTransport{localTransport: lt}
	right.Remote = &checkpointTransport{localTransport: rt}
	stateOf := func(c *Client) workgraph.State {
		t.Helper()
		s, err := c.Store.State("project")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	apply := func(client *Client, configure func(*workgraph.Commit, workgraph.State)) {
		t.Helper()
		s := stateOf(client)
		c := workgraph.Commit{Schema: 1, ID: fmt.Sprintf("commit_%d", s.Revision), ProjectID: "project", Actor: client.Writer(), Base: s.Revision, RunID: "test_run", At: "2026-10-13T00:00:00Z"}
		configure(&c, s)
		if err := client.Publish(ctx, c); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Store.Apply(c, false); err != nil {
			t.Fatal(err)
		}
	}
	stage := func(client *Client, src workgraph.Source, id string) {
		apply(client, func(c *workgraph.Commit, s workgraph.State) {
			c.Stage = &workgraph.StageDecision{Checkpoint: true, Source: src, SessionRevision: s.Sessions[src.SessionID].Revision, Action: "start", NewNodeID: id, Text: &workgraph.Text{Objective: id}, Reason: "new work"}
		})
	}
	capture := func(e *localdag.Engine, input localdag.Capture) workgraph.Source {
		t.Helper()
		for i := range input.Entries {
			input.Entries[i].Turn.Ordinal = i
			input.Entries[i].Turn.TurnID = fmt.Sprintf("turn_%d", i)
		}
		src, err := e.Capture(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		return src
	}
	peerSource := capture(supplier, source("supplier", "供应实现"))
	stage(right, peerSource, "supplier")
	input := source("author", "先做第一步")
	a := capture(producer, input)
	stage(left, a, "a")
	input.Entries = append(input.Entries, source("author", "完成第二步").Entries...)
	b := capture(producer, input)
	stage(left, b, "b")
	input.Entries = append(input.Entries, source("author", "整合第三步").Entries...)
	c := capture(producer, input)
	stage(left, c, "c")
	apply(left, func(commit *workgraph.Commit, s workgraph.State) {
		p, child := s.Nodes["supplier"], s.Nodes["c"]
		commit.Relations = &workgraph.RelationDecision{NodeID: child.ID, NodeRevision: child.Revision, Through: child.EndTurn, Complete: true, Reason: "uses supplier", Parents: []workgraph.Parent{{NodeID: p.ID, Revision: p.Revision, Source: p.Source, Reason: "uses result", Evidence: []string{"e3"}}}}
	})
	if err := right.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if stateOf(right).Nodes["supplier"].State != "sealed" {
		t.Fatal("supplier not pinned")
	}
	input.Entries, input.Authoritative = input.Entries[:2], true
	r := capture(producer, input)
	if err := right.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	want := stateOf(left)
	if want.Nodes["b"].State != "current" || want.Nodes["supplier"].State != "current" || len(want.Nodes) != 3 || workgraph.Digest(want) != workgraph.Digest(stateOf(right)) {
		t.Fatal("replicas did not restore both editable tails")
	}
	if err := left.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	page, err := server.Checkpoint("project")
	if err != nil || page.Checkpoint == nil {
		t.Fatal("checkpoint missing", err)
	}
	cp := *page.Checkpoint
	if cp.Checkpoint.Codec != "coconet-state-json-v4" || cp.Anchor.Commit.Codec != "coconet-json-v6" {
		t.Fatal("new semantics lack protocol version")
	}
	// Relabelling new semantics as an older codec must fail before applying it.
	badCommit := cp.Anchor
	badCommit.Commit.Codec = "coconet-json-v4"
	if _, err := decodeReceipt("project", badCommit); err == nil {
		t.Fatal("new rewrite accepted as legacy")
	}
	badCheckpoint := cp
	badCheckpoint.Checkpoint.Codec = "coconet-state-json-v2"
	if _, _, err := decodeCheckpoint("project", badCheckpoint); err == nil {
		t.Fatal("new checkpoint accepted as legacy")
	}
	fresh, _, ft := replica(t, server, "carol", "fresh")
	fresh.Remote = &checkpointTransport{localTransport: ft}
	if err := fresh.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if workgraph.Digest(stateOf(fresh)) != workgraph.Digest(want) {
		t.Fatal("checkpoint bootstrap changed mutability")
	}
	for _, client := range []*Client{left, right, fresh} {
		replay, err := client.Store.Replay("project")
		if err != nil || workgraph.Digest(replay) != workgraph.Digest(want) {
			t.Fatal("replay diverged", err)
		}
	}
	if !want.Snapshots[r.Key()].Rewrite.RestoreEditableTails {
		t.Fatal("capture omitted restoration")
	}
	// A no-content rewind is processed without another model decision; the
	// next actual edit continues the same node on both replicas.
	apply(left, func(commit *workgraph.Commit, s workgraph.State) {
		commit.Stage = &workgraph.StageDecision{Checkpoint: true, Source: r, SessionRevision: s.Sessions[r.SessionID].Revision, Action: "none", Reason: "no new dialogue"}
	})
	input.Entries = append(input.Entries, source("author", "调整第二步").Entries...)
	d := capture(producer, input)
	apply(left, func(commit *workgraph.Commit, s workgraph.State) {
		commit.Stage = &workgraph.StageDecision{Checkpoint: true, Source: d, SessionRevision: s.Sessions[d.SessionID].Revision, Action: "continue", Text: &workgraph.Text{Objective: "第二步更新"}, Reason: "same work"}
	})
	if err := right.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	updated := stateOf(right)
	if updated.Nodes["b"].Source != d || updated.Sessions[d.SessionID].Tail != "b" || len(updated.Nodes) != 3 {
		t.Fatal("continued work was split")
	}
}
