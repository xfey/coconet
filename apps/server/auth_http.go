package main

import (
	"net/http"
	"strings"

	"github.com/xfey/coconet/packages/contracts"
)

func (h *serverHandler) handleAuthAPI(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == "/v1/auth/config" && r.Method == http.MethodGet {
		h.writeJSON(w, 200, contracts.AuthConfiguration{SchemaVersion: 1, Provider: h.auth.Kind, Label: h.auth.Label})
		return
	}
	if r.URL.Path == "/v1/auth/callback" && r.Method == http.MethodGet {
		if !h.dashboardHTTPS(r) || !h.browserHost(r) || h.auth.Kind == "key" {
			h.writeError(w, requestID, forbidden("login_unavailable", "browser login requires HTTPS and a configured provider"))
			return
		}
		h.finishBrowserLogin(w, r, requestID)
		return
	}
	if r.Method != http.MethodPost {
		h.methodNotAllowed(w, requestID, http.MethodPost)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		h.writeError(w, requestID, invalid("invalid_json", "JSON request required"))
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "https://"+r.Host {
		h.writeError(w, requestID, forbidden("login_origin_invalid", "same-origin request required"))
		return
	}
	if err := h.limiter.check("auth"+r.URL.Path, h.addressResolver.resolve(r)); err != nil {
		h.writeError(w, requestID, err)
		return
	}
	switch r.URL.Path {
	case "/v1/auth/start":
		if !h.dashboardHTTPS(r) || !h.browserHost(r) || r.Header.Get("Origin") != "https://"+r.Host {
			h.writeError(w, requestID, forbidden("login_origin_invalid", "same-origin HTTPS request required"))
			return
		}
		h.startBrowserLogin(w, r, requestID)
	case "/v1/auth/device":
		var input contracts.DeviceAuthorizationRequest
		if decodeJSONLimit(r, &input, 16384) != nil {
			h.writeError(w, requestID, invalid("invalid_device_authorization", "invalid device authorization request"))
			return
		}
		result, err := h.authority.startDeviceAuthorization(input, h.browserOrigin())
		h.writeResult(w, requestID, 200, result, err)
	case "/v1/auth/token":
		var input contracts.DeviceAuthorizationPoll
		if decodeJSONLimit(r, &input, 2048) != nil {
			h.writeError(w, requestID, invalid("invalid_device_authorization", "invalid device authorization request"))
			return
		}
		result, err := h.authority.pollDeviceAuthorization(input)
		h.writeResult(w, requestID, 200, result, err)
	case "/v1/auth/directory":
		var input contracts.DeviceDirectoryUpdate
		if decodeJSONLimit(r, &input, 16384) != nil {
			h.writeError(w, requestID, invalid("invalid_directory", "invalid directory update"))
			return
		}
		err := h.authority.updateDeviceDirectory(input)
		if err != nil {
			h.writeError(w, requestID, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "/v1/auth/logout":
		actor, err := h.authenticate(r)
		if err != nil {
			h.writeError(w, requestID, err)
			return
		}
		key := hashToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		err = h.authority.store.update(func(state *databaseState) error {
			token := state.Tokens.Get(key)
			if token.AccountID != actor.AccountID || token.DeviceID == "" {
				return forbidden("device_credential_required", "this operation requires a device credential")
			}
			state.Tokens.Delete(key)
			return nil
		})
		if err != nil {
			h.writeError(w, requestID, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		h.writeError(w, requestID, notFound("route_not_found", "authentication route was not found"))
	}
}

func (h *serverHandler) handleDashboardAccount(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account) bool {
	switch {
	case r.URL.Path == "/v1/dashboard/authorize" && r.Method == http.MethodPost:
		var input contracts.DeviceAuthorizationDecision
		if decodeJSONLimit(r, &input, 1024) != nil {
			h.writeError(w, requestID, invalid("invalid_authorization", "invalid authorization decision"))
			return true
		}
		category := "auth.decision"
		if input.Approve == nil && !input.ChooseDirectory {
			category = "auth.preview"
		}
		if err := h.limiter.check(category, actor.AccountID); err != nil {
			h.writeError(w, requestID, err)
			return true
		}
		result, err := h.authority.decideDeviceAuthorization(actor, input)
		h.writeResult(w, requestID, 200, result, err)
	case r.URL.Path == "/v1/dashboard/devices" && r.Method == http.MethodGet:
		result, err := h.authority.accountDevices(actor)
		h.writeResult(w, requestID, 200, result, err)
	case strings.HasPrefix(r.URL.Path, "/v1/dashboard/devices/") && r.Method == http.MethodDelete:
		err := h.authority.revokeDevice(actor, strings.TrimPrefix(r.URL.Path, "/v1/dashboard/devices/"))
		if err != nil {
			h.writeError(w, requestID, err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	case r.URL.Path == "/v1/dashboard/profile" && r.Method == http.MethodPut:
		var input contracts.AccountProfileUpdate
		if decodeJSONLimit(r, &input, 1024) != nil {
			h.writeError(w, requestID, invalid("invalid_profile", "invalid profile"))
			return true
		}
		result, err := h.authority.updateAccountProfile(actor, input)
		h.writeResult(w, requestID, 200, result, err)
	case r.URL.Path == "/v1/dashboard/projects" && r.Method == http.MethodPost:
		h.handleCreateProject(w, r, requestID, actor)
	case r.URL.Path == "/v1/dashboard/connections" && r.Method == http.MethodPost:
		h.handleConnectProject(w, r, requestID, actor)
	default:
		return false
	}
	return true
}
