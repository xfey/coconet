package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestRelayHTTPEnforcesMembershipAndCAS(t *testing.T) {
	a, h, actor, project, token := dashboardFixture(t)
	payload := []byte(`{"opaque":"relay must not infer this value"}`)
	input := contracts.RelayCommit{SchemaVersion: 1, ID: "commit_one", DeviceID: "device_one", Codec: "coconet-json-v2", Payload: payload, SHA256: contentstore.ObjectDigest(payload), Objects: []string{}}
	invoke := func(method, suffix string, body []byte, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/v1/projects/"+project.ProjectID+"/relay"+suffix, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(input)
	if w := invoke("POST", "/commits", body, ""); w.Code != 401 {
		t.Fatalf("missing authentication: %d", w.Code)
	}
	w := invoke("POST", "/commits", body, token)
	if w.Code != 200 {
		t.Fatalf("append: %d %s", w.Code, w.Body.String())
	}
	var receipt contracts.RelayReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Writer != contracts.RelayWriter(actor.AccountID, input.DeviceID) || receipt.Revision != 1 {
		t.Fatal("writer or revision differs")
	}
	input.ID = "commit_two"
	body, _ = json.Marshal(input)
	if w := invoke("POST", "/commits", body, token); w.Code != 409 {
		t.Fatal("CAS conflict missing")
	}
	if w := invoke("GET", "/commits?after=0", nil, token); w.Code != 200 {
		t.Fatal("cannot read relay commits")
	}
	if err := a.store.update(func(state *databaseState) error {
		state.Memberships.Delete(membershipKey(project.ProjectID, actor.AccountID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := invoke("GET", "/commits?after=0", nil, token); w.Code != http.StatusForbidden {
		t.Fatalf("revoked membership: %d", w.Code)
	}
}

func TestRelayHTTPReplicasConvergeAndPullExplicitly(t *testing.T) {
	_, h, actor, project, token := dashboardFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	transport := replication.HTTP{BaseURL: server.URL, Token: token, Client: server.Client()}
	create := func(device string) (*replication.Client, *localdag.Engine) {
		s, err := graphstore.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		client := &replication.Client{Store: s, Remote: transport, Account: actor.AccountID, Device: device}
		return client, &localdag.Engine{Store: s, Publisher: client, Actor: client.Writer()}
	}
	left, producer := create("left")
	right, peer := create("right")
	ctx := context.Background()
	capture := localdag.Capture{ProjectID: project.ProjectID, Agent: "fixture", NativeID: "session", Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: "完善失败重试。"}}}, Artifact: []byte("fixed original source")}
	source, err := producer.Capture(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	// A real stale HTTP append must catch up and rebase instead of losing data.
	capture.NativeID = "other"
	capture.Artifact = []byte("other original source")
	if _, err := peer.Capture(ctx, capture); err != nil {
		t.Fatal(err)
	}
	if err := left.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	l, _ := left.Store.State(project.ProjectID)
	r, _ := right.Store.State(project.ProjectID)
	if l.Revision != 2 || workgraph.Digest(l) != workgraph.Digest(r) {
		t.Fatal("replicas did not converge")
	}
	native := l.Snapshots[source.Key()].Artifact
	if _, err := right.Store.Object(native); err == nil {
		t.Fatal("metadata sync downloaded Session content")
	}
	if err := left.SyncContents(ctx, project.ProjectID); err == nil {
		t.Fatal("peer content unexpectedly available before publication")
	}
	if err := right.Pull(ctx, project.ProjectID, source, false); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Store.Object(native); err == nil {
		t.Fatal("compact pull downloaded native content")
	}
	if err := right.Pull(ctx, project.ProjectID, source, true); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Store.Object(native); err != nil {
		t.Fatal(err)
	}
	if err := left.PublishCheckpoint(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	fresh, _ := create("fresh")
	if err := fresh.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	restored, _ := fresh.Store.State(project.ProjectID)
	history, _ := fresh.Store.History(project.ProjectID)
	if workgraph.Digest(restored) != workgraph.Digest(l) || len(history) != 0 {
		t.Fatal("HTTP checkpoint bootstrap did not restore fixed graph")
	}
	// Malformed input is a caller error, not a retryable storage outage.
	req := httptest.NewRequest("POST", "/v1/projects/"+project.ProjectID+"/relay/commits", strings.NewReader(`{"bad":1}`))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("invalid JSON: %d %s", w.Code, w.Body.String())
	}
}
