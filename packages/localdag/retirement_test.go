package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
)

func retireFixture(t *testing.T) (*Engine, *graphstore.Store) {
	e, store := fixture(t)
	for i := 1; i <= 3; i++ {
		texts := []string{"先补上失败提示。", "完成修改并测试。", "再把说明精简一下。"}
		capture(t, e, "session", i, texts[:i]...)
		action := "continue"
		if i == 1 {
			action = "start"
		}
		e.Runner = output(stage(action, &workgraph.Text{Objective: "完善失败提示", Summary: "完成当前修改"}, "e"+string(rune('0'+i))))
		process(t, e, "stage", "session")
	}
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		t.Error("maintenance called a model")
		return inference.Response{}, errors.New("unexpected call")
	})
	return e, store
}
func TestRetirementProtectsPausedAndRunningSourcesThenReplays(t *testing.T) {
	e, store := retireFixture(t)
	ctx := context.Background()
	first := workgraph.Source{SessionID: "session", Version: 1}
	second := workgraph.Source{SessionID: "session", Version: 2}
	if err := store.PutTask(graphstore.Task{ID: "paused_work", ProjectID: "project", Kind: "fixture", Status: "paused", Source: &first}); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(graphstore.Run{ID: "running", TaskID: "running", ProjectID: "project", Kind: "relations", Status: "running", StartedAt: e.stamp(), Input: json.RawMessage(`{}`), Source: &second}); err != nil {
		t.Fatal(err)
	}
	if err := e.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if len(state.Snapshots) != 3 {
		t.Fatal("unfinished work lost its input")
	}
	if err := store.DeleteTask("project", "paused_work"); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish("running", "superseded", "stale", e.stamp()); err != nil {
		t.Fatal(err)
	}
	if err := e.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	if len(state.Snapshots) != 1 || state.Sessions["session"].Latest != 3 || state.Sessions["session"].ProcessedThrough != 3 {
		t.Fatal("retirement did not preserve monotonic boundaries")
	}
	replay, err := store.Replay("project")
	if err != nil || workgraph.Digest(replay) != workgraph.Digest(state) {
		t.Fatal("replay differs", err)
	}
	if err := e.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	after, _ := store.State("project")
	if after.Revision != state.Revision {
		t.Fatal("empty maintenance created a commit")
	}
}
func TestRetirementUncertainPublicationRetriesWithoutModel(t *testing.T) {
	e, store := retireFixture(t)
	ctx := context.Background()
	e.Publisher = publisherFunc(func(context.Context, workgraph.Commit) error { return errors.New("offline") })
	if err := e.RetireConsumed(ctx, "project"); err == nil {
		t.Fatal("offline publication hidden")
	}
	pending, _ := store.Pending("project")
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	if err := e.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	again, _ := store.Pending("project")
	if len(again) != 1 || again[0].ID != pending[0].ID {
		t.Fatal("uncertain request replaced")
	}
	e.Publisher = nil
	now := e.now()
	e.Now = func() time.Time { return now.Add(time.Hour) }
	if err := e.Recover(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if len(state.Snapshots) != 1 {
		t.Fatal("maintenance not recovered")
	}
	tasks, _ := store.Tasks("project")
	for _, task := range tasks {
		if task.Kind == "retire" {
			t.Fatal("retired task remains", task)
		}
	}
}
func TestRetirementConflictRecomputesWithoutQueuingSemanticTask(t *testing.T) {
	e, store := retireFixture(t)
	ctx := context.Background()
	e.Publisher = publisherFunc(func(_ context.Context, commit workgraph.Commit) error {
		state, _ := store.State("project")
		snapshot := state.Snapshots[workgraph.Source{SessionID: "session", Version: 3}.Key()]
		snapshot.Version = 4
		snapshot.Digest = workgraph.Digest("new observation")
		_, err := store.Apply(workgraph.Commit{Schema: 1, ID: "concurrent", ProjectID: "project", Actor: e.Actor, Base: state.Revision, RunID: "concurrent_run", At: e.stamp(), Snapshot: &snapshot}, false)
		if err != nil {
			return err
		}
		return workgraph.ErrConflict
	})
	if err := e.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.Pending("project")
	if len(pending) != 0 {
		t.Fatal("stale plan remains")
	}
	e.Publisher = nil
	if err := e.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if len(state.Snapshots) != 2 {
		t.Fatal("latest and unprocessed boundary not preserved", len(state.Snapshots))
	}
	tasks, _ := store.Tasks("project")
	for _, task := range tasks {
		if task.Kind == "retire" {
			t.Fatal("maintenance became a model task")
		}
	}
}

func TestArchivePressureDoesNotBlockModelOrPublication(t *testing.T) {
	e, store := fixture(t)
	if err := store.SetDiagnosticPolicy(graphstore.DiagnosticPolicy{Days: 7, MaxBytes: 32 << 20}); err != nil {
		t.Fatal(err)
	}
	capture(t, e, "session", 1, "检查提示。")
	for _, id := range []string{"pinned1", "pinned2"} {
		if err := store.Begin(graphstore.Run{ID: id, TaskID: id, ProjectID: "project", Kind: "fixture", Status: "running", StartedAt: e.stamp(), Input: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := store.PinRun("project", id, true); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveResponse(id, strings.Repeat("\x00", 3<<20), nil); err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(id, "completed", "", e.stamp()); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		calls++
		return inference.Response{Text: `{"action":"none","stage":null,"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"inspection only","evidence":[]}`}, nil
	})
	if err := e.Process(context.Background(), task(t, e, "stage", "session")); err != nil {
		t.Fatal(err)
	}
	state, err := store.State("project")
	if err != nil || calls != 1 || state.Sessions["session"].ProcessedThrough != 1 {
		t.Fatal("archive blocked model or publication", calls, state.Sessions, err)
	}
}
