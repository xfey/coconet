package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
)

func activeObservation(labels ...string) Observation {
	turns := []contracts.NormalizedTurn{}
	history := &contracts.NativeActiveHistory{IDs: []string{}}
	for i, label := range labels {
		turns = append(turns, nativeTurn(label, "user", label, i))
		history.IDs = append(history.IDs, label)
	}
	observation := nativeObservation(turns...)
	observation.ActiveHistory = history
	return observation
}
func assertReplay(t *testing.T, e *Engine) workgraph.State {
	t.Helper()
	state, err := e.Store.State("project")
	if err != nil {
		t.Fatal(err)
	}
	if err := workgraph.ValidateState(state); err != nil {
		t.Fatalf("invalid state: %v\n%+v", err, state)
	}
	replay, err := e.Store.Replay("project")
	if err != nil || workgraph.Digest(replay) != workgraph.Digest(state) {
		t.Fatal("replay mismatch", err)
	}
	return state
}
func TestRewindRetainsValidNodesDropsWithdrawnTextAndSkipsOldTasks(t *testing.T) {
	e, store := fixture(t)
	ctx := context.Background()
	var source workgraph.Source
	nodes := []string{}
	for i, label := range []string{"A", "B", "C"} {
		var err error
		source, err = e.CaptureObservation(ctx, activeObservation([]string{"A", "B", "C"}[:i+1]...))
		if err != nil {
			t.Fatal(err)
		}
		e.Runner = output(stage("start", &workgraph.Text{Objective: label}, "e"+string(rune('1'+i))))
		process(t, e, "stage", source.SessionID)
		state := assertReplay(t, e)
		nodes = append(nodes, state.Sessions[source.SessionID].Tail)
	}
	if _, err := store.IndexProject(ctx, "project", 64); err != nil {
		t.Fatal(err)
	}
	// Pending work after C must be skipped even if its processing has failed.
	stale, err := e.CaptureObservation(ctx, activeObservation("A", "B", "C", "obsolete"))
	if err != nil {
		t.Fatal(err)
	}
	oldTask := task(t, e, "stage", source.SessionID)
	next, err := e.CaptureObservation(ctx, activeObservation("A", "B", "D"))
	if err != nil {
		t.Fatal(err)
	}
	state := assertReplay(t, e)
	if len(state.Nodes) != 2 || state.Sessions[source.SessionID].Tail != nodes[1] || state.Sessions[source.SessionID].ConsumedTurns != 2 || !state.Snapshots[source.Key()].Superseded || !state.Snapshots[stale.Key()].Superseded {
		t.Fatal("wrong rewind boundary", state)
	}
	if _, ok := state.Nodes[nodes[2]]; ok {
		t.Fatal("C remains")
	}
	if state.Nodes[nodes[0]].State != "sealed" || state.Nodes[nodes[1]].State != "current" {
		t.Fatal("rewind must reopen only the unpinned tail", state.Nodes)
	}
	// Search must stop exposing C before the index has caught up or GC ran.
	found, err := e.Search("project", WorkSearch{Query: "C"})
	if err != nil || len(found.ContentMatches) != 0 {
		t.Fatal("stale cached text remains searchable", found, err)
	}
	if _, err := e.ReadSelectedNode(ctx, "project", "", source, false); err == nil {
		t.Fatal("withdrawn exact source readable")
	}
	input, err := prepareStage(store, state, next, false)
	if err != nil {
		t.Fatal(err)
	}
	var body stageInput
	json.Unmarshal(input.Request.Input, &body)
	if body.Previous.Stage.Objective != "B" || len(body.Evidence.Turns) != 3 || body.Evidence.Turns[2].Text != "D" {
		t.Fatal(string(input.Request.Input))
	}
	if body.Previous.State != "current" {
		t.Fatal("model still sees a sealed tail", string(input.Request.Input))
	}
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		t.Fatal("obsolete job invoked model")
		return inference.Response{}, nil
	})
	if err := e.Process(ctx, oldTask); err != nil {
		t.Fatal(err)
	}
	e.Runner = output(stage("continue", &workgraph.Text{Objective: "B revised by D"}, "e3"))
	process(t, e, "stage", source.SessionID)
	if err := e.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state = assertReplay(t, e)
	if len(state.Nodes) != 2 || state.Sessions[source.SessionID].Tail != nodes[1] || state.Nodes[nodes[1]].Source != next || state.Nodes[nodes[1]].Objective != "B revised by D" {
		t.Fatal("continuation must update the restored node without a new identity", state)
	}
	if _, exists := state.Snapshots[source.Key()]; exists {
		t.Fatal("withdrawn source remained pinned")
	}
	repeat, err := e.CaptureObservation(ctx, activeObservation("A", "B", "D"))
	if err != nil || repeat != next {
		t.Fatal("duplicate rewind", repeat, err)
	}
}
func TestRewindInsideMutableNodeRegeneratesWithoutStaleItems(t *testing.T) {
	e, store := fixture(t)
	ctx := context.Background()
	a, err := e.CaptureObservation(ctx, activeObservation("A"))
	if err != nil {
		t.Fatal(err)
	}
	e.Runner = output(stage("start", &workgraph.Text{Objective: "A"}, "e1"))
	process(t, e, "stage", a.SessionID)
	_, err = e.CaptureObservation(ctx, activeObservation("A", "B"))
	if err != nil {
		t.Fatal(err)
	}
	e.Runner = output(`{"action":"start","stage":{"objective":"B","summary":"","approach":""},"items":{"add":["B pending"],"revise":[],"complete":[],"withdraw":[]},"reason":"new work","evidence":["e2"]}`)
	process(t, e, "stage", a.SessionID)
	_, err = e.CaptureObservation(ctx, activeObservation("A", "B", "C"))
	if err != nil {
		t.Fatal(err)
	}
	e.Runner = output(stage("continue", &workgraph.Text{Objective: "B and C"}, "e3"))
	process(t, e, "stage", a.SessionID)
	restored, err := e.CaptureObservation(ctx, activeObservation("A", "B"))
	if err != nil {
		t.Fatal(err)
	}
	state := assertReplay(t, e)
	if len(state.Nodes) != 1 || state.Sessions[a.SessionID].ConsumedTurns != 1 || len(state.Sessions[a.SessionID].Items) != 0 {
		t.Fatal(state)
	}
	input, err := prepareStage(store, state, restored, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(input.Request.Input), "B and C") || strings.Contains(string(input.Request.Input), "B pending") {
		t.Fatal("stale derived state", string(input.Request.Input))
	}
	// Rewinding all the way to empty is valid only with authoritative evidence.
	_, err = e.CaptureObservation(ctx, activeObservation())
	if err != nil {
		t.Fatal(err)
	}
	state = assertReplay(t, e)
	if len(state.Nodes) != 0 || state.Sessions[a.SessionID].ConsumedTurns != 0 {
		t.Fatal(state)
	}
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		t.Fatal("empty rewind invoked model")
		return inference.Response{}, nil
	})
	process(t, e, "stage", a.SessionID)
	assertReplay(t, e)
}
func TestRewindRejectsLateModelResultAndFixedObservation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			e, _ := fixture(t)
			ctx := context.Background()
			source, err := e.CaptureObservation(ctx, activeObservation("A", "B", "C"))
			if err != nil {
				t.Fatal(err)
			}
			delayed := activeObservation("A", "B", "C", "old")
			v := source.Version
			delayed.BaseVersion = &v
			old := task(t, e, "stage", source.SessionID)
			e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
				if _, err := e.CaptureObservation(ctx, activeObservation("A", "B", "D")); err != nil {
					t.Fatal(err)
				}
				if failed {
					return inference.Response{}, errors.New("late provider failure")
				}
				return inference.Response{Text: stage("start", &workgraph.Text{Objective: "C"}, "e3")}, nil
			})
			if err := e.Process(ctx, old); err != nil {
				t.Fatal(err)
			}
			state := assertReplay(t, e)
			if len(state.Nodes) != 0 {
				t.Fatal("late model resurrected C")
			}
			if _, err := e.CaptureObservation(ctx, delayed); !errors.Is(err, ErrObservationSuperseded) {
				t.Fatal("old frozen observation restored old branch", err)
			}
			tasks, _ := e.Store.Tasks("project")
			for _, job := range tasks {
				if job.ID == old.ID {
					t.Fatal("late task requeued", job)
				}
			}
		})
	}
}
func TestActiveHistoryPreservesCompactionAncestorsButNotWithdrawnBranch(t *testing.T) {
	e, store := fixture(t)
	ctx := context.Background()
	_, err := e.CaptureObservation(ctx, activeObservation("A", "B", "C"))
	if err != nil {
		t.Fatal(err)
	}
	observation := activeObservation("D")
	observation.ActiveHistory.IDs = []string{"A", "B", "C", "D"}
	source, err := e.CaptureObservation(ctx, observation)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if state.Snapshots[source.Key()].Turns != 4 || state.Snapshots[source.Key()].Rewrite != nil {
		t.Fatal("compaction withdrew history")
	}
	observation = activeObservation("E")
	observation.ActiveHistory.IDs = []string{"A", "B", "E"}
	source, err = e.CaptureObservation(ctx, observation)
	if err != nil {
		t.Fatal(err)
	}
	state = assertReplay(t, e)
	entries, err := readEntries(store, state.Snapshots[source.Key()], 0, 3)
	if err != nil || entries[2].Turn.Text != "E" {
		t.Fatal(entries, err)
	}
}

