package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

type nativeForkFixture struct {
	adapters.Adapter
	agent                  contracts.AgentType
	imported               int
	calls, created         int
	marker                 string
	source, childPath, cwd string
	observationFailure     bool
}

type forkPublicationProbe struct {
	online bool
	calls  int
}

func (p *forkPublicationProbe) Publish(context.Context, workgraph.Commit) error {
	p.calls++
	if !p.online {
		return errors.New("Relay unavailable")
	}
	return nil
}

func (p *forkPublicationProbe) Pull(context.Context, string, workgraph.Source, bool) error {
	p.calls++
	return errors.New("unexpected remote read of a complete local Fork source")
}

func (f *nativeForkFixture) Type() contracts.AgentType {
	if f.agent != "" {
		return f.agent
	}
	return contracts.AgentCodex
}
func (f *nativeForkFixture) ImportText(ctx context.Context, request adapters.TextImportRequest) (adapters.ForkResult, error) {
	f.imported++
	var texts []string
	if err := adapters.WalkCompact(ctx, request, func(m adapters.TextMessage) error { texts = append(texts, m.Text); return nil }); err != nil {
		return adapters.ForkResult{}, err
	}
	if len(texts) != 1 || texts[0] != "原会话已经完成重试说明。" {
		return adapters.ForkResult{}, errors.New("wrong fixed dialogue")
	}
	f.calls++
	return f.createChild()
}
func (f *nativeForkFixture) Fork(_ context.Context, request adapters.ForkRequest) (adapters.ForkResult, error) {
	f.calls++
	data, err := os.ReadFile(request.SourceArtifactPath)
	if err != nil || string(data) != f.source {
		return adapters.ForkResult{}, errors.New("wrong fixed source")
	}
	return f.createChild()
}
func (f *nativeForkFixture) createChild() (adapters.ForkResult, error) {
	if f.marker == "" {
		f.marker = "independent-native-child"
		f.created++
		if err := os.WriteFile(f.childPath, []byte("independent child bytes"), 0600); err != nil {
			return adapters.ForkResult{}, err
		}
	}
	return adapters.ForkResult{NativeSessionID: f.marker}, nil
}
func (f *nativeForkFixture) ResumeCommand(id string) contracts.ResumeCommand {
	return contracts.ResumeCommand{Program: "isolated-fixture", Args: []string{"resume", id}}
}
func (f *nativeForkFixture) Observe(_ context.Context, cwd, id string, _ adapters.ObservationCheckpoint) (contracts.SessionObservation, adapters.ObservationCheckpoint, error) {
	if f.observationFailure {
		f.observationFailure = false
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("lost observation response")
	}
	if id != f.marker || cwd != f.cwd {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("wrong child selection")
	}
	hash, size, err := adapters.HashFile(f.childPath)
	if err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
	}
	turn := contracts.NormalizedTurn{TurnID: "inherited-turn", Ordinal: 0, Role: "assistant", Kind: "message", Text: "原会话已经完成重试说明。", ContentHash: adapters.Digest("inherited")}
	normalized, _ := adapters.ApplyCheckpoint([]contracts.NormalizedTurn{turn}, adapters.ObservationCheckpoint{})
	visible := &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: len(turn.Text)}}}}}
	return contracts.SessionObservation{Session: contracts.SessionEnvelope{NativeSessionID: id, AgentType: f.Type(), CWD: cwd}, RawArtifact: contracts.ArtifactObservation{Path: f.childPath, MediaType: "application/jsonl", SHA256: hash, Size: size}, Normalized: normalized, VisibleText: visible}, adapters.ObservationCheckpoint{}, nil
}

