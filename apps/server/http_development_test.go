package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestHTTPConfigPersistsExplicitEndpoint(t *testing.T) {
	dir := t.TempDir()
	args := []string{"init", "--data-dir", dir, "--advertised-endpoint", "http://dev.example.test:7788", "--listen", "0.0.0.0:9123"}
	var out, stderr bytes.Buffer
	if code := runServerCLI(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, err := loadServerConfig(filepath.Join(dir, "server.json"))
	if err != nil || config.Listen != "0.0.0.0:9123" {
		t.Fatalf("explicit listen not preserved: %#v %v", config, err)
	}
	t.Setenv("COCONET_AUTH_PROVIDER", "github")
	if _, err := loginProviderFromEnvironment(context.Background(), "http://dev.example.test:7788"); err == nil || !strings.Contains(err.Error(), "require HTTPS") {
		t.Fatal("HTTP OAuth accepted", err)
	}
}

func TestHTTPDashboardLoginReadLogoutAndOriginBoundaries(t *testing.T) {
	_, h, _, _, key := dashboardFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	h.advertisedEndpoint = server.URL
	login := contracts.DashboardLoginRequest{SchemaVersion: 1, AccountKey: key}
	for _, origin := range []string{"", "http://unrelated.test", "https://" + strings.TrimPrefix(server.URL, "http://")} {
		if response, _ := dashboardRequest(t, server, "POST", "/v1/dashboard/session", login, nil, origin); response.StatusCode != 403 {
			t.Fatal("cross-origin HTTP login accepted")
		}
	}
	response, _ := dashboardRequest(t, server, "POST", "/v1/dashboard/session", login, nil, server.URL)
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		t.Fatal("HTTP login failed", response.StatusCode)
	}
	cookie := response.Cookies()[0]
	if cookie.Name != "coconet_http_dashboard" || cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Domain != "" {
		t.Fatal("invalid HTTP cookie")
	}
	if response, _ := dashboardRequest(t, server, "GET", "/v1/dashboard/projects", nil, cookie, ""); response.StatusCode != 200 {
		t.Fatal("HTTP cookie not accepted")
	}
	h.advertisedEndpoint = "https://" + strings.TrimPrefix(server.URL, "http://")
	if response, _ := dashboardRequest(t, server, "GET", "/v1/dashboard/projects", nil, cookie, ""); response.StatusCode == 200 {
		t.Fatal("HTTP request downgraded HTTPS deployment")
	}
	h.advertisedEndpoint = "http://different.test:7788"
	if response, _ := dashboardRequest(t, server, "GET", "/v1/dashboard/projects", nil, cookie, ""); response.StatusCode == 200 {
		t.Fatal("HTTP accepted a different Host")
	}
	h.advertisedEndpoint = server.URL
	if response, _ := dashboardRequest(t, server, "DELETE", "/v1/dashboard/session", nil, cookie, server.URL); response.StatusCode != 204 || response.Cookies()[0].Name != cookie.Name || response.Cookies()[0].MaxAge != -1 {
		t.Fatal("HTTP logout failed")
	}
	if response, _ := dashboardRequest(t, server, "GET", "/v1/dashboard/projects", nil, cookie, ""); response.StatusCode != 401 {
		t.Fatal("HTTP logout retained access")
	}
}
