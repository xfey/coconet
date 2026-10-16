package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
	"github.com/xfey/coconet/packages/projectlink"
	"github.com/xfey/coconet/packages/storageguard"
)

const (
	maxRequestBody      = 1 << 20
	maxIndexRequestBody = 16 << 20
)

type serverHandler struct {
	auth               *loginProvider
	version            string
	deployment         *deploymentIdentity
	advertisedEndpoint string
	webOrigin          string
	authority          *authority
	logger             *observability.JSONLogger
	verboseRequests    bool
	limiter            *requestLimiter
	addressResolver    clientAddressResolver
}

func newHandler(serverVersion string, deployment *deploymentIdentity, authority *authority) http.Handler {
	result := &serverHandler{auth: &loginProvider{Kind: "key", Label: "Account Key"}, version: serverVersion, deployment: deployment, authority: authority, advertisedEndpoint: projectlink.HostedEndpoint, limiter: newRequestLimiter()}

	return result
}

func newHandlerWithLogger(serverVersion string, deployment *deploymentIdentity, authority *authority, advertisedEndpoint string, addressResolver clientAddressResolver, logger *observability.JSONLogger) http.Handler {
	result := newHandler(serverVersion, deployment, authority).(*serverHandler)
	result.advertisedEndpoint = advertisedEndpoint
	result.addressResolver = addressResolver
	result.logger = logger
	result.verboseRequests = os.Getenv("COCONET_LOG_REQUESTS") == "all"
	return result
}

func (h *serverHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	observed := &observedResponseWriter{ResponseWriter: response, productStatus: contracts.StatusOK}
	response = observed
	correlationID, _ := randomID("corr")
	if correlationID != "" {
		response.Header().Set("X-Coconet-Correlation-ID", correlationID)
	}
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID, _ = randomID("req")
	}
	response.Header().Set("X-Request-ID", requestID)
	started := time.Now()
	defer func() {
		if recover() != nil {
			observed.productStatus = contracts.StatusInternalError
			observed.failureCategory = contracts.FailureServerInternal
			observed.errorCode = "handler_panic"
			observed.retryable = true
			if observed.statusCode == 0 {
				h.writeJSON(response, http.StatusInternalServerError, contracts.ErrorEnvelope{
					SchemaVersion: contracts.SchemaVersion, Status: contracts.StatusInternalError,
					RequestID: requestID, Code: "handler_panic", Message: "the server could not complete the request", Retryable: true,
				})
			}
		}
		h.recordRequest(request.Context(), request, observed, correlationID, started)
	}()

	if request.URL.Path == "/healthz" {
		h.handleHealth(response, request, requestID)
		return
	}
	if request.URL.Path == "/v1/deployment" {
		h.handleDeployment(response, request, requestID)
		return
	}
	if h.authority == nil {
		h.writeError(response, requestID, errors.New("project authority is unavailable"))
		return
	}
	if request.URL.Path == "/" || dashboardProjectPath(request.URL.Path) || strings.HasPrefix(request.URL.Path, "/assets/") {
		h.serveDashboard(response, request, requestID)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/dashboard/") {
		h.handleDashboardAPI(response, request, requestID)
		return
	}
	if strings.HasPrefix(request.URL.Path, artifactTransferPrefix) {
		h.handleArtifactTransfer(response, request, requestID)
		return
	}

	if strings.HasPrefix(request.URL.Path, "/v1/auth/") {
		h.handleAuthAPI(response, request, requestID)
		return
	}

	actor, err := h.authenticate(request)
	if err != nil {
		h.writeError(response, requestID, err)
		return
	}
	if request.URL.Path == "/v1/account" {
		if request.Method != http.MethodGet {
			h.methodNotAllowed(response, requestID, http.MethodGet)
			return
		}
		h.writeJSON(response, http.StatusOK, actor)
		return
	}
	if request.URL.Path == "/v1/account/key" {
		h.handleAccountKey(response, request, requestID, actor)
		return
	}
	if request.URL.Path == "/v1/workspaces" {
		h.handleWorkspaceReport(response, request, requestID, actor)
		return
	}
	if request.URL.Path == "/v1/projects" {
		if request.Method == http.MethodGet {
			result, err := h.authority.dashboardProjects(actor)
			h.writeResult(response, requestID, 200, result, err)
		} else {
			h.handleCreateProject(response, request, requestID, actor)
		}
		return
	}
	if request.URL.Path == "/v1/connections" {
		h.handleConnectProject(response, request, requestID, actor)
		return
	}
	h.handleProjectRoute(response, request, requestID, actor)
}

