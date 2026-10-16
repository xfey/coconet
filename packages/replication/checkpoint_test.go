package replication

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

type checkpointTransport struct {
	*localTransport
	cursors []int64
	broken  bool
	lost    bool
	puts    int
}

func (t *checkpointTransport) Since(ctx context.Context, p string, after int64) (contracts.RelayPage, error) {
	t.cursors = append(t.cursors, after)
	return t.localTransport.Since(ctx, p, after)
}
func (t *checkpointTransport) Checkpoint(_ context.Context, p string) (contracts.RelayCheckpointPage, error) {
	page, err := t.s.Checkpoint(p)
	if t.broken && page.Checkpoint != nil {
		page.Checkpoint.Checkpoint.Payload[0] = '!'
	}
	return page, err
}
func (t *checkpointTransport) PutCheckpoint(_ context.Context, p string, in contracts.RelayCheckpoint) (contracts.RelayCheckpointAck, error) {
	t.puts++
	r, err := t.s.PutCheckpoint(p, t.account, in)
	if errors.Is(err, relay.ErrConflict) {
		return r.Acknowledgement(), ErrConflict
	}
	if err == nil && t.lost {
		t.lost = false
		return r.Acknowledgement(), errors.New("lost checkpoint confirmation")
	}
	return r.Acknowledgement(), err
}

