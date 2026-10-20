package localdag

import (
	"context"
	"errors"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
)

// Reacquire publication rights for a completed result, not permission to rerun
// inference. The exact request and semantic baseline must still be unchanged.
func (e *Engine) reclaimPublication(ctx context.Context, commit workgraph.Commit) (workgraph.Commit, error) {
	c := e.coordinator(ctx)
	if c == nil || commit.Processing == nil {
		return commit, contracts.ErrProcessingStale
	}
	run, err := e.Store.Run(commit.ProjectID, commit.RunID)
	if err != nil {
		return commit, err
	}
	if run.RecoveryInput == nil {
		return commit, errors.New("fixed publication input is unavailable; inspect this task before retrying")
	}
	state, err := e.Store.State(commit.ProjectID)
	if err != nil {
		return commit, err
	}
	shared := contracts.ProcessingTask{SchemaVersion: 1, Spec: *run.RecoveryInput}
	value, err := readProcessingInput(shared)
	if err != nil {
		return commit, err
	}
	if !processingBaselineCurrent(state, shared, value) {
		return commit, workgraph.ErrConflict
	}
	shared, err = c.RegisterProcessing(ctx, commit.ProjectID, shared.Spec)
	if err != nil {
		return commit, err
	}
	if shared.Status == "completed" || shared.Status == "superseded" {
		return commit, workgraph.ErrConflict
	}
	shared, err = c.ActProcessing(ctx, commit.ProjectID, shared.ID, "claim_result", "", "")
	if err != nil {
		return commit, err
	}
	next := commit
	next.ID = newID("commit_")
	next.Base = state.Revision
	auth := shared.Authorization()
	next.Processing = &auth
	if _, _, err := workgraph.Apply(state, next); err != nil {
		return commit, workgraph.ErrConflict
	}
	if err := e.Store.RebasePending(commit, next); err != nil {
		return commit, err
	}
	return next, nil
}
