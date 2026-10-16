package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
)

func TestIdleReplicaUsesOneProjectRequestAndWakesForRepair(t *testing.T) {
	objects, _ := isolatedS3Fixture(t)
	for name, value := range map[string]string{"ENDPOINT": objects.URL, "REGION": "us-east-1", "BUCKET": "replica-fixture", "ACCESS_KEY": "fixture-key", "SECRET_KEY": "fixture-secret"} {
		t.Setenv("COCONET_RELAY_S3_"+name, value)
	}
	_, h, actor, project, token := dashboardFixture(t)
	var mu sync.Mutex
	paths := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	store, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := &replication.Client{Store: store, Account: actor.AccountID, Device: "fixture", Remote: replication.HTTP{BaseURL: server.URL, Token: token, Client: server.Client()}}
	e := &localdag.Engine{Store: store, Publisher: c, Actor: c.Writer()}
	ctx := context.Background()
	_, err = e.Capture(ctx, localdag.Capture{ProjectID: project.ProjectID, Agent: "fixture", NativeID: "session", Artifact: []byte("fixed content")})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SyncContents(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := c.PublishCheckpoint(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	clear(paths)
	mu.Unlock()
	for range 5 {
		if err := c.Sync(ctx, project.ProjectID); err != nil {
			t.Fatal(err)
		}
		if err := c.SyncContentsBackground(ctx, project.ProjectID); err != nil {
			t.Fatal(err)
		}
		if err := c.CollectUnused(ctx, project.ProjectID); err != nil {
			t.Fatal(err)
		}
		if err := c.PublishCheckpoint(ctx, project.ProjectID); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if len(paths) != 1 || paths["/v1/projects/"+project.ProjectID+"/relay/commits"] != 5 {
		t.Fatal("idle tasks made redundant requests", paths)
	}
	mu.Unlock()
	// New local content must still transfer while a fresh hint says no repairs.
	source, err := e.Capture(ctx, localdag.Capture{ProjectID: project.ProjectID, Agent: "fixture", NativeID: "new-session", Artifact: []byte("new content after idle")})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SyncContentsBackground(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	state, err := store.State(project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := store.Replica(project.ProjectID, state.Snapshots[source.Key()])
	if err != nil || !progress.Uploaded || !progress.TextReady || !progress.NativeReady {
		t.Fatal("idle hint suppressed new local content", progress, err)
	}
	storage, err := h.authority.store.relayStore()
	if err != nil {
		t.Fatal(err)
	}
	// A new missing hash need not change DAG revision to wake the repair worker.
	if err := storage.RequestRepair(project.ProjectID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := c.SyncContentsBackground(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths["/v1/projects/"+project.ProjectID+"/relay/repairs"] != 1 {
		t.Fatal("new repair was hidden by unchanged graph", paths)
	}
	t.Log("five idle cycles: five project requests; new repair discovered without graph revision change")
}
