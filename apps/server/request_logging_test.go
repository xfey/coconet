package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
)

func TestQuietRequestLogsKeepFailuresAndIdentityChanges(t *testing.T) {
	for _, row := range []struct {
		method, path, code string
		status             int
		logged             bool
	}{
		{"GET", "/healthz", "", 200, false},
		{"GET", "/v1/projects/p/relay/commits", "", 200, false},
		{"GET", "/v1/dashboard/projects/p/published-graph", "", 304, false},
		{"POST", "/v1/projects/p/relay/transfers/download", "", 200, false},
		{"POST", "/v1/projects/p/relay/commits", "", 200, false},
		{"POST", "/v1/projects/p/relay/commits", "relay_revision_conflict", 409, false},
		{"POST", "/v1/projects/p/relay/transfers/download", "relay_object_missing", 404, false},
		{"POST", "/v1/auth/logout", "", 200, true},
		{"POST", "/v1/projects", "", 200, true},
		{"DELETE", "/v1/projects/p/members/a", "", 200, true},
		{"GET", "/v1/projects/p/relay/commits", "invalid_token", 401, true},
		{"GET", "/v1/projects/p/relay/commits", "relay_revision_conflict", 500, true},
		{"POST", "/v1/projects/p/relay/commits", "relay_request_rejected", 400, true},
		{"POST", "/v1/projects/p/relay/commits", "relay_storage_quota", 507, true},
	} {
		t.Run(row.method+row.path+row.code, func(t *testing.T) {
			var logs bytes.Buffer
			h := &serverHandler{logger: observability.NewJSONLogger(&logs, "server", "test")}
			r := httptest.NewRequest(row.method, row.path, nil)
			w := &observedResponseWriter{statusCode: row.status, productStatus: contracts.StatusOK, errorCode: row.code}
			h.recordRequest(context.Background(), r, w, "corr_test1234", time.Now())
			if (logs.Len() > 0) != row.logged {
				t.Fatal("unexpected default logging", logs.String())
			}
			logs.Reset()
			h.verboseRequests = true
			h.recordRequest(context.Background(), r, w, "corr_test1234", time.Now())
			if logs.Len() == 0 {
				t.Fatal("verbose diagnostic log omitted request")
			}
		})
	}
	// Ordinary requests still return correlation IDs when no log is written.
	h := newTestHandler(t).(*serverHandler)
	var logs bytes.Buffer
	h.logger = observability.NewJSONLogger(&logs, "server", "test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if logs.Len() != 0 || w.Header().Get("X-Coconet-Correlation-ID") == "" {
		t.Fatal("quiet health changed response tracing")
	}
}
