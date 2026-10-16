package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
)

func TestHealthRoute(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	newTestHandler(t).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("unexpected content type %q", contentType)
	}
	var health contracts.HealthReport
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.SchemaVersion != contracts.SchemaVersion || health.Component != "server" || health.Version != "test" {
		t.Fatalf("unexpected health %#v", health)
	}
}

func TestHealthRejectsMutationMethods(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	response := httptest.NewRecorder()
	newTestHandler(t).ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status %d", response.Code)
	}
}

func TestServerOperationNamesCoverContextPullForkAndArtifactTransfers(t *testing.T) {
	tests := map[string]string{
		"POST /v1/connections":                                  "project.join",
		"DELETE /v1/projects/coco_fixture/membership":           "membership.leave",
		"POST /v1/projects/coco_fixture/work-graph":             "work_graph.read",
		"POST /v1/projects/coco_fixture/sessions/pull":          "session.pull",
		"POST /v1/projects/coco_fixture/sessions/recent":        "session.recent",
		"POST /v1/projects/coco_fixture/sessions/search":        "session.search",
		"POST /v1/projects/coco_fixture/sessions/fork/prepare":  "session.fork.prepare",
		"POST /v1/projects/coco_fixture/sessions/fork/complete": "session.fork.complete",
		"PUT /v1/artifacts/objects/chunk_fixture":               "artifact.upload",
		"GET /v1/artifacts/objects/chunk_fixture":               "artifact.download",
	}
	for requestLine, expected := range tests {
		parts := strings.SplitN(requestLine, " ", 2)
		request := httptest.NewRequest(parts[0], parts[1], nil)
		if operation := serverOperation(request); operation != expected {
			t.Fatalf("%s mapped to %q, want %q", requestLine, operation, expected)
		}
	}
}

func TestHTTPConnectionCodeJoinEqualAccessAndLeave(t *testing.T) {
	handler := newTestHandler(t).(*serverHandler)
	var logs bytes.Buffer
	handler.logger = observability.NewJSONLogger(&logs, "server", "test")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	creatorToken := issueHTTPIdentity(t, server, "Creator", "creator-http@coconet.test")
	memberToken := issueHTTPIdentity(t, server, "Member", "member-http@coconet.test")
	var created contracts.ProjectAccess
	requestJSON(t, http.MethodPost, server.URL+"/v1/projects", creatorToken, contracts.CreateProjectRequest{
		SchemaVersion: 1, RequestID: "request-http-code-project", Name: "connection-project",
	}, http.StatusCreated, &created)
	projectID := created.Project.ProjectID

	requestJSON(t, http.MethodPost, server.URL+"/v1/connections", memberToken, contracts.ConnectProjectRequest{
		SchemaVersion: 1, RequestID: "request-http-unknown-project", ProjectID: "coco_1111111111111111",
	}, http.StatusNotFound, nil)

	otherServer := httptest.NewServer(newTestHandler(t))
	t.Cleanup(otherServer.Close)
	otherDeploymentToken := issueHTTPIdentity(t, otherServer, "Other Deployment", "other-deployment@coconet.test")
	requestJSON(t, http.MethodPost, otherServer.URL+"/v1/connections", otherDeploymentToken, contracts.ConnectProjectRequest{
		SchemaVersion: 1, RequestID: "request-http-wrong-deployment",
		ProjectID: projectID,
	}, http.StatusNotFound, nil)

	var joined contracts.ProjectAccess
	requestJSON(t, http.MethodPost, server.URL+"/v1/connections", memberToken, contracts.ConnectProjectRequest{
		SchemaVersion: 1, RequestID: "request-http-code-connect",
		ProjectID: projectID,
	}, http.StatusOK, &joined)
	if joined.Membership == nil || joined.Membership.Role != contracts.RoleMember || joined.Capabilities["manage"] || !joined.Capabilities["share"] {
		t.Fatalf("joined Account did not receive equal access: %#v", joined)
	}

	requestJSON(t, http.MethodDelete, server.URL+"/v1/projects/"+projectID+"/membership", memberToken, nil, http.StatusNoContent, nil)
	requestJSON(t, http.MethodDelete, server.URL+"/v1/projects/"+projectID+"/membership", memberToken, nil, http.StatusNoContent, nil)
	var denied contracts.ProjectAccess
	requestJSON(t, http.MethodGet, server.URL+"/v1/projects/"+projectID+"/access", memberToken, nil, http.StatusOK, &denied)
	if denied.Status != contracts.StatusForbidden {
		t.Fatalf("left Account retained access: %#v", denied)
	}
	requestJSON(t, http.MethodPost, server.URL+"/v1/projects/"+projectID+"/join-requests", memberToken, contracts.ProjectMutationRequest{
		SchemaVersion: 1, RequestID: "request-http-old-join",
	}, http.StatusConflict, nil)

	encodedLogs := logs.String()
	for _, sensitive := range []string{
		creatorToken, memberToken,
	} {
		if strings.Contains(encodedLogs, sensitive) {
			t.Fatalf("structured logs leaked connection material %q: %s", sensitive, encodedLogs)
		}
	}
}

func TestInternalServerErrorsAreRetryable(t *testing.T) {
	response := httptest.NewRecorder()
	handler := &serverHandler{}
	handler.writeError(response, "request-internal-error", errors.New("database temporarily unavailable"))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d", response.Code)
	}
	var envelope contracts.ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Status != contracts.StatusInternalError || !envelope.Retryable {
		t.Fatalf("unexpected internal error envelope %#v", envelope)
	}
}

