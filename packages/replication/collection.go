package replication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

var ErrContentBusy = errors.New("Relay content is protected by a reader or upload")
var ErrCollectionGrace = errors.New("Relay collection is waiting for its protection period")
var ErrObjectMissing = errors.New("Relay content or collection receipt is missing")

type CollectionTransport interface {
	RetentionTransport
	ObjectCatalog(context.Context, string, string, int) (contracts.RelayObjectPage, error)
}

func (t HTTP) ObjectCatalog(ctx context.Context, project, after string, limit int) (contracts.RelayObjectPage, error) {
	var page contracts.RelayObjectPage
	err := t.json(ctx, "GET", project, "objects?after="+url.QueryEscape(after)+"&limit="+strconv.Itoa(limit), nil, &page)
	return page, err
}

// CollectUnused is bounded maintenance: inspect at most 8192 catalog records,
// mark at most 512 objects, and persist the exact request before sending it.
// All snapshot descriptors (including unprocessed ones) protect their closure.
// Raw conversation and native chunks are never downloaded to compute that set.
func (c *Client) CollectUnused(ctx context.Context, project string) (failure error) {
	if remote, ok := c.Remote.(interface {
		ContentPolicy(context.Context, string) (contracts.RelayContentPolicy, error)
	}); ok {
		policy, err := c.contentPolicy(ctx, project, remote.ContentPolicy)
		if err != nil {
			return err
		}
		if policy.Temporary {
			return nil
		} // Temporary storage has server-owned expiry.
	}
	remote, ok := c.Remote.(CollectionTransport)
	if !ok {
		return nil
	}
	c.maintenanceMu.Lock()
	defer c.maintenanceMu.Unlock()
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "collection", Status: "failed", Error: failure.Error()}))
		}
	}()
	progress, err := c.Store.CollectionProgress(project)
	if err != nil {
		return err
	}
	if progress.Plan != nil {
		result, err := remote.Collect(ctx, project, c.Device, progress.Plan.ID)
		if errors.Is(err, ErrObjectMissing) {
			// Either the initial mark response was lost, or it never reached Relay.
			result, err = remote.MarkCollection(ctx, project, *progress.Plan)
			if errors.Is(err, ErrConflict) {
				progress.Plan = nil
				return c.Store.SaveCollectionProgress(project, progress)
			}
		}
		if errors.Is(err, ErrCollectionGrace) || errors.Is(err, ErrContentBusy) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateCollectionResult(result, *progress.Plan, c.Writer()); err != nil {
			return err
		}
		if result.Status == "marked" || result.Status == "deleting" {
			return nil
		}
		if err := c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "collection", Status: result.Status, Revision: progress.Plan.Revision}); err != nil {
			return err
		}
		progress.Plan = nil
		return c.Store.SaveCollectionProgress(project, progress)
	}
	state, err := c.Store.State(project)
	if err != nil || state.Revision == 0 {
		return err
	}
	// Publish the precise accepted boundary before considering object deletion.
	if err := c.PublishCheckpoint(ctx, project); err != nil {
		return err
	}
	readCtx, finish, err := c.sourceReadLease(ctx, project, state.Revision)
	if err != nil {
		return err
	}
	live, err := c.liveObjectClosure(readCtx, state)
	releaseErr := finish()
	if err != nil || releaseErr != nil {
		return errors.Join(err, releaseErr)
	}
	objects := []string{}
	after := progress.After
	for pages := 0; pages < 16 && len(objects) < 512; pages++ {
		page, err := remote.ObjectCatalog(ctx, project, after, 512)
		if err != nil {
			return err
		}
		if page.SchemaVersion != 1 || page.Head != state.Revision || len(page.Objects) > 512 || page.Next != "" && page.Next <= after {
			return errors.New("content catalog changed or has an invalid cursor")
		}
		for _, object := range page.Objects {
			if !validObjectDigest(object.SHA256) || object.Size < 0 {
				return errors.New("invalid content catalog object")
			}
			if !live[object.SHA256] {
				objects = append(objects, object.SHA256)
				if len(objects) == 512 {
					page.Next = object.SHA256
					break
				}
			}
		}
		after = page.Next
		if after == "" {
			break
		}
	}
	progress.After = after
	if len(objects) == 0 {
		return c.Store.SaveCollectionProgress(project, progress)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	plan := contracts.RelayCollectionPlan{SchemaVersion: 1, ID: hex.EncodeToString(nonce[:]), DeviceID: c.Device, Revision: state.Revision, CheckpointSHA256: workgraph.Digest(state), Objects: objects}
	progress.Plan = &plan
	if err := c.Store.SaveCollectionProgress(project, progress); err != nil {
		return err
	}
	result, err := remote.MarkCollection(ctx, project, plan)
	if errors.Is(err, ErrConflict) {
		progress.Plan = nil
		return c.Store.SaveCollectionProgress(project, progress)
	}
	if errors.Is(err, ErrContentBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateCollectionResult(result, plan, c.Writer())
}
func validObjectDigest(digest string) bool {
	b, err := hex.DecodeString(digest)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == digest
}
func validateCollectionResult(result contracts.RelayCollection, plan contracts.RelayCollectionPlan, writer string) error {
	// Relay sorts the set; compare canonical identities independently of order.
	got, want := result.Plan, plan
	got.Objects = nil
	want.Objects = nil
	if result.SchemaVersion != 1 || result.Writer != writer || workgraph.Digest(got) != workgraph.Digest(want) || len(result.Plan.Objects) != len(plan.Objects) {
		return errors.New("Relay collection receipt differs from request")
	}
	seen := map[string]bool{}
	for _, d := range plan.Objects {
		seen[d] = true
	}
	for _, d := range result.Plan.Objects {
		if !seen[d] {
			return errors.New("Relay collection object differs")
		}
		delete(seen, d)
	}
	switch result.Status {
	case "marked", "deleting", "cancelled", "deleted":
		return nil
	default:
		return errors.New("unknown Relay collection status")
	}
}
func (c *Client) liveObjectClosure(ctx context.Context, state workgraph.State) (map[string]bool, error) {
	release := c.Store.PinContent()
	defer release()
	live := map[string]bool{}
	snapshots := []workgraph.Snapshot{}
	for _, snapshot := range state.Snapshots {
		snapshots = append(snapshots, snapshot)
	}
	pending, err := c.Store.Pending(state.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, commit := range pending {
		if commit.Snapshot != nil {
			snapshots = append(snapshots, *commit.Snapshot)
		}
	}
	for _, snapshot := range snapshots {
		for _, digest := range []string{snapshot.Conversation, snapshot.Artifact} {
			if digest != "" {
				if err := c.fetch(ctx, state.ProjectID, digest); err != nil {
					return nil, err
				}
			}
		}
		objects, err := c.objects(workgraph.Commit{Snapshot: &snapshot})
		if err != nil {
			return nil, err
		}
		for _, object := range objects {
			live[object.SHA256] = true
		}
	}
	return live, nil
}
