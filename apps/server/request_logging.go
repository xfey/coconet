package main

import (
	"net/http"
	"strings"
)

// Successful data transfer and polling already have durable protocol receipts.
// Keep security/business changes and actionable failures in default logs.
func importantRequest(r *http.Request, status int, code string) bool {
	if status >= 500 {
		return true
	}
	if status >= 400 {
		if status == 404 || status == 409 {
			switch code {
			case "relay_revision_conflict", "relay_content_busy", "relay_collection_grace", "relay_upload_in_progress", "relay_object_missing":
				return false
			}
		}
		return true
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	path := r.URL.Path
	return strings.HasPrefix(path, "/v1/auth/") ||
		strings.HasPrefix(path, "/v1/account") || path == "/v1/connections" ||
		path == "/v1/workspaces" || path == "/v1/projects" ||
		strings.HasPrefix(path, "/v1/dashboard/") ||
		strings.HasPrefix(path, "/v1/projects/") && !strings.Contains(path, "/relay/")
}
