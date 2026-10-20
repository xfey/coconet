package replication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

type CheckpointTransport interface {
	Checkpoint(context.Context, string) (contracts.RelayCheckpointPage, error)
	PutCheckpoint(context.Context, string, contracts.RelayCheckpoint) (contracts.RelayCheckpointAck, error)
	CheckpointHead(context.Context, string) (contracts.RelayCheckpointHead, error)
}

func decodeCheckpoint(project string, r contracts.RelayCheckpointReceipt) (workgraph.State, workgraph.Commit, error) {
	var state workgraph.State
	cp := r.Checkpoint
	switch cp.Codec {
	case "coconet-state-json-v1", "coconet-state-json-v2", "coconet-state-json-v3", "coconet-state-json-v4":
	default:
		return state, workgraph.Commit{}, errors.New("unsupported project checkpoint format; update Coconet on this device and retry synchronization")
	}
	anchor, err := decodeReceipt(project, r.Anchor)
	if err != nil {
		return state, anchor, err
	}
	if r.SchemaVersion != 1 || r.ProjectID != project || r.Writer == "" || cp.SchemaVersion != 1 || cp.Revision != r.Anchor.Revision || cp.CommitID != anchor.ID || cp.SHA256 != r.Anchor.Commit.StateSHA256 || len(cp.Payload) > contracts.MaxRelayCheckpointBytes || graphstore.ObjectDigest(cp.Payload) != cp.SHA256 {
		return state, anchor, errors.New("invalid checkpoint anchor or envelope")
	}
	decoder := json.NewDecoder(bytes.NewReader(cp.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, anchor, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF || state.ProjectID != project || state.Revision != cp.Revision || workgraph.Digest(state) != cp.SHA256 {
		return state, anchor, errors.New("checkpoint state differs from anchor")
	}
	if cp.Codec != "coconet-state-json-v3" && cp.Codec != "coconet-state-json-v4" {
		for _, snapshot := range state.Snapshots {
			if r := snapshot.Rewrite; r != nil && r.RestoreEditableTails {
				return state, anchor, errors.New("editable rewind requires coconet-state-json-v3")
			}
		}
	}
	if len(state.LibrarySources) > 0 && cp.Codec != "coconet-state-json-v4" {
		return state, anchor, errors.New("fixed Library sources require coconet-state-json-v4; update Coconet")
	}
	return state, anchor, workgraph.ValidateState(state)
}

func (c *Client) bootstrap(ctx context.Context, project string) error {
	remote, ok := c.Remote.(CheckpointTransport)
	if !ok {
		return nil
	}
	ready, err := c.Store.CanBootstrap(project)
	if err != nil || !ready {
		return err
	}
	page, err := remote.Checkpoint(ctx, project)
	if err != nil {
		return err
	}
	if page.SchemaVersion != 1 || page.Head < 0 {
		return errors.New("invalid checkpoint page")
	}
	if page.Checkpoint == nil {
		return nil
	}
	state, anchor, err := decodeCheckpoint(project, *page.Checkpoint)
	if err != nil {
		// Preserve the bounded rejected bytes for diagnosis; the advertised hash
		// may be exactly what is broken. Nothing here advances the accepted graph.
		var digest string
		var archiveErr error
		if len(page.Checkpoint.Checkpoint.Payload) <= contracts.MaxRelayCheckpointBytes {
			digest, archiveErr = c.Store.PutObject(page.Checkpoint.Checkpoint.Payload)
		}
		logErr := c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "checkpoint_bootstrap", Status: "failed", Error: err.Error(), SHA256: digest, Revision: page.Checkpoint.Checkpoint.Revision, Receipt: &page.Checkpoint.Anchor})
		return errors.Join(err, archiveErr, logErr)
	}
	if state.Revision > page.Head {
		return errors.New("checkpoint is ahead of relay")
	}
	if err := c.Store.InstallCheckpoint(state, *page.Checkpoint, anchor); err != nil {
		if errors.Is(err, graphstore.ErrBootstrapBusy) {
			return nil
		}
		return err
	}
	return c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "checkpoint_bootstrap", Status: "accepted", CommitID: anchor.ID, Revision: state.Revision, SHA256: page.Checkpoint.Checkpoint.SHA256})
}

