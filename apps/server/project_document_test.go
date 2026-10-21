package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func TestProjectNotesOwnershipConcurrentEditsAndRetry(t *testing.T) {
	a, _, author, project, _ := dashboardFixture(t)
	member, _ := issueTestIdentity(t, a, "Teammate", "notes-member@coconet.test")
	if _, err := a.joinProject(member, project.ProjectID, "join-notes-member"); err != nil {
		t.Fatal(err)
	}
	id := project.ProjectID
	input := contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "publish-first-note", Text: "Search first"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doc, err := a.saveProjectNote(author, id, "note-first", input, false)
			if err != nil || doc.Version != 1 || len(doc.Notes) != 1 {
				t.Errorf("retry: %+v %v", doc, err)
			}
		}()
	}
	wg.Wait()
	first, _ := a.projectNotes(author, id)
	second := contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "publish-second-note", Text: "Keep handoff stable"}
	if _, err := a.saveProjectNote(member, id, "note-second", second, false); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{false, true} {
		_, err := a.saveProjectNote(member, id, "note-first", contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "deny-foreign-note", BaseRevision: 1, Text: "overwrite"}, remove)
		assertAuthorityCode(t, err, "note_not_owned")
	}
	// Two different notes can be updated independently, without a shared base version.
	for i, actor := range []contracts.Account{author, member} {
		wg.Add(1)
		go func(i int, actor contracts.Account) {
			defer wg.Done()
			key := []string{"note-first", "note-second"}[i]
			_, err := a.saveProjectNote(actor, id, key, contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "edit-own-note", BaseRevision: 1, Text: key + " updated"}, false)
			if err != nil {
				t.Error(err)
			}
		}(i, actor)
	}
	wg.Wait()
	view, _ := a.projectNotes(author, id)
	if view.Version != 4 || view.Notes[0].ID != "note-first" || view.Notes[0].CreatedAt != first.Notes[0].CreatedAt || view.Notes[1].ID != "note-second" {
		t.Fatal("order or publication time changed", view)
	}
	stale := contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "stale-edit-request", BaseRevision: 1, Text: "stale"}
	_, err := a.saveProjectNote(author, id, "note-first", stale, false)
	assertAuthorityCode(t, err, "note_conflict")
	unchanged := contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "unchanged-request", BaseRevision: 2, Text: view.Notes[0].Text}
	if doc, err := a.saveProjectNote(author, id, "note-first", unchanged, false); err != nil || doc.Version != 4 {
		t.Fatal(doc, err)
	}
	deleteRequest := contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "delete-first-note", BaseRevision: 2}
	deleted, err := a.saveProjectNote(author, id, "note-first", deleteRequest, true)
	if err != nil || deleted.Version != 5 || len(deleted.Notes) != 1 {
		t.Fatal(deleted, err)
	}
	// A late create response retry returns today's view and cannot resurrect it.
	replay, err := a.saveProjectNote(author, id, "note-first", input, false)
	if err != nil || replay.Version != 5 || len(replay.Notes) != 1 {
		t.Fatal(replay, err)
	}
	stale.BaseRevision = 2
	_, err = a.saveProjectNote(author, id, "note-first", stale, false)
	assertAuthorityCode(t, err, "note_missing")
	input.Text = "changed retry"
	_, err = a.saveProjectNote(author, id, "note-first", input, false)
	assertAuthorityCode(t, err, "note_request_reused")
	text := notesDocument(deleted).Markdown
	if strings.Contains(text, "Search first") || !strings.Contains(text, "[1] Teammate · "+deleted.Notes[0].CreatedAt+"\nnote-second updated") {
		t.Fatal(text)
	}
	if _, err := a.leaveProject(member, id, contracts.ProjectMutationRequest{SchemaVersion: 1, RequestID: "leave-notes-project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.projectNotes(member, id); err == nil {
		t.Fatal("left member read notes")
	}
	if _, err := a.saveProjectNote(member, id, "note-second", second, false); err == nil {
		t.Fatal("left member replayed mutation")
	}
}

