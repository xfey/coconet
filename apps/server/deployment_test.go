package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestDeploymentIdentityPersistsAndSignsChallenge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator", "deployment.json")
	first, err := loadOrCreateDeploymentIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateDeploymentIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.deploymentID != second.deploymentID || !first.privateKey.Equal(second.privateKey) {
		t.Fatal("Deployment identity changed after reload")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected identity permissions: %v %v", info, err)
	}

	challenge := "test_challenge_1234567890"
	info, err := second.info("0.5.0", challenge)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(info.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(publicKey)
	signature, err := base64.RawURLEncoding.DecodeString(info.Signature)
	if err != nil {
		t.Fatal(err)
	}
	capabilitySignature, capabilityErr := base64.RawURLEncoding.DecodeString(info.CapabilitiesSignature)
	if info.PublicKeySHA256 != hex.EncodeToString(fingerprint[:]) || !ed25519.Verify(publicKey, contracts.DeploymentSigningPayload(info), signature) ||
		capabilityErr != nil || !slices.Equal(info.Capabilities, []string{contracts.DeploymentCapabilityAccountLogin, contracts.DeploymentCapabilityAtomicSnapshots, contracts.DeploymentCapabilityDirectorySetup, contracts.DeploymentCapabilityNativeLineage, contracts.DeploymentCapabilityConnections, contracts.DeploymentCapabilityProjectSetup, "session_artifact_gzip_v2", contracts.DeploymentCapabilityLibraryBrowse, contracts.DeploymentCapabilityLibraryCheckpoint, contracts.DeploymentCapabilityLibraryTags, contracts.DeploymentCapabilitySessionLibrary, contracts.DeploymentCapabilityVisibleText, contracts.DeploymentCapabilityWorkspaceRegistry}) ||
		!ed25519.Verify(publicKey, contracts.DeploymentCapabilitiesSigningPayload(info), capabilitySignature) {
		t.Fatal("Deployment signature or public-key fingerprint is invalid")
	}
}

func TestDeploymentRouteBindsCallerChallenge(t *testing.T) {
	handler := newTestHandler(t)
	challenge := "route_challenge_123456789"
	request := httptest.NewRequest(http.MethodGet, "/v1/deployment?challenge="+challenge, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", response.Code)
	}
	var info contracts.DeploymentInfo
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Challenge != challenge || info.ProtocolVersion != contracts.DeploymentProtocolVersion || !deploymentIDPattern.MatchString(info.DeploymentID) {
		t.Fatalf("unexpected Deployment handshake %#v", info)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/deployment?challenge=short", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid challenge returned %d", response.Code)
	}
}

func TestRelayAlwaysAdvertisesLocalDAGWorkflow(t *testing.T) {
	for _, setting := range []string{"", "1"} {
		t.Setenv("COCONET_LOCAL_DAG_DEFAULT", setting)
		handler := newTestHandler(t)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/deployment?challenge=local_dag_capability_123456789", nil))
		var info contracts.DeploymentInfo
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &info) != nil {
			t.Fatal("handshake failed")
		}
		if !slices.Contains(info.Capabilities, contracts.DeploymentCapabilityRelay) || !slices.Contains(info.Capabilities, contracts.DeploymentCapabilityLocalDAG) {
			t.Fatal(info.Capabilities)
		}
		publicKey, _ := base64.RawURLEncoding.DecodeString(info.PublicKey)
		signature, _ := base64.RawURLEncoding.DecodeString(info.CapabilitiesSignature)
		if !ed25519.Verify(publicKey, contracts.DeploymentCapabilitiesSigningPayload(info), signature) {
			t.Fatal("workflow capability is unsigned")
		}
	}
}
