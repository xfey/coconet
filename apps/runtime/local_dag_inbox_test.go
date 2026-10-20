package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestResumedForkInboxWaitsForBaselineWithoutExhaustingCaptureRetries(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: "device"}
	observation := func(extra bool) localdag.Observation {
		turns := []contracts.NormalizedTurn{{TurnID: "old", Ordinal: 0, Role: "assistant", Kind: "message", Text: "已完成计数接口。", ContentHash: adapters.Digest("old")}}
		if extra {
			turns = append(turns, contracts.NormalizedTurn{TurnID: "new", Ordinal: 1, Role: "user", Kind: "message", Text: "接下来补一个边界测试。", ContentHash: adapters.Digest("new")})
		}
		normalized, _ := adapters.ApplyCheckpoint(turns, adapters.ObservationCheckpoint{})
		visible := &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor}
		for _, turn := range turns {
			visible.Turns = append(visible.Turns, contracts.VisibleTextTurn{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: len(turn.Text)}}})
		}
		return localdag.Observation{ProjectID: "project", NativeID: "child", Agent: "fixture", Normalized: normalized, Visible: visible, Inherited: !extra}
	}
	baseline, err := engine.QueueInheritedObservation(context.Background(), observation(false))
	if err != nil {
		t.Fatal(err)
	}
	request := localDAGObservationRequest{ProjectID: "project", Agent: "fixture", NativeID: "child", ConfigRoot: "/nonexistent-original-history"}
	if err := enqueueLocalDAGObservation(root, request); err != nil {
		t.Fatal(err)
	}
	paths, _ := localDAGInboxFiles(root)
	event, _ := readLocalDAGInbox(paths[0])
	data, _ := json.Marshal(observation(true))
	event.Prepared, err = store.PutObject(data)
	if err != nil {
		t.Fatal(err)
	}
	event.Attempts = 7
	for i := 0; i < 10; i++ {
		event.NextAt = ""
		if err := atomicWriteJSON(paths[0], event, 0600); err != nil {
			t.Fatal(err)
		}
		if err := drainLocalDAGInbox(context.Background(), root, engine); !errors.Is(err, localdag.ErrCapturePending) {
			t.Fatal("pending baseline was not treated as a dependency", err)
		}
		event, err = readLocalDAGInbox(paths[0])
		if err != nil || event.Paused || event.Attempts != 7 || event.Prepared == "" || event.NextAt == "" {
			t.Fatal("waiting lost the prepared copy or exhausted capture retries", event, err)
		}
	}
	runs, _ := store.Runs("project")
	for _, run := range runs {
		if run.Kind == "observation" && run.Status != "deferred" {
			t.Fatal("dependency wait was logged as failure", run)
		}
	}
	if err := engine.Flush(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	event.NextAt = ""
	if err := atomicWriteJSON(paths[0], event, 0600); err != nil {
		t.Fatal(err)
	}
	if err := drainLocalDAGInbox(context.Background(), root, engine); err != nil {
		t.Fatal("resumed capture did not recover automatically", err)
	}
	queue, _ := localDAGInbox(root)
	state, _ := store.State("project")
	session := state.Sessions[baseline.SessionID]
	if len(queue) != 0 || session.Latest != 2 || session.ProcessedThrough != 1 || session.ConsumedTurns != 1 {
		t.Fatal("lost or misattributed resumed work", queue, state)
	}
	entries := []conversation.Entry{}
	snapshot := state.Snapshots[(workgraph.Source{SessionID: session.ID, Version: session.Latest}).Key()]
	if err := store.WalkConversation(context.Background(), snapshot.Conversation, snapshot.Turns, func(entry conversation.Entry) error {
		entries = append(entries, entry)
		return nil
	}); err != nil || len(entries) != 2 || !entries[0].Inherited || entries[1].Inherited {
		t.Fatal("inherited boundary did not survive waiting", entries, err)
	}
}

