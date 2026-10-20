package main

import (
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/xfey/coconet/apps/dashboard"
	"github.com/xfey/coconet/packages/projectlink"
)

const dashboardBrandFontAsset = "fonts/outfit-brand-400-ed2c6d023cf3.woff2"

func dashboardProjectPath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	_, ok := projectlink.ProjectUID(strings.TrimPrefix(path, "/"))
	return ok
}

func (h *serverHandler) serveDashboard(w http.ResponseWriter, r *http.Request, requestID string) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; worker-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.methodNotAllowed(w, requestID, "GET, HEAD")
		return
	}
	if !h.browserHost(r) {
		// Redirect only public pages/assets. API requests and credentials are
		// never forwarded to another origin; URL fragments remain browser-side.
		http.Redirect(w, r, h.webOrigin+r.URL.RequestURI(), http.StatusTemporaryRedirect)
		return
	}
	files := map[string]struct{ file, mime string }{
		"/":                                       {"index.html", "text/html; charset=utf-8"},
		"/assets/connection.mjs":                  {"connection.mjs", "text/javascript; charset=utf-8"},
		"/assets/account.mjs":                     {"account.mjs", "text/javascript; charset=utf-8"},
		"/assets/library.mjs":                     {"library.mjs", "text/javascript; charset=utf-8"},
		"/assets/app.mjs":                         {"app.mjs", "text/javascript; charset=utf-8"},
		"/assets/platform.mjs":                    {"platform.mjs", "text/javascript; charset=utf-8"},
		"/assets/vendor/noble-sha256-1.8.0.mjs":   {"vendor/noble-sha256-1.8.0.mjs", "text/javascript; charset=utf-8"},
		"/assets/vendor/noble-hashes-LICENSE.txt": {"vendor/noble-hashes-LICENSE.txt", "text/plain; charset=utf-8"},
		"/assets/login-ripple.mjs":                {"login-ripple.mjs", "text/javascript; charset=utf-8"},
		"/assets/published-graph.mjs":             {"published-graph.mjs", "text/javascript; charset=utf-8"},
		"/assets/events.mjs":                      {"events.mjs", "text/javascript; charset=utf-8"},
		"/assets/installation.mjs":                {"installation.mjs", "text/javascript; charset=utf-8"},
		"/assets/button.mjs":                      {"button.mjs", "text/javascript; charset=utf-8"},
		"/assets/button.css":                      {"button.css", "text/css; charset=utf-8"},
		"/assets/dialog.mjs":                      {"dialog.mjs", "text/javascript; charset=utf-8"},
		"/assets/catalog.mjs":                     {"catalog.mjs", "text/javascript; charset=utf-8"},
		"/assets/notifications.mjs":               {"notifications.mjs", "text/javascript; charset=utf-8"},
		"/assets/canvas.mjs":                      {"canvas.mjs", "text/javascript; charset=utf-8"},
		"/assets/i18n.mjs":                        {"i18n.mjs", "text/javascript; charset=utf-8"},
		"/assets/icons.mjs":                       {"icons.mjs", "text/javascript; charset=utf-8"},
		"/assets/layout-client.mjs":               {"layout-client.mjs", "text/javascript; charset=utf-8"},
		"/assets/vendor/elk-0.12.0.worker.min.js": {"vendor/elk-0.12.0.worker.min.js", "text/javascript; charset=utf-8"},
		"/assets/vendor/elk-0.12.0.api.js":        {"vendor/elk-0.12.0.api.js", "text/javascript; charset=utf-8"},
		"/assets/vendor/elk-LICENSE.md":           {"vendor/elk-LICENSE.md", "text/plain; charset=utf-8"},
		"/assets/vendor/README.md":                {"vendor/README.md", "text/plain; charset=utf-8"},
		"/assets/graph.mjs":                       {"graph.mjs", "text/javascript; charset=utf-8"},
		"/assets/style.css":                       {"style.css", "text/css; charset=utf-8"},
		"/assets/coconet-logo.svg":                {"coconet-logo.svg", "image/svg+xml"},
		"/assets/coconet-app-icon.svg":            {"coconet-app-icon.svg", "image/svg+xml"},
		"/assets/" + dashboardBrandFontAsset:      {dashboardBrandFontAsset, "font/woff2"},
		"/assets/fonts/OFL.txt":                   {"fonts/OFL.txt", "text/plain; charset=utf-8"},
		"/assets/fonts/README.md":                 {"fonts/README.md", "text/plain; charset=utf-8"},
	}
	path := r.URL.Path
	if dashboardProjectPath(path) {
		path = "/"
	}
	asset, ok := files[path]
	if !ok {
		h.writeError(w, requestID, notFound("route_not_found", "Dashboard asset was not found"))
		return
	}
	file := asset.file
	if file == dashboardBrandFontAsset {
		// Font filenames include a content hash, so a new build gets a new URL.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if file == "vendor/elk-0.12.0.worker.min.js" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Vary", "Accept-Encoding")
		if dashboardAcceptsGzip(r.Header.Get("Accept-Encoding")) {
			file += ".gz"
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	content, err := dashboard.Assets.ReadFile("assets/" + file)
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	if file == "index.html" {
		content = []byte(strings.ReplaceAll(string(content), "{{SERVER_VERSION}}", html.EscapeString(h.version)))
		content = []byte(strings.ReplaceAll(string(content), "{{API_ORIGIN}}", html.EscapeString(h.advertisedEndpoint)))
	}
	w.Header().Set("Content-Type", asset.mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}

// Serve the pinned engine's precompressed representation only when accepted.
// An explicit gzip;q=0 overrides a wildcard; absent/invalid quality is conservative.
func dashboardAcceptsGzip(header string) bool {
	wildcard := false
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(entry, ";")
		encoding := strings.TrimSpace(strings.ToLower(parts[0]))
		if encoding != "gzip" && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if strings.EqualFold(strings.TrimSpace(name), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if !ok || err != nil || !(parsed >= 0 && parsed <= 1) {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if encoding == "gzip" {
			return quality > 0
		}
		wildcard = quality > 0
	}
	return wildcard
}
