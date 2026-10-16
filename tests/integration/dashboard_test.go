package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestDashboardRuntimeServerIdentityLifecycle(t *testing.T) {
	root, temporary := projectRoot(t), t.TempDir()
	runtimeBinary, serverBinary := filepath.Join(temporary, "coconet"), filepath.Join(temporary, "coconet-server")
	buildBinary(t, root, runtimeBinary, "./apps/runtime")
	buildBinary(t, root, serverBinary, "./apps/server")
	t.Setenv("COCONET_TRUSTED_PROXY_CIDRS", "127.0.0.1/32")
	t.Setenv("COCONET_INFERENCE_URL", "")
	t.Setenv("COCONET_INFERENCE_MODEL", "")
	t.Setenv("COCONET_INFERENCE_API_KEY", "")
	serverURL, config, _, stop := startServer(t, serverBinary, filepath.Join(temporary, "server"))
	defer func() { stop() }()
	upstream, _ := url.Parse(serverURL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https")
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	repository, localHome := filepath.Join(temporary, "repository"), filepath.Join(temporary, "runtime")
	git(t, root, "init", "-q", repository)
	git(t, repository, "config", "user.name", "Dashboard Fixture")
	git(t, repository, "config", "user.email", "dashboard@coconet.test")
	runCoconet(t, runtimeBinary, repository, localHome, serverURL, "", "y\n", "init", "--new")
	binding := readOnlyBinding(t, filepath.Join(localHome, "bindings.json"))
	key := strings.TrimSpace(runCoconet(t, runtimeBinary, repository, localHome, serverURL, "", "", "account", "key"))
	request := func(method, path string, input any, cookie *http.Cookie) (*http.Response, []byte) {
		t.Helper()
		content, err := json.Marshal(input)
		if err != nil {
			t.Fatal("cannot encode browser request")
		}
		r, err := http.NewRequest(method, gateway.URL+path, bytes.NewReader(content))
		if err != nil {
			t.Fatal("cannot construct browser request")
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", gateway.URL)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response, err := gateway.Client().Do(r)
		if err != nil {
			t.Fatal("browser request failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("browser response failed")
		}
		return response, body
	}
	response, body := request("GET", "/", nil, nil)
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("/assets/app.mjs")) {
		t.Fatal("Server binary did not embed Dashboard")
	}
	login := contracts.DashboardLoginRequest{SchemaVersion: 1, AccountKey: key}
	response, _ = request("POST", "/v1/dashboard/session", login, nil)
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		t.Fatal("CLI Account Key could not sign into proxied Dashboard")
	}
	cookie := response.Cookies()[0]
	_, body = request("GET", "/v1/dashboard/projects", nil, cookie)
	var projects contracts.DashboardProjectsResponse
	if json.Unmarshal(body, &projects) != nil || len(projects.Projects) != 1 || projects.Projects[0].ProjectID != binding.ProjectID {
		t.Fatal("Runtime project is not visible to its browser identity")
	}
	stop()
	_, stop = startServerAtURL(t, serverBinary, config, serverURL)
	if response, _ := request("GET", "/v1/dashboard/session", nil, cookie); response.StatusCode != 200 {
		t.Fatal("process restart invalidated persistent browser session")
	}
	resetOutput := runCoconet(t, runtimeBinary, repository, localHome, serverURL, "", "", "account", "reset-key")
	replacement := strings.TrimSpace(runCoconet(t, runtimeBinary, repository, localHome, serverURL, "", "", "account", "key"))
	if replacement == key || len(replacement) != 43 || strings.Contains(resetOutput, replacement) || strings.Contains(resetOutput, key) {
		t.Fatal("Runtime key reset did not safely persist a distinct credential")
	}
	if response, _ := request("GET", "/v1/dashboard/session", nil, cookie); response.StatusCode != 401 {
		t.Fatal("Runtime key reset retained old browser access")
	}
	if response, _ := request("POST", "/v1/dashboard/session", login, nil); response.StatusCode != 401 {
		t.Fatal("old Account Key retained login access")
	}
	login.AccountKey = replacement
	if response, _ := request("POST", "/v1/dashboard/session", login, nil); response.StatusCode != 200 {
		t.Fatal("replacement Account Key could not log in")
	}
	runCoconet(t, runtimeBinary, repository, localHome, serverURL, "", "", "status")
}
