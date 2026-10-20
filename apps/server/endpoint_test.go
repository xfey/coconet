package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"golang.org/x/oauth2"
)

func TestPrefixedDeploymentRoundTrip(t *testing.T) {
	for _, mode := range []string{"direct", "preserved", "stripped"} {
		t.Run(mode, func(t *testing.T) {
			_, h, actor, project, key := dashboardFixture(t)
			prefix := "/teams/demo"
			h.addressResolver, _ = parseTrustedProxyCIDRs("127.0.0.1/32,::1/128")
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, prefix+"/") {
					http.NotFound(w, r)
					return
				}
				if mode != "direct" {
					r.Header.Set("X-Forwarded-Host", r.Host)
					r.Header.Set("X-Forwarded-Proto", "https")
					r.Host, r.TLS = "127.0.0.1:7788", nil
					if mode == "stripped" {
						r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
					}
				}
				h.ServeHTTP(w, r)
			}))
			defer server.Close()
			h.advertisedEndpoint, h.webOrigin = server.URL+prefix, server.URL+prefix
			client := server.Client()
			client.Jar, _ = cookiejar.New(nil)
			call := func(method, path string, input any, origin string, want int) (*http.Response, []byte) {
				t.Helper()
				data, _ := json.Marshal(input)
				r, _ := http.NewRequest(method, server.URL+prefix+path, bytes.NewReader(data))
				r.Header.Set("Origin", origin)
				r.Header.Set("Content-Type", "application/json")
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, _ := io.ReadAll(response.Body)
				if response.StatusCode != want {
					t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, body)
				}
				return response, body
			}
			_, page := call("GET", "/"+project.ProjectID, nil, "", 200)
			for _, expected := range []string{`href="` + prefix + `/assets/coconet-logo.svg"`, `src="` + prefix + `/assets/app.mjs"`, `name="coconet-base-path" content="` + prefix + `"`, `content="` + server.URL + prefix + `"`} {
				if !bytes.Contains(page, []byte(expected)) {
					t.Fatalf("page missing %s", expected)
				}
			}
			for _, path := range []string{"/assets/app.mjs", "/assets/endpoint.mjs", "/assets/style.css", "/assets/" + dashboardBrandFontAsset, "/healthz", "/v1/deployment?challenge=prefix_fixture_12345678"} {
				call("GET", path, nil, "", 200)
			}
			input := contracts.DashboardLoginRequest{SchemaVersion: 1, AccountKey: key}
			for _, bad := range []string{"", "https://evil.test", server.URL + prefix} {
				call("POST", "/v1/dashboard/session", input, bad, 403)
			}
			login, _ := call("POST", "/v1/dashboard/session", input, server.URL, 200)
			cookies := login.Cookies()
			if len(cookies) != 1 || cookies[0].Path != prefix+"/" || !strings.HasPrefix(cookies[0].Name, "__Secure-") || !cookies[0].Secure {
				t.Fatalf("wrong cookie scope: %#v", cookies)
			}
			other, _ := url.Parse(server.URL + "/teams/other/v1/dashboard/session")
			if len(client.Jar.Cookies(other)) != 0 {
				t.Fatal("credential leaked to another prefix")
			}
			call("GET", "/v1/dashboard/projects", nil, "", 200)
			call("POST", "/v1/dashboard/projects", contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "prefix-create", Name: "Second project"}, server.URL, 201)
			_, body := call("POST", "/v1/auth/device", contracts.DeviceAuthorizationRequest{DeviceName: "Prefix test", CodeChallenge: oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())}, "", 200)
			var authorization contracts.DeviceAuthorization
			if json.Unmarshal(body, &authorization) != nil || !strings.HasPrefix(authorization.VerificationURI, server.URL+prefix+"/#authorize=") {
				t.Fatalf("wrong browser URI: %s", body)
			}
			// Exercise actual capture, object transfers and replication through the prefix.
			store, err := graphstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			remote := replication.HTTP{BaseURL: server.URL + prefix, Token: key, Client: client}
			producer := &replication.Client{Store: store, Remote: remote, Account: actor.AccountID, Device: "prefix_device"}
			engine := &localdag.Engine{Store: store, Publisher: producer, Actor: producer.Writer()}
			source, err := engine.Capture(context.Background(), localdag.Capture{ProjectID: project.ProjectID, Agent: "claude", NativeID: "prefix-native", Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0, Role: "user", Kind: "message", Text: "Validate subpath collaboration"}}}, Artifact: []byte("synthetic artifact")})
			if err != nil {
				t.Fatal(err)
			}
			replica, err := graphstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer replica.Close()
			consumer := &replication.Client{Store: replica, Remote: remote, Account: actor.AccountID, Device: "second_device"}
			if err := consumer.Sync(context.Background(), project.ProjectID); err != nil {
				t.Fatal(err)
			}
			state, err := replica.State(project.ProjectID)
			if err != nil || state.Sessions[source.SessionID].Latest != source.Version {
				t.Fatalf("replica did not receive source: %v", err)
			}
			call("DELETE", "/v1/dashboard/session", nil, server.URL, 204)
			call("GET", "/v1/dashboard/session", nil, "", 401)
		})
	}
}

func TestForwardedBrowserOriginRequiresTrustedProxy(t *testing.T) {
	_, h, _, _, key := dashboardFixture(t)
	h.webOrigin, h.advertisedEndpoint = "https://public.test/service", "https://public.test/service"
	h.addressResolver, _ = parseTrustedProxyCIDRs("127.0.0.1/32")
	for _, test := range []struct {
		peer, host, proto string
		want              int
	}{
		{"203.0.113.8:4000", "public.test", "https", 403},
		{"127.0.0.1:4000", "evil.test", "https", 403},
		{"127.0.0.1:4000", "public.test, evil.test", "https", 403},
		{"127.0.0.1:4000", "public.test", "http", 403},
		{"127.0.0.1:4000", "public.test", "https", 200},
	} {
		request := httptest.NewRequest("POST", "http://127.0.0.1:7788/v1/dashboard/session", strings.NewReader(`{"schema_version":1,"account_key":"`+key+`"}`))
		request.RemoteAddr = test.peer
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://public.test")
		request.Header.Set("X-Forwarded-Host", test.host)
		request.Header.Set("X-Forwarded-Proto", test.proto)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%+v: %d %s", test, response.Code, response.Body.String())
		}
	}
}