func (h *serverHandler) handleArtifactTransfer(response http.ResponseWriter, request *http.Request, requestID string) {
	transfer, ok := h.authority.artifacts.(artifactTransferStore)
	if !ok {
		h.writeError(response, requestID, notFound("artifact_route_not_found", "artifact transfer route was not found"))
		return
	}
	if err := transfer.ServeArtifactTransfer(response, request); err != nil {
		if observed, ok := response.(*observedResponseWriter); ok && observed.statusCode != 0 {
			var domain *authorityError
			if errors.As(err, &domain) {
				observed.productStatus = domain.Status
				observed.failureCategory = observability.CategoryForResponse(domain.Status, domain.Code, observed.statusCode)
				observed.errorCode = domain.Code
				observed.retryable = domain.Retryable
			}
			return
		}
		h.writeError(response, requestID, err)
	}
}

func (h *serverHandler) handleDeployment(response http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet {
		h.methodNotAllowed(response, requestID, http.MethodGet)
		return
	}
	if h.deployment == nil {
		h.writeError(response, requestID, errors.New("Deployment identity is unavailable"))
		return
	}
	capabilities := []string{}
	if h.authority.store.db != nil || filepath.IsAbs(os.Getenv("COCONET_RELAY_HOME")) {
		capabilities = append(capabilities, contracts.DeploymentCapabilityRelay, contracts.DeploymentCapabilityLocalDAG, contracts.DeploymentCapabilityProcessingTasks)
	}
	result, err := h.deployment.info(h.version, request.URL.Query().Get("challenge"), capabilities...)
	if err != nil {
		h.writeError(response, requestID, invalid("invalid_challenge", err.Error()))
		return
	}
	h.writeJSON(response, http.StatusOK, result)
}

func (h *serverHandler) handleHealth(response http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet {
		h.methodNotAllowed(response, requestID, http.MethodGet)
		return
	}
	storageHealth := contracts.ComponentHealth{Name: "artifact_store", Status: contracts.StatusOK}
	status := contracts.StatusOK
	if h.authority == nil || h.authority.artifacts == nil {
		storageHealth.Status = contracts.StatusUnavailable
		storageHealth.Message = "artifact storage is not configured"
		status = contracts.StatusDegraded
	} else {
		storageHealth.Message = h.authority.artifacts.Driver()
	}
	result := contracts.HealthReport{
		SchemaVersion: contracts.SchemaVersion,
		Status:        status,
		Component:     "server",
		Version:       h.version,
		Components: []contracts.ComponentHealth{
			{Name: "http", Status: contracts.StatusOK},
			{Name: "authority", Status: contracts.StatusOK},
			storageHealth,
		},
	}
	h.writeJSON(response, http.StatusOK, result)
}

func (h *serverHandler) handleCreateProject(response http.ResponseWriter, request *http.Request, requestID string, actor contracts.Account) {
	if request.Method != http.MethodPost {
		h.methodNotAllowed(response, requestID, http.MethodPost)
		return
	}
	if !h.limiter.allow("project.create.ip", h.addressResolver.resolve(request), 20, time.Hour) || !h.limiter.allow("project.create.account", actor.AccountID, 10, time.Hour) {
		h.writeError(response, requestID, rateLimitError())
		return
	}
	var input contracts.CreateProjectRequest
	if err := decodeJSON(request, &input); err != nil {
		h.writeError(response, requestID, invalid("invalid_json", err.Error()))
		return
	}
	if input.SchemaVersion != contracts.SchemaVersion {
		h.writeError(response, requestID, invalid("unsupported_schema", "schema_version must be 1"))
		return
	}
	result, err := h.authority.createProject(actor, input)
	if err != nil {
		h.writeError(response, requestID, err)
		return
	}
	h.writeJSON(response, http.StatusCreated, result)
}

