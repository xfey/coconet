package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
)

// This fixture exercises the actual S3 SDK and HTTP client without pretending
// to certify a cloud provider's signature or lifecycle implementation.
func isolatedS3Fixture(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	var copies atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("Relay credential sent to object storage")
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/replica-fixture/" || r.URL.Path == "/replica-fixture" {
			w.WriteHeader(200)
			return
		}
		key := r.URL.Path
		switch r.Method {
		case "PUT":
			if source := r.Header.Get("X-Amz-Copy-Source"); source != "" {
				source, _ = url.PathUnescape(source)
				if !strings.HasPrefix(source, "/") {
					source = "/" + source
				}
				data, ok := objects[source]
				if !ok {
					w.WriteHeader(404)
					return
				}
				objects[key] = bytes.Clone(data)
				copies.Add(1)
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, `<CopyObjectResult><LastModified>2026-10-10T00:00:00Z</LastModified><ETag>"fixture"</ETag></CopyObjectResult>`)
				return
			}
			if !strings.Contains(r.URL.Query().Get("X-Amz-SignedHeaders"), "content-length") {
				t.Error("upload capability did not bind object size")
			}
			data, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
			if err != nil || int64(len(data)) != r.ContentLength {
				w.WriteHeader(400)
				return
			}
			objects[key] = data
			w.Header().Set("ETag", `"fixture"`)
		case "HEAD", "GET":
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			hash := md5.Sum(data)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("ETag", `"`+hex.EncodeToString(hash[:])+`"`)
			w.Header().Set("Last-Modified", "Sat, 10 Oct 2026 00:00:00 GMT")
			if r.Method == "GET" {
				_, _ = w.Write(data)
			}
		case "DELETE":
			delete(objects, key)
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	return server, &copies
}

func TestRelayS3DirectReplicaTransfer(t *testing.T) {
	objects, copies := isolatedS3Fixture(t)
	for name, value := range map[string]string{"ENDPOINT": objects.URL, "REGION": "us-east-1", "BUCKET": "replica-fixture", "ACCESS_KEY": "fixture-key", "SECRET_KEY": "fixture-secret"} {
		t.Setenv("COCONET_RELAY_S3_"+name, value)
	}
	_, handler, actor, project, token := dashboardFixture(t)
	var bulkRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == "PUT" || r.Method == "GET") && strings.Contains(r.URL.Path, "/relay/objects/") {
			bulkRequests.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	makeClient := func(device string) (*replication.Client, *localdag.Engine) {
		store, err := graphstore.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		client := &replication.Client{Store: store, Account: actor.AccountID, Device: device, Remote: replication.HTTP{BaseURL: server.URL, Token: token, Client: server.Client()}}
		return client, &localdag.Engine{Store: store, Publisher: client, Actor: client.Writer()}
	}
	owner, engine := makeClient("source")
	peer, _ := makeClient("reader")
	ctx := context.Background()
	source, err := engine.Capture(ctx, localdag.Capture{ProjectID: project.ProjectID, Agent: "fixture", NativeID: "native-copy", Artifact: []byte("exact native content"), Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: "先看看其他同事在做什么。"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.SyncContents(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := peer.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := peer.SyncContents(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	state, _ := peer.Store.State(project.ProjectID)
	file, err := peer.Store.ArtifactFile(state.Snapshots[source.Key()].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "exact native content" {
		t.Fatal("native transfer differs", err)
	}
	if copies.Load() < 2 || bulkRequests.Load() != 0 {
		t.Fatal("direct transfer used application bulk relay", copies.Load(), bulkRequests.Load())
	}
	// Delete the provider's content, including staging, to simulate expiry.
	// Keep the original source offline: only the receiving peer may repair it.
	storage, err := handler.authority.store.relayStore()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := storage.ObjectCatalog(project.ProjectID, "", 512)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range catalog.Objects {
		target, err := storage.DownloadTarget(ctx, project.ProjectID, object.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest("DELETE", target.URL, nil)
		response, err := objects.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	late, _ := makeClient("late")
	if err := late.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	complete := false
	for range 24 {
		err := late.Pull(ctx, project.ProjectID, source, true)
		if err == nil {
			complete = true
			break
		}
		if !errors.Is(err, replication.ErrWaitingForReplica) {
			t.Fatal("missing content not queued for repair", err)
		}
		if err := peer.SyncContents(ctx, project.ProjectID); err != nil {
			t.Fatal("holder repair failed", err)
		}
	}
	if !complete {
		t.Fatal("third device could not recover from peer")
	}
	lateState, _ := late.Store.State(project.ProjectID)
	if lateState.Revision != state.Revision {
		t.Fatal("repair modified DAG")
	}
	lateFile, err := late.Store.ArtifactFile(state.Snapshots[source.Key()].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	lateData, err := os.ReadFile(lateFile)
	if err != nil || !bytes.Equal(lateData, data) {
		t.Fatal("repaired exact source differs", err)
	}
	if err := owner.CollectUnused(ctx, project.ProjectID); err != nil {
		t.Fatal("legacy collector ran on temporary backend", err)
	}
}
