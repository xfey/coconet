package localdag

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

func librarySelection(t *testing.T, e *Engine) LibraryRequest {
	t.Helper()
	capture(t, e, "session", 1, "先把重试提示写清楚。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "明确重试提示"}, "e1"))
	process(t, e, "stage", "session")
	state, _ := e.Store.State("project")
	id := state.Sessions["session"].Tail
	return LibraryRequest{RequestID: "collect-request", NodeID: id, Source: state.Nodes[id].Source, Note: "后面可以参考停止条件"}
}
func TestCollectPinsFixedSourceReplaysAfterArchivePruneAndRejectsChanges(t *testing.T) {
	e, s := fixture(t)
	request := librarySelection(t, e)
	result, err := e.Collect(context.Background(), "project", request)
	if err != nil || result.State != "collected" {
		t.Fatal(result, err)
	}
	before, _ := s.State("project")
	if before.Nodes[request.NodeID].State != "current" || before.LibrarySources[request.NodeID].Source != request.Source || before.Library[request.NodeID] != request.Note {
		t.Fatal("collection did not pin source independently")
	}
	capture(t, e, "session", 2, "先把重试提示写清楚。", "好的，接下来处理并发。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "处理并发"}, "e2"))
	process(t, e, "stage", "session")
	if err := s.PruneDiagnostics(time.Now().Add(8 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	before, _ = s.State("project")
	replay, err := e.Collect(context.Background(), "project", request)
	if err != nil || replay != result {
		t.Fatal("idempotent result lost after archive prune", replay, err)
	}
	after, _ := s.State("project")
	if workgraph.Digest(before) != workgraph.Digest(after) {
		t.Fatal("retry changed graph")
	}
	request.Note = "different"
	if _, err := e.Collect(context.Background(), "project", request); err == nil {
		t.Fatal("request identity reused")
	}
	e.Actor = "other"
	request.RequestID = "other-request"
	if _, err := e.Collect(context.Background(), "project", request); err != nil {
		t.Fatal("member could not collect accessible source", err)
	}
}

func TestLibraryNoteEditAndRemovalKeepAuthorNodeAndRecoverWithoutInference(t *testing.T) {
	e, store := fixture(t)
	request := librarySelection(t, e)
	if _, err := e.Collect(context.Background(), "project", request); err != nil {
		t.Fatal(err)
	}
	request.RequestID, request.Note = "edit-note-request", "新的使用说明"
	if _, err := e.Collect(context.Background(), "project", request); err != nil {
		t.Fatal(err)
	}
	before, _ := store.State("project")
	if before.Library[request.NodeID] != request.Note {
		t.Fatal("explicit new request did not edit note")
	}
	request.RequestID, request.Note = "remove-request", ""
	e.Runner, e.Publisher = nil, libraryOffline{}
	if result, err := e.Uncollect(context.Background(), "project", request); err == nil || result.State != "pending" {
		t.Fatal("offline removal not retained", result, err)
	}
	restarted := &Engine{Store: store, Actor: e.Actor}
	if err := restarted.RecoverLocal(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	result, err := restarted.Uncollect(context.Background(), "project", request)
	if err != nil || result.State != "removed" {
		t.Fatal("removal did not recover", result, err)
	}
	after, _ := store.State("project")
	if _, collected := after.Library[request.NodeID]; collected || workgraph.Digest(after.Nodes[request.NodeID]) != workgraph.Digest(before.Nodes[request.NodeID]) || len(after.Snapshots) != len(before.Snapshots) {
		t.Fatal("removal changed sealed node or source")
	}
	if err := store.PruneDiagnostics(time.Now().Add(8 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if replay, err := restarted.Uncollect(context.Background(), "project", request); err != nil || replay != result {
		t.Fatal("removal lost idempotence after archive prune", err)
	}
	if _, err := restarted.Collect(context.Background(), "project", request); err == nil {
		t.Fatal("same request ID changed removal into collection")
	}
	restarted.Actor = "other"
	request.RequestID = "not-owned-request"
	if _, err := restarted.Uncollect(context.Background(), "project", request); err != nil {
		t.Fatal("member could not remove entry", err)
	}
	after, _ = store.State("project")
	replay, err := store.Replay("project")
	if err != nil || workgraph.Digest(replay) != workgraph.Digest(after) {
		t.Fatal("removal not deterministic", err)
	}
}

type lostLibrary struct {
	store *graphstore.Store
	once  bool
}

func (p *lostLibrary) Publish(_ context.Context, c workgraph.Commit) error {
	if !p.once {
		p.once = true
		if _, err := p.store.Apply(c, true); err != nil {
			return err
		}
		return errors.New("lost accepted confirmation")
	}
	return nil
}
func TestCollectLostReceiptAndOutboxRestartNeverInvokeModel(t *testing.T) {
	e, s := fixture(t)
	request := librarySelection(t, e)
	e.Runner = nil
	e.Publisher = &lostLibrary{store: s}
	result, err := e.Collect(context.Background(), "project", request)
	if err != nil || result.State != "collected" {
		t.Fatal(result, err)
	}
	before, _ := s.State("project")
	if _, err := e.Collect(context.Background(), "project", request); err != nil {
		t.Fatal(err)
	}
	after, _ := s.State("project")
	if before.Revision != after.Revision {
		t.Fatal("duplicate commit")
	}
}

type libraryOffline struct{}

func (libraryOffline) Publish(context.Context, workgraph.Commit) error { return errors.New("offline") }
func TestCollectDurablePendingRecoversAndSourceAdvanceCannotRetarget(t *testing.T) {
	e, s := fixture(t)
	request := librarySelection(t, e)
	e.Publisher = libraryOffline{}
	if result, err := e.Collect(context.Background(), "project", request); err == nil || result.State != "pending" {
		t.Fatal(result, err)
	}
	restarted := &Engine{Store: s, Actor: e.Actor}
	if err := restarted.RecoverLocal(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	if result, err := restarted.Collect(context.Background(), "project", request); err != nil || result.State != "collected" {
		t.Fatal(result, err)
	}
	state, _ := s.State("project")
	if state.Nodes[request.NodeID].Source != request.Source {
		t.Fatal("selection changed")
	}
	// Another collection starts from an editable source, then loses its baseline.
	e2, s2 := fixture(t)
	r2 := librarySelection(t, e2)
	e2.Publisher = libraryOffline{}
	if _, err := e2.Collect(context.Background(), "project", r2); err == nil {
		t.Fatal("expected offline")
	}
	e2.Publisher = nil
	capture(t, e2, "session", 2, "先把重试提示写清楚。", "这个提示再改短一点。")
	e2.Runner = output(stage("continue", nil, "e2"))
	process(t, e2, "stage", "session")
	if _, err := e2.Collect(context.Background(), "project", r2); !errors.Is(err, ErrSelectionChanged) {
		t.Fatal("stale collection retargeted", err)
	}
	state, _ = s2.State("project")
	if len(state.Library) != 0 {
		t.Fatal("stale request collected later source")
	}
	tasks, _ := s2.Tasks("project")
	for _, task := range tasks {
		if task.Kind == "library" {
			t.Fatal("stale mutation became model work")
		}
	}
}

func TestLibraryAllowsAnotherOwnDeviceButPreservesInferenceOwnership(t *testing.T) {
	e, s := fixture(t)
	e.Actor = "account.left"
	request := librarySelection(t, e)
	e.Actor = "account.right"
	if result, err := e.Collect(context.Background(), "project", request); err != nil || result.State != "collected" {
		t.Fatal(result, err)
	}
	state, _ := s.State("project")
	node := state.Nodes[request.NodeID]
	if _, _, err := workgraph.Apply(state, workgraph.Commit{Schema: 1, ID: "not-owner", ProjectID: "project", Actor: e.Actor, RunID: "run", Base: state.Revision, At: e.stamp(), Relations: &workgraph.RelationDecision{NodeID: node.ID, NodeRevision: node.Revision, Through: node.EndTurn, Complete: true, Reason: "not the device owner"}}); !errors.Is(err, workgraph.ErrPermission) {
		t.Fatal("Library permission expanded inference ownership", err)
	}
}

func TestLibraryReadAndListingRemainFixedWhileAuthorContinues(t *testing.T) {
	e, store := fixture(t)
	request := librarySelection(t, e)
	e.Actor = "colleague"
	if _, err := e.Collect(context.Background(), "project", request); err != nil {
		t.Fatal(err)
	}
	e.Actor = "owner"
	capture(t, e, "session", 2, "先把重试提示写清楚。", "好的，这里再补充错误恢复。")
	e.Runner = output(stage("continue", nil, "e2"))
	process(t, e, "stage", "session")
	if err := e.RetireConsumed(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	graph, _ := e.Graph("project")
	library, err := e.GraphPage("project", GraphQuery{}, true)
	if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].Source.Version != 2 || len(library.Nodes) != 1 || library.Nodes[0].Source != request.Source || graph.Nodes[0].Library.Source != request.Source {
		t.Fatal("graph and saved version not independent", graph, library, err)
	}
	if _, err := e.ReadSelectedNode(context.Background(), "project", request.NodeID, request.Source, false); err != nil {
		t.Fatal("saved source unavailable", err)
	}
	state, _ := store.State("project")
	if err := workgraph.ValidateState(state); err != nil {
		t.Fatal(err)
	}
	request.RequestID, request.Note = "remove-saved-version", ""
	if _, err := e.Uncollect(context.Background(), "project", request); err != nil {
		t.Fatal(err)
	}
	if err := e.RetireConsumed(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	if _, ok := state.Snapshots[request.Source.Key()]; ok {
		t.Fatal("unreferenced saved source not retired")
	}
	if state.Nodes[request.NodeID].Source.Version != 2 || state.Nodes[request.NodeID].State != "current" {
		t.Fatal("removal changed author progress")
	}
}