func (h *serverHandler) handleConnectProject(response http.ResponseWriter, request *http.Request, requestID string, actor contracts.Account) {
	if request.Method != http.MethodPost {
		h.methodNotAllowed(response, requestID, http.MethodPost)
		return
	}
	if !h.limiter.allow("project.join.ip", h.addressResolver.resolve(request), 60, time.Minute) {
		h.writeError(response, requestID, rateLimitError())
		return
	}
	var input contracts.ConnectProjectRequest
	if err := decodeJSON(request, &input); err != nil {
		h.writeError(response, requestID, invalid("invalid_json", err.Error()))
		return
	}
	if input.SchemaVersion != contracts.SchemaVersion || input.RequestID == "" {
		h.writeError(response, requestID, invalid("unsupported_schema", "schema_version must be 1 and request_id is required"))
		return
	}
	membership, err := h.authority.joinProject(actor, input.ProjectID, input.RequestID)
	if err != nil {
		h.writeError(response, requestID, err)
		return
	}
	result, err := h.authority.projectAccess(actor, membership.ProjectID)
	h.writeResult(response, requestID, http.StatusOK, result, err)
}

func (h *serverHandler) handleProjectRoute(response http.ResponseWriter, request *http.Request, requestID string, actor contracts.Account) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "projects" || parts[2] == "" {
		h.writeError(response, requestID, notFound("route_not_found", "route was not found"))
		return
	}
	projectID := parts[2]
	if len(parts) >= 5 && parts[3] == "relay" {
		h.handleRelay(response, request, requestID, actor, projectID, parts[4:])
		return
	}
	switch {
	case len(parts) == 4 && parts[3] == "access" && request.Method == http.MethodGet:
		result, err := h.authority.projectAccess(actor, projectID)
		h.writeResult(response, requestID, http.StatusOK, result, err)
	case len(parts) == 4 && parts[3] == "membership" && request.Method == http.MethodDelete:
		_, err := h.authority.leaveProject(actor, projectID, contracts.ProjectMutationRequest{SchemaVersion: contracts.SchemaVersion, RequestID: requestID})
		if err != nil {
			h.writeError(response, requestID, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	case len(parts) == 4 && parts[3] == "join-requests" && request.Method == http.MethodPost:
		h.writeError(response, requestID, deprecatedFlow())
	case len(parts) == 4 && parts[3] == "manage" && request.Method == http.MethodGet:
		result, err := h.authority.management(actor, projectID)
		h.writeResult(response, requestID, http.StatusOK, result, err)
	case len(parts) == 6 && parts[3] == "join-requests" && parts[5] == "decision" && request.Method == http.MethodPost:
		h.writeError(response, requestID, deprecatedFlow())
	case len(parts) == 6 && parts[3] == "members" && parts[5] == "remove" && request.Method == http.MethodPost:
		h.writeError(response, requestID, deprecatedFlow())
	case len(parts) == 4 && parts[3] == "leave" && request.Method == http.MethodPost:
		var input contracts.ProjectMutationRequest
		if err := decodeJSON(request, &input); err != nil {
			h.writeError(response, requestID, invalid("invalid_json", err.Error()))
			return
		}
		if input.SchemaVersion != contracts.SchemaVersion {
			h.writeError(response, requestID, invalid("unsupported_schema", "schema_version must be 1"))
			return
		}
		result, err := h.authority.leaveProject(actor, projectID, input)
		h.writeResult(response, requestID, http.StatusOK, result, err)
	case len(parts) == 4 && parts[3] == "dissolve" && request.Method == http.MethodPost:
		h.writeError(response, requestID, deprecatedFlow())
	case parts[3] == "work" || parts[3] == "work-graph" || parts[3] == "library" || parts[3] == "projection" || parts[3] == "history-batches" || parts[3] == "sessions":
		h.writeError(response, requestID, unsupported("local_runtime_required", "Update Coconet and run coconet connect in this folder. Session processing and DAG generation run automatically through your local Agent."))
	default:
		h.writeError(response, requestID, notFound("route_not_found", "route was not found"))
	}
}

func (h *serverHandler) authenticate(request *http.Request) (contracts.Account, error) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return contracts.Account{}, &authorityError{HTTPStatus: 401, Status: contracts.StatusForbidden, Code: "authentication_required", Message: "a bearer token is required"}
	}
	return h.authority.authenticate(parts[1])
}

func (h *serverHandler) writeResult(response http.ResponseWriter, requestID string, status int, result any, err error) {
	if err != nil {
		h.writeError(response, requestID, err)
		return
	}
	h.writeJSON(response, status, result)
}

