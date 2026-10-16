package main

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestUnifiedLoginBrowserHarness(t *testing.T) {
	file := os.Getenv("COCONET_ACCOUNT_BROWSER_FIXTURE")
	if file == "" {
		t.Skip("isolated browser harness is opt-in")
	}
	h := newTestHandler(t).(*serverHandler)
	server := httptest.NewTLSServer(h)
	defer server.Close()
	var mu sync.Mutex
	type fixtureAuthorization struct {
		challenge string
		identity  string
	}
	codes := map[string]fixtureAuthorization{}
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/authorize":
			if r.URL.Query().Get("redirect_uri") != server.URL+"/v1/auth/callback" || r.URL.Query().Get("code_challenge_method") != "S256" {
				http.Error(w, "invalid client", 400)
				return
			}
			code, _ := randomSecret(24)
			mu.Lock()
			identity, _ := r.Cookie("fixture_identity")
			codes[code] = fixtureAuthorization{r.URL.Query().Get("code_challenge"), fixtureIdentity(identity)}
			mu.Unlock()
			http.Redirect(w, r, server.URL+"/v1/auth/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(r.URL.Query().Get("state")), 303)
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			authorization := codes[r.Form.Get("code")]
			delete(codes, r.Form.Get("code"))
			mu.Unlock()
			if authorization.challenge == "" || authorization.challenge != pkceChallenge(r.Form.Get("code_verifier")) {
				http.Error(w, "invalid grant", 400)
				return
			}
			token := "isolated-provider-token"
			if authorization.identity != "" {
				token += "-" + authorization.identity
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "token_type": "bearer"})
		case "/user":
			if r.Header.Get("Authorization") == "Bearer isolated-provider-token-teammate" {
				_, _ = w.Write([]byte(`{"id":43,"login":"teammate","name":"Browser teammate"}`))
				return
			}
			for index, identity := range []string{"member-a", "member-b", "newcomer"} {
				if r.Header.Get("Authorization") == "Bearer isolated-provider-token-"+identity {
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 44 + index, "login": identity, "name": identity})
					return
				}
			}

			if r.Header.Get("Authorization") != "Bearer isolated-provider-token" {
				http.Error(w, "invalid token", 401)
				return
			}
			_, _ = w.Write([]byte(`{"id":42,"login":"fixture","name":"Browser developer"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	h.advertisedEndpoint = server.URL
	h.auth = &loginProvider{Kind: "github", Label: "GitHub", Issuer: idp.URL, HTTP: idp.Client(), UserURL: idp.URL + "/user", OAuth: oauth2.Config{ClientID: "fixture-client", ClientSecret: "fixture-secret", RedirectURL: server.URL + "/v1/auth/callback", Endpoint: oauth2.Endpoint{AuthURL: idp.URL + "/authorize", TokenURL: idp.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}}
	// A separate owned loopback listener lets the real CLI exercise this same
	// authority without installing the fixture TLS certificate into the OS.
	cliHandler := *h
	cliServer := httptest.NewServer(&cliHandler)
	defer cliServer.Close()
	cliHandler.advertisedEndpoint = cliServer.URL
	h.advertisedEndpoint = cliServer.URL
	h.webOrigin, cliHandler.webOrigin = server.URL, server.URL
	certificate := file + ".crt"
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"url": server.URL, "cli_url": cliServer.URL, "certificate": certificate})
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(file + ".stop"); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("browser harness timed out")
}

// Only the isolated provider reads this cookie; production authentication is unchanged.
func fixtureIdentity(cookie *http.Cookie) string {
	if cookie != nil {
		switch cookie.Value {
		case "teammate", "member-a", "member-b", "newcomer":
			return cookie.Value
		}
	}
	return ""
}
