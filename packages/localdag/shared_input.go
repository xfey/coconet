package localdag

import (
	"encoding/json"
	"errors"
	"fmt"

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
	if err := validateInput(input); err != nil {
		return contracts.ProcessingSpec{}, err
	}
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
		return contracts.ProcessingSpec{}, fmt.Errorf("complete task package requires %d bytes; limit is %d bytes; input was retained without calling the model", len(data), contracts.MaxProcessingInputBytes)
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
	if err := validateInput(p); err != nil {
		return value, err
	}
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

const maxModelRequestBytes = 512 << 10

func validateInput(input prepared) error {
	if !input.Complete || input.Audit.Omitted != 0 || input.Audit.Truncated != 0 || input.Audit.CandidateOmitted != 0 {
		return errors.New("incomplete task input; no model call or partial result is allowed")
	}
	size := len(input.Request.System) + len(input.Request.Input) + len(input.Request.Schema)
	if size > maxModelRequestBytes {
		return fmt.Errorf("complete model request requires %d bytes; limit is %d bytes (512 KiB), covering turns %d–%d and %d candidates; no text was truncated or sent to the model", size, maxModelRequestBytes, input.Audit.From+1, input.Audit.Through, input.Audit.CandidateCount)
	}
	return nil
}

func (e *Engine) waitInput(task graphstore.Task, state workgraph.State, cause error) error {
	task.Status, task.NextAt, task.Error = "paused", "", cause.Error()
	if task.Kind == "relations" {
		task.ContextBasis = relationContextBasis(state, task.NodeID)
	}
	if err := e.Store.PutTask(task); err != nil {
		return err
	}
	return cause
}

// Context expansion may add background, never just repeat the same request
// under a larger size tier. An unchanged insufficient input waits for evidence.
func (e *Engine) contextRetry(state workgraph.State, task graphstore.Task, input prepared) string {
	if task.Expanded {
		return "paused"
	}
	var expanded prepared
	var err error
	if task.Kind == "stage" {
		expanded, err = prepareStage(e.Store, state, input.Source, true)
	} else {
		expanded, err = prepareRelations(e.Store, state, task.NodeID, true)
	}
	if err != nil || len(expanded.Anchors) <= len(input.Anchors) {
		return "paused"
	}
	return "queued"
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
