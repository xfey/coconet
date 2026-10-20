package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"golang.org/x/oauth2"
)

func TestLoginTwoDevicesConnectSameProjectAndLogoutLocally(t *testing.T) {
	for _, prefix := range []string{"", "/teams/demo"} {
		t.Run(prefix, func(t *testing.T) { testTwoDevicesLogin(t, prefix) })
	}
}

func testTwoDevicesLogin(t *testing.T, prefix string) {
	remote := newFakeCoconetServer()
	var mu sync.Mutex
	challenges := map[string]string{}
	setups := map[string]bool{}
	directories := map[string]*contracts.ConnectionDirectory{}
	tokens := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		if r.URL.Path == "/v1/deployment" {
			recorder := httptest.NewRecorder()
			serveFixtureDeployment(recorder, r)
			var info contracts.DeploymentInfo
			_ = json.Unmarshal(recorder.Body.Bytes(), &info)
			info.Capabilities = []string{contracts.DeploymentCapabilityAccountLogin, contracts.DeploymentCapabilityDirectorySetup, contracts.DeploymentCapabilityConnections, contracts.DeploymentCapabilityProjectSetup, contracts.DeploymentCapabilityLocalDAG, contracts.DeploymentCapabilityRelay}
			slices.Sort(info.Capabilities)
			info.CapabilitiesSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixtureDeploymentPrivateKey, contracts.DeploymentCapabilitiesSigningPayload(info)))
			_ = json.NewEncoder(w).Encode(info)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			mu.Lock()
			defer mu.Unlock()
			switch r.URL.Path {
			case "/v1/auth/device":
				var input contracts.DeviceAuthorizationRequest
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input.CallbackURL != "" {
					t.Error("device authorization must stay in the Dashboard")
				}
				code := newRequestID()
				challenges[code] = input.CodeChallenge
				setups[code] = input.ConnectProject
				directories[code] = input.Directory
				if input.ConnectProject && input.ProjectName == "" {
					t.Error("setup omitted folder name suggestion")
				}
				_ = json.NewEncoder(w).Encode(contracts.DeviceAuthorization{Status: contracts.StatusPending, DeviceCode: code, AuthorizationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", VerificationURI: server.URL + prefix + "/#authorize=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Interval: 1, ExpiresIn: 60})
			case "/v1/auth/token":
				var input contracts.DeviceAuthorizationPoll
				_ = json.NewDecoder(r.Body).Decode(&input)
				if challenges[input.DeviceCode] != oauth2.S256ChallengeFromVerifier(input.CodeVerifier) {
					http.Error(w, "wrong verifier", 400)
					return
				}
				delete(challenges, input.DeviceCode)
				token := "device-token-" + newRequestID() + "-fixture"
				tokens[token] = true
				projectID := ""
				if setups[input.DeviceCode] {
					projectID = remote.project.ProjectID
				}
				_ = json.NewEncoder(w).Encode(contracts.DeviceAuthorization{Status: contracts.StatusOK, AccessToken: token, Account: &remote.account, ProjectID: projectID, Directory: directories[input.DeviceCode]})
			case "/v1/auth/logout":
				delete(tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				w.WriteHeader(204)
			}
			return
		}
		remote.ServeHTTP(w, r)
	}))
	defer server.Close()
	endpoint := server.URL + prefix
	remote.advertisedEndpoint = endpoint
	var paths [2]runtimePaths
	var credentials [2]storedCredential
	var bindings [2]*contracts.LocalBinding
	for i := range paths {
		directory := t.TempDir()
		setRuntimeTestHome(t, filepath.Join(directory, "runtime"))
		t.Setenv("COCONET_CWD", directory)
		setRuntimeTestServer(t, endpoint)
		t.Setenv("COCONET_DISABLE_BACKGROUND_SYNC", "1")
		t.Setenv("COCONET_CODEX_COMMAND", "coconet-test-no-codex")
		t.Setenv("COCONET_CLAUDE_NODE", "coconet-test-no-claude")
		var err error
		paths[i], err = resolveRuntimePaths()
		if err != nil {
			t.Fatal(err)
		}
		var output, stderr bytes.Buffer
		args := []string{"--no-browser"}
		if i == 0 {
			credentials[i], err = loginToDeployment(context.Background(), paths[i], fixtureDeploymentTarget(endpoint), true, &output)
			if err != nil {
				t.Fatal("login failed", err)
			}
			args = append(args, "--new")
		}
		if code := runInit(context.Background(), args, strings.NewReader(""), &output, &stderr); code != 0 {
			t.Fatal("connect failed", stderr.String())
		}
		credentials[i], _, err = loadCredential(paths[i], fixtureDeploymentTarget(endpoint))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), credentials[i].AccessToken) {
			t.Fatal("CLI leaked device credential")
		}
		if i == 1 && (strings.Contains(output.String(), "  Code:") || strings.Contains(output.String(), "Choose a project in your browser") || strings.Contains(output.String(), "Server:") || strings.Contains(output.String(), "Dashboard")) {
			t.Fatal("default connect did not use concise browser authorization output", output.String())
		}
		bindings[i], err = bindingAtRoot(paths[i], directory)
		if err != nil || bindings[i] == nil {
			t.Fatal("missing local binding", err)
		}
	}
	if credentials[0].AccessToken == credentials[1].AccessToken || bindings[0].AccountID != bindings[1].AccountID || bindings[0].ProjectID != bindings[1].ProjectID || remote.connectRequests != 0 || remote.createTransactions != 1 {
		t.Fatal("same-account project connection required invitation or created duplicates")
	}
	var output, stderr bytes.Buffer
	if err := logoutDeployment(context.Background(), paths[0], fixtureDeploymentTarget(endpoint), &output, &stderr); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	firstValid, secondValid := tokens[credentials[0].AccessToken], tokens[credentials[1].AccessToken]
	mu.Unlock()
	if firstValid || !secondValid {
		t.Fatal("logout affected another device")
	}
	if _, found, err := loadCredential(paths[0], fixtureDeploymentTarget(endpoint)); err != nil || found {
		t.Fatal("logged-out device retained credentials")
	}
	if binding, err := bindingAtRoot(paths[0], bindings[0].CollaborationRoot); err != nil || binding == nil {
		t.Fatal("logout removed project binding")
	}
	if _, err := os.Stat(paths[1].credentialPath); err != nil {
		t.Fatal("other device state changed", err)
	}
}

