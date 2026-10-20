package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestDashboardLibrarySharesAgentStateAndRetryReceipts(t *testing.T) {
	a, h, actor, project, token := dashboardFixture(t)
	producer, _, publish := publishedGraphFixture(t, a, h, actor, project.ProjectID, token)
	server := httptest.NewTLSServer(h)
	defer server.Close()
	_, secret, err := a.createDashboardSession(token, "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: dashboardCookieName, Value: secret}
	path := "/v1/dashboard/projects/" + project.ProjectID + "/library-selection"
	before, _ := producer.Store.State(project.ProjectID)
	node := before.Nodes["docs"]
	input := dashboardLibraryRequest{SchemaVersion: 1, RequestID: "browser-save-request", NodeID: node.ID, Source: node.Source}
	storage, _ := a.store.relayStore()
	head, _ := storage.CheckpointHead(project.ProjectID)
	// Same-origin and session checks still gate this explicit browser writer.
	for _, trial := range []struct {
		cookie *http.Cookie
		origin string
		status int
	}{{nil, server.URL, 401}, {cookie, "https://elsewhere.test", 403}} {
		response, _ := dashboardRequest(t, server, "POST", path, input, trial.cookie, trial.origin)
		if response.StatusCode != trial.status {
			t.Fatal("access check", response.StatusCode)
		}
	}
	// Simultaneous copies of one intent must append exactly once.
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response, body := dashboardRequest(t, server, "POST", path, input, cookie, server.URL)
			if response.StatusCode != 200 {
				t.Errorf("save %d: %s", response.StatusCode, body)
			}
		}()
	}
	group.Wait()
	saved, _ := storage.CheckpointHead(project.ProjectID)
	if saved.Head != head.Head+1 || saved.Checkpoint.Revision != saved.Head || saved.Generation != head.Generation+1 {
		t.Fatal("not atomic or appended twice", saved)
	}
	ctx := context.Background()
	if err := producer.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	after, _ := producer.Store.State(project.ProjectID)
	if _, ok := after.Library[node.ID]; !ok || after.Nodes[node.ID].State != node.State || after.LibrarySources[node.ID].Source != node.Source {
		t.Fatal("device did not receive exact collection")
	}
	if workgraph.Digest(after.Nodes[node.ID].Text) != workgraph.Digest(node.Text) {
		t.Fatal("collection rewrote semantic content")
	}
	// The ordinary Agent tool removes the same entry and publishes a newer graph.
	engine := localdag.Engine{Store: producer.Store, Publisher: producer, Actor: producer.Writer()}
	if _, err := engine.Uncollect(ctx, project.ProjectID, localdag.LibraryRequest{RequestID: "agent-remove-request", NodeID: node.ID, Source: node.Source}); err != nil {
		t.Fatal(err)
	}
	publish()
	if _, err := storage.CompactHistory(ctx, 0, 1024); err != nil {
		t.Fatal(err)
	}
	latest, _ := storage.CheckpointHead(project.ProjectID)
	// A lost save reply retried after Agent removal must not save again.
	response, body := dashboardRequest(t, server, "POST", path, input, cookie, server.URL)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(body))
	}
	repeated, _ := storage.CheckpointHead(project.ProjectID)
	if workgraph.Digest(latest) != workgraph.Digest(repeated) {
		t.Fatal("old retry advanced graph")
	}
	if err := producer.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	final, _ := producer.Store.State(project.ProjectID)
	if _, ok := final.Library[node.ID]; ok {
		t.Fatal("old browser save overwrote Agent removal")
	}
	if final.Nodes[node.ID].State != node.State {
		t.Fatal("removal reopened fixed work")
	}
	// Other project members can collect the same fixed source.
	other, otherToken := issueTestIdentity(t, a, "Other", "other-library@test")
	if _, err := a.joinProject(other, project.ProjectID, "join-library-test"); err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := a.createDashboardSession(otherToken, "")
	if err != nil {
		t.Fatal(err)
	}
	input.RequestID = "foreign-save-request"
	response, body = dashboardRequest(t, server, "POST", path, input, &http.Cookie{Name: dashboardCookieName, Value: otherSecret}, server.URL)
	if response.StatusCode != 200 {
		t.Fatal("member collection", response.StatusCode, string(body))
	}
	archiveTestProject(t, a, project.ProjectID)
	response, _ = dashboardRequest(t, server, "POST", path, input, cookie, server.URL)
	if response.StatusCode != 409 && response.StatusCode != 403 {
		t.Fatal("archived project accepted collection", response.StatusCode)
	}
}

func TestDashboardLibraryReadsUnpublishedTailAndRejectsChangedSelection(t *testing.T) {
	a, h, actor, project, token := dashboardFixture(t)
	producer, advance, _ := publishedGraphFixture(t, a, h, actor, project.ProjectID, token)
	before, _ := producer.Store.State(project.ProjectID)
	input := dashboardLibraryRequest{SchemaVersion: 1, RequestID: "stale-save-request", NodeID: "docs", Source: before.Nodes["docs"].Source}
	advance() // Same node advanced; its checkpoint deliberately has not caught up.
	storage, _ := a.store.relayStore()
	head, _ := storage.CheckpointHead(project.ProjectID)
	server := httptest.NewTLSServer(h)
	defer server.Close()
	_, secret, _ := a.createDashboardSession(token, "")
	cookie := &http.Cookie{Name: dashboardCookieName, Value: secret}
	path := "/v1/dashboard/projects/" + project.ProjectID + "/library-selection"
	response, body := dashboardRequest(t, server, "POST", path, input, cookie, server.URL)
	var failure struct {
		Code string `json:"code"`
	}
	json.Unmarshal(body, &failure)
	if response.StatusCode != 409 || failure.Code != "library_selection_changed" {
		t.Fatal("stale source silently replaced", response.StatusCode, string(body))
	}
	unchanged, _ := storage.CheckpointHead(project.ProjectID)
	if workgraph.Digest(head) != workgraph.Digest(unchanged) {
		t.Fatal("rejected selection mutated graph")
	}
	current, _ := producer.Store.State(project.ProjectID)
	input.Source = current.Nodes["docs"].Source
	input.RequestID = "fresh-save-request"
	response, body = dashboardRequest(t, server, "POST", path, input, cookie, server.URL)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(body))
	}
	if err := producer.Sync(context.Background(), project.ProjectID); err != nil {
		t.Fatal(err)
	}
	current, _ = producer.Store.State(project.ProjectID)
	if current.LibrarySources["docs"].Source != input.Source || current.Nodes["docs"].State != "current" {
		t.Fatal("wrong source collected")
	}
}
