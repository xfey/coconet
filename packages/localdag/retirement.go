package localdag

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

// RetireConsumed publishes a deterministic metadata transaction. It never
// removes bytes; shared object collection follows an accepted checkpoint.
func (e *Engine) RetireConsumed(ctx context.Context, project string) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if e.Store == nil || e.Actor == "" {
		return errors.New("retirement requires a local writer")
	}
	pending, err := e.Store.Pending(project)
	if err != nil {
		return err
	}
	// An uncertain publication must resolve before another maintenance attempt.
	for _, c := range pending {
		if c.Retire != nil {
			return nil
		}
	}
	state, err := e.Store.State(project)
	if err != nil {
		return err
	}
	protected, err := e.Store.ProtectedSources(project)
	if err != nil {
		return err
	}
	candidates := workgraph.RetirementCandidates(state, e.Actor, protected)
	if len(candidates) == 0 {
		return nil
	}
	input, err := json.Marshal(map[string]any{"revision": state.Revision, "sources": candidates})
	if err != nil {
		return err
	}
	runID := newID("run_")
	taskID := newID("retire_")
	if err := e.Store.Begin(graphstore.Run{ID: runID, TaskID: taskID, ProjectID: project, Kind: "retire", Status: "running", StartedAt: e.stamp(), Template: "retire-consumed/1", Base: state.Revision, Input: input}); err != nil {
		return err
	}
	if err := e.Store.SaveResponse(runID, `{"unreferenced":true}`, nil); err != nil {
		return err
	}
	if err := e.Store.Finish(runID, "ready", "consumed sources have no current or unfinished references", e.stamp()); err != nil {
		return err
	}
	commit := workgraph.Commit{Schema: workgraph.SchemaVersion, ID: newID("commit_"), ProjectID: project, Actor: e.Actor, Base: state.Revision, RunID: runID, At: e.stamp(), Retire: &workgraph.Retirement{Sources: candidates}}
	task := graphstore.Task{ID: taskID, ProjectID: project, Kind: "retire", Status: "publish_pending", RunID: runID}
	if err := e.Store.PutTask(task); err != nil {
		return err
	}
	if err := e.Store.SavePending(commit); err != nil {
		return err
	}
	return e.publish(ctx, commit, task)
}