// PublishCheckpoint runs after catch-up, never in the model publication path.
// Its failure cannot roll back an accepted graph or trigger another model call.
func (c *Client) PublishCheckpoint(ctx context.Context, project string) (failure error) {
	c.checkpointMu.Lock()
	defer c.checkpointMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, ok := c.Remote.(CheckpointTransport)
	if !ok {
		return nil
	}
	revision, err := c.Store.AcceptedRevision(project)
	if err != nil || revision == 0 {
		return err
	}
	if published, err := c.Store.PublishedCheckpoint(project); err != nil {
		return err
	} else if published >= revision {
		delete(c.checkpointPending, project)
		return nil
	}
	state, commit, err := c.Store.CheckpointState(project)
	if err != nil {
		return err
	}
	record := graphstore.Transfer{ProjectID: project, Operation: "checkpoint_publish", Status: "accepted", CommitID: commit.ID, Revision: state.Revision}
	defer func() {
		if failure != nil {
			record.Status, record.Error = "failed", failure.Error()
		}
		failure = errors.Join(failure, c.Store.RecordTransfer(record))
	}()
	if err := workgraph.ValidateState(state); err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(payload) > contracts.MaxRelayCheckpointBytes {
		return errors.New("DAG checkpoint exceeds 16 MiB limit")
	}
	input := contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: c.Device, Revision: state.Revision, CommitID: commit.ID, Codec: "coconet-state-json-v4", SHA256: graphstore.ObjectDigest(payload), Payload: payload}
	record.SHA256 = input.SHA256
	head, err := remote.CheckpointHead(ctx, project)
	if err != nil {
		return err
	}
	if head.SchemaVersion != 1 || head.ProjectID != project || head.Head < state.Revision {
		return errors.New("checkpoint preflight differs from accepted project")
	}
	if current := head.Checkpoint; current != nil {
		if current.SchemaVersion != 1 || current.ProjectID != project || current.Revision < 1 || current.Revision > head.Head {
			return errors.New("invalid checkpoint preflight")
		}
		if current.Revision >= state.Revision {
			if current.Revision == state.Revision && (current.SHA256 != input.SHA256 || current.CommitID != input.CommitID) {
				return errors.New("published checkpoint differs from local accepted state")
			}
			record.Status = "already_published"
			return c.Store.MarkCheckpointPublished(project, state.Revision)
		}
	}
	// Confirm the immutable accepted anchor before uploading the state.
	page, err := c.Remote.Since(ctx, project, state.Revision-1)
	if errors.Is(err, contracts.ErrRelayHistoryCompacted) {
		return c.Store.MarkCheckpointPublished(project, state.Revision)
	}
	if err != nil {
		return err
	}
	if page.SchemaVersion != 1 || page.Head < state.Revision || len(page.Entries) == 0 {
		return errors.New("checkpoint anchor missing")
	}
	anchor := page.Entries[0]
	accepted, err := decodeReceipt(project, anchor)
	if err != nil || workgraph.Digest(accepted) != workgraph.Digest(commit) {
		return errors.New("checkpoint anchor differs from local head")
	}
	if anchor.Commit.StateSHA256 == "" {
		return errors.New("accepted commit has no checkpoint digest")
	}
	if anchor.Commit.StateSHA256 != input.SHA256 {
		return errors.New("checkpoint digest differs from accepted head")
	}
	receipt, err := remote.PutCheckpoint(ctx, project, input)
	if errors.Is(err, ErrConflict) {
		// Another verified device may already have published a newer cache.
		record.Status = "superseded"
		return c.Store.MarkCheckpointPublished(project, state.Revision)
	}
	if err != nil {
		return err
	}
	if receipt.SchemaVersion != 1 || receipt.ProjectID != project || receipt.Revision != input.Revision || receipt.CommitID != input.CommitID || receipt.SHA256 != input.SHA256 {
		return errors.New("relay acknowledged a different checkpoint")
	}
	return c.Store.MarkCheckpointPublished(project, state.Revision)
}

func (t HTTP) Checkpoint(ctx context.Context, project string) (contracts.RelayCheckpointPage, error) {
	var out contracts.RelayCheckpointPage
	err := t.json(ctx, "GET", project, "checkpoint", nil, &out)
	return out, err
}
func (t HTTP) PutCheckpoint(ctx context.Context, project string, input contracts.RelayCheckpoint) (contracts.RelayCheckpointAck, error) {
	var out contracts.RelayCheckpointAck
	err := t.json(ctx, "POST", project, "checkpoint", input, &out)
	return out, err
}

func (t HTTP) CheckpointHead(ctx context.Context, project string) (contracts.RelayCheckpointHead, error) {
	var out contracts.RelayCheckpointHead
	err := t.json(ctx, "GET", project, "checkpoint-head", nil, &out)
	return out, err
}

type checkpointWindow struct {
	revision       int64
	first, changed time.Time
}

// Background publication groups short bursts, with a ten-second maximum wait.
// Explicit synchronization still calls PublishCheckpoint immediately.
func (c *Client) PublishCheckpointDeferred(ctx context.Context, project string) error {
	revision, err := c.Store.AcceptedRevision(project)
	if err != nil {
		return err
	}
	published, err := c.Store.PublishedCheckpoint(project)
	if err != nil {
		return err
	}
	if published >= revision {
		c.checkpointMu.Lock()
		delete(c.checkpointPending, project)
		c.checkpointMu.Unlock()
		return nil
	}
	c.checkpointMu.Lock()
	if c.checkpointPending == nil {
		c.checkpointPending = map[string]checkpointWindow{}
	}
	now := time.Now()
	window, ok := c.checkpointPending[project]
	if !ok {
		window = checkpointWindow{revision: revision, first: now, changed: now}
	}
	if window.revision != revision {
		window.revision = revision
		window.changed = now
	}
	due := now.Sub(window.changed) >= 2*time.Second || now.Sub(window.first) >= 10*time.Second
	c.checkpointPending[project] = window
	c.checkpointMu.Unlock()
	if !due {
		return nil
	}
	if err := c.PublishCheckpoint(ctx, project); err != nil {
		return err
	}
	c.checkpointMu.Lock()
	delete(c.checkpointPending, project)
	c.checkpointMu.Unlock()
	return nil
}
