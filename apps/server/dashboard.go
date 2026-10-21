package main

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/projectlink"
)

const dashboardSessionTTL = 30 * 24 * time.Hour
const dashboardCookieName = "__Host-coconet_dashboard"

type dashboardSessionRecord struct {
	AccountID string `json:"account_id"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

func dashboardAuthError() error {
	return &authorityError{HTTPStatus: 401, Status: contracts.StatusForbidden, Code: "dashboard_login_required", Message: "sign in to continue"}
}

func (a *authority) dashboardTime(state databaseState) time.Time {
	if !state.storageTime.IsZero() {
		return state.storageTime.UTC()
	}
	return a.now().UTC()
}

func (a *authority) createDashboardSession(key, previous string) (contracts.DashboardSessionResponse, string, error) {
	account, err := a.authenticate(key)
	if err != nil {
		return contracts.DashboardSessionResponse{}, "", dashboardAuthError()
	}
	return a.createDashboardSessionForAccount(account, previous)
}

func (a *authority) createDashboardSessionForAccount(account contracts.Account, previous string) (contracts.DashboardSessionResponse, string, error) {
	result := contracts.DashboardSessionResponse{SchemaVersion: 1, Status: contracts.StatusOK}
	token, err := randomSecret(32)
	if err != nil {
		return result, "", err
	}
	err = a.store.update(func(state *databaseState) error {
		if found := state.Accounts.Has(account.AccountID); !found {
			return dashboardAuthError()
		}
		now := a.dashboardTime(*state)
		active := []string{}
		for hash, session := range state.DashboardSessions.All() {
			expires, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
			if err != nil || !expires.After(now) || hash == hashToken(previous) {
				state.dashboardAccessChanged = true
				state.DashboardSessions.Delete(hash)
				continue
			}
			if session.AccountID == account.AccountID {
				active = append(active, hash)
			}
		}
		// A bounded set of browsers per identity; repeated logins cannot grow state indefinitely.
		sort.Slice(active, func(i, j int) bool {
			left, _ := time.Parse(time.RFC3339Nano, state.DashboardSessions.Get(active[i]).CreatedAt)
			right, _ := time.Parse(time.RFC3339Nano, state.DashboardSessions.Get(active[j]).CreatedAt)
			if left.Equal(right) {
				return active[i] < active[j]
			}
			return left.Before(right)
		})
		for len(active) >= 10 {
			state.dashboardAccessChanged = true
			state.DashboardSessions.Delete(active[0])
			active = active[1:]
		}
		result.Account = account
		result.ExpiresAt = now.Add(dashboardSessionTTL).Format(time.RFC3339Nano)
		state.DashboardSessions.Set(hashToken(token), dashboardSessionRecord{AccountID: account.AccountID, ExpiresAt: result.ExpiresAt, CreatedAt: now.Format(time.RFC3339Nano)})
		return nil
	})
	return result, token, err
}

func (a *authority) authenticateDashboard(token string) (contracts.DashboardSessionResponse, error) {
	result := contracts.DashboardSessionResponse{SchemaVersion: 1, Status: contracts.StatusOK}
	err := a.store.view(func(state databaseState) error {
		session, exists := state.DashboardSessions.Lookup(hashToken(token))
		expires, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		account, accountExists := state.Accounts.Lookup(session.AccountID)
		if token == "" || !exists || err != nil || !expires.After(a.dashboardTime(state)) || !accountExists {
			return dashboardAuthError()
		}
		result.Account, result.ExpiresAt = account, session.ExpiresAt
		return nil
	})
	return result, err
}

func (a *authority) dashboardProjects(actor contracts.Account) (contracts.DashboardProjectsResponse, error) {
	result := contracts.DashboardProjectsResponse{SchemaVersion: 1, Status: contracts.StatusOK, Projects: []contracts.ProjectReference{}}
	err := a.store.view(func(state databaseState) error {
		for _, membership := range state.Memberships.By("account_id", actor.AccountID) {
			id := membership.ProjectID
			if project, _, err := requireReadableMember(&state, actor.AccountID, id); err == nil {
				result.Projects = append(result.Projects, contracts.ProjectReference{ProjectID: id, Name: project.Name, Status: project.Status})
			}
		}
		sort.Slice(result.Projects, func(i, j int) bool {
			if result.Projects[i].Name == result.Projects[j].Name {
				return result.Projects[i].ProjectID < result.Projects[j].ProjectID
			}
			return result.Projects[i].Name < result.Projects[j].Name
		})
		return nil
	})
	return result, err
}

// A signed-in recipient may preview a shared project before explicitly joining.
// Return only its public label, never membership, account or collaboration data.
func (a *authority) dashboardProjectPreview(projectID string) (contracts.ProjectReference, error) {
	var result contracts.ProjectReference
	if _, ok := projectlink.ProjectUID(projectID); !ok {
		return result, invalid("invalid_project_id", "enter a valid Project ID")
	}
	err := a.store.view(func(state databaseState) error {
		project, ok := state.Projects.Lookup(projectID)
		if !ok {
			return notFound("project_not_found", "project was not found")
		}
		if project.Status != contracts.ProjectActive {
			return conflict("project_archived", "archived projects do not accept connections")
		}
		result = contracts.ProjectReference{ProjectID: project.ProjectID, Name: project.Name, Status: project.Status}
		return nil
	})
	return result, err
}

func (a *authority) resetAccountKey(actor contracts.Account, oldKey string, request contracts.ResetAccountKeyRequest) (contracts.ResetAccountKeyResponse, error) {
	result := contracts.ResetAccountKeyResponse{SchemaVersion: 1, Status: contracts.StatusOK, AccountID: actor.AccountID}
	decoded, err := base64.RawURLEncoding.DecodeString(request.NewKey)
	if request.SchemaVersion != 1 || err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != request.NewKey {
		return result, invalid("invalid_account_key", "new_key must encode 32 random bytes")
	}
	err = a.store.update(func(state *databaseState) error {
		old, exists := state.Tokens.Lookup(hashToken(oldKey))
		if !exists || old.AccountID != actor.AccountID {
			return dashboardAuthError()
		}
		newHash := hashToken(request.NewKey)
		if existing, exists := state.Tokens.Lookup(newHash); exists {
			if existing.AccountID != actor.AccountID {
				return invalid("invalid_account_key", "replacement key is unavailable")
			}
			// The first request committed, but its response may have been lost.
			if hashToken(oldKey) == newHash {
				return nil
			}
		}
		for hash, key := range state.Tokens.By("account_id", actor.AccountID) {
			if key.AccountID == actor.AccountID {
				state.Tokens.Delete(hash)
			}
		}
		for hash, session := range state.DashboardSessions.By("account_id", actor.AccountID) {
			if session.AccountID == actor.AccountID {
				state.dashboardAccessChanged = true
				state.DashboardSessions.Delete(hash)
			}
		}
		state.Tokens.Set(newHash, tokenRecord{AccountID: actor.AccountID})
		return nil
	})
	return result, err
}

func (h *serverHandler) dashboardHTTPS(request *http.Request) bool {
	if request.TLS != nil {
		return true
	}
	return h.trustedProxy(request) && request.Header.Get("X-Forwarded-Proto") == "https"
}

func (h *serverHandler) handleDashboardAPI(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !h.browserHost(r) {
		h.writeError(w, requestID, forbidden("dashboard_origin_invalid", "use the configured browser origin"))
		return
	}
	plainHTTP := h.dashboardHTTPAllowed(r)
	if !h.dashboardHTTPS(r) && !plainHTTP {
		h.writeError(w, requestID, invalid("dashboard_https_required", "Dashboard requires HTTPS"))
		return
	}
	// SameSite plus exact Origin checks also protect login against session swapping.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		origin := h.requestOrigin(r)
		if r.Header.Get("Origin") != origin {
			h.writeError(w, requestID, forbidden("dashboard_origin_invalid", "same-origin Dashboard request required"))
			return
		}
	}
	cookieName := dashboardCookieName
	if plainHTTP {
		cookieName = "coconet_http_dashboard"
	}
	cookieName = h.cookieName(cookieName, !plainHTTP)
	cookie, _ := r.Cookie(cookieName)
	token := ""
	if cookie != nil {
		token = cookie.Value
	}
	if r.URL.Path == "/v1/dashboard/session" {
		switch r.Method {
		case http.MethodPost:
			if h.auth.Kind != "key" {
				h.writeError(w, requestID, forbidden("provider_login_required", "use the configured login provider"))
				return
			}
			if err := h.limiter.check("dashboard.login", h.addressResolver.resolve(r)); err != nil {
				h.writeError(w, requestID, err)
				return
			}
			var input contracts.DashboardLoginRequest
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || decodeJSONLimit(r, &input, 2048) != nil || input.SchemaVersion != 1 || len(input.AccountKey) > 512 {
				h.writeError(w, requestID, invalid("invalid_dashboard_login", "valid Account Key JSON is required"))
				return
			}
			result, created, err := h.authority.createDashboardSession(input.AccountKey, token)
			if err != nil {
				h.writeError(w, requestID, err)
				return
			}
			expires, _ := time.Parse(time.RFC3339Nano, result.ExpiresAt)
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: created, Path: h.cookiePath(), Secure: !plainHTTP, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(dashboardSessionTTL.Seconds()), Expires: expires})
			h.writeJSON(w, 200, result)
		case http.MethodGet:
			result, err := h.authority.authenticateDashboard(token)
			h.writeResult(w, requestID, 200, result, err)
		case http.MethodDelete:
			if err := h.authority.store.update(func(state *databaseState) error {
				state.dashboardAccessChanged = true
				state.DashboardSessions.Delete(hashToken(token))
				return nil
			}); err != nil {
				h.writeError(w, requestID, err)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: h.cookiePath(), Secure: !plainHTTP, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
			w.WriteHeader(http.StatusNoContent)
		default:
			h.methodNotAllowed(w, requestID, "GET, POST, DELETE")
		}
		return
	}
	session, err := h.authority.authenticateDashboard(token)
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	actor := session.Account
	if h.handleDashboardAccount(w, r, requestID, actor) {
		return
	}
	if r.URL.Path == "/v1/dashboard/projects" && r.Method == http.MethodGet {
		result, err := h.authority.dashboardProjects(actor)
		h.writeResult(w, requestID, 200, result, err)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 5 && parts[0] == "v1" && parts[1] == "dashboard" && parts[2] == "projects" {
		projectID := parts[3]
		if parts[4] == "document" {
			h.handleProjectDocument(w, r, requestID, actor, projectID, parts[5:], true)
			return
		}
		switch {
		case len(parts) == 5 && parts[4] == "preview" && r.Method == http.MethodGet:
			if err := h.limiter.check("project.preview.account", actor.AccountID); err != nil {
				h.writeError(w, requestID, err)
				return
			}
			result, err := h.authority.dashboardProjectPreview(projectID)
			h.writeResult(w, requestID, http.StatusOK, result, err)
			return
		case len(parts) == 5 && parts[4] == "events" && r.Method == http.MethodGet:
			h.serveDashboardEvents(w, r, requestID, projectID, token)
			return
		case len(parts) == 5 && parts[4] == "workspaces" && r.Method == http.MethodGet:
			h.handleProjectFolders(w, r, requestID, actor, projectID)
			return
		case len(parts) == 5 && parts[4] == "membership" && r.Method == http.MethodDelete:
			_, err := h.authority.leaveProject(actor, projectID, contracts.ProjectMutationRequest{SchemaVersion: 1, RequestID: requestID})
			if err != nil {
				h.writeError(w, requestID, err)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		case len(parts) == 5 && parts[4] == "published-graph" && r.Method == http.MethodGet:
			h.servePublishedGraph(w, r, requestID, actor, projectID)
			return
		case len(parts) == 5 && parts[4] == "library-selection" && r.Method == http.MethodPost:
			h.handleDashboardLibrary(w, r, requestID, actor, projectID)
			return
		case parts[4] == "graph" || parts[4] == "projection" || parts[4] == "sessions" || parts[4] == "nodes" || parts[4] == "library" || parts[4] == "activity":
			h.writeError(w, requestID, unsupported("local_runtime_required", "Refresh the Dashboard to read the published local DAG; Session actions run on your connected device."))
			return
		}
	}
	h.writeError(w, requestID, notFound("route_not_found", "Dashboard route was not found"))
}

// Dashboard cookies are never accepted by CLI routes or Account Key mutation.
func (h *serverHandler) handleAccountKey(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPut {
		h.methodNotAllowed(w, requestID, http.MethodPut)
		return
	}
	var input contracts.ResetAccountKeyRequest
	if err := decodeJSONLimit(r, &input, 2048); err != nil {
		h.writeError(w, requestID, invalid("invalid_json", "invalid key replacement"))
		return
	}
	if err := h.limiter.check("account.key", actor.AccountID); err != nil {
		h.writeError(w, requestID, err)
		return
	}
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	result, err := h.authority.resetAccountKey(actor, key, input)
	h.writeResult(w, requestID, 200, result, err)
}
