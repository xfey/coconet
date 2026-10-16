package replication

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

type localTransport struct {
	s           *relay.Store
	account     string
	puts        int
	gets        int
	loseReceipt bool
}

func (t *localTransport) Append(_ context.Context, p string, c contracts.RelayCommit) (contracts.RelayReceipt, error) {
	receipt, err := t.s.Append(p, t.account, c)
	if errors.Is(err, relay.ErrConflict) {
		return receipt, ErrConflict
	}
	if err == nil && t.loseReceipt {
		t.loseReceipt = false
		return contracts.RelayReceipt{}, errors.New("response lost")
	}
	return receipt, err
}
func (t *localTransport) Since(_ context.Context, p string, after int64) (contracts.RelayPage, error) {
	return t.s.Since(p, after, 100)
}
func (t *localTransport) Missing(_ context.Context, p string, c contracts.RelayObjectCheck) (contracts.RelayMissingObjects, error) {
	return t.s.Missing(p, c)
}
func (t *localTransport) Put(_ context.Context, p, d string, size int64, r io.Reader) error {
	t.puts++
	return t.s.PutObject(p, d, size, r)
}
func (t *localTransport) Get(_ context.Context, p, d string) (io.ReadCloser, error) {
	t.gets++
	return t.s.Object(p, d)
}

func replica(t *testing.T, remote *relay.Store, account, device string) (*Client, *localdag.Engine, *localTransport) {
	t.Helper()
	store, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	transport := &localTransport{s: remote, account: account}
	client := &Client{Store: store, Remote: transport, Account: account, Device: device}
	engine := &localdag.Engine{Store: store, Publisher: client, Actor: client.Writer()}
	return client, engine, transport
}
func source(native, text string) localdag.Capture {
	return localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: native, Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: text}}}, Artifact: []byte("original source " + text)}
}

func TestMetadataSyncDoesNotDownloadSessionsAndPullIsExplicit(t *testing.T) {
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, producer, uploads := replica(t, server, "account_a", "device_a")
	right, _, downloads := replica(t, server, "account_b", "device_b")
	snapshot, err := producer.Capture(context.Background(), source("native", "完善重试。"))
	if err != nil {
		t.Fatal(err)
	}
	if uploads.puts != 0 {
		t.Fatal("graph publication uploaded Session bytes")
	}
	if err := left.SyncContents(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	if uploads.puts < 2 {
		t.Fatal("background content transfer did not upload closure")
	}
	if err := right.Sync(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	leftState, _ := left.Store.State("project")
	rightState, _ := right.Store.State("project")
	if workgraph.Digest(leftState) != workgraph.Digest(rightState) || downloads.gets != 0 {
		t.Fatal("metadata sync did not converge independently of content")
	}
	if err := right.Pull(context.Background(), "project", snapshot, false); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Store.Object(rightState.Snapshots[snapshot.Key()].Artifact); err == nil {
		t.Fatal("ordinary pull downloaded native artifact")
	}
	before := downloads.gets
	if err := right.Pull(context.Background(), "project", snapshot, true); err != nil {
		t.Fatal(err)
	}
	manifest, err := right.Store.ArtifactManifest(rightState.Snapshots[snapshot.Key()].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if downloads.gets != before+1+len(manifest.Chunks) {
		t.Fatal("explicit native pull did not reuse compact content")
	}
	path, err := right.Store.ArtifactFile(rightState.Snapshots[snapshot.Key()].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original source 完善重试。" {
		t.Fatal("downloaded artifact did not reconstruct its exact bytes")
	}
}

func TestLostRelayReceiptRecoversWithoutDuplicateCapture(t *testing.T) {
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, engine, transport := replica(t, server, "account", "device")
	transport.loseReceipt = true
	if _, err := engine.Capture(context.Background(), source("native", "检查边界。")); err == nil {
		t.Fatal("lost receipt hidden")
	}
	if err := engine.Recover(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	page, err := server.Since("project", 0, 100)
	if err != nil || page.Head != 1 || len(page.Entries) != 1 {
		t.Fatal("recovery duplicated relay record")
	}
	state, _ := client.Store.State("project")
	if state.Revision != 1 || len(state.Snapshots) != 1 {
		t.Fatal("capture not recovered")
	}
	runs, _ := client.Store.Runs("project")
	if len(runs) != 1 || runs[0].Status != "published" {
		t.Fatal("capture audit not recovered")
	}
}

func TestConflictCatchesUpAndRebasesIndependentCapture(t *testing.T) {
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, left, _ := replica(t, server, "a", "left")
	rightClient, right, _ := replica(t, server, "b", "right")
	if _, err := left.Capture(context.Background(), source("left", "方案完成。")); err != nil {
		t.Fatal(err)
	}
	// A concurrent independent capture can be rebased after verified catch-up.
	_, err = right.Capture(context.Background(), source("right", "开始实现。"))
	state, _ := rightClient.Store.State("project")
	if state.Revision != 2 || len(state.Snapshots) != 2 {
		t.Fatal("conflict did not refresh and rebase replica")
	}
	if err != nil {
		t.Fatal(err)
	}
	proposals, _ := rightClient.Store.Proposals("project")
	conflicts := 0
	published := 0
	for _, proposal := range proposals {
		if proposal.Status == "conflict" {
			conflicts++
		}
		if proposal.Status == "published" {
			published++
		}
	}
	if conflicts != 1 || published != 1 {
		t.Fatal("rebase lost rejected or accepted proposal evidence")
	}
}

func TestInvalidPeerCommitStopsCursorAndArchivesReceipt(t *testing.T) {
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, _, _ := replica(t, server, "account", "device")
	// The opaque server cannot validate semantics. A client must retain the
	// rejection and stop at the last valid revision instead of silently skipping.
	payload := []byte(`{"schema":1,"id":"wrong"}`)
	_, err = server.Append("project", "account", contracts.RelayCommit{SchemaVersion: 1, ID: "broken", DeviceID: "device", Codec: "coconet-json-v2", Payload: payload, SHA256: graphstore.ObjectDigest(payload), Objects: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Sync(context.Background(), "project"); err == nil {
		t.Fatal("invalid commit accepted")
	}
	state, _ := client.Store.State("project")
	if state.Revision != 0 {
		t.Fatal("invalid receipt advanced cursor")
	}
	logs, err := client.Store.Transfers("project", 10)
	if err != nil || len(logs) != 1 || logs[0].Status != "failed" || logs[0].Receipt == nil || logs[0].Receipt.Commit.ID != "broken" {
		t.Fatalf("missing rejected receipt: %+v %v", logs, err)
	}
}
