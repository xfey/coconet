package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelayRejectsRetiredSemanticRoutesWithoutProcessingBody(t *testing.T) {
	h := newTestHandler(t).(*serverHandler)
	_, token := issueTestIdentity(t, h.authority, "Relay", "relay@coconet.test")
	for _, path := range []string{"work/search", "work-graph", "library/entries", "projection/retry", "history-batches", "sessions/prepare", "sessions/commit", "sessions/pull", "sessions/fork/prepare"} {
		r := httptest.NewRequest(http.MethodPost, "/v1/projects/coco_fixture/"+path, strings.NewReader("not-json"))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 422 || !strings.Contains(w.Body.String(), "local_runtime_required") {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}
