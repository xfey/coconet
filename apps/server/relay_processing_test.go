package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/replication"
)

func TestProcessingHTTPRequiresActiveMembershipAndPreservesFixedInput(t *testing.T) {
	a, h, actor, project, token := dashboardFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	remote := replication.HTTP{BaseURL: server.URL, Token: token, Client: server.Client()}
	payload := []byte(`{"opaque":"fixed inference context"}`)
	input := contracts.ProcessingRegister{SchemaVersion: 1, DeviceID: "a", Spec: contracts.ProcessingSpec{Key: "stage/source/1", Kind: "stage", Owner: contracts.RelayWriter(actor.AccountID, "a"), SessionID: "source", Version: 1, SHA256: contentstore.ObjectDigest(payload), Payload: payload}}
	body, _ := json.Marshal(input)
	request := httptest.NewRequest("POST", "/v1/projects/"+project.ProjectID+"/relay/processing", bytes.NewReader(body))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("anonymous task accepted", response.Code)
	}
	task, err := remote.RegisterProcessing(context.Background(), project.ProjectID, input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := remote.ProcessingTask(context.Background(), project.ProjectID, task.ID)
	if err != nil || !bytes.Equal(got.Spec.Payload, payload) {
		t.Fatal("fixed input changed", err)
	}
	page, err := remote.ProcessingTasks(context.Background(), project.ProjectID, 0)
	if err != nil || len(page.Tasks) != 1 || len(page.Tasks[0].Spec.Payload) != 0 {
		t.Fatal("listing loaded request bodies", err)
	}
	if err := a.store.update(func(state *databaseState) error {
		state.Memberships.Delete(membershipKey(project.ProjectID, actor.AccountID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("POST", "/v1/projects/"+project.ProjectID+"/relay/processing/"+task.ID, bytes.NewBufferString(`{"schema_version":1,"device_id":"a","operation":"claim"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("revoked member claimed inference", response.Code)
	}
}