func TestCheckpointBootstrapDeltasPullAndRecovery(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, producer, lt := replica(t, server, "a", "left")
	transport := &checkpointTransport{localTransport: lt}
	left.Remote = transport
	source, err := producer.Capture(ctx, source("native", "理解，先把重试说明写清楚。"))
	if err != nil {
		t.Fatal(err)
	}
	apply := func(id string, stage *workgraph.StageDecision, relation *workgraph.RelationDecision) {
		t.Helper()
		state, _ := left.Store.State("project")
		c := workgraph.Commit{Schema: 1, ID: id, ProjectID: "project", Actor: left.Writer(), Base: state.Revision, RunID: "run_" + id, At: "2026-10-09T00:00:00Z", Stage: stage, Relations: relation}
		if err := left.Publish(ctx, c); err != nil {
			t.Fatal(err)
		}
		if _, err := left.Store.Apply(c, false); err != nil {
			t.Fatal(err)
		}
	}
	apply("stage", &workgraph.StageDecision{Source: source, Action: "start", Text: &workgraph.Text{Objective: "完善重试说明", Summary: "已明确停止条件，尚未发布。"}, NewNodeID: "node", Reason: "明确交付"}, nil)
	transport.lost = true
	if err := left.PublishCheckpoint(ctx, "project"); err == nil {
		t.Fatal("lost confirmation hidden")
	}
	if err := left.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if transport.puts != 1 {
		t.Fatal("lost acknowledgement caused duplicate upload")
	}
	page, _ := server.Checkpoint("project")
	if page.Head != 2 || page.Checkpoint == nil {
		t.Fatal("checkpoint not published")
	}
	// The cached revision is deliberately behind the log.
	apply("relations", nil, &workgraph.RelationDecision{NodeID: "node", NodeRevision: 1, Through: 1, Complete: true, Reason: "无实际依赖"})
	right, _, rt := replica(t, server, "b", "right")
	download := &checkpointTransport{localTransport: rt}
	right.Remote = download
	if err := right.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	a, _ := left.Store.State("project")
	b, _ := right.Store.State("project")
	if workgraph.Digest(a) != workgraph.Digest(b) || len(download.cursors) != 1 || download.cursors[0] != 2 || rt.gets != 0 {
		t.Fatalf("bootstrap did not use checkpoint + deltas: %v", download.cursors)
	}
	history, _ := right.Store.History("project")
	if len(history) != 1 || history[0].Commit.ID != "relations" {
		t.Fatal("checkpoint pretended to replay earlier commits")
	}
	replay, err := right.Store.Replay("project")
	if err != nil || workgraph.Digest(replay) != workgraph.Digest(a) {
		t.Fatalf("replay from baseline: %v", err)
	}
	if err := left.SyncContents(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := right.Pull(ctx, "project", source, false); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Store.Object(a.Snapshots[source.Key()].Artifact); err == nil {
		t.Fatal("compact pull fetched native")
	}
	if err := right.Pull(ctx, "project", source, true); err != nil {
		t.Fatal(err)
	}
	if err := right.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.PutCheckpoint("project", "a", page.Checkpoint.Checkpoint); !errors.Is(err, relay.ErrConflict) {
		t.Fatal("old checkpoint replaced newer")
	}
	// Existing replicas keep validating deltas and never replace their state.
	transport.broken = true
	if err := left.Sync(ctx, "project"); err != nil {
		t.Fatal("existing replica used remote checkpoint", err)
	}
	// A fresh replica rejects corrupt bytes without changing its cursor.
	fresh, _, ft := replica(t, server, "c", "fresh")
	fresh.Remote = &checkpointTransport{localTransport: ft, broken: true}
	if err := fresh.Sync(ctx, "project"); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
	f, _ := fresh.Store.State("project")
	if f.Revision != 0 {
		t.Fatal("failed bootstrap changed state")
	}
	logs, _ := fresh.Store.Transfers("project", 10)
	if len(logs) != 2 || logs[0].Status != "failed" || logs[1].Operation != "checkpoint_bootstrap" || logs[1].SHA256 == "" {
		t.Fatal("bootstrap failure bytes or metadata not archived")
	}
	if _, err := fresh.Store.Object(logs[1].SHA256); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointCannotHidePendingLostAcknowledgement(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, producer, lt := replica(t, server, "a", "left")
	left.Remote = &checkpointTransport{localTransport: lt}
	lt.loseReceipt = true
	if _, err := producer.Capture(ctx, source("native", "先检查登录。")); err == nil {
		t.Fatal("expected lost receipt")
	}
	peer, _, pt := replica(t, server, "b", "peer")
	peer.Remote = &checkpointTransport{localTransport: pt}
	if err := peer.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := peer.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	// This device is recovering its own accepted publication, not bootstrapping.
	// An unrelated broken checkpoint must not hide the valid commit log.
	left.Remote = &checkpointTransport{localTransport: lt, broken: true}
	if err := left.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	runs, _ := left.Store.Runs("project")
	pending, _ := left.Store.Pending("project")
	history, _ := left.Store.History("project")
	if len(runs) != 1 || runs[0].Status != "published" || len(pending) != 0 || len(history) != 1 {
		t.Fatal("bootstrap hid local publication recovery")
	}
}

func TestCheckpointLostAcknowledgementSurvivesBothStoreRestarts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	server, err := relay.Open(filepath.Join(root, "relay"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	store, err := graphstore.Open(filepath.Join(root, "device"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	transport := &checkpointTransport{localTransport: &localTransport{s: server, account: "a"}, lost: true}
	client := &Client{Store: store, Remote: transport, Account: "a", Device: "device"}
	engine := &localdag.Engine{Store: store, Publisher: client, Actor: client.Writer()}
	if _, err := engine.Capture(ctx, source("native", "先把重试说明写清楚。")); err != nil {
		t.Fatal(err)
	}
	if err := client.PublishCheckpoint(ctx, "project"); err == nil {
		t.Fatal("lost response must remain visible")
	}
	before, err := server.Checkpoint("project")
	if err != nil || before.Checkpoint == nil {
		t.Fatalf("checkpoint was not durable: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server, err = relay.Open(filepath.Join(root, "relay"))
	if err != nil {
		t.Fatal(err)
	}
	store, err = graphstore.Open(filepath.Join(root, "device"))
	if err != nil {
		t.Fatal(err)
	}
	client.Store = store
	transport.localTransport.s = server
	if err := client.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	after, err := server.Checkpoint("project")
	if err != nil || after.Generation != before.Generation || after.Checkpoint.AcceptedAt != before.Checkpoint.AcceptedAt {
		t.Fatalf("retry duplicated publication: %v", err)
	}
	marked, err := store.PublishedCheckpoint("project")
	if err != nil || marked != after.Head {
		t.Fatalf("local confirmation not recovered: %v", err)
	}
	logs, err := store.Transfers("project", 20)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{}
	for _, log := range logs {
		if log.Operation == "checkpoint_publish" {
			statuses[log.Status]++
		}
	}
	if statuses["failed"] != 1 || statuses["already_published"] != 1 {
		t.Fatalf("recovery trace lost: %v", statuses)
	}
	if err := client.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	logsAfter, _ := store.Transfers("project", 20)
	if len(logsAfter) != len(logs) {
		t.Fatal("idle publication repeated")
	}
	peer, _, pt := replica(t, server, "b", "fresh")
	peer.Remote = &checkpointTransport{localTransport: pt}
	if err := peer.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	want, _ := store.State("project")
	got, err := peer.Store.Replay("project")
	if err != nil || workgraph.Digest(got) != workgraph.Digest(want) || pt.gets != 0 {
		t.Fatalf("reopened baseline differs: %v", err)
	}
}

func TestResultDigestMismatchDoesNotApplyCommit(t *testing.T) {
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	c, producer, _ := replica(t, server, "a", "left")
	if _, err := producer.Capture(context.Background(), source("native", "检查。")); err != nil {
		t.Fatal(err)
	}
	page, _ := server.Since("project", 0, 100)
	r := page.Entries[0]
	r.Commit.StateSHA256 = workgraph.Digest("wrong")
	peer, _, _ := replica(t, server, "b", "peer")
	commit, err := decodeReceipt("project", r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Store.ApplyVerified(commit, false, r.Commit.StateSHA256); err == nil {
		t.Fatal("incorrect post-state accepted")
	}
	state, _ := peer.Store.State("project")
	if state.Revision != 0 {
		t.Fatal("digest check happened after apply")
	}
	// Rehashed malformed state still cannot bootstrap.
	good, _ := c.Store.State("project")
	session := good.Sessions[commit.Snapshot.SessionID]
	session.Latest = 10
	good.Sessions[session.ID] = session
	data, _ := json.Marshal(good)
	cp := contracts.RelayCheckpointReceipt{SchemaVersion: 1, ProjectID: "project", Writer: c.Writer(), Checkpoint: contracts.RelayCheckpoint{SchemaVersion: 1, Codec: "coconet-state-json-v1", Revision: 1, CommitID: commit.ID, SHA256: graphstore.ObjectDigest(data), Payload: data}, Anchor: r}
	cp.Anchor.Commit.StateSHA256 = cp.Checkpoint.SHA256
	if _, _, err := decodeCheckpoint("project", cp); err == nil {
		t.Fatal("invalid state accepted")
	}
}

func TestPublishRejectsUncheckpointableGraphBeforeRemoteIO(t *testing.T) {
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, _, transport := replica(t, server, "a", "left")
	// Construct a valid synthetic baseline just below capacity. Every snapshot
	// remains pending, so it cannot be silently discarded to fit the checkpoint.
	state := workgraph.New("project")
	state.Revision = 32000
	for i := 1; i <= 32000; i++ {
		src := workgraph.Source{SessionID: "session", Version: i}
		state.Snapshots[src.Key()] = workgraph.Snapshot{Source: src, Owner: client.Writer(), Agent: "fixture", NativeID: "native", Digest: workgraph.Digest(src), Conversation: workgraph.Digest("body"), Turns: i, ObservedAt: "2026-10-09T00:00:00Z"}
	}
	state.Sessions["session"] = workgraph.Session{ID: "session", Owner: client.Writer(), Agent: "fixture", NativeID: "native", Latest: 32000, Items: []workgraph.Item{}}
	data, _ := json.Marshal(state)
	// Library notes and node text are bounded separately. Fill remaining room
	// using the native metadata of a single source rather than huge allocations.
	padding := contracts.MaxRelayCheckpointBytes - len(data) - 2048
	if padding <= 0 {
		t.Fatal("fixture grew past baseline capacity")
	}
	src := workgraph.Source{SessionID: "padding", Version: 1}
	native := strings.Repeat("n", padding/2)
	state.Snapshots[src.Key()] = workgraph.Snapshot{Source: src, Owner: client.Writer(), Agent: "fixture", NativeID: native, Digest: workgraph.Digest(src), Conversation: workgraph.Digest("body"), Turns: 1, ObservedAt: "2026-10-09T00:00:00Z"}
	state.Sessions[src.SessionID] = workgraph.Session{ID: src.SessionID, Owner: client.Writer(), Agent: "fixture", NativeID: native, Latest: 1, Items: []workgraph.Item{}}
	data, _ = json.Marshal(state)
	if len(data) >= contracts.MaxRelayCheckpointBytes {
		t.Fatal("baseline must fit")
	}
	anchor := workgraph.Commit{Schema: 1, ID: "baseline", ProjectID: "project", Actor: client.Writer(), Base: state.Revision - 1, RunID: "run", At: "2026-10-09T00:00:00Z"}
	receipt := contracts.RelayCheckpointReceipt{ProjectID: "project", Checkpoint: contracts.RelayCheckpoint{Revision: state.Revision, CommitID: anchor.ID, SHA256: workgraph.Digest(state), Payload: data}}
	if err := client.Store.InstallCheckpoint(state, receipt, anchor); err != nil {
		t.Fatal(err)
	}
	nextSource := workgraph.Source{SessionID: "overflow", Version: 1}
	commit := workgraph.Commit{Schema: 1, ID: "overflow", ProjectID: "project", Actor: client.Writer(), Base: state.Revision, RunID: "run_overflow", At: anchor.At, Snapshot: &workgraph.Snapshot{Source: nextSource, Owner: client.Writer(), Agent: "fixture", NativeID: strings.Repeat("x", 2048), Digest: workgraph.Digest(nextSource), Conversation: workgraph.Digest("unavailable-body"), Turns: 1, ObservedAt: anchor.At}}
	if err := client.Publish(context.Background(), commit); !errors.Is(err, ErrCheckpointCapacity) {
		t.Fatal("capacity guard did not run before content lookup", err)
	}
	if transport.puts != 0 {
		t.Fatal("uploaded before capacity check")
	}
	page, err := server.Since("project", 0, 100)
	if err != nil || page.Head != 0 {
		t.Fatal("remote graph advanced", page.Head, err)
	}
	actual, _ := client.Store.State("project")
	if workgraph.Digest(actual) != workgraph.Digest(state) {
		t.Fatal("capacity rejection changed local baseline")
	}
	logs, err := client.Store.Transfers("project", 10)
	if err != nil || len(logs) != 1 || logs[0].Status != "failed" {
		t.Fatal("capacity error not traceable", logs, err)
	}
}

func (t *checkpointTransport) CheckpointHead(_ context.Context, p string) (contracts.RelayCheckpointHead, error) {
	return t.s.CheckpointHead(p)
}

func TestCheckpointPreflightAndDeferredWindows(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, producer, lt := replica(t, server, "a", "left")
	transport := &checkpointTransport{localTransport: lt}
	left.Remote = transport
	if _, err := producer.Capture(ctx, source("native", "先检查副本是否完整")); err != nil {
		t.Fatal(err)
	}
	if err := left.PublishCheckpointDeferred(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if transport.puts != 0 {
		t.Fatal("background did not debounce")
	}
	w := left.checkpointPending["project"]
	w.changed = time.Now().Add(-3 * time.Second)
	left.checkpointPending["project"] = w
	if err := left.PublishCheckpointDeferred(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if transport.puts != 1 {
		t.Fatal("quiet window did not publish")
	}
	right, _, rt := replica(t, server, "b", "right")
	remote := &checkpointTransport{localTransport: rt}
	right.Remote = remote
	if err := right.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := right.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if remote.puts != 0 {
		t.Fatal("peer reuploaded existing checkpoint")
	}
	if _, err := producer.Capture(ctx, source("next-native", "继续检查新增内容")); err != nil {
		t.Fatal(err)
	}
	// New revisions cannot postpone publication forever.
	left.checkpointPending["project"] = checkpointWindow{revision: 1, first: time.Now().Add(-11 * time.Second), changed: time.Now()}
	if err := left.PublishCheckpointDeferred(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if transport.puts != 2 {
		t.Fatal("maximum window did not publish")
	}
	page, err := server.Checkpoint("project")
	if err != nil {
		t.Fatal(err)
	}
	ack, _ := json.Marshal(page.Checkpoint.Acknowledgement())
	full, _ := json.Marshal(page.Checkpoint)
	if len(ack) > 512 || len(ack) >= len(full) {
		t.Fatal("checkpoint reply is not bounded metadata")
	}
	t.Logf("checkpoint receipt: full=%d bytes, ack=%d bytes; duplicate peer uploads=0", len(full), len(ack))
}
