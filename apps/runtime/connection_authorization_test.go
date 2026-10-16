package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"golang.org/x/oauth2"
)

func TestCLIFolderChooserRoundTrip(t *testing.T) {
	for _, scenario := range []string{"selected", "cancelled", "already_connected", "mismatched-grant"} {
		t.Run(scenario, func(t *testing.T) {
			remote := newFakeCoconetServer()
			var mu sync.Mutex
			var directory contracts.ConnectionDirectory
			var challenge string
			updated, revoked := false, false
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/v1/deployment":
					recorder := httptest.NewRecorder()
					serveFixtureDeployment(recorder, r)
					var info contracts.DeploymentInfo
					_ = json.Unmarshal(recorder.Body.Bytes(), &info)
					info.Capabilities = []string{contracts.DeploymentCapabilityAccountLogin, contracts.DeploymentCapabilityDirectorySetup, contracts.DeploymentCapabilityConnections, contracts.DeploymentCapabilityProjectSetup, contracts.DeploymentCapabilityLocalDAG, contracts.DeploymentCapabilityRelay}
					slices.Sort(info.Capabilities)
					info.CapabilitiesSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixtureDeploymentPrivateKey, contracts.DeploymentCapabilitiesSigningPayload(info)))
					_ = json.NewEncoder(w).Encode(info)
				case "/v1/auth/device":
					var input contracts.DeviceAuthorizationRequest
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input.Directory == nil {
						t.Error("missing requested folder")
						http.Error(w, "missing directory", 400)
						return
					}
					directory, challenge = *input.Directory, input.CodeChallenge
					_ = json.NewEncoder(w).Encode(contracts.DeviceAuthorization{Status: contracts.StatusPending, DeviceCode: "fixture-device-code", AuthorizationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", VerificationURI: server.URL + "/#authorize=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Interval: 1, ExpiresIn: 60})
				case "/v1/auth/token":
					var input contracts.DeviceAuthorizationPoll
					_ = json.NewDecoder(r.Body).Decode(&input)
					if oauth2.S256ChallengeFromVerifier(input.CodeVerifier) != challenge {
						t.Error("wrong verifier")
						http.Error(w, "verifier", 400)
						return
					}
					if !updated {
						_ = json.NewEncoder(w).Encode(contracts.DeviceAuthorization{Status: contracts.StatusPending, DirectoryRequestID: "folder-fixture"})
						return
					}
					grant := directory
					if scenario == "mismatched-grant" {
						grant.Path = "/unexpected"
					}
					_ = json.NewEncoder(w).Encode(contracts.DeviceAuthorization{Status: contracts.StatusOK, AccessToken: "new-device-credential-0123456789-fixture", Account: &remote.account, ProjectID: remote.project.ProjectID, Directory: &grant})
				case "/v1/auth/directory":
					var input contracts.DeviceDirectoryUpdate
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input.RequestID != "folder-fixture" || oauth2.S256ChallengeFromVerifier(input.CodeVerifier) != challenge {
						t.Error("unbound chooser update")
					}
					if scenario == "cancelled" || scenario == "already_connected" {
						if input.Error != scenario || input.Path != "" {
							t.Error("failed chooser changed the directory")
						}
					} else {
						directory.CWD, directory.Path = input.CWD, input.Path
					}
					directory.Revision++
					updated = true
					w.WriteHeader(204)
				case "/v1/auth/logout":
					revoked = true
					w.WriteHeader(204)
				default:
					remote.ServeHTTP(w, r)
				}
			}))
			t.Cleanup(server.Close)
			root, selected := t.TempDir(), t.TempDir()
			configureRuntimeTest(t, t.TempDir(), root, server.URL)
			t.Setenv("SSH_CONNECTION", "")
			t.Setenv("SSH_TTY", "")
			// Browser launching is an OS boundary, independently of the chooser.
			bin := t.TempDir()
			for _, name := range []string{"open", "xdg-open"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			paths, _ := resolveRuntimePaths()
			if scenario == "already_connected" {
				if err := saveBinding(paths, fixtureBinding(selected, remote.project.ProjectID, remote.account.AccountID, fixtureDeploymentTarget(server.URL))); err != nil {
					t.Fatal(err)
				}
			}
			original, _ := os.ReadFile(paths.credentialPath)
			input, _, _, err := connectionDirectory(paths, root, false)
			if err != nil {
				t.Fatal(err)
			}
			input.CanBrowse = true
			calls := 0
			chooser := func(ctx context.Context, cwd string) (string, error) {
				calls++
				if cwd != input.CWD {
					t.Error("chooser started from wrong directory")
				}
				if scenario == "cancelled" {
					return "", errFolderSelectionCancelled
				}
				return selected, nil
			}
			var output bytes.Buffer
			result, err := authorizeDeploymentWithChooser(context.Background(), paths, fixtureDeploymentTarget(server.URL), false, &output, contracts.DeviceAuthorizationRequest{ConnectProject: true, Directory: &input}, chooser)
			if calls != 1 {
				t.Fatal("wrong number of chooser calls", calls, err)
			}
			mu.Lock()
			wasRevoked := revoked
			mu.Unlock()
			if scenario == "mismatched-grant" {
				after, _ := os.ReadFile(paths.credentialPath)
				if err == nil || !wasRevoked || !bytes.Equal(original, after) {
					t.Fatal("mismatched grant changed credentials", err)
				}
				return
			}
			expected := selected
			if scenario == "cancelled" || scenario == "already_connected" {
				expected = root
			}
			expected, _ = canonicalPath(expected)
			if err != nil || result.directory == nil || result.directory.Path != expected || result.directory.Revision != 2 {
				t.Fatal("incorrect folder grant", err)
			}
			if bytes.Contains(output.Bytes(), []byte(result.credential.AccessToken)) {
				t.Fatal("CLI output contains device credential")
			}
		})
	}
}
