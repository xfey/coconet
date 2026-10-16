package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

type LibraryRequest struct {
	RequestID string           `json:"request_id"`
	NodeID    string           `json:"work_node_id"`
	Source    workgraph.Source `json:"source"`
	Note      string           `json:"note,omitempty"`
}
type LibraryResult struct {
	RequestID string           `json:"request_id"`
	RunID     string           `json:"run_id"`
	State     string           `json:"state"`
	NodeID    string           `json:"work_node_id"`
	Source    workgraph.Source `json:"source"`
	Note      string           `json:"note"`
}

// Collect fixes an explicitly selected own node. The source comparison is
// repeated by the reducer on every rebase; a retry can never collect a newer
// source merely because the node advanced while the network was unavailable.
func (e *Engine) Collect(ctx context.Context, project string, request LibraryRequest) (LibraryResult, error) {
	return e.changeLibrary(ctx, project, request, false)
}

// Uncollect keeps active sealed work. A Library-only withdrawn node loses its
// final reference and is removed; native files are never modified.
func (e *Engine) Uncollect(ctx context.Context, project string, request LibraryRequest) (LibraryResult, error) {
	return e.changeLibrary(ctx, project, request, true)
}

func (e *Engine) changeLibrary(ctx context.Context, project string, request LibraryRequest, remove bool) (LibraryResult, error) {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	result := LibraryResult{RequestID: request.RequestID, State: "not_collected", NodeID: request.NodeID, Source: request.Source, Note: request.Note}
	if e.Actor == "" || utf8.RuneCountInString(request.RequestID) < 8 || utf8.RuneCountInString(request.RequestID) > 200 || strings.TrimSpace(request.NodeID) == "" || request.Source.SessionID == "" || request.Source.Version < 1 || !utf8.ValidString(request.Note) || utf8.RuneCountInString(request.Note) > 1000 {
		return result, errors.New("collection requires a stable request_id, selected work node and exact source; note is limited to 1000 characters")
	}
	input, _ := json.Marshal(request)
	completed := "collected"
	if remove {
		if request.Note != "" {
			return result, errors.New("removing a Library entry does not accept a note")
		}
		input, _ = json.Marshal(struct {
			LibraryRequest
			Remove bool `json:"remove"`
		}{request, true})
		completed, result.State = "removed", "not_removed"
	}
	id := "library_" + workgraph.Digest([]string{project, e.Actor, request.RequestID})
	result.RunID = id
	run, err := e.Store.Run(project, id)
	if err != nil && !errors.Is(err, graphstore.ErrRunUnavailable) {
		return result, err
	}
	if err == nil {
		digest := graphstore.ObjectDigest(run.Input)
		if run.Archive != nil {
			digest = run.Archive.InputSHA256
		}
		if digest != graphstore.ObjectDigest(input) {
			return result, errors.New("request_id already selects different collection content; retry with the original arguments")
		}
		if run.Status == "published" {
			result.State = completed
			return result, nil
		}
		if run.Status != "ready" {
			return result, errors.New("collection selection expired after a conflict; refresh the graph and use a new request_id")
		}
		pending, err := e.Store.Pending(project)
		if err != nil {
			return result, err
		}
		tasks, err := e.Store.Tasks(project)
		if err != nil {
			return result, err
		}
		for _, commit := range pending {
			if commit.RunID != id {
				continue
			}
			for _, task := range tasks {
				if task.RunID != id {
					continue
				}
				result.State = "pending"
				err := e.publish(ctx, commit, task)
				if err == nil {
					result.State = completed
				}
				return result, err
			}
		}
		return result, errors.New("collection is missing its durable publication; inspect diagnostics")
	}
	state, err := e.Store.State(project)
	if err != nil {
		return result, err
	}
	node, ok := state.Nodes[request.NodeID]
	if !ok || node.Source != request.Source {
		return result, ErrSelectionChanged
	}
	if !workgraph.SameAccountWriter(state.Sessions[node.Source.SessionID].Owner, e.Actor) {
		return result, workgraph.ErrPermission
	}
	stamp := e.stamp()
	commit := workgraph.Commit{Schema: workgraph.SchemaVersion, ID: newID("commit_"), ProjectID: project, Actor: e.Actor, Base: state.Revision, RunID: id, At: stamp, Library: &workgraph.LibraryChange{NodeID: node.ID, Source: &request.Source, Note: request.Note, Remove: remove}}
	task := graphstore.Task{ID: id, ProjectID: project, Kind: "library", Status: "publish_pending", RunID: id}
	run = graphstore.Run{ID: id, TaskID: id, ProjectID: project, Kind: "library", Status: "ready", StartedAt: stamp, FinishedAt: stamp, Template: "library-selection/1", Base: state.Revision, Source: &request.Source, NodeID: node.ID, Input: input, Output: `{"explicit_collection":true}`}
	if remove {
		run.Output = `{"explicit_removal":true}`
	}
	if err := e.Store.QueueLibrary(run, task, commit); err != nil {
		return result, err
	}
	result.State = "pending"
	if err := e.publish(ctx, commit, task); err != nil {
		return result, err
	}
	result.State = completed
	return result, e.reconcile(project)
}
