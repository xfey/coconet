package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func dashboardFixture(t *testing.T) (*authority, *serverHandler, contracts.Account, contracts.Project, string) {
	t.Helper()
	a := newTestAuthority(t)
	actor, _ := issueTestIdentity(t, a, "Owner", "dashboard@coconet.test")
	project := createContextProject(t, a, actor, "Dashboard", "request-dashboard-project")
	key, err := randomSecret(32)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.update(func(state *databaseState) error {
		for hash, token := range state.Tokens.All() {
			if token.AccountID == actor.AccountID {
				state.Tokens.Delete(hash)
			}
		}
		state.Tokens.Set(hashToken(key), tokenRecord{AccountID: actor.AccountID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	identity, err := generateDeploymentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return a, newHandler("fixture", identity, a).(*serverHandler), actor, project, key
}

func dashboardRequest(t *testing.T, server *httptest.Server, method, path string, body any, cookie *http.Cookie, origin string) (*http.Response, []byte) {
	t.Helper()
	var content []byte
	if body != nil {
		var err error
		content, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, server.URL+path, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal("Dashboard request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

func TestDashboardSessionReadBoundaryAndRevocation(t *testing.T) {
	a, h, actor, project, key := dashboardFixture(t)
	server := httptest.NewTLSServer(h)
	defer server.Close()
	login := contracts.DashboardLoginRequest{SchemaVersion: 1, AccountKey: key}
	for _, origin := range []string{"", "https://unrelated.invalid"} {
		response, _ := dashboardRequest(t, server, "POST", "/v1/dashboard/session", login, nil, origin)
		if response.StatusCode != 403 {
			t.Fatal("cross-origin login accepted")
		}
	}
	response, body := dashboardRequest(t, server, "POST", "/v1/dashboard/session", login, nil, server.URL)
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		t.Fatal("login failed")
	}
	cookie := response.Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != int(dashboardSessionTTL.Seconds()) || cookie.Value == key || bytes.Contains(body, []byte(key)) {
		t.Fatal("credential or Cookie boundary is invalid")
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("session response can be cached")
	}
	var logged contracts.DashboardSessionResponse
	if json.Unmarshal(body, &logged) != nil || logged.Account.AccountID != actor.AccountID {
		t.Fatal("login identity differs")
	}
	paths := []string{"/v1/dashboard/projects", "/v1/dashboard/projects/" + project.ProjectID + "/published-graph"}
	for _, path := range paths {
		if response, _ := dashboardRequest(t, server, "GET", path, nil, cookie, ""); response.StatusCode != 200 {
			t.Fatalf("authorized read failed: %s", path)
		}
		if response, _ := dashboardRequest(t, server, "GET", path, nil, nil, ""); response.StatusCode != 401 {
			t.Fatal("unauthenticated read accepted")
		}
	}
	readPath := "/v1/dashboard/projects/" + project.ProjectID + "/sessions/retired/read"
	response, _ = dashboardRequest(t, server, "POST", readPath, map[string]int{"schema_version": 1}, cookie, server.URL)
	if response.StatusCode != 422 {
		t.Fatal("retired semantic read accepted")
	}
	if response, _ := dashboardRequest(t, server, "POST", readPath, map[string]int{"schema_version": 1}, cookie, "https://unrelated.invalid"); response.StatusCode != 403 {
		t.Fatal("cross-origin read accepted")
	}
	for _, path := range []string{"/v1/projects", "/v1/account/key"} {
		if response, _ := dashboardRequest(t, server, "POST", path, map[string]int{"schema_version": 1}, cookie, server.URL); response.StatusCode < 400 {
			t.Fatal("browser Cookie authorized a CLI mutation")
		}
	}
	if err := a.store.update(func(state *databaseState) error {
		member := state.Memberships.Get(membershipKey(project.ProjectID, actor.AccountID))
		member.Status = contracts.MembershipLeft
		state.Memberships.Set(membershipKey(project.ProjectID, actor.AccountID), member)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if response, _ := dashboardRequest(t, server, "GET", paths[1], nil, cookie, ""); response.StatusCode != 403 {
		t.Fatal("revoked membership retained graph access")
	}
	_, body = dashboardRequest(t, server, "GET", paths[0], nil, cookie, "")
	if bytes.Contains(body, []byte(project.ProjectID)) {
		t.Fatal("revoked project remains in browser project list")
	}
	if response, _ := dashboardRequest(t, server, "DELETE", "/v1/dashboard/session", nil, cookie, server.URL); response.StatusCode != 204 {
		t.Fatal("logout failed")
	}
	if response, _ := dashboardRequest(t, server, "GET", "/v1/dashboard/session", nil, cookie, ""); response.StatusCode != 401 {
		t.Fatal("logged-out session accepted")
	}
}

func TestDashboardDurabilityExpiryAndAccountKeyReset(t *testing.T) {
	a, _, actor, _, key := dashboardFixture(t)
	_, token, err := a.createDashboardSession(key, "")
	if err != nil {
		t.Fatal(err)
	}
	path := a.store.db.Path()
	if err := a.store.close(); err != nil {
		t.Fatal(err)
	}
	a.store, err = openStore(path, a.store.contents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.close() })
	if _, err := a.authenticateDashboard(token); err != nil {
		t.Fatal("restart lost browser session")
	}
	if err := a.store.view(func(state databaseState) error {
		serialized, _ := json.Marshal(state)
		if bytes.Contains(serialized, []byte(token)) || bytes.Contains(serialized, []byte(key)) {
			t.Fatal("plaintext credential persisted on Server")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newKey, err := randomSecret(32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.resetAccountKey(actor, key, contracts.ResetAccountKeyRequest{SchemaVersion: 1, NewKey: newKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.authenticate(key); err == nil {
		t.Fatal("old Account Key retained access")
	}
	if _, err := a.authenticateDashboard(token); err == nil {
		t.Fatal("key reset retained browser access")
	}
	if _, err := a.resetAccountKey(actor, newKey, contracts.ResetAccountKeyRequest{SchemaVersion: 1, NewKey: newKey}); err != nil {
		t.Fatal("lost reset response cannot be retried")
	}
	if _, err := a.resetAccountKey(actor, key, contracts.ResetAccountKeyRequest{SchemaVersion: 1, NewKey: key}); err == nil {
		t.Fatal("stale reset resurrected an old key")
	}
	_, token, err = a.createDashboardSession(newKey, "")
	if err != nil {
		t.Fatal(err)
	}
	oldNow := a.now
	a.now = func() time.Time { return oldNow().Add(dashboardSessionTTL) }
	if _, err := a.authenticateDashboard(token); err == nil {
		t.Fatal("expired browser session accepted")
	}
}

func TestDashboardTLSAssets(t *testing.T) {
	_, h, _, _, key := dashboardFixture(t)
	request := httptest.NewRequest("POST", "http://fixture/v1/dashboard/session", strings.NewReader(`{"schema_version":1}`))
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Origin", "https://fixture")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "dashboard_https_required") {
		t.Fatal("untrusted proxy bypassed HTTPS requirement")
	}
	for _, path := range []string{"/", "/assets/app.mjs", "/assets/project-document.mjs", "/assets/login-ripple.mjs", "/assets/account.mjs", "/assets/connection.mjs", "/assets/installation.mjs", "/assets/dialog.mjs", "/assets/button.mjs", "/assets/button.css", "/assets/style.css", "/assets/graph.mjs", "/assets/canvas.mjs", "/assets/notifications.mjs", "/assets/icons.mjs", "/assets/catalog.mjs", "/assets/i18n.mjs", "/assets/layout-client.mjs", "/assets/vendor/elk-0.12.0.api.js", "/assets/vendor/README.md", "/assets/vendor/elk-LICENSE.md"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 200 || r.Header().Get("Content-Security-Policy") == "" || r.Header().Get("Referrer-Policy") != "no-referrer" || strings.Contains(r.Body.String(), key) {
			t.Fatal("Dashboard static asset or policy is invalid")
		}
		policy := r.Header().Get("Content-Security-Policy")
		if !strings.Contains(policy, "worker-src 'self'") || strings.Contains(policy, "unsafe-") || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Dashboard worker policy or application cache is invalid")
		}
	}
}

func TestDashboardBrandFont(t *testing.T) {
	_, handler, _, _, _ := dashboardFixture(t)
	const path = "/assets/" + dashboardBrandFontAsset
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
	if get.Code != http.StatusOK || get.Header().Get("Content-Type") != "font/woff2" || !bytes.HasPrefix(get.Body.Bytes(), []byte("wOF2")) {
		t.Fatal("brand font is not served as WOFF2")
	}
	if get.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || get.Header().Get("Content-Length") != strconv.Itoa(get.Body.Len()) {
		t.Fatal("content-addressed font has incorrect caching or length")
	}
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") || head.Header().Get("Cache-Control") != get.Header().Get("Cache-Control") {
		t.Fatal("font HEAD differs from GET")
	}
	for _, licensePath := range []string{"/assets/fonts/OFL.txt", "/assets/fonts/README.md"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, licensePath, nil))
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("font license or source information is unavailable")
		}
	}
}

func TestDashboardProjectPath(t *testing.T) {
	_, handler, _, project, key := dashboardFixture(t)
	path := "/" + project.ProjectID
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "/assets/app.mjs") || strings.Contains(get.Body.String(), key) || get.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("direct project path did not serve the public Dashboard shell")
	}
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatal("project path HEAD differs from GET")
	}
	for _, invalid := range []string{"/prj_1111111111111111", "/coco_111", path + "/extra", "//" + project.ProjectID, "/v1/projects/" + project.ProjectID, "/coco_zzzzzzzzzzzzzzzzzzzzzz"} {
		if dashboardProjectPath(invalid) {
			t.Fatalf("accepted invalid project path %q", invalid)
		}
	}
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, path, nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatal("project page accepted a write method")
	}
	handler.webOrigin = "https://web.example"
	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "https://api.example"+path, nil))
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != handler.webOrigin+path {
		t.Fatal("API-origin project link did not preserve the project path")
	}
}

func TestDashboardVersionRendering(t *testing.T) {
	_, handler, _, _, _ := dashboardFixture(t)
	for _, version := range []string{"0.16.6", "dev", `<script>alert("version")</script>&`} {
		handler.version = version
		get := httptest.NewRecorder()
		handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/", nil))
		body := get.Body.String()
		if get.Code != http.StatusOK || strings.Contains(body, "{{SERVER_VERSION}}") || strings.Count(body, `name="coconet-server-version" content="`+html.EscapeString(version)+`"`) != 1 || strings.Contains(body, `<script>alert(`) {
			t.Fatal("Dashboard did not safely provide the running version for settings")
		}
		if get.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
			t.Fatal("rendered page has an incorrect content length")
		}
		head := httptest.NewRecorder()
		handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/", nil))
		if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
			t.Fatal("Dashboard HEAD differs from the rendered GET representation")
		}
	}
}