func TestManagedHookPinsOriginAndQueuesEveryEventWhileDaemonIsStopped(t *testing.T) {
	root, workspace, other, profile := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("COCONET_LOCAL_DAG_HOME", root)
	t.Setenv("CODEX_HOME", profile)
	t.Setenv("COCONET_DISABLE_BACKGROUND_SYNC", "1")
	config := localDAGServiceConfig{Schema: 1, Actor: "device", Provider: "codex", Command: "codex-fixture", Profile: filepath.Join(root, "inference"), Model: "fixture", MaxCalls: 1, TimeoutSeconds: 120, Workspaces: []localDAGWorkspace{{Path: workspace, ProjectID: "project"}, {Path: other, ProjectID: "other"}}}
	if err := atomicWriteJSON(filepath.Join(root, "runtime.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	event := hookEvent{HookEventName: "SessionStart", SessionID: "native", CWD: workspace}
	invoke := func() {
		data, _ := json.Marshal(event)
		var out bytes.Buffer
		if code := runHook([]string{"--agent", "codex"}, bytes.NewReader(data), &out, &out); code != 0 || event.HookEventName != "SessionStart" && out.Len() != 0 {
			t.Fatal("managed hook blocked the conversation")
		}
	}
	invoke()
	event.HookEventName, event.CWD = "Stop", other
	invoke()
	invoke()
	queue, err := localDAGInbox(root)
	if err != nil || len(queue) != 2 {
		t.Fatalf("queue did not retain distinct observations: %+v %v", queue, err)
	}
	for _, item := range queue {
		if !samePath(item.Request.CWD, workspace) || !samePath(item.Request.Root, workspace) || item.Request.ProjectID != "project" || !samePath(item.Request.ConfigRoot, profile) {
			t.Fatal("dynamic cwd transferred Session ownership")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "graph.db")); !os.IsNotExist(err) {
		t.Fatal("Hook opened the daemon's graph database")
	}
}

func TestPreparedInboxRetryDoesNotReadNativeHistoryAgain(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: "device"}
	text := "完善失败提示。"
	turn := contracts.NormalizedTurn{TurnID: "t", Ordinal: 0, Role: "user", Kind: "message", Text: text, ContentHash: adapters.Digest(text)}
	normalized, _ := adapters.ApplyCheckpoint([]contracts.NormalizedTurn{turn}, adapters.ObservationCheckpoint{})
	observation := localdag.Observation{ProjectID: "project", Agent: "fixture", NativeID: "native", Normalized: normalized, Visible: &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{{TurnID: "t", ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: len(text)}}}}}}
	data, _ := json.Marshal(observation)
	prepared, err := store.PutObject(data)
	if err != nil {
		t.Fatal(err)
	}
	request := localDAGObservationRequest{ProjectID: "project", Agent: "fixture", NativeID: "native", ConfigRoot: "/nonexistent-original-history"}
	if err := enqueueLocalDAGObservation(root, request); err != nil {
		t.Fatal(err)
	}
	paths, _ := localDAGInboxFiles(root)
	event, err := readLocalDAGInbox(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	event.Prepared = prepared
	if err := atomicWriteJSON(paths[0], event, 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after publication but before removing the inbox event.
	if err := processLocalDAGInbox(context.Background(), root, engine, paths[0], &event); err != nil {
		t.Fatal(err)
	}
	if err := drainLocalDAGInbox(context.Background(), root, engine); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	queue, err := localDAGInbox(root)
	if err != nil || len(queue) != 0 || state.Revision != 1 || len(state.Snapshots) != 1 {
		t.Fatal("inbox recovery duplicated or lost fixed capture", err)
	}
	runs, err := store.Runs("project")
	if err != nil || len(runs) != 2 {
		t.Fatal("capture inbox receipt was not preserved after event deletion", err)
	}
	found := false
	for _, run := range runs {
		found = found || run.Kind == "observation" && run.Status == "captured" && run.TaskID == "inbox/"+event.ID && run.Output != ""
	}
	if !found {
		t.Fatal("missing successful observation receipt")
	}
}

func TestInboxRetryPreservesPreparedCopyAndFailureEvidence(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := localDAGObservationRequest{ProjectID: "project", Agent: "fixture", NativeID: "native"}
	if err := enqueueLocalDAGObservation(root, request); err != nil {
		t.Fatal(err)
	}
	paths, _ := localDAGInboxFiles(root)
	event, _ := readLocalDAGInbox(paths[0])
	event.Attempts = 7
	event.Prepared = adapters.Digest("missing fixed observation")
	if err := atomicWriteJSON(paths[0], event, 0600); err != nil {
		t.Fatal(err)
	}
	engine := &localdag.Engine{Store: store, Actor: "device"}
	if err := drainLocalDAGInbox(context.Background(), root, engine); err == nil {
		t.Fatal("missing evidence succeeded")
	}
	paused, _ := readLocalDAGInbox(paths[0])
	if paused.Paused || paused.Attempts != 8 || paused.NextAt == "" {
		t.Fatal("missing object must retain automatic retry")
	}
	// Explicit retry also clears a content-change wait, without deleting its receipt.
	paused.Paused = true
	paused.FailedFiles, paused.FailedRevision = []string{"fixture"}, "old"
	if err := atomicWriteJSON(paths[0], paused, 0600); err != nil {
		t.Fatal(err)
	}
	if err := retryLocalDAGInbox(root, event.ID); err != nil {
		t.Fatal(err)
	}
	queued, _ := readLocalDAGInbox(paths[0])
	if queued.Paused || queued.Attempts != 0 || queued.Prepared != event.Prepared || queued.FailedRevision != "" || len(queued.FailedFiles) != 0 {
		t.Fatal("retry lost prepared evidence")
	}
	runs, err := store.Runs("project")
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" || runs[0].Error == "" {
		t.Fatal("retry erased failure history", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := retryLocalDAGInbox(root, event.ID); err == nil {
		t.Fatal("retry bypassed active processing lock")
	}
}

func TestUnchangedBadCaptureWaitsWhileOtherSessionsProgress(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: "device"}
	file := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(file, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, native := range []string{"bad", "healthy"} {
		if err := enqueueLocalDAGObservation(root, localDAGObservationRequest{ProjectID: "project", Agent: "fixture", NativeID: native}); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := localDAGInboxFiles(root)
	bad, _ := readLocalDAGInbox(paths[0])
	bad.Paused, bad.Attempts, bad.FailedFiles = true, 1, []string{file}
	bad.FailedRevision = adapters.CaptureRevision(bad.FailedFiles)
	if err := atomicWriteJSON(paths[0], bad, 0600); err != nil {
		t.Fatal(err)
	}
	healthy, _ := readLocalDAGInbox(paths[1])
	turn := contracts.NormalizedTurn{TurnID: "t", Ordinal: 0, Role: "user", Kind: "message", Text: "healthy", ContentHash: adapters.Digest("healthy")}
	normalized, _ := adapters.ApplyCheckpoint([]contracts.NormalizedTurn{turn}, adapters.ObservationCheckpoint{})
	observation := localdag.Observation{ProjectID: "project", Agent: "fixture", NativeID: "healthy", Normalized: normalized, Visible: &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{{TurnID: "t", ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: 7}}}}}}
	data, _ := json.Marshal(observation)
	healthy.Prepared, err = store.PutObject(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteJSON(paths[1], healthy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := drainLocalDAGInbox(context.Background(), root, engine); err != nil {
		t.Fatal(err)
	}
	bad, _ = readLocalDAGInbox(paths[0])
	queue, _ := localDAGInbox(root)
	if len(queue) != 1 || !bad.Paused || bad.Attempts != 1 || bad.NextAt == "" {
		t.Fatal("bad capture blocked other session or retried unchanged bytes", queue)
	}
	if err := os.WriteFile(file, []byte("updated content"), 0600); err != nil {
		t.Fatal(err)
	}
	bad.NextAt = ""
	if err := atomicWriteJSON(paths[0], bad, 0600); err != nil {
		t.Fatal(err)
	}
	// The fixture has no native adapter. Its new attempt must be retryable,
	// rather than retaining the old content wait or its stale file fingerprint.
	if err := drainLocalDAGInbox(context.Background(), root, engine); err == nil {
		t.Fatal("missing fixture adapter accepted")
	}
	bad, _ = readLocalDAGInbox(paths[0])
	if bad.Paused || bad.Attempts != 1 || bad.FailedRevision != "" {
		t.Fatal("changed file did not resume", bad)
	}
}

type malformedObservationAdapter struct {
	adapters.Adapter
	observation contracts.SessionObservation
}

func (a malformedObservationAdapter) Type() contracts.AgentType { return contracts.AgentCodex }
func (a malformedObservationAdapter) Observe(context.Context, string, string, adapters.ObservationCheckpoint) (contracts.SessionObservation, adapters.ObservationCheckpoint, error) {
	return a.observation, adapters.ObservationCheckpoint{}, nil
}

func TestNativePreparationRejectsDuplicateIDsBeforeProjectingText(t *testing.T) {
	root := t.TempDir()
	turns := []contracts.NormalizedTurn{{TurnID: "duplicate", Ordinal: 0, Role: "user", Text: "x"}, {TurnID: "duplicate", Ordinal: 1, Role: "user", Text: "longer", ContentHash: "hash"}}
	normalized, _ := adapters.ApplyCheckpoint(turns, adapters.ObservationCheckpoint{})
	visible := &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{{TurnID: "duplicate", ContentHash: "hash", Ranges: []contracts.TextRange{{Start: 0, End: 6}}}}}
	adapter := malformedObservationAdapter{observation: contracts.SessionObservation{Session: contracts.SessionEnvelope{NativeSessionID: "native", AgentType: contracts.AgentCodex, CWD: root}, Normalized: normalized, VisibleText: visible}}
	request := localDAGObservationRequest{ProjectID: "project", Agent: contracts.AgentCodex, NativeID: "native", Root: root, CWD: root, ConfigRoot: t.TempDir()}
	if _, err := prepareLocalDAGObservation(context.Background(), root, nil, request, adapter); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
}