func TestDeviceAuthorizationBrowserLaunchAndFallback(t *testing.T) {
	for _, test := range []struct {
		name, expected string
		remote, failed bool
	}{
		{name: "desktop", expected: "  Opening your browser...\n"},
		{name: "failed", failed: true, expected: "Could not open your browser."},
		{name: "remote", remote: true, expected: "Open this link in your browser:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := contracts.DeviceAuthorization{AuthorizationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", VerificationURI: "https://fixture.test/#authorize=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
			calls := 0
			var output bytes.Buffer
			showDeviceAuthorization(context.Background(), &output, started, "test-device", "", test.remote, func(ctx context.Context, address string) error {
				calls++
				if address != started.VerificationURI {
					t.Fatal("browser received a different authorization")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("browser launch has no short timeout")
				}
				if test.failed {
					return errors.New("no desktop opener")
				}
				return nil
			})
			if calls != 1 && !test.remote || calls != 0 && test.remote {
				t.Fatal("unexpected browser launch count", calls)
			}
			for _, text := range []string{test.expected, "Connection Information\n  Device: test-device\nBrowser Confirmation\n", "  " + started.VerificationURI + "\n"} {
				if !strings.Contains(output.String(), text) {
					t.Fatal("missing authorization guidance", text, output.String())
				}
			}
			if strings.Contains(output.String(), "Waiting for") || strings.Contains(output.String(), "If the browser") || strings.Contains(output.String(), "\n\n") {
				t.Fatal("authorization output contains redundant guidance", output.String())
			}
			if strings.Contains(output.String(), "\x1b[") {
				t.Fatal("redirected output contains terminal styling")
			}
		})
	}
}

func TestLoginAccountSwitchPreservesExistingLocalOwnership(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	defer server.Close()
	root, paths := configureBoundRepository(t, remote, server.URL)
	target := fixtureDeploymentTarget(server.URL)
	before, err := os.ReadFile(paths.credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	other := fixtureAccount("acct_otherlogin", "Another user")
	if _, err := persistLogin(context.Background(), paths, target, other, "another-device-token-with-enough-entropy"); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatal("login replaced a locally bound account", err)
	}
	after, err := os.ReadFile(paths.credentialPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected account switch changed credentials", err)
	}
	binding, err := bindingAtRoot(paths, root)
	if err != nil || binding == nil || binding.AccountID != remote.account.AccountID {
		t.Fatal("rejected account switch changed session ownership", err)
	}
}