func (h *serverHandler) writeError(response http.ResponseWriter, requestID string, err error) {
	status := http.StatusInternalServerError
	result := contracts.ErrorEnvelope{
		SchemaVersion: contracts.SchemaVersion,
		Status:        contracts.StatusInternalError,
		RequestID:     requestID,
		Code:          "internal_error",
		Message:       "the server could not complete the request",
		Retryable:     true,
	}
	if errors.Is(err, storageguard.ErrCapacity) || errors.Is(err, storageguard.ErrSpace) {
		status = http.StatusInsufficientStorage
		result.Status, result.Code, result.Message = contracts.StatusUnavailable, "server_storage_capacity", "server storage reserve reached; new writes are paused while existing data remains readable"
	}
	var domainError *authorityError
	if errors.As(err, &domainError) {
		status = domainError.HTTPStatus
		result.Status = domainError.Status
		result.Code = domainError.Code
		result.Message = domainError.Message
		result.Retryable = domainError.Retryable
	}
	if status == http.StatusUnauthorized {
		response.Header().Set("WWW-Authenticate", "Bearer")
	}
	h.writeJSON(response, status, result)
}

func (h *serverHandler) methodNotAllowed(response http.ResponseWriter, requestID string, allowed string) {
	response.Header().Set("Allow", allowed)
	h.writeJSON(response, http.StatusMethodNotAllowed, contracts.ErrorEnvelope{
		SchemaVersion: contracts.SchemaVersion,
		Status:        contracts.StatusInvalidRequest,
		RequestID:     requestID,
		Code:          "method_not_allowed",
		Message:       "method not allowed",
		Retryable:     false,
	})
}

