package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
)

func TestCapacityHTTPRefusesNewWritesAndKeepsAuthenticatedReads(t *testing.T) {
	a, h, _, project, token := dashboardFixture(t)
	t.Setenv("COCONET_MIN_FREE_BYTES", "1152921504606846976")
	if err := a.store.configureCapacity(); err != nil {
		t.Fatal(err)
	}
	payload := []byte("opaque")
	input := contracts.RelayCommit{SchemaVersion: 1, ID: "first", DeviceID: "device", Codec: "coconet-json-v2", Payload: payload, SHA256: contentstore.ObjectDigest(payload), Objects: []string{}}
	body, _ := json.Marshal(input)
	invoke := func(method, path string, data []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	path := "/v1/projects/" + project.ProjectID + "/relay/commits"
	w := invoke("POST", path, body)
	if w.Code != http.StatusInsufficientStorage {
		t.Fatal(w.Code, w.Body.String())
	}
	var failure contracts.ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Code != "server_storage_capacity" {
		t.Fatal(failure, err)
	}
	w = invoke("GET", path+"?after=0", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page contracts.RelayPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Head != 0 || len(page.Entries) != 0 {
		t.Fatal(page, err)
	}
}
