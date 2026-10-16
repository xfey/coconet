// Package replication validates ordered Relay receipts and applies local graph
// replicas. Synchronization of graph metadata does not fetch Session content.
package replication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"sync"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/sessionartifact"
	"github.com/xfey/coconet/packages/workgraph"
)

var ErrConflict = errors.New("relay revision conflict")
var ErrCheckpointCapacity = errors.New("project DAG would exceed the 16 MiB checkpoint limit; retire consumed unused versions or split new work into another project before retrying")

type Transport interface {
	Append(context.Context, string, contracts.RelayCommit) (contracts.RelayReceipt, error)
	Since(context.Context, string, int64) (contracts.RelayPage, error)
	Missing(context.Context, string, contracts.RelayObjectCheck) (contracts.RelayMissingObjects, error)
	Put(context.Context, string, string, int64, io.Reader) error
	Get(context.Context, string, string) (io.ReadCloser, error)
}
type Client struct {
	hintsMu sync.Mutex
	hints   map[string]syncHint

	checkpointMu      sync.Mutex
	checkpointPending map[string]checkpointWindow
	maintenanceMu     sync.Mutex
	contentMu         sync.Mutex
	repairCursor      map[string]string
	sourceCursor      map[string]string
	Store             *graphstore.Store
	Remote            Transport
	Account, Device   string
}

func (c *Client) Writer() string { return contracts.RelayWriter(c.Account, c.Device) }

func decodeReceipt(project string, receipt contracts.RelayReceipt) (workgraph.Commit, error) {
	var commit workgraph.Commit
	if receipt.SchemaVersion != 1 || receipt.ProjectID != project || receipt.Commit.SchemaVersion != 1 || (receipt.Commit.Codec != "coconet-json-v2" && receipt.Commit.Codec != "coconet-json-v3" && receipt.Commit.Codec != "coconet-json-v4" && receipt.Commit.Codec != "coconet-json-v5") || receipt.Commit.SHA256 != graphstore.ObjectDigest(receipt.Commit.Payload) || receipt.Revision != receipt.Commit.Base+1 {
		return commit, errors.New("invalid relay receipt envelope")
	}
	decoder := json.NewDecoder(bytes.NewReader(receipt.Commit.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&commit); err != nil {
		return commit, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return commit, errors.New("relay payload is not one commit")
	}
	if commit.ID != receipt.Commit.ID || commit.ProjectID != project || commit.Base != receipt.Commit.Base || commit.Actor != receipt.Writer {
		return commit, errors.New("relay header and graph commit disagree")
	}
	if !reflect.DeepEqual(commit.Processing, receipt.Commit.Processing) || (commit.Processing != nil && receipt.Commit.Codec != "coconet-json-v3" && receipt.Commit.Codec != "coconet-json-v4" && receipt.Commit.Codec != "coconet-json-v5") {
		return commit, errors.New("processing authorization differs from accepted receipt")
	}
	if commit.Snapshot != nil {
		if r := commit.Snapshot.Rewrite; r != nil && r.RestoreEditableTails && receipt.Commit.Codec != "coconet-json-v5" {
			return commit, errors.New("editable rewind requires coconet-json-v5")
		}
		refs := map[string]bool{}
		for _, digest := range receipt.Commit.Objects {
			refs[digest] = true
		}
		if !refs[commit.Snapshot.Conversation] || commit.Snapshot.Artifact != "" && !refs[commit.Snapshot.Artifact] {
			return commit, errors.New("snapshot content identity was not declared in relay commit")
		}
	} else if len(receipt.Commit.Objects) > 0 {
		return commit, errors.New("non-snapshot commit carries unexpected objects")
	}
	return commit, nil
}

func (c *Client) Sync(ctx context.Context, project string) (failure error) {
	var observed *contracts.RelayReceipt
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "sync", Status: "failed", Error: failure.Error(), Receipt: observed}))
		}
	}()
	if err := c.bootstrap(ctx, project); err != nil {
		return err
	}
	for {
		revision, err := c.Store.AcceptedRevision(project)
		if err != nil {
			return err
		}
		page, err := c.Remote.Since(ctx, project, revision)
		if errors.Is(err, contracts.ErrRelayHistoryCompacted) {
			if err := c.recoverCheckpoint(ctx, project, revision); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if page.SchemaVersion != 1 || page.Head < revision {
			return errors.New("relay is behind the accepted local revision")
		}
		if err := c.rememberHints(project, page.Hints); err != nil {
			return err
		}
		if len(page.Entries) == 0 {
			if page.Head != revision {
				return errors.New("relay omitted required commits")
			}
			return nil
		}
		for _, receipt := range page.Entries {
			observed = &receipt
			if receipt.Revision != revision+1 || receipt.Revision > page.Head {
				return errors.New("relay commit sequence is not contiguous")
			}
			commit, err := decodeReceipt(project, receipt)
			if err != nil {
				return err
			}
			if err := c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "receive", Status: "validated", CommitID: commit.ID, Receipt: &receipt}); err != nil {
				return err
			}
			if _, err := c.Store.ApplyVerified(commit, false, receipt.Commit.StateSHA256); err != nil {
				return err
			}
			revision = receipt.Revision
		}
		if revision == page.Head {
			return nil
		}
	}
}

