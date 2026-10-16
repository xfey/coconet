package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
)

func TestDiscoverDeploymentRejectsTamperedSignature(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		publicKey := privateKey.Public().(ed25519.PublicKey)
		fingerprint := sha256.Sum256(publicKey)
		info := contracts.DeploymentInfo{
			SchemaVersion: contracts.SchemaVersion, ProtocolVersion: contracts.DeploymentProtocolVersion,
			DeploymentID: "dpl_tampered01", ServerVersion: "test",
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), PublicKeySHA256: hex.EncodeToString(fingerprint[:]),
			Challenge: request.URL.Query().Get("challenge"), Signature: base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
		}
		_ = json.NewEncoder(response).Encode(info)
	}))
	t.Cleanup(server.Close)
	if _, err := discoverDeployment(context.Background(), server.URL); err == nil {
		t.Fatal("tampered Deployment signature was accepted")
	}
}

func TestDiscoverDeploymentRequiresSignedConnectionCapability(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(capabilities []string, signCapabilities bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			publicKey := privateKey.Public().(ed25519.PublicKey)
			fingerprint := sha256.Sum256(publicKey)
			info := contracts.DeploymentInfo{
				SchemaVersion: contracts.SchemaVersion, ProtocolVersion: contracts.DeploymentProtocolVersion,
				DeploymentID: "dpl_capability01", ServerVersion: "test",
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), PublicKeySHA256: hex.EncodeToString(fingerprint[:]),
				Challenge: request.URL.Query().Get("challenge"), Capabilities: capabilities,
			}
			info.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, contracts.DeploymentSigningPayload(info)))
			if signCapabilities {
				info.CapabilitiesSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, contracts.DeploymentCapabilitiesSigningPayload(info)))
			}
			_ = json.NewEncoder(response).Encode(info)
		}))
	}

	for name, fixture := range map[string]struct {
		capabilities []string
		signed       bool
		errorCode    string
	}{
		"old server":        {nil, false, "server_upgrade_required"},
		"unsigned claim":    {[]string{contracts.DeploymentCapabilityConnections}, false, "server_upgrade_required"},
		"missing feature":   {[]string{"session_content_v1"}, true, "server_upgrade_required"},
		"unsorted features": {[]string{contracts.DeploymentCapabilityConnections, "account_bootstrap_v1"}, true, "deployment_capabilities_invalid"},
		"invalid feature":   {[]string{"project\nconnections"}, true, "deployment_capabilities_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			server := serve(fixture.capabilities, fixture.signed)
			defer server.Close()
			_, err := discoverDeployment(context.Background(), server.URL)
			if err == nil || observability.Describe(err).Code != fixture.errorCode {
				t.Fatalf("unexpected capability result: %v", err)
			}
		})
	}
}

func TestAuthenticatedClientRejectsChangedDeploymentBeforeSendingCredential(t *testing.T) {
	accountRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveFixtureDeployment(response, request) {
			return
		}
		if request.URL.Path == "/v1/account" {
			accountRequests++
		}
	}))
	t.Cleanup(server.Close)
	pinned := fixtureDeploymentTarget(server.URL)
	pinned.DeploymentID = "dpl_different01"
	if _, err := authenticatedClientForDeployment(context.Background(), pinned, "sensitive-token"); err == nil {
		t.Fatal("changed Deployment identity was accepted")
	}
	if accountRequests != 0 {
		t.Fatalf("credential was sent before Deployment verification: %d", accountRequests)
	}
}
