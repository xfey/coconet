package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/xfey/coconet/packages/contracts"
)

const fixtureDeploymentID = "dpl_fixture01"

var fixtureDeploymentPrivateKey = func() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("coconet-runtime-test-deployment"))
	return ed25519.NewKeyFromSeed(seed[:])
}()

var alternateFixtureDeploymentPrivateKey = func() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("coconet-runtime-test-alternate-deployment"))
	return ed25519.NewKeyFromSeed(seed[:])
}()

func fixtureDeploymentTarget(serverURL string) deploymentTarget {
	publicKey := fixtureDeploymentPrivateKey.Public().(ed25519.PublicKey)
	fingerprint := sha256.Sum256(publicKey)
	normalized, err := normalizeServerURL(serverURL)
	if err != nil {
		panic(err)
	}
	return deploymentTarget{
		DeploymentID:          fixtureDeploymentID,
		ServerURL:             normalized,
		ServerPublicKeySHA256: hex.EncodeToString(fingerprint[:]),
		ServerVersion:         "test",
	}
}

func serveFixtureDeployment(response http.ResponseWriter, request *http.Request) bool {
	return serveFixtureDeploymentIdentity(response, request, fixtureDeploymentID, fixtureDeploymentPrivateKey)
}

func serveAlternateFixtureDeployment(response http.ResponseWriter, request *http.Request) bool {
	return serveFixtureDeploymentIdentity(response, request, "dpl_alternate01", alternateFixtureDeploymentPrivateKey)
}

func serveFixtureDeploymentIdentity(response http.ResponseWriter, request *http.Request, deploymentID string, privateKey ed25519.PrivateKey) bool {
	if request.URL.Path != "/v1/deployment" {
		return false
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	fingerprint := sha256.Sum256(publicKey)
	info := contracts.DeploymentInfo{
		SchemaVersion: contracts.SchemaVersion, ProtocolVersion: contracts.DeploymentProtocolVersion,
		DeploymentID: deploymentID, ServerVersion: "test",
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), PublicKeySHA256: hex.EncodeToString(fingerprint[:]),
		Challenge:    request.URL.Query().Get("challenge"),
		Capabilities: []string{contracts.DeploymentCapabilityAccountLogin, contracts.DeploymentCapabilityConnections, contracts.DeploymentCapabilityDirectorySetup, contracts.DeploymentCapabilityLocalDAG, contracts.DeploymentCapabilityProjectSetup, contracts.DeploymentCapabilityRelay},
	}
	info.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, contracts.DeploymentSigningPayload(info)))
	slices.Sort(info.Capabilities)
	info.CapabilitiesSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, contracts.DeploymentCapabilitiesSigningPayload(info)))
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(info)
	return true
}