// Publish validates the graph and local content before declaring their identity.
// Byte transfer runs independently. A conflict refreshes the replica before
// invalidating the caller's semantic baseline.
func (c *Client) Publish(ctx context.Context, commit workgraph.Commit) (failure error) {
	release := c.Store.PinContent()
	defer release()
	var observed *contracts.RelayReceipt
	defer func() {
		record := graphstore.Transfer{ProjectID: commit.ProjectID, Operation: "publish", Status: "accepted", CommitID: commit.ID, Receipt: observed}
		if failure != nil {
			record.Status, record.Error = "failed", failure.Error()
		}
		failure = errors.Join(failure, c.Store.RecordTransfer(record))
	}()
	if commit.Actor != c.Writer() {
		return errors.New("local writer does not match relay credentials")
	}
	state, err := c.Store.State(commit.ProjectID)
	if err != nil {
		return err
	}
	// A receipt may have been lost after peer catch-up already applied this ID.
	if state.Revision > commit.Base {
		history, err := c.Store.History(commit.ProjectID)
		if err != nil {
			return err
		}
		for _, entry := range history {
			if entry.Commit.ID == commit.ID {
				if workgraph.Digest(entry.Commit) != workgraph.Digest(commit) {
					return errors.New("accepted commit identity differs")
				}
				return nil
			}
		}
		return workgraph.ErrConflict
	}
	next, _, err := workgraph.Apply(state, commit)
	if err != nil {
		return err
	}
	// A graph accepted by Relay must remain publishable as a checkpoint. Check
	// before any upload or append, including commits that add only metadata.
	statePayload, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(statePayload) > contracts.MaxRelayCheckpointBytes {
		return ErrCheckpointCapacity
	}
	if commit.Snapshot != nil {
		snapshot := commit.Snapshot
		if err := c.Store.WalkConversation(ctx, snapshot.Conversation, snapshot.Turns, func(conversation.Entry) error { return nil }); err != nil {
			return err
		}
		if snapshot.Artifact != "" {
			if err := c.Store.ValidateArtifact(snapshot.Artifact); err != nil {
				return err
			}
		}
	}
	objects, err := c.objects(commit)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(commit)
	if err != nil {
		return err
	}
	refs := []string{}
	for _, object := range objects {
		refs = append(refs, object.SHA256)
	}
	proposal := contracts.RelayCommit{SchemaVersion: 1, ID: commit.ID, DeviceID: c.Device, Base: commit.Base, Codec: "coconet-json-v5", Payload: payload, SHA256: graphstore.ObjectDigest(payload), Objects: refs, StateSHA256: graphstore.ObjectDigest(statePayload)}
	proposal.Processing = commit.Processing
	receipt, err := c.Remote.Append(ctx, commit.ProjectID, proposal)
	if errors.Is(err, ErrConflict) || errors.Is(err, contracts.ErrProcessingStale) {
		if err := c.Sync(ctx, commit.ProjectID); err != nil {
			return err
		}
		return workgraph.ErrConflict
	}
	if err != nil {
		return err
	}
	observed = &receipt
	accepted, err := decodeReceipt(commit.ProjectID, receipt)
	if err != nil {
		return err
	}
	if workgraph.Digest(accepted) != workgraph.Digest(commit) || receipt.Commit.StateSHA256 != proposal.StateSHA256 {
		return errors.New("relay acknowledged different commit content")
	}
	return nil
}

