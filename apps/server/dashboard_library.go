package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/storageguard"
	"github.com/xfey/coconet/packages/workgraph"
)

type dashboardLibraryRequest struct {
	SchemaVersion int              `json:"schema_version"`
	RequestID     string           `json:"request_id"`
	NodeID        string           `json:"work_node_id"`
	Source        workgraph.Source `json:"source"`
	Remove        bool             `json:"remove"`
}

// This adapter exposes metadata only. Explicit browser edits use the same
// reducer and ordered stream as Agent tools, without a model or Session reads.
type dashboardStateReader struct{ storage *relay.Store }

func (r dashboardStateReader) Checkpoint(ctx context.Context, project string) (contracts.RelayCheckpointPage, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RelayCheckpointPage{}, err
	}
	return r.storage.Checkpoint(project)
}
func (r dashboardStateReader) Since(ctx context.Context, project string, after int64) (contracts.RelayPage, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RelayPage{}, err
	}
	return r.storage.Since(project, after, 100)
}

func (h *serverHandler) handleDashboardLibrary(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string) {
	var input dashboardLibraryRequest
	if decodeJSONLimit(r, &input, 4096) != nil || input.SchemaVersion != 1 || len(input.RequestID) < 8 || len(input.RequestID) > 200 || strings.TrimSpace(input.NodeID) == "" || len(input.NodeID) > 200 || input.Source.SessionID == "" || len(input.Source.SessionID) > 200 || input.Source.Version < 1 {
		h.writeError(w, requestID, invalid("invalid_library_selection", "select an exact work node and source with a stable request ID"))
		return
	}
	if err := h.limiter.check("dashboard.library", actor.AccountID); err != nil {
		h.writeError(w, requestID, err)
		return
	}
	err := h.authority.store.view(func(state databaseState) error {
		_, _, err := requireActiveMember(&state, actor.AccountID, project)
		return err
	})
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	storage, err := h.authority.store.relayStore()
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	// The full intent is the idempotency key. Retrying the same input after a lost
	// reply reuses its receipt even after log compaction or a later opposite edit.
	id := "web_library_" + workgraph.Digest([]any{project, actor.AccountID, input})
	writer := contracts.RelayWriter(actor.AccountID, "dashboard")
	var revision int64
	for attempt := 0; attempt < 4; attempt++ {
		if prior, priorErr := storage.Receipt(project, id); priorErr == nil {
			if prior.Writer != writer {
				err = workgraph.ErrPermission
				break
			}
			revision, err = prior.Revision, nil
			break
		} else if !errors.Is(priorErr, contracts.ErrRelayReceiptMissing) {
			err = priorErr
			break
		}
		var state workgraph.State
		state, err = replication.ReadState(r.Context(), dashboardStateReader{storage}, project)
		if errors.Is(err, contracts.ErrRelayHistoryCompacted) {
			continue
		}
		if err != nil {
			break
		}
		_, selectErr := workgraph.SelectNode(state, input.NodeID, &input.Source)
		if selectErr != nil {
			err = workgraph.ErrSource
			break
		}
		commit := workgraph.Commit{Schema: 1, ID: id, RunID: id, ProjectID: project, Actor: writer, Base: state.Revision, At: h.authority.timestamp(), Library: &workgraph.LibraryChange{Pin: true, NodeID: input.NodeID, Source: &input.Source, Remove: input.Remove, Note: state.Library[input.NodeID]}}
		if input.Remove {
			commit.Library.Note = ""
		}
		var next workgraph.State
		next, _, err = workgraph.Apply(state, commit)
		if err != nil {
			break
		}
		payload, _ := json.Marshal(commit)
		checkpoint, _ := json.Marshal(next)
		if len(checkpoint) > contracts.MaxRelayCheckpointBytes {
			err = relay.ErrQuota
			break
		}
		digest := contentstore.ObjectDigest(checkpoint)
		proposal := contracts.RelayCommit{SchemaVersion: 1, DeviceID: "dashboard", ID: id, Base: commit.Base, Codec: "coconet-json-v6", Payload: payload, SHA256: contentstore.ObjectDigest(payload), StateSHA256: digest, Objects: []string{}}
		var receipt contracts.RelayReceipt
		receipt, err = storage.AppendCheckpoint(project, actor.AccountID, proposal, contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "dashboard", Revision: next.Revision, CommitID: id, Codec: "coconet-state-json-v4", Payload: checkpoint, SHA256: digest})
		// A simultaneous retry may have accepted this exact intent with a different
		// rebase/time. The next iteration reads its durable receipt before reducing.
		if errors.Is(err, relay.ErrConflict) || errors.Is(err, relay.ErrInvalid) {
			continue
		}
		if err != nil {
			break
		}
		revision = receipt.Revision
		break
	}
	if err != nil {
		switch {
		case errors.Is(err, workgraph.ErrSource):
			err = conflict("library_selection_changed", "the selected node or version changed; refresh and select again")
		case errors.Is(err, workgraph.ErrPermission):
			err = forbidden("library_access_denied", "project access is required to change this collection")
		case errors.Is(err, relay.ErrConflict), errors.Is(err, replication.ErrConflict), errors.Is(err, contracts.ErrRelayHistoryCompacted):
			err = conflict("library_busy", "project changed; try again")
		case errors.Is(err, storageguard.ErrCapacity), errors.Is(err, storageguard.ErrSpace):
		default:
			err = unavailable("library_update_failed", "collection could not be updated; retry with the same selection")
		}
		h.writeError(w, requestID, err)
		return
	}
	h.authority.store.events.publish(map[string]bool{project: true}, false)
	result := "collected"
	if input.Remove {
		result = "removed"
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "request_id": input.RequestID, "work_node_id": input.NodeID, "source": input.Source, "state": result, "revision": revision})
}