func TestProjectNotesBrowserOnlyWritesAndValidation(t *testing.T) {
	a, handler, actor, project, token := dashboardFixture(t)
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	_, secret, err := a.createDashboardSession(token, "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: dashboardCookieName, Value: secret}
	path := "/v1/dashboard/projects/" + project.ProjectID + "/document"
	input := contracts.SaveProjectNoteRequest{SchemaVersion: 1, RequestID: "browser-note-save", Text: "Shared note"}
	for _, trial := range []struct {
		cookie *http.Cookie
		origin string
		status int
	}{{nil, server.URL, 401}, {cookie, "https://elsewhere.test", 403}, {cookie, server.URL, 200}} {
		r, body := dashboardRequest(t, server, "PUT", path+"/notes/browser-note", input, trial.cookie, trial.origin)
		if r.StatusCode != trial.status {
			t.Fatal(r.StatusCode, string(body))
		}
	}
	// Retired full-document and history APIs cannot bypass per-note ownership.
	for _, trial := range []struct{ method, suffix string }{{"PUT", ""}, {"GET", "/history"}, {"GET", "/versions/1"}} {
		r, _ := dashboardRequest(t, server, trial.method, path+trial.suffix, input, cookie, server.URL)
		if r.StatusCode != 405 {
			t.Fatal("retired endpoint exposed", trial, r.StatusCode)
		}
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		r, _ := http.NewRequest(method, server.URL+"/v1/projects/"+project.ProjectID+"/relay/document", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if method == "GET" {
			var doc contracts.ProjectDocument
			err = json.NewDecoder(response.Body).Decode(&doc)
			if err != nil || !strings.Contains(doc.Markdown, input.Text) || !strings.Contains(doc.Markdown, actor.DisplayName) {
				t.Fatal(doc, err)
			}
		} else if response.StatusCode != 405 {
			t.Fatal("CLI write exposed", response.StatusCode)
		}
		response.Body.Close()
	}
	for _, text := range []string{" ", strings.Repeat("a", contracts.MaxProjectDocumentBytes+1), string([]byte{0xff})} {
		input.Text = text
		_, err = a.saveProjectNote(actor, project.ProjectID, "invalid-note", input, false)
		assertAuthorityCode(t, err, "invalid_note_content")
	}
	input.RequestID = "oversized-note-request"
	input.Text = strings.Repeat("a", contracts.MaxProjectDocumentBytes-10)
	_, err = a.saveProjectNote(actor, project.ProjectID, "huge-note", input, false)
	assertAuthorityCode(t, err, "notes_too_large")
	view, _ := a.projectNotes(actor, project.ProjectID)
	if view.Version != 1 || len(view.Notes) != 1 {
		t.Fatal("invalid write changed state", view)
	}
}

func TestProjectNotesDiscardLegacyTextAndAdvanceCacheVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes-schema26.db")
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{"project_document_heads": map[string]int64{"project": 7}, "project_documents": map[string]any{"project:7": map[string]any{"markdown": "discard old text"}}, "project_document_receipts": map[string]any{"old-request": map[string]any{"version": 7}}}
	data, _ := json.Marshal(legacy)
	if err := db.Update(func(tx *bolt.Tx) error {
		meta, e := tx.CreateBucketIfNotExists(metaBucket)
		if e != nil {
			return e
		}
		if e = meta.Put(schemaKey, []byte{26}); e != nil {
			return e
		}
		bucket, e := tx.CreateBucketIfNotExists(stateBucket)
		if e != nil {
			return e
		}
		return bucket.Put(stateKey, data)
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.view(func(state databaseState) error {
		view := notesAt(&state, "project")
		if view.Version != 8 || len(view.Notes) != 0 || notesDocument(view).Markdown != "" {
			t.Fatal(view)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		data, e := exportRecordJSON(tx)
		if e != nil {
			return e
		}
		if strings.Contains(string(data), "discard old text") || strings.Contains(string(data), "project_documents") || strings.Contains(string(data), "old-request") {
			t.Fatal("legacy content retained")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
