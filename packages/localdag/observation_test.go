package localdag

import (
	"context"
	"testing"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
)

func nativeObservation(turns ...contracts.NormalizedTurn) Observation {
	normalized, _ := adapters.ApplyCheckpoint(turns, adapters.ObservationCheckpoint{})
	visible := &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{}}
	for _, turn := range turns {
		if turn.Role == "user" || turn.Role == "assistant" {
			visible.Turns = append(visible.Turns, contracts.VisibleTextTurn{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: len(turn.Text)}}})
		}
	}
	return Observation{ProjectID: "project", Agent: "fixture", NativeID: "session", Normalized: normalized, Visible: visible}
}
func nativeTurn(id, role, text string, ordinal int) contracts.NormalizedTurn {
	return contracts.NormalizedTurn{TurnID: id, Ordinal: ordinal, Role: role, Kind: "message", Text: text, ContentHash: adapters.Digest(text)}
}

func TestNativeCompactionAndRevisionPreserveVisiblePrefix(t *testing.T) {
	e, store := fixture(t)
	first := nativeObservation(nativeTurn("user", "user", "先完善重试。", 0), nativeTurn("tool", "tool", "opaque tool output", 1), nativeTurn("reply", "assistant", "已完成修改。", 2))
	one, err := e.CaptureObservation(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	entries, err := readEntries(store, state.Snapshots[one.Key()], 0, 2)
	if err != nil || len(entries) != 2 || entries[1].Turn.Text != "已完成修改。" {
		t.Fatal("tool body entered compact content", err)
	}
	second := nativeObservation(nativeTurn("reply", "assistant", "已完成修改，测试通过。", 0), nativeTurn("next", "user", "提示再改得直接一些。", 1))
	two, err := e.CaptureObservation(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	all, err := readEntries(store, state.Snapshots[two.Key()], 0, 4)
	if err != nil || workgraph.Digest(all[:2]) != workgraph.Digest(entries) || all[2].Turn.SourceTurnID != "reply" {
		t.Fatal("compaction rewrote or discarded captured evidence", err)
	}
	repeat, err := e.CaptureObservation(context.Background(), second)
	if err != nil || repeat != two {
		t.Fatal("repeated native observation created another snapshot", err)
	}
	if _, err := e.CaptureObservation(context.Background(), nativeObservation()); err == nil {
		t.Fatal("empty reset silently accepted")
	}
}

func TestDuplicateNativeIDsCannotConfuseVisibleRanges(t *testing.T) {
	e, _ := fixture(t)
	observation := nativeObservation(nativeTurn("duplicate", "user", "a", 0), nativeTurn("duplicate", "user", "longer", 1))
	if _, err := e.CaptureObservation(context.Background(), observation); err == nil {
		t.Fatal("duplicate native identity accepted")
	}
}

func TestNativeForkInheritedBaselineSkipsModelsAndLaterTurnsRemainNew(t *testing.T) {
	e, store := fixture(t)
	inherited := nativeObservation(nativeTurn("u", "user", "先完善重试提示。", 0), nativeTurn("a", "assistant", "已经修改完成。", 1))
	inherited.Inherited = true
	source, err := e.CaptureObservation(context.Background(), inherited)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	session := state.Sessions[source.SessionID]
	if session.ProcessedThrough != 1 || session.ConsumedTurns != 2 || len(state.Nodes) != 0 || !state.Snapshots[source.Key()].Inherited {
		t.Fatal("inherited history was attributed as new work", state)
	}
	tasks, _ := store.Tasks("project")
	if len(tasks) != 0 {
		t.Fatal("inherited copy queued inference", tasks)
	}
	again, err := e.CaptureObservation(context.Background(), inherited)
	if err != nil || again != source {
		t.Fatal("inherited initialization is not idempotent", err)
	}
	resumed := nativeObservation(inherited.Normalized.Turns...)
	resumed.Normalized.Turns = append(resumed.Normalized.Turns, nativeTurn("next", "user", "好的，接下来检查并发问题。", 2))
	resumed = nativeObservation(resumed.Normalized.Turns...)
	next, err := e.CaptureObservation(context.Background(), resumed)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	entries, err := readEntries(store, state.Snapshots[next.Key()], 0, 3)
	if err != nil || !entries[0].Inherited || !entries[1].Inherited || entries[2].Inherited {
		t.Fatal("new dialogue lost attribution", entries, err)
	}
	input, err := prepareStage(store, state, next, false)
	if err != nil || input.Audit.From != 2 || input.Anchors["e1"] || !input.Anchors["e3"] {
		t.Fatal("wrong evidence boundary", input, err)
	}
	e.Runner = output(stage("start", &workgraph.Text{Objective: "检查并发问题"}, "e3"))
	process(t, e, "stage", source.SessionID)
	state, _ = store.State("project")
	node := state.Nodes[state.Sessions[source.SessionID].Tail]
	if node.StartTurn != 2 || node.Source != next || len(state.Nodes) != 1 {
		t.Fatal("inherited dialogue became a second work node", state)
	}
	inherited.Normalized = resumed.Normalized
	inherited.Visible = resumed.Visible
	if _, err := e.CaptureObservation(context.Background(), inherited); err == nil {
		t.Fatal("later work relabeled inherited")
	}
	if err := workgraph.ValidateState(state); err != nil {
		t.Fatal(err)
	}
	replay, err := store.Replay("project")
	if err != nil || workgraph.Digest(replay) != workgraph.Digest(state) {
		t.Fatal("inherited checkpoint cannot replay", err)
	}
}