func TestDashboardPinnedEngineAssets(t *testing.T) {
	_, h, _, _, _ := dashboardFixture(t)
	const path = "/assets/vendor/elk-0.12.0.worker.min.js"
	identity := httptest.NewRecorder()
	h.ServeHTTP(identity, httptest.NewRequest("GET", path, nil))
	if identity.Code != 200 || identity.Body.Len() != 1595334 {
		t.Fatal("pinned engine missing")
	}
	for _, test := range []struct {
		accept string
		gzip   bool
	}{
		{"", false}, {"identity", false}, {"br, gzip", true}, {"gzip;q=0.5", true},
		{"gzip;q=0, *;q=1", false}, {"*;q=1, gzip;q=0", false}, {"*", true},
		{"gzip;q=invalid", false}, {"gzip;q=NaN", false}, {"gzip;q=2", false},
		{"xgzip", false}, {"gzip;q=0.000", false}, {"GZIP; Q=1", true},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("Accept-Encoding", test.accept)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, r)
			if response.Code != 200 || (response.Header().Get("Content-Encoding") == "gzip") != test.gzip || response.Header().Get("Vary") != "Accept-Encoding" || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || response.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
				t.Fatalf("invalid engine headers: %s %q", method, test.accept)
			}
			length := identity.Body.Len()
			if test.gzip {
				length = 461595
			}
			if response.Header().Get("Content-Length") != strconv.Itoa(length) || method == "HEAD" && response.Body.Len() != 0 {
				t.Fatal("invalid representation length or HEAD body")
			}
			if method == "GET" && test.gzip {
				reader, err := gzip.NewReader(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := io.ReadAll(reader)
				reader.Close()
				if err != nil || !bytes.Equal(decoded, identity.Body.Bytes()) {
					t.Fatal("gzip differs from pinned engine")
				}
			}
		}
	}
	for _, path := range []string{"/assets/vendor/elk-0.12.0.worker.min.js.gz", "/assets/vendor/unknown.js"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 404 {
			t.Fatal("unlisted vendor resource served")
		}
	}
}

