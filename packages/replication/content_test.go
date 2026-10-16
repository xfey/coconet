package replication

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/relay"
)

type unavailableContent struct {
	*localTransport
	unavailable bool
}

func (t *unavailableContent) Put(ctx context.Context, project, digest string, size int64, body io.Reader) error {
	if t.unavailable {
		return errors.New("object transport unavailable")
	}
	return t.localTransport.Put(ctx, project, digest, size, body)
}

func TestContentFailureDoesNotBlockGraphAndResumesAfterReopen(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	transport := &unavailableContent{localTransport: &localTransport{s: server, account: "owner"}, unavailable: true}
	client := &Client{Store: store, Remote: transport, Account: "owner", Device: "source"}
	engine := &localdag.Engine{Store: store, Actor: client.Writer(), Publisher: client}
	fixed, err := engine.Capture(ctx, source("native", "先把这个项目的失败反馈理清楚。"))
	if err != nil {
		t.Fatal("content outage blocked capture", err)
	}
	peer, _, _ := replica(t, server, "peer", "reader")
	if err := peer.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := client.SyncContents(ctx, "project"); err == nil {
		t.Fatal("transfer outage hidden")
	}
	state, _ := store.State("project")
	snapshot := state.Snapshots[fixed.Key()]
	progress, err := store.Replica("project", snapshot)
	if err != nil || progress.Uploaded || progress.Error == "" {
		t.Fatal("incomplete transfer reported ready", progress, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	client.Store = store
	restored, err := store.Replica("project", snapshot)
	if err != nil || restored.Attempts != progress.Attempts || restored.Error != progress.Error {
		t.Fatal("transfer progress lost on reopen", err)
	}
	restored.NextAt = "" // Explicit retry, not a wall-clock sleep in this fixture.
	if err := store.SaveReplica("project", restored); err != nil {
		t.Fatal(err)
	}
	transport.unavailable = false
	if err := client.SyncContents(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := peer.SyncContents(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	coverage, err := peer.Store.ReplicaCoverage("project", peer.Writer())
	if err != nil || coverage.Expected != 1 || coverage.TextReady != 1 || coverage.NativeReady != 1 {
		t.Fatal("replica did not complete", coverage, err)
	}
	page, err := server.Since("project", 0, 100)
	if err != nil || page.Head != 1 {
		t.Fatal("content retry created another graph revision", err)
	}
	// Stop the only remote source: the received durable copy remains readable.
	peer.Remote = nil
	path, err := peer.Store.ArtifactFile(snapshot.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original source 先把这个项目的失败反馈理清楚。" {
		t.Fatal("offline exact source differs", err)
	}
}
