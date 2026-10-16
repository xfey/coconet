package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

type NodeRead struct {
	NodeID       string           `json:"node_id,omitempty"`
	NodeRevision int64            `json:"node_revision,omitempty"`
	Source       workgraph.Source `json:"source"`
	CompactPath  string           `json:"compact_path"`
	NativePath   string           `json:"native_path,omitempty"`
}

var ErrSelectionChanged = errors.New("work node source changed; refresh the project graph and select its current source")

// ReadNode resolves a fixed source from the accepted DAG. Callers cannot request
// arbitrary historical versions that no longer appear on retained work nodes.
func (e *Engine) ReadNode(ctx context.Context, project, id string, native bool) (result NodeRead, failure error) {
	return e.readNode(ctx, project, id, nil, native)
}

// ReadSelectedNode checks the caller's selection against the same accepted
// state used to resolve the download. Later graph updates cannot replace it.
func (e *Engine) ReadSelectedNode(ctx context.Context, project, id string, expected workgraph.Source, native bool) (NodeRead, error) {
	return e.readNode(ctx, project, id, &expected, native)
}

func (e *Engine) readNode(ctx context.Context, project, id string, expected *workgraph.Source, native bool) (result NodeRead, failure error) {
	release := e.Store.PinContent()
	defer release()
	var fixedSource *workgraph.Source
	input, _ := json.Marshal(map[string]any{"node_id": id, "native": native, "expected_source": expected})
	runID := newID("read_")
	if err := e.Store.Begin(graphstore.Run{ID: runID, TaskID: runID, ProjectID: project, Kind: "read", Status: "running", StartedAt: e.stamp(), Template: "node-read/1", Input: input}); err != nil {
		return result, err
	}
	defer func() {
		output, _ := json.Marshal(map[string]any{"result": result, "fixed_source": fixedSource})
		if err := e.Store.SaveResponse(runID, string(output), nil); err != nil {
			failure = errors.Join(failure, err)
			return
		}
		status, message := "completed", ""
		if failure != nil {
			status, message = "failed", failure.Error()
			if len(message) > 4000 {
				message = clip(message, 4000)
			}
		}
		failure = errors.Join(failure, e.Store.Finish(runID, status, message, e.stamp()))
	}()
	state, err := e.Store.State(project)
	if err != nil {
		return NodeRead{}, err
	}
	node, ok := state.Nodes[id]
	if id == "" {
		if expected == nil || native || !workgraph.ActiveSources(state)[expected.Key()] {
			return NodeRead{}, errors.New("compact reading requires a retained exact source; original artifacts require a work node")
		}
		node.Source = *expected
	} else {
		if !ok {
			return NodeRead{}, errors.New("work node is unavailable; refresh the project graph")
		}
		if expected != nil && *expected != node.Source {
			return NodeRead{}, ErrSelectionChanged
		}
	}
	snapshot, ok := state.Snapshots[node.Source.Key()]
	if !ok {
		return NodeRead{}, workgraph.ErrSource
	}
	fixedSource = &snapshot.Source
	if native && snapshot.Artifact == "" {
		return NodeRead{}, errors.New("this work source has no native artifact")
	}
	result = NodeRead{NodeID: node.ID, NodeRevision: node.Revision, Source: node.Source}
	materialize := func() error {
		var err error
		result.CompactPath, err = e.Store.ConversationFile(ctx, snapshot.Conversation, snapshot.Turns)
		if err == nil && native {
			result.NativePath, err = e.Store.ArtifactFile(snapshot.Artifact)
		}
		return err
	}
	// Verified local content needs no remote lease. PinContent protects it from
	// local collection; a lease is only necessary when downloading missing bytes.
	if err = materialize(); err == nil {
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return NodeRead{}, err
	}
	if fetcher, ok := e.Publisher.(interface {
		Pull(context.Context, string, workgraph.Source, bool) error
	}); ok {
		if err := fetcher.Pull(ctx, project, node.Source, native); err != nil {
			return NodeRead{}, err
		}
	}
	if err := materialize(); err != nil {
		return NodeRead{}, err
	}
	return result, nil
}