func TestDashboardSessionLimitUsesCreationTime(t *testing.T) {
	a, _, _, _, key := dashboardFixture(t)
	start := a.now().Truncate(time.Second)
	tokens := []string{}
	for i := 0; i < 11; i++ {
		now := start.Add(time.Duration(i) * 100 * time.Millisecond)
		a.now = func() time.Time { return now }
		_, token, err := a.createDashboardSession(key, "")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	if _, err := a.authenticateDashboard(tokens[0]); err == nil {
		t.Fatal("oldest browser was not evicted")
	}
	for _, token := range tokens[1:] {
		if _, err := a.authenticateDashboard(token); err != nil {
			t.Fatal("a newer browser was evicted")
		}
	}
	_, replacement, err := a.createDashboardSession(key, tokens[10])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.authenticateDashboard(tokens[10]); err == nil {
		t.Fatal("login did not replace its previous browser session")
	}
	if _, err := a.authenticateDashboard(replacement); err != nil {
		t.Fatal("replacement browser session is invalid")
	}
}

// Opt-in browser harness owns its synthetic data and a TLS loopback endpoint.

// Include untrusted text to exercise browser rendering as text, never HTML.

// Mixed native messages contain visible text and hidden tool data.

// Rebuild immutable fixture content from the replacement turns.

// Synthetic confirmed registry facts exercise catalog pagination without
// invoking a local Agent or changing the published four-node graph.
