package replication

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

// UploadSource transfers verified local bytes separately from graph publication.
// Retries ask for missing chunks, so interrupted transfers retain useful progress.
func (c *Client) UploadSource(ctx context.Context, project string, snapshot workgraph.Snapshot) error {
	release := c.Store.PinContent()
	defer release()
	objects, err := c.objects(workgraph.Commit{Snapshot: &snapshot})
	if err != nil {
		return err
	}
	missing, err := c.Remote.Missing(ctx, project, contracts.RelayObjectCheck{SchemaVersion: 1, Objects: objects})
	if err != nil {
		return err
	}
	if missing.SchemaVersion != 1 {
		return errors.New("unsupported object check response")
	}
	sizes := map[string]int64{}
	for _, object := range objects {
		sizes[object.SHA256] = object.Size
	}
	seen := map[string]bool{}
	for _, digest := range missing.Missing {
		size, ok := sizes[digest]
		if !ok || seen[digest] {
			return errors.New("relay requested an unoffered object")
		}
		seen[digest] = true
		file, err := c.Store.OpenObject(digest)
		if err != nil {
			return err
		}
		err = c.Remote.Put(ctx, project, digest, size, file)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

// SyncContents advances a bounded batch of durable source tasks. Graph sync and
// model execution never wait for this worker. A snapshot is the durable queue
// root; lost task acknowledgements can safely be repeated after reopening.
func (c *Client) SyncContents(ctx context.Context, project string) error {
	return c.syncContents(ctx, project, true)
}

// Background workers share the last graph poll's hints. Explicit operations
// remain immediate; a missing/expired hint always falls back to a real check.
func (c *Client) SyncContentsBackground(ctx context.Context, project string) error {
	hint, fresh := c.syncHint(project)
	return c.syncContents(ctx, project, !fresh || hint.RepairsPending)
}

func (c *Client) syncContents(ctx context.Context, project string, repairs bool) error {
	c.contentMu.Lock()
	defer c.contentMu.Unlock()
	release := c.Store.PinContent()
	defer release()
	state, err := c.Store.State(project)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(state.Snapshots))
	for key := range workgraph.ReadableSources(state) {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if c.sourceCursor == nil {
		c.sourceCursor = map[string]string{}
	}
	start := sort.SearchStrings(keys, c.sourceCursor[project])
	if start < len(keys) && keys[start] == c.sourceCursor[project] {
		start++
	}
	keys = append(append([]string{}, keys[start:]...), keys[:start]...)
	var failures []error
	if repairs {
		if err := c.serveRepairs(ctx, project, state); err != nil {
			failures = append(failures, err)
		}
	}
	processed := 0
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		snapshot, exists := state.Snapshots[key]
		if !exists {
			continue
		}
		progress, err := c.Store.Replica(project, snapshot)
		if err != nil {
			return err
		}
		owner := snapshot.Owner == c.Writer()
		if progress.TextReady && progress.NativeReady && (!owner || progress.Uploaded) {
			continue
		}
		if next, _ := time.Parse(time.RFC3339Nano, progress.NextAt); time.Now().Before(next) {
			continue
		}
		if processed == 16 {
			break
		}
		processed++
		c.sourceCursor[project] = key
		started := time.Now()
		attempt, cancel := context.WithTimeout(ctx, 45*time.Second)
		if owner {
			if !progress.TextReady {
				err = c.Store.WalkConversation(attempt, snapshot.Conversation, snapshot.Turns, func(conversation.Entry) error { return nil })
				progress.TextReady = err == nil
			}
			if err == nil && !progress.NativeReady {
				if snapshot.Artifact != "" {
					err = c.Store.ValidateArtifact(snapshot.Artifact)
				}
				progress.NativeReady = err == nil
			}
			if err == nil {
				err = c.UploadSource(attempt, project, snapshot)
			}
			progress.Uploaded = err == nil
		} else {
			if !progress.TextReady {
				err = c.Pull(attempt, project, snapshot.Source, false)
				progress.TextReady = err == nil
			}
			if err == nil && !progress.NativeReady {
				if snapshot.Artifact != "" {
					err = c.Pull(attempt, project, snapshot.Source, true)
				}
				progress.NativeReady = err == nil
			}
		}
		cancel()
		progress.Error, progress.NextAt = "", ""
		progress.Attempts++
		if err != nil {
			progress.Error = err.Error()
			if len(progress.Error) > 4000 {
				progress.Error = progress.Error[:4000]
			}
			progress.NextAt = time.Now().Add(time.Duration(1<<min(progress.Attempts, 6)) * time.Second).UTC().Format(time.RFC3339Nano)
			failures = append(failures, err)
		}
		if err := c.Store.SaveReplica(project, progress); err != nil && !errors.Is(err, workgraph.ErrSource) {
			return err
		}
		status := "completed"
		if progress.Error != "" {
			status = "waiting"
		}
		if err := c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "replica", Status: status, Error: progress.Error, SHA256: snapshot.Digest, Revision: state.Revision, Source: &snapshot.Source, DurationMS: time.Since(started).Milliseconds()}); err != nil {
			return err
		}
	}
	if err := c.Store.PruneReplicaProgress(project); err != nil {
		return err
	}
	return errors.Join(failures...)
}
