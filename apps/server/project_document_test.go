package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestProjectDocumentConcurrentEditsAndRetryHistory(t *testing.T) {
	a, _, author, project, _ := dashboardFixture(t)
	member, _ := issueTestIdentity(t, a, "Teammate", "doc-member@coconet.test")
	if _, err := a.joinProject(member, project.ProjectID, "join-document-member"); err != nil {
		t.Fatal(err)
	}
	id := project.ProjectID
	initial, err := a.projectDocument(author, id, -1)
	if err != nil || initial.Version != 0 || initial.Markdown != "" {
		t.Fatal(initial, err)
	}
	input := contracts.SaveProjectDocumentRequest{SchemaVersion: 1, RequestID: "save-first-document", Markdown: "# Team plan\n\nShared priorities"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doc, err := a.saveProjectDocument(author, id, input)
			if err != nil || doc.Version != 1 {
				t.Errorf("concurrent retry: %+v %v", doc, err)
			}
		}()
	}
	wg.Wait()
	second := contracts.SaveProjectDocumentRequest{SchemaVersion: 1, RequestID: "save-second-document", BaseVersion: 1, Markdown: "# New priorities"}
	saved, err := a.saveProjectDocument(member, id, second)
	if err != nil || saved.Version != 2 || saved.Author.AccountID != member.AccountID {
		t.Fatal(saved, err)
	}
	stale := second
	stale.RequestID = "conflicting-document"
	stale.Markdown = "Stale draft"
	_, err = a.saveProjectDocument(author, id, stale)
	assertAuthorityCode(t, err, "document_conflict")
	replay, err := a.saveProjectDocument(author, id, input)
	if err != nil || replay.Version != 1 || replay.Markdown != input.Markdown {
		t.Fatal("lost response replay", replay, err)
	}
	input.Markdown = "different request payload"
	_, err = a.saveProjectDocument(author, id, input)
	assertAuthorityCode(t, err, "document_request_reused")
	unchanged := second
	unchanged.RequestID = "unchanged-document"
	unchanged.BaseVersion = 2
	if doc, err := a.saveProjectDocument(author, id, unchanged); err != nil || doc.Version != 2 {
		t.Fatal("no-op save", doc, err)
	}
	// Restoring an old revision is an ordinary new save with the current base.
	restore := contracts.SaveProjectDocumentRequest{SchemaVersion: 1, RequestID: "restore-document-v1", BaseVersion: 2, Markdown: replay.Markdown}
	if doc, err := a.saveProjectDocument(member, id, restore); err != nil || doc.Version != 3 {
		t.Fatal("restore", doc, err)
	}
	history, err := a.projectDocumentHistory(author, id, 0)
	if err != nil || len(history.Versions) != 3 || history.Versions[0].Version != 3 || history.Versions[2].Version != 1 {
		t.Fatal(history, err)
	}
	page, err := a.projectDocumentHistory(member, id, 3)
	if err != nil || len(page.Versions) != 2 || page.Versions[0].Version != 2 {
		t.Fatal(page, err)
	}
	old, err := a.projectDocument(member, id, 2)
	if err != nil || old.Markdown != second.Markdown {
		t.Fatal(old, err)
	}
	// Clearing content is also a durable revision, not removal of the history.
	clear := contracts.SaveProjectDocumentRequest{SchemaVersion: 1, RequestID: "clear-document-text", BaseVersion: 3}
	if doc, err := a.saveProjectDocument(member, id, clear); err != nil || doc.Version != 4 || doc.Markdown != "" {
		t.Fatal(doc, err)
	}
	if _, err := a.leaveProject(member, id, contracts.ProjectMutationRequest{SchemaVersion: 1, RequestID: "leave-doc-project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.projectDocument(member, id, -1); err == nil {
		t.Fatal("left member could read")
	}
	if _, err := a.saveProjectDocument(member, id, clear); err == nil {
		t.Fatal("left member could replay save")
	}
}

func TestProjectDocumentBrowserOnlyWritesAndValidation(t *testing.T) {
	a, handler, actor, project, token := dashboardFixture(t)
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	_, secret, err := a.createDashboardSession(token, "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: dashboardCookieName, Value: secret}
	path := "/v1/dashboard/projects/" + project.ProjectID + "/document"
	input := contracts.SaveProjectDocumentRequest{SchemaVersion: 1, RequestID: "browser-document-save", Markdown: "Shared note"}
	for _, trial := range []struct {
		cookie *http.Cookie
		origin string
		status int
	}{{nil, server.URL, 401}, {cookie, "https://elsewhere.test", 403}, {cookie, server.URL, 200}} {
		r, body := dashboardRequest(t, server, "PUT", path, input, trial.cookie, trial.origin)
		if r.StatusCode != trial.status {
			t.Fatal(r.StatusCode, string(body))
		}
	}
	for _, suffix := range []string{"", "/history", "/versions/1"} {
		r, body := dashboardRequest(t, server, "GET", path+suffix, nil, cookie, server.URL)
		if r.StatusCode != 200 {
			t.Fatal(suffix, r.StatusCode, string(body))
		}
	}
	for _, method := range []string{"GET", "PUT"} {
		r, _ := http.NewRequest(method, server.URL+"/v1/projects/"+project.ProjectID+"/relay/document", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if method == "GET" {
			var doc contracts.ProjectDocument
			err = json.NewDecoder(response.Body).Decode(&doc)
			if err != nil || doc.Markdown != input.Markdown {
				t.Fatal(doc, err)
			}
		} else if response.StatusCode != 405 {
			t.Fatal("CLI writer exposed", response.StatusCode)
		}
		response.Body.Close()
	}
	input.BaseVersion = 1
	input.RequestID = "oversized-document-save"
	input.Markdown = strings.Repeat("a", contracts.MaxProjectDocumentBytes+1)
	_, err = a.saveProjectDocument(actor, project.ProjectID, input)
	assertAuthorityCode(t, err, "invalid_document_content")
	input.Markdown = "draft"
	input.BaseVersion = -1
	_, err = a.saveProjectDocument(actor, project.ProjectID, input)
	assertAuthorityCode(t, err, "invalid_document_version")
	outsider, _ := issueTestIdentity(t, a, "Outsider", "document-outsider@coconet.test")
	if _, err := a.projectDocumentHistory(outsider, project.ProjectID, 0); err == nil {
		t.Fatal("outsider read history")
	}
}