func TestDecodeJSONLimitRejectsOversizedChunkedBody(t *testing.T) {
	type fixture struct {
		OK bool `json:"ok"`
	}
	body := `{"ok":true}` + strings.Repeat(" ", maxRequestBody)
	request := httptest.NewRequest(http.MethodPost, "/fixture", strings.NewReader(body))
	request.ContentLength = -1
	var decoded fixture
	if err := decodeJSONLimit(request, &decoded, maxRequestBody); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized chunked body was not rejected by the size boundary: %v", err)
	}
}

func TestStructuredRequestLoggingUsesServerCorrelationAndSafeFields(t *testing.T) {
	handler := newTestHandler(t).(*serverHandler)
	var logs bytes.Buffer
	handler.logger = observability.NewJSONLogger(&logs, "server", "test")
	request := httptest.NewRequest(http.MethodGet, "/v1/projects/coco_sensitive01/access?query=secret", nil)
	request.Header.Set("Authorization", "Bearer bearer-secret-value")
	request.Header.Set("X-Request-ID", "client-request-sensitive")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected response %d", response.Code)
	}
	correlationID := response.Header().Get("X-Coconet-Correlation-ID")
	if correlationID == "" || correlationID == "client-request-sensitive" {
		t.Fatalf("unexpected correlation ID %q", correlationID)
	}
	var event observability.Event
	if err := json.NewDecoder(&logs).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Operation != "project.access" || event.HTTPStatus != http.StatusUnauthorized || event.Status != contracts.StatusForbidden || event.FailureCategory != contracts.FailureAuthentication || event.ErrorCode != "invalid_token" || event.CorrelationID != correlationID {
		t.Fatalf("unexpected event %#v", event)
	}
	encoded := logs.String()
	for _, forbidden := range []string{"coco_sensitive01", "secret", "bearer-secret-value", "client-request-sensitive", "X-Request-ID", "Authorization"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("structured event leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestHandlerPanicIsRecoveredIntoStructuredFailure(t *testing.T) {
	var logs bytes.Buffer
	handler := &serverHandler{version: "test", authority: &authority{}, logger: observability.NewJSONLogger(&logs, "server", "test")}
	request := httptest.NewRequest(http.MethodGet, "/v1/account", nil)
	request.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected response %d", response.Code)
	}
	var event observability.Event
	if err := json.NewDecoder(&logs).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Operation != "account.get" || event.Status != contracts.StatusInternalError || event.FailureCategory != contracts.FailureServerInternal || event.ErrorCode != "handler_panic" || !event.Retryable {
		t.Fatalf("unexpected panic event %#v", event)
	}
}

func TestHTTPAccountAndProtectedProjectAPI(t *testing.T) {
	server := httptest.NewServer(newTestHandler(t))
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/account", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated account route returned %d", response.StatusCode)
	}

	token := issueHTTPIdentity(t, server, "Fixture Account", "fixture@coconet.test")
	create := contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "request-http-create", Name: "http-project"}
	var first contracts.ProjectAccess
	requestJSON(t, http.MethodPost, server.URL+"/v1/projects", token, create, http.StatusCreated, &first)
	if first.Project == nil || first.Membership == nil || first.Project.CreatedBy != first.Membership.Account.AccountID || first.Membership.Role != contracts.RoleMember || first.Capabilities["manage"] || !first.Capabilities["share"] {
		t.Fatalf("unexpected project response %#v", first)
	}
	var replay contracts.ProjectAccess
	requestJSON(t, http.MethodPost, server.URL+"/v1/projects", token, create, http.StatusCreated, &replay)
	if replay.Project == nil || replay.Project.ProjectID != first.Project.ProjectID {
		t.Fatalf("HTTP idempotency failed: %#v", replay)
	}

	var account contracts.Account
	requestJSON(t, http.MethodGet, server.URL+"/v1/account", token, nil, http.StatusOK, &account)
	if account.DisplayName != "Fixture Account" || account.Email != "fixture@coconet.test" {
		t.Fatalf("unexpected account %#v", account)
	}
}

func TestRetiredSessionAPIsGiveExplicitUpgradeGuidance(t *testing.T) {
	handler := newTestHandler(t).(*serverHandler)
	actor, token := issueTestIdentity(t, handler.authority, "Context HTTP", "context-http@coconet.test")
	project := createContextProject(t, handler.authority, actor, "context-http", "request-context-http-project")
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, route := range []string{"sessions/search", "sessions/recent", "sessions/sess_test/read", "sessions/sess_test/versions/1/index"} {
		var response contracts.ErrorEnvelope
		requestJSON(t, http.MethodPost, server.URL+"/v1/projects/"+project.ProjectID+"/"+route, token, map[string]any{"schema_version": 1, "project_id": project.ProjectID}, http.StatusUnprocessableEntity, &response)
		if response.Status != contracts.StatusUnsupported || response.Code != "local_runtime_required" {
			t.Fatal("retired API silently succeeded", response)
		}
	}
}

func issueHTTPIdentity(t *testing.T, server *httptest.Server, displayName, email string) string {
	t.Helper()
	_, key := issueTestIdentity(t, server.Config.Handler.(*serverHandler).authority, displayName, email)
	return key
}

func requestJSON(t *testing.T, method, target, token string, input any, expectedStatus int, output any) {
	t.Helper()
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, target, &body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		var envelope contracts.ErrorEnvelope
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		t.Fatalf("%s %s returned %d: %#v", method, target, response.StatusCode, envelope)
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	storage, err := openStore(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.close() })
	deployment, err := generateDeploymentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return newHandler("test", deployment, newAuthority(storage))
}