func (h *serverHandler) writeJSON(response http.ResponseWriter, status int, value any) {
	if envelope, ok := value.(contracts.ErrorEnvelope); ok {
		if observed, ok := response.(*observedResponseWriter); ok {
			observed.productStatus = envelope.Status
			observed.failureCategory = observability.CategoryForResponse(envelope.Status, envelope.Code, status)
			observed.errorCode = envelope.Code
			observed.retryable = envelope.Retryable
		}
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

type observedResponseWriter struct {
	http.ResponseWriter
	statusCode      int
	productStatus   contracts.Status
	failureCategory contracts.FailureCategory
	errorCode       string
	retryable       bool
}

func (response *observedResponseWriter) Unwrap() http.ResponseWriter { return response.ResponseWriter }

func (response *observedResponseWriter) WriteHeader(status int) {
	if response.statusCode != 0 {
		return
	}
	response.statusCode = status
	response.ResponseWriter.WriteHeader(status)
}

func (response *observedResponseWriter) Write(content []byte) (int, error) {
	if response.statusCode == 0 {
		response.WriteHeader(http.StatusOK)
	}
	written, err := response.ResponseWriter.Write(content)
	if err != nil {
		response.productStatus = contracts.StatusUnavailable
		response.failureCategory = contracts.FailureNetwork
		response.errorCode = "response_write_failed"
		response.retryable = true
	}
	return written, err
}

func (h *serverHandler) recordRequest(ctx context.Context, request *http.Request, response *observedResponseWriter, correlationID string, started time.Time) {
	if h.logger == nil {
		return
	}
	statusCode := response.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if !h.verboseRequests && !importantRequest(request, statusCode, response.errorCode) {
		return
	}
	severity := observability.SeverityInfo
	if statusCode >= 500 {
		severity = observability.SeverityError
	} else if statusCode >= 400 {
		severity = observability.SeverityWarning
	}
	_ = h.logger.Record(ctx, observability.Event{
		Severity: severity, Operation: serverOperation(request), HTTPStatus: statusCode,
		Status: response.productStatus, FailureCategory: response.failureCategory,
		ErrorCode: response.errorCode, Retryable: response.retryable,
		DurationMS: time.Since(started).Milliseconds(), CorrelationID: correlationID,
	})
}

func serverOperation(request *http.Request) string {
	path := request.URL.Path
	if strings.HasPrefix(path, "/v1/auth/") {
		switch path {
		case "/v1/auth/config":
			return "auth.config"
		case "/v1/auth/start":
			return "auth.start"
		case "/v1/auth/callback":
			return "auth.callback"
		case "/v1/auth/device":
			return "auth.device"
		case "/v1/auth/token":
			return "auth.token"
		case "/v1/auth/logout":
			return "auth.logout"
		default:
			return "route.unknown"
		}
	}
	if path == "/" || dashboardProjectPath(path) || strings.HasPrefix(path, "/assets/") {
		return "dashboard.asset"
	}
	if strings.HasPrefix(path, "/v1/dashboard/") {
		if path == "/v1/dashboard/session" {
			switch request.Method {
			case http.MethodPost:
				return "dashboard.login"
			case http.MethodDelete:
				return "dashboard.logout"
			default:
				return "dashboard.session"
			}
		}
		return "dashboard.read"
	}
	if strings.HasPrefix(path, artifactTransferPrefix) {
		switch request.Method {
		case http.MethodPut:
			return "artifact.upload"
		case http.MethodGet:
			return "artifact.download"
		default:
			return "artifact.transfer"
		}
	}
	switch path {
	case "/healthz":
		return "health.get"
	case "/v1/deployment":
		return "deployment.get"
	case "/v1/account":
		return "account.get"
	case "/v1/account/key":
		return "account.key.reset"
	case "/v1/projects":
		return "project.create"
	case "/v1/connections":
		return "project.join"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "projects" {
		return "route.unknown"
	}
	switch {
	case len(parts) == 4 && parts[3] == "access":
		return "project.access"
	case len(parts) == 4 && parts[3] == "membership":
		return "membership.leave"
	case len(parts) == 4 && parts[3] == "join-requests":
		return "join.request"
	case len(parts) == 4 && parts[3] == "manage":
		return "project.manage"
	case len(parts) == 6 && parts[3] == "library" && parts[4] == "entries" && parts[5] == "list":
		return "library.list"
	case len(parts) == 5 && parts[3] == "library" && parts[4] == "entries":
		return "library.add"
	case len(parts) == 4 && parts[3] == "projection":
		return "project.projection"
	case len(parts) == 5 && parts[3] == "projection" && parts[4] == "retry":
		return "projection.retry"
	case len(parts) == 4 && parts[3] == "history-batches":
		return "history.begin"
	case len(parts) == 6 && parts[3] == "history-batches" && parts[5] == "resolve":
		return "history.resolve"
	case len(parts) == 6 && parts[3] == "history-batches" && parts[5] == "complete":
		return "history.complete"
	case len(parts) == 5 && parts[3] == "work" && parts[4] == "search":
		return "work.search"
	case len(parts) == 4 && parts[3] == "work-graph":
		return "work_graph.read"
	case len(parts) == 6 && parts[3] == "join-requests" && parts[5] == "decision":
		return "join.decide"
	case len(parts) == 6 && parts[3] == "members" && parts[5] == "remove":
		return "member.remove"
	case len(parts) == 4 && parts[3] == "leave":
		return "project.leave"
	case len(parts) == 4 && parts[3] == "dissolve":
		return "project.dissolve"
	case len(parts) == 5 && parts[3] == "sessions" && parts[4] == "prepare":
		return "session.prepare"
	case len(parts) == 5 && parts[3] == "sessions" && parts[4] == "commit":
		return "session.commit"
	case len(parts) == 5 && parts[3] == "sessions" && parts[4] == "pull":
		return "session.pull"
	case len(parts) == 6 && parts[3] == "sessions" && parts[4] == "fork" && parts[5] == "prepare":
		return "session.fork.prepare"
	case len(parts) == 6 && parts[3] == "sessions" && parts[4] == "fork" && parts[5] == "complete":
		return "session.fork.complete"
	case len(parts) == 8 && parts[3] == "sessions" && parts[5] == "versions" && parts[7] == "index":
		return "session.index"
	case len(parts) == 5 && parts[3] == "sessions" && parts[4] == "search":
		return "session.search"
	case len(parts) == 5 && parts[3] == "sessions" && parts[4] == "recent":
		return "session.recent"
	case len(parts) == 6 && parts[3] == "sessions" && parts[5] == "read":
		return "session.read"
	default:
		return "route.unknown"
	}
}

func decodeJSON(request *http.Request, target any) error {
	return decodeJSONLimit(request, target, maxRequestBody)
}

func decodeJSONLimit(request *http.Request, target any, limit int64) error {
	defer request.Body.Close()
	if request.ContentLength > limit {
		return fmt.Errorf("request body exceeds %d bytes", limit)
	}
	limited := &io.LimitedReader{R: request.Body, N: limit + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	if limited.N == 0 {
		return fmt.Errorf("request body exceeds %d bytes", limit)
	}
	return nil
}