func TestRewindKeepsColleagueWorkAndExplicitLibrary(t *testing.T) {
	e, store := fixture(t)
	ctx := context.Background()
	source, err := e.CaptureObservation(ctx, activeObservation("A", "C"))
	if err != nil {
		t.Fatal(err)
	}
	e.Runner = output(stage("start", &workgraph.Text{Objective: "C"}, "e2"))
	process(t, e, "stage", source.SessionID)
	state := assertReplay(t, e)
	c := state.Nodes[state.Sessions[source.SessionID].Tail]
	if _, err := e.Collect(ctx, "project", LibraryRequest{RequestID: "bookmark-original", NodeID: c.ID, Source: c.Source, Note: "fixed evidence"}); err != nil {
		t.Fatal(err)
	}
	peer := &Engine{Store: store, Actor: "peer", Now: e.Now}
	peerObservation := activeObservation("E used C")
	peerObservation.NativeID = "peer-session"
	peerSource, err := peer.CaptureObservation(ctx, peerObservation)
	if err != nil {
		t.Fatal(err)
	}
	peer.Runner = output(stage("start", &workgraph.Text{Objective: "E"}, "e1"))
	process(t, peer, "stage", peerSource.SessionID)
	peer.Runner = modelFunc(func(r inference.Request) (inference.Response, error) {
		var input relationInput
		json.Unmarshal(r.Input, &input)
		var alias string
		for _, candidate := range input.Candidates {
			if candidate.Objective == "C" {
				alias = candidate.ID
			}
		}
		encoded, _ := json.Marshal(map[string]any{"status": "complete", "parents": []any{map[string]any{"id": alias, "reason": "used C", "evidence": []string{"e1"}}}, "reason": "use documented"})
		return inference.Response{Text: string(encoded)}, nil
	})
	peerState := assertReplay(t, peer)
	if err := peer.Reconcile("project"); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.Tasks("project")
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.Kind == "relations" && job.NodeID == peerState.Sessions[peerSource.SessionID].Tail {
			if err := peer.Process(ctx, job); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := assertReplay(t, e)
	peerID := before.Sessions[peerSource.SessionID].Tail
	if len(before.Nodes[peerID].Parents) != 1 {
		t.Fatal("fixture lacks dependency")
	}
	if _, err := e.CaptureObservation(ctx, activeObservation("A", "D")); err != nil {
		t.Fatal(err)
	}
	after := assertReplay(t, e)
	if after.Nodes[peerID].Text != before.Nodes[peerID].Text || after.Nodes[peerID].Source != before.Nodes[peerID].Source || len(after.Nodes[peerID].Parents) != 0 {
		t.Fatal("peer work changed or dangling edge retained")
	}
	if after.LibrarySources[c.ID].Source != c.Source {
		t.Fatal("bookmark lost its evidence")
	}
	graph, _ := e.Graph("project")
	for _, node := range graph.Nodes {
		if node.ID == c.ID {
			t.Fatal("withdrawn bookmark leaked into graph")
		}
	}
	library, err := e.GraphPage("project", GraphQuery{}, true)
	if err != nil || len(library.Nodes) != 1 {
		t.Fatal("explicit bookmark disappeared", library, err)
	}
	if _, err := e.ReadSelectedNode(ctx, "project", c.ID, c.Source, false); err != nil {
		t.Fatal("explicit saved version unavailable", err)
	}
	if _, err := e.Uncollect(ctx, "project", LibraryRequest{RequestID: "remove-old-bookmark", NodeID: c.ID, Source: c.Source}); err != nil {
		t.Fatal(err)
	}
	after = assertReplay(t, e)
	if _, ok := after.Nodes[c.ID]; ok {
		t.Fatal("uncollected withdrawn node remains")
	}
}

func TestActiveReturnedEmptyMessageDoesNotReuseOldBody(t *testing.T) {
	e, _ := fixture(t)
	ctx := context.Background()
	if _, err := e.CaptureObservation(ctx, activeObservation("A", "B")); err != nil {
		t.Fatal(err)
	}
	next := activeObservation("A", "B")
	next.Normalized.Turns[1].Kind = "event"
	next.Visible.Turns = next.Visible.Turns[:1]
	source, err := e.CaptureObservation(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	state := assertReplay(t, e)
	snapshot := state.Snapshots[source.Key()]
	if snapshot.Turns != 1 || snapshot.Rewrite == nil || snapshot.Rewrite.CommonTurns != 1 {
		t.Fatal(snapshot)
	}
}

func TestRewindOfForkKeepsInheritedBoundaryAndHandlesPendingAppend(t *testing.T) {
	e, _ := fixture(t)
	ctx := context.Background()
	inherited := activeObservation("A", "B")
	inherited.Inherited = true
	if _, err := e.CaptureObservation(ctx, inherited); err != nil {
		t.Fatal(err)
	}
	own, err := e.CaptureObservation(ctx, activeObservation("A", "B", "C"))
	if err != nil {
		t.Fatal(err)
	}
	e.Runner = output(stage("start", &workgraph.Text{Objective: "C"}, "e3"))
	process(t, e, "stage", own.SessionID)
	next, err := e.CaptureObservation(ctx, activeObservation("A", "B", "D"))
	if err != nil {
		t.Fatal(err)
	}
	state := assertReplay(t, e)
	if len(state.Nodes) != 0 || state.Sessions[next.SessionID].ConsumedTurns != 2 {
		t.Fatal("inherited work became new", state)
	}
	if _, err := e.CaptureObservation(ctx, activeObservation("A", "B", "D", "E")); err != nil {
		t.Fatal(err)
	}
	assertReplay(t, e)
	e.Runner = output(stage("start", &workgraph.Text{Objective: "D"}, "e3"))
	process(t, e, "stage", next.SessionID)
	assertReplay(t, e)
	// Rewinding inside the inherited prefix retains only the surviving inherited text.
	if _, err := e.CaptureObservation(ctx, activeObservation("A")); err != nil {
		t.Fatal(err)
	}
	state = assertReplay(t, e)
	if state.Sessions[next.SessionID].ConsumedTurns != 1 || len(state.Nodes) != 0 {
		t.Fatal(state)
	}
}