func (c *Client) objects(commit workgraph.Commit) ([]contracts.RelayObject, error) {
	result := []contracts.RelayObject{}
	if commit.Snapshot == nil {
		return result, nil
	}
	data, err := c.Store.Object(commit.Snapshot.Conversation)
	if err != nil {
		return nil, err
	}
	var manifest conversation.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if err := conversation.Validate(manifest); err != nil {
		return nil, err
	}
	if manifest.Count != commit.Snapshot.Turns {
		return nil, errors.New("snapshot conversation length differs")
	}
	objects := map[string]int64{commit.Snapshot.Conversation: int64(len(data))}
	for _, block := range manifest.Blocks {
		objects[block.SHA256] = int64(block.Size)
	}
	if digest := commit.Snapshot.Artifact; digest != "" {
		manifest, err := c.Store.ArtifactManifest(digest)
		if err != nil {
			return nil, err
		}
		for _, chunk := range manifest.Chunks {
			objects[sessionartifact.StoredHash(chunk)] = sessionartifact.StoredSize(chunk)
		}
		file, err := c.Store.OpenObject(digest)
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		file.Close()
		if err != nil {
			return nil, err
		}
		objects[digest] = info.Size()
	}
	for digest, size := range objects {
		result = append(result, contracts.RelayObject{SHA256: digest, Size: size})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SHA256 < result[j].SHA256 })
	return result, nil
}

func (c *Client) fetch(ctx context.Context, project, digest string) error {
	if file, err := c.Store.OpenObject(digest); err == nil {
		return file.Close()
	}
	reader, err := c.Remote.Get(ctx, project, digest)
	if errors.Is(err, ErrObjectMissing) {
		if remote, ok := c.Remote.(repairTransport); ok {
			if requestErr := remote.RequestRepair(ctx, project, digest); requestErr != nil {
				return errors.Join(err, requestErr)
			}
			return ErrWaitingForReplica
		}
	}
	if err != nil {
		return err
	}
	defer reader.Close()
	return c.Store.Receive(reader, digest)
}

// Pull fetches only a retained fixed source. Native bytes are opt-in for an
// explicit continuation; ordinary graph replication never invokes this method.
func (c *Client) Pull(ctx context.Context, project string, source workgraph.Source, original bool) (failure error) {
	release := c.Store.PinContent()
	defer release()
	state, err := c.Store.State(project)
	if err != nil {
		return err
	}
	leaseContext, finish, err := c.sourceReadLease(ctx, project, state.Revision)
	if errors.Is(err, ErrConflict) {
		if err := c.Sync(ctx, project); err != nil {
			return err
		}
		state, err = c.Store.State(project)
		if err != nil {
			return err
		}
		leaseContext, finish, err = c.sourceReadLease(ctx, project, state.Revision)
	}
	if err != nil {
		return err
	}
	ctx = leaseContext
	defer func() { failure = errors.Join(failure, finish()) }()
	if !workgraph.ReadableSources(state)[source.Key()] {
		return workgraph.ErrSource
	}
	snapshot, ok := state.Snapshots[source.Key()]
	if !ok {
		return workgraph.ErrSource
	}
	if err := c.fetch(ctx, project, snapshot.Conversation); err != nil {
		return err
	}
	data, err := c.Store.Object(snapshot.Conversation)
	if err != nil {
		return err
	}
	var manifest conversation.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if err := conversation.Validate(manifest); err != nil {
		return err
	}
	if manifest.Count != snapshot.Turns {
		return errors.New("downloaded conversation differs from source")
	}
	for _, block := range manifest.Blocks {
		if err := c.fetch(ctx, project, block.SHA256); err != nil {
			return err
		}
	}
	if err := c.Store.WalkConversation(ctx, snapshot.Conversation, snapshot.Turns, func(conversation.Entry) error { return nil }); err != nil {
		return err
	}
	if original {
		if snapshot.Artifact == "" {
			return errors.New("fixed source has no native artifact")
		}
		if err := c.fetch(ctx, project, snapshot.Artifact); err != nil {
			return err
		}
		manifest, err := c.Store.ArtifactManifest(snapshot.Artifact)
		if err != nil {
			return err
		}
		for _, chunk := range manifest.Chunks {
			if err := c.fetch(ctx, project, sessionartifact.StoredHash(chunk)); err != nil {
				return err
			}
		}
		return c.Store.ValidateArtifact(snapshot.Artifact)
	}
	return nil
}
