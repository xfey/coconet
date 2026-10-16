package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/replication"
)

func TestRelayHTTPRetentionOwnershipBoundsAndMembership(t *testing.T) {
	a, h, actor, project, token := dashboardFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	remote := replication.HTTP{BaseURL: server.URL, Token: token, Client: server.Client()}
	ctx := context.Background()
	body := []byte("unreferenced test content")
	digest := contentstore.ObjectDigest(body)
	if err := remote.Put(ctx, project.ProjectID, digest, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"opaque":true}`)
	sha := contentstore.ObjectDigest(payload)
	commit := contracts.RelayCommit{SchemaVersion: 1, ID: "head", DeviceID: "writer", Codec: "coconet-json-v2", Payload: payload, SHA256: sha, StateSHA256: sha, Objects: []string{}}
	if _, err := remote.Append(ctx, project.ProjectID, commit); err != nil {
		t.Fatal(err)
	}
	cp := contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "writer", Revision: 1, CommitID: "head", Codec: "coconet-state-json-v1", SHA256: sha, Payload: payload}
	if _, err := remote.PutCheckpoint(ctx, project.ProjectID, cp); err != nil {
		t.Fatal(err)
	}
	lease, err := remote.ReadLease(ctx, project.ProjectID, contracts.RelayReadLeaseRequest{SchemaVersion: 1, DeviceID: "reader", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.ReleaseReadLease(ctx, project.ProjectID, "foreign", lease.ID); err == nil {
		t.Fatal("foreign device released lease")
	}
	plan := contracts.RelayCollectionPlan{SchemaVersion: 1, ID: "plan", DeviceID: "writer", Revision: 1, CheckpointSHA256: sha, Objects: []string{digest}}
	if _, err := remote.MarkCollection(ctx, project.ProjectID, plan); !errors.Is(err, replication.ErrContentBusy) {
		t.Fatal(err)
	}
	if err := remote.ReleaseReadLease(ctx, project.ProjectID, "reader", lease.ID); err != nil {
		t.Fatal(err)
	}
	if result, err := remote.MarkCollection(ctx, project.ProjectID, plan); err != nil || result.Status != "marked" {
		t.Fatal(result, err)
	}
	if _, err := remote.Collect(ctx, project.ProjectID, "writer", "plan"); !errors.Is(err, replication.ErrCollectionGrace) {
		t.Fatal(err)
	}
	for _, route := range []string{"leases", "collections"} {
		for _, body := range []string{`{"extra":true}`, `{} {}`, strings.Repeat(" ", 1<<20) + `{}`} {
			r := httptest.NewRequest("POST", "/v1/projects/"+project.ProjectID+"/relay/"+route, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatal(route, w.Code, w.Body.String())
			}
		}
	}
	if err := a.store.update(func(state *databaseState) error {
		state.Memberships.Delete(membershipKey(project.ProjectID, actor.AccountID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"leases", "collections", "collections/plan?device=writer"} {
		input, _ := json.Marshal(plan)
		r := httptest.NewRequest("POST", "/v1/projects/"+project.ProjectID+"/relay/"+route, bytes.NewReader(input))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal("revoked membership", route, w.Code)
		}
	}
}
