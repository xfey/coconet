package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

type retentionWire struct {
	s        *Store
	account  string
	loseMark bool
}

func retentionError(err error) error {
	switch {
	case errors.Is(err, ErrConflict):
		return replication.ErrConflict
	case errors.Is(err, ErrObject):
		return replication.ErrObjectMissing
	case errors.Is(err, ErrBusy):
		return replication.ErrContentBusy
	case errors.Is(err, ErrGrace):
		return replication.ErrCollectionGrace
	default:
		return err
	}
}
func (w *retentionWire) Append(_ context.Context, p string, c contracts.RelayCommit) (contracts.RelayReceipt, error) {
	v, e := w.s.Append(p, w.account, c)
	return v, retentionError(e)
}
func (w *retentionWire) Since(_ context.Context, p string, a int64) (contracts.RelayPage, error) {
	return w.s.Since(p, a, 100)
}
func (w *retentionWire) Missing(_ context.Context, p string, c contracts.RelayObjectCheck) (contracts.RelayMissingObjects, error) {
	return w.s.Missing(p, c)
}
func (w *retentionWire) Put(_ context.Context, p, d string, n int64, r io.Reader) error {
	return w.s.PutObject(p, d, n, r)
}
func (w *retentionWire) Get(_ context.Context, p, d string) (io.ReadCloser, error) {
	return w.s.Object(p, d)
}
func (w *retentionWire) Checkpoint(_ context.Context, p string) (contracts.RelayCheckpointPage, error) {
	return w.s.Checkpoint(p)
}
func (w *retentionWire) PutCheckpoint(_ context.Context, p string, c contracts.RelayCheckpoint) (contracts.RelayCheckpointAck, error) {
	v, e := w.s.PutCheckpoint(p, w.account, c)
	return v.Acknowledgement(), retentionError(e)
}
func (w *retentionWire) ReadLease(_ context.Context, p string, c contracts.RelayReadLeaseRequest) (contracts.RelayReadLease, error) {
	v, e := w.s.ReadLease(p, w.account, c)
	return v, retentionError(e)
}
func (w *retentionWire) ReleaseReadLease(_ context.Context, p, d, id string) error {
	return w.s.ReleaseReadLease(p, w.account, d, id)
}
func (w *retentionWire) ObjectCatalog(_ context.Context, p, a string, n int) (contracts.RelayObjectPage, error) {
	return w.s.ObjectCatalog(p, a, n)
}
func (w *retentionWire) MarkCollection(_ context.Context, p string, c contracts.RelayCollectionPlan) (contracts.RelayCollection, error) {
	v, e := w.s.MarkCollection(p, w.account, c)
	if e == nil && w.loseMark {
		w.loseMark = false
		return v, errors.New("mark response lost")
	}
	return v, retentionError(e)
}
func (w *retentionWire) Collect(_ context.Context, p, d, id string) (contracts.RelayCollection, error) {
	v, e := w.s.Collect(p, w.account, d, id)
	return v, retentionError(e)
}

type retirementModel string

func (m retirementModel) Infer(context.Context, inference.Request) (inference.Response, error) {
	return inference.Response{Text: string(m), Visible: string(m)}, nil
}

func TestRetirementCollectionRestartAndFreshDeviceReads(t *testing.T) {
	ctx := context.Background()
	server := fixture(t)
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	wire := &retentionWire{s: server, account: "owner"}
	client := &replication.Client{Store: store, Remote: wire, Account: "owner", Device: "producer"}
	engine := &localdag.Engine{Store: store, Publisher: client, Actor: client.Writer()}
	entries := []conversation.Entry{}
	var latest workgraph.Source
	for version := 1; version <= 3; version++ {
		entries = append(entries, conversation.Entry{Turn: contracts.NormalizedTurn{TurnID: fmt.Sprint(version), Ordinal: version - 1, Role: "user", Kind: "message", Text: fmt.Sprintf("Update the same deliverable %d", version)}})
		latest, err = engine.Capture(ctx, localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "history-copy", Entries: entries, Artifact: []byte(fmt.Sprintf("fixed original version %d", version))})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.SyncContents(ctx, "project"); err != nil {
			t.Fatal(err)
		}
		action := "continue"
		if version == 1 {
			action = "start"
		}
		engine.Runner = retirementModel(fmt.Sprintf(`{"action":%q,"stage":{"objective":"Improve feedback","summary":"Current implementation","approach":"Explicit errors"},"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"same deliverable","evidence":["e%d"]}`, action, version))
		tasks, _ := store.Tasks("project")
		for _, task := range tasks {
			if task.Kind == "stage" {
				if err := engine.Process(ctx, task); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := engine.RetireConsumed(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if len(state.Snapshots) != 1 {
		t.Fatal("old descriptors remain", len(state.Snapshots))
	}
	wire.loseMark = true
	if err := client.CollectUnused(ctx, "project"); err == nil {
		t.Fatal("lost mark response hidden")
	}
	progress, err := store.CollectionProgress("project")
	if err != nil || progress.Plan == nil || len(progress.Plan.Objects) == 0 {
		t.Fatal(progress, err)
	}
	snapshot := state.Snapshots[latest.Key()]
	for _, digest := range progress.Plan.Objects {
		if digest == snapshot.Conversation || digest == snapshot.Artifact {
			t.Fatal("live root selected")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	client = &replication.Client{Store: store, Remote: wire, Account: "owner", Device: "producer"}
	if err := client.CollectUnused(ctx, "project"); err != nil {
		t.Fatal("grace retry", err)
	}
	now = now.Add(collectionGrace + time.Second)
	if err := client.CollectUnused(ctx, "project"); err != nil {
		t.Fatal("collection retry", err)
	}
	for _, digest := range progress.Plan.Objects {
		if f, err := server.objects.OpenObject(digest); err == nil {
			f.Close()
			t.Fatal("unused physical bytes remain")
		}
	}
	freshStore, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer freshStore.Close()
	fresh := &replication.Client{Store: freshStore, Remote: &retentionWire{s: server, account: "reader"}, Account: "reader", Device: "fresh"}
	if err := fresh.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	restored, _ := freshStore.State("project")
	if workgraph.Digest(restored) != workgraph.Digest(state) {
		t.Fatal("checkpoint did not restore retained graph")
	}
	history, _ := freshStore.History("project")
	if len(history) != 0 {
		t.Fatal("fresh device replayed discarded metadata history")
	}
	if err := fresh.Pull(ctx, "project", latest, true); err != nil {
		t.Fatal("key source unreadable after collection", err)
	}
	if _, err := freshStore.ArtifactFile(snapshot.Artifact); err != nil {
		t.Fatal(err)
	}
	old := latest
	old.Version = 1
	if err := fresh.Pull(ctx, "project", old, false); !errors.Is(err, workgraph.ErrSource) {
		t.Fatal("retired version became selectable", err)
	}
	replay, err := store.Replay("project")
	if err != nil || workgraph.Digest(replay) != workgraph.Digest(state) {
		t.Fatal("metadata replay needed collected content", err)
	}
}

func (w *retentionWire) CheckpointHead(_ context.Context, p string) (contracts.RelayCheckpointHead, error) {
	return w.s.CheckpointHead(p)
}
