package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"golang.org/x/oauth2"
)

func TestWebOriginConfiguration(t *testing.T) {
	for _, value := range []string{"", "https://COCONET.SPACE/", "https://workspace.example:9443"} {
		t.Setenv("COCONET_WEB_ORIGIN", value)
		origin, err := webOriginFromEnvironment("https://api.example")
		want := strings.TrimSuffix(strings.ToLower(value), "/")
		if want == "" {
			want = "https://api.example"
		}
		if err != nil || origin != want {
			t.Fatalf("origin %q: %q, %v", value, origin, err)
		}
	}
	for _, value := range []string{"http://example.com", "http://localhost:8080", "https://a.example/path", "https://a.example?x=1", "https://a.example/#fragment", "https://user:secret@a.example", "//a.example"} {
		t.Setenv("COCONET_WEB_ORIGIN", value)
		if _, err := webOriginFromEnvironment("https://api.example"); err == nil {
			t.Fatalf("accepted invalid web origin %q", value)
		}
	}
}

func TestSeparateBrowserOrigin(t *testing.T) {
	h := newTestHandler(t).(*serverHandler)
	h.webOrigin, h.advertisedEndpoint = "https://coconet.space", "https://api.coconet.space"
	for _, path := range []string{"/", "/", "/?from=cli", "/assets/style.css"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", h.advertisedEndpoint+path, nil))
		if response.Code != 307 || response.Header().Get("Location") != h.webOrigin+path {
			t.Fatalf("bad browser redirect: %d %s", response.Code, response.Header().Get("Location"))
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(method, h.webOrigin+"/", nil))
		if response.Code != 200 {
			t.Fatalf("root page returned %d", response.Code)
		}
		if method == "GET" && !strings.Contains(response.Body.String(), `name="coconet-api-origin" content="https://api.coconet.space"`) {
			t.Fatal("page did not advertise the API origin for copied CLI commands")
		}
	}
	// API discovery stays on both origins so existing CLI versions can verify
	// that the browser belongs to the same signed Deployment before opening it.
	for _, origin := range []string{h.webOrigin, h.advertisedEndpoint} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", origin+"/v1/deployment?challenge=fixture_web_origin_123456", nil))
		if response.Code != 200 || response.Header().Get("Location") != "" {
			t.Fatal("Deployment discovery was redirected")
		}
	}
	request := httptest.NewRequest("POST", h.advertisedEndpoint+"/v1/auth/device", strings.NewReader(`{"device_name":"Test device","code_challenge":"`+pkceChallenge(oauth2.GenerateVerifier())+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	var started contracts.DeviceAuthorization
	if json.Unmarshal(response.Body.Bytes(), &started) != nil || response.Code != 200 || !strings.HasPrefix(started.VerificationURI, h.webOrigin+"/#authorize=") {
		t.Fatalf("device did not use web origin: %d", response.Code)
	}
	// Do not initiate or restore browser credentials on the API host, and do
	// not redirect POSTs carrying credentials to another origin.
	for _, path := range []string{"/v1/auth/start", "/v1/dashboard/session"} {
		request := httptest.NewRequest("POST", h.advertisedEndpoint+path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", h.advertisedEndpoint)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || response.Header().Get("Location") != "" || len(response.Result().Cookies()) != 0 {
			t.Fatalf("browser credentials accepted outside web origin: %s %d", path, response.Code)
		}
	}
}
