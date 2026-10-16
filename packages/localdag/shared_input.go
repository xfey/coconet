package localdag

import (
	"encoding/json"
	"errors"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
	"github.com/xfey/coconet/prompts"
)

// taskInput is portable. Execution (paths, provider configuration, credentials)
// is deliberately absent. The exact request is hashed before the first call.
type taskInput struct {
	Schema       int      `json:"schema"`
	Kind         string   `json:"kind"`
	Expanded     bool     `json:"expanded"`
	ContextBasis string   `json:"context_basis,omitempty"`
	Prepared     prepared `json:"prepared"`
}

func fixedProcessingInput(state workgraph.State, task graphstore.Task, input prepared) (contracts.ProcessingSpec, error) {
	input.Execution = nil
	capsule := taskInput{Schema: 1, Kind: task.Kind, Expanded: task.Expanded, Prepared: input}
	if task.Kind == "relations" {
		capsule.ContextBasis = relationContextBasis(state, task.NodeID)
	}
	data, err := json.Marshal(capsule)
	if err != nil {
		return contracts.ProcessingSpec{}, err
	}
	if len(data) > contracts.MaxProcessingInputBytes {
		return contracts.ProcessingSpec{}, errors.New("fixed task input exceeds shared processing limit")
	}
	return contracts.ProcessingSpec{Key: task.ID, Kind: task.Kind, Owner: state.Sessions[input.Source.SessionID].Owner, SessionID: input.Source.SessionID, Version: input.Source.Version, NodeID: task.NodeID, NodeRevision: input.NodeRevision, Payload: data, SHA256: graphstore.ObjectDigest(data)}, nil
}

func readProcessingInput(t contracts.ProcessingTask) (taskInput, error) {
	var value taskInput
	if t.SchemaVersion != 1 || len(t.Spec.Payload) == 0 || len(t.Spec.Payload) > contracts.MaxProcessingInputBytes || graphstore.ObjectDigest(t.Spec.Payload) != t.Spec.SHA256 {
		return value, errors.New("shared processing input is missing or corrupted")
	}
	if err := json.Unmarshal(t.Spec.Payload, &value); err != nil {
		return value, err
	}
	p := value.Prepared
	if value.Schema != 1 || value.Kind != t.Spec.Kind || p.Execution != nil || p.Source.SessionID != t.Spec.SessionID || p.Source.Version != t.Spec.Version || p.NodeRevision != t.Spec.NodeRevision || p.Audit.Through < p.Audit.From || p.Anchors == nil {
		return value, errors.New("shared processing input metadata differs")
	}
	// Only compatible production templates can be executed. A task cannot
	// distribute arbitrary instructions, schemas, executables or tool settings.
	system, schema := prompts.LocalStageSystem(), stageSchema
	if value.Kind == "relations" {
		system, schema = prompts.LocalRelationsSystem(), relationSchema
	}
	if p.Request.System != system || workgraph.Digest(p.Request.Schema) != workgraph.Digest(schema) {
		return value, errors.New("shared processing template requires a compatible client version")
	}
	return value, nil
}

func processingBaselineCurrent(state workgraph.State, t contracts.ProcessingTask, value taskInput) bool {
	p := value.Prepared
	s, ok := state.Sessions[p.Source.SessionID]
	if !ok || s.Owner != t.Spec.Owner {
		return false
	}
	if _, ok := state.Snapshots[p.Source.Key()]; !ok {
		return false
	}
	if value.Kind == "stage" {
		return s.Revision == p.SessionRevision && s.ProcessedThrough+1 == p.Source.Version
	}
	n, ok := state.Nodes[t.Spec.NodeID]
	return ok && n.Source == p.Source && n.Revision == p.NodeRevision && n.EndTurn == p.Audit.Through && n.Relations != "ready" && relationContextBasis(state, n.ID) == value.ContextBasis
}