func TestLocalNativeForkUsesExactCopyRecoversChildAndDoesNotAttributeInheritedWork(t *testing.T) {
	for _, source := range []contracts.AgentType{contracts.AgentCodex, contracts.AgentClaude, contracts.AgentCursor} {
		for _, target := range []contracts.AgentType{contracts.AgentCodex, contracts.AgentClaude, contracts.AgentCursor} {
			t.Run(string(source)+"-to-"+string(target), func(t *testing.T) { testLocalForkRecovery(t, source, target) })
		}
	}
}
func testLocalForkRecovery(t *testing.T, sourceAgent, targetAgent contracts.AgentType) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := initTestRepository(t, "native-fork")
	if out, err := exec.Command("git", "-C", workspace, "commit", "--allow-empty", "-m", "fixture").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	workspace, err := canonicalPath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	managedMCPConfig(t, root, []localDAGWorkspace{{Path: workspace, ProjectID: "project"}})
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	engine := &localdag.Engine{Store: store, Actor: "reader"}
	original := filepath.Join(profile, "source-fixture")
	sourceBytes := []byte("older fixed native snapshot")
	if err := os.WriteFile(original, sourceBytes, 0600); err != nil {
		t.Fatal(err)
	}
	capture := localdag.Capture{ProjectID: "project", NativeID: "native-source", Agent: string(sourceAgent), Artifact: sourceBytes, Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "assistant", Kind: "message", Text: "原会话已经完成重试说明。"}}}}
	if sourceAgent != targetAgent {
		capture.Artifact = nil
	}
	source, err := engine.Capture(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.Apply(workgraph.Commit{Schema: 1, ID: "node", ProjectID: "project", Actor: "reader", RunID: "stage", Base: state.Revision, At: stamp, Stage: &workgraph.StageDecision{Source: source, Action: "start", NewNodeID: "node", Text: &workgraph.Text{Objective: "重试提示"}, Reason: "fixture"}}, false); err != nil {
		t.Fatal(err)
	}
	// The source Agent's original file has advanced. The native operation must
	// receive the graph-selected older cache, never this live original path.
	latest := []byte("newer original history; must stay intact")
	if err := os.WriteFile(original, latest, 0600); err != nil {
		t.Fatal(err)
	}
	fake := &nativeForkFixture{agent: targetAgent, source: string(sourceBytes), childPath: filepath.Join(profile, "child-fixture"), cwd: workspace, observationFailure: true}
	request := localNativeForkRequest{localForkSelection: localForkSelection{RequestID: "native-fork-request", NodeID: "node", Source: source}, ProjectID: "project", Agent: targetAgent, CWD: workspace, ConfigRoot: profile}
	if _, err := forkLocalDAG(ctx, root, engine, request, fake); err == nil || !strings.Contains(err.Error(), "lost observation") {
		t.Fatal("injected failure disappeared", err)
	}
	// Advance and retire the graph's earlier source while the local native
	// operation is unfinished. Its downloaded input must survive local GC.
	capture.Artifact = latest
	capture.Entries = append(capture.Entries, conversation.Entry{Turn: contracts.NormalizedTurn{TurnID: "second", Ordinal: 1, Role: "user", Kind: "message", Text: "提示再简短一些"}})
	next, err := engine.Capture(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	if _, err := store.Apply(workgraph.Commit{Schema: 1, ID: "continued", ProjectID: "project", Actor: engine.Actor, RunID: "next-stage", Base: state.Revision, At: stamp, Stage: &workgraph.StageDecision{Source: next, SessionRevision: state.Sessions[source.SessionID].Revision, Action: "continue", Reason: "fixture"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := engine.Reconcile("project"); err != nil {
		t.Fatal(err)
	}
	if err := engine.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	if _, exists := state.Snapshots[source.Key()]; exists {
		t.Fatal("old descriptor was not retired")
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := filepath.WalkDir(filepath.Join(root, "objects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		return os.Chtimes(path, past, past)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := maintainLocalDAGCache(ctx, root, engine); err != nil {
		t.Fatal("pending Fork lost local input protection", err)
	}
	// Reopen the store like a restarted Runtime. Adapter-owned marker preserves
	// the already-created native child even when preparation did not finish.
	store.Close()
	store, err = graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	remote := &forkPublicationProbe{}
	engine = &localdag.Engine{Store: store, Actor: "reader", Publisher: remote}
	result, err := forkLocalDAG(ctx, root, engine, request, fake)
	if err != nil || result.State != "created" || fake.created != 1 || fake.calls != 2 {
		t.Fatal(result, fake, err)
	}
	if sourceAgent != targetAgent && (fake.imported != 2 || result.TransferFormat != adapters.TextImportFormat || result.SourceAgent != string(sourceAgent)) {
		t.Fatal("cross-Agent route or provenance missing", result)
	}
	if got, err := os.ReadFile(original); err != nil || !bytes.Equal(got, latest) {
		t.Fatal("original history changed", err)
	}
	pending, err := store.Pending("project")
	if err != nil || len(pending) != 1 || pending[0].Snapshot == nil || pending[0].Snapshot.Source != result.CreatedSource || !pending[0].Snapshot.Inherited || remote.calls != 0 || result.Resume.Display == "" {
		t.Fatal("offline Fork did not return a usable child with a durable baseline", pending, result, remote.calls, err)
	}
	state, _ = store.State("project")
	if _, exists := state.Sessions[result.CreatedSource.SessionID]; exists {
		t.Fatal("queued source was reported as accepted")
	}
	store.Close()
	store, err = graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	engine = &localdag.Engine{Store: store, Actor: "reader", Publisher: remote}
	if err := engine.RecoverLocal(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	replayed, err := forkLocalDAG(ctx, root, engine, request, fake)
	if err != nil || replayed.NativeID != result.NativeID || fake.calls != 2 || remote.calls != 0 {
		t.Fatal("offline restart recreated the child or contacted Relay", replayed, err)
	}
	if err := filepath.WalkDir(filepath.Join(root, "objects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		return os.Chtimes(path, past, past)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := maintainLocalDAGCache(ctx, root, engine); err != nil {
		t.Fatal("completed Fork lost its queued baseline during collection", err)
	}
	if err := store.ValidateArtifact(pending[0].Snapshot.Artifact); err != nil {
		t.Fatal("unpublished child artifact was collected", err)
	}
	remote.online = true
	if err := engine.Flush(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	child := state.Sessions[result.CreatedSource.SessionID]
	if child.NativeID == capture.NativeID || child.ProcessedThrough != 1 || child.ConsumedTurns != 1 || len(state.Nodes) != 1 {
		t.Fatal("native copy was treated as new work", state)
	}
	tasks, _ := store.Tasks("project")
	for _, task := range tasks {
		if task.Source != nil && task.Source.SessionID == child.ID {
			t.Fatal("native baseline queued inference", task)
		}
	}
	replay, err := forkLocalDAG(ctx, root, engine, request, fake)
	if err != nil || replay.NativeID != result.NativeID || fake.calls != 2 {
		t.Fatal("completed replay recreated child", replay, err)
	}
	request.Source.Version++
	if _, err := forkLocalDAG(ctx, root, engine, request, fake); err == nil {
		t.Fatal("changed request reused")
	}
	logs, _ := store.Runs("project")
	completed, failed := false, false
	for _, run := range logs {
		if run.Kind == "fork" {
			completed = completed || run.Status == "completed"
			failed = failed || run.Status == "failed"
		}
	}
	if !completed || !failed {
		t.Fatal("native Fork outcomes are not traceable")
	}
}
