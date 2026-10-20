package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
	"github.com/xfey/coconet/packages/projectlink"
)

var (
	runtimeDeploymentIDPattern  = regexp.MustCompile(`^dpl_[A-Za-z0-9_-]{8,}$`)
	publicKeyFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type deploymentTarget struct {
	Capabilities          []string
	DeploymentID          string
	ServerURL             string
	ServerPublicKeySHA256 string
	ServerVersion         string
}

func discoverDeployment(ctx context.Context, serverURL string) (deploymentTarget, error) {
	client, err := newAPIClient(serverURL, "")
	if err != nil {
		return deploymentTarget{}, err
	}
	challengeBytes := make([]byte, 24)
	if _, err := rand.Read(challengeBytes); err != nil {
		return deploymentTarget{}, fmt.Errorf("create Deployment challenge: %w", err)
	}
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes)
	var info contracts.DeploymentInfo
	if err := client.do(ctx, "GET", "/v1/deployment?challenge="+url.QueryEscape(challenge), nil, &info, false); err != nil {
		return deploymentTarget{}, err
	}
	if info.SchemaVersion != contracts.SchemaVersion || info.ProtocolVersion != contracts.DeploymentProtocolVersion || info.Challenge != challenge || !runtimeDeploymentIDPattern.MatchString(info.DeploymentID) || strings.TrimSpace(info.ServerVersion) == "" {
		return deploymentTarget{}, observability.Wrap(errors.New("Coconet Server returned an invalid Deployment handshake"), contracts.FailureDeploymentIdentity, "deployment_handshake_invalid", false)
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(info.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return deploymentTarget{}, observability.Wrap(errors.New("Coconet Server returned an invalid Deployment public key"), contracts.FailureDeploymentIdentity, "deployment_public_key_invalid", false)
	}
	fingerprint := sha256.Sum256(publicKey)
	encodedFingerprint := hex.EncodeToString(fingerprint[:])
	if info.PublicKeySHA256 != encodedFingerprint {
		return deploymentTarget{}, observability.Wrap(errors.New("Coconet Server returned an inconsistent Deployment public-key fingerprint"), contracts.FailureDeploymentIdentity, "deployment_fingerprint_mismatch", false)
	}
	signature, err := base64.RawURLEncoding.DecodeString(info.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), contracts.DeploymentSigningPayload(info), signature) {
		return deploymentTarget{}, observability.Wrap(errors.New("Coconet Server Deployment signature verification failed"), contracts.FailureDeploymentIdentity, "deployment_signature_invalid", false)
	}
	if err := verifyDeploymentCapabilities(info, ed25519.PublicKey(publicKey)); err != nil {
		return deploymentTarget{}, err
	}
	normalizedURL, err := normalizeServerURL(serverURL)
	if err != nil {
		return deploymentTarget{}, err
	}
	return deploymentTarget{
		DeploymentID:          info.DeploymentID,
		ServerURL:             normalizedURL,
		ServerPublicKeySHA256: encodedFingerprint,
		ServerVersion:         info.ServerVersion,
		Capabilities:          append([]string(nil), info.Capabilities...),
	}, nil
}

func verifyDeploymentCapabilities(info contracts.DeploymentInfo, publicKey ed25519.PublicKey) error {
	if len(info.Capabilities) == 0 || len(info.Capabilities) > 64 {
		return serverUpgradeRequired()
	}
	foundConnections := false
	previous := ""
	for _, capability := range info.Capabilities {
		if !validDeploymentCapability(capability) || capability <= previous {
			return observability.Wrap(errors.New("Coconet Server returned invalid signed capabilities"), contracts.FailureDeploymentIdentity, "deployment_capabilities_invalid", false)
		}
		previous = capability
		if capability == contracts.DeploymentCapabilityConnections {
			foundConnections = true
		}
	}
	signature, err := base64.RawURLEncoding.DecodeString(info.CapabilitiesSignature)
	if err != nil || !ed25519.Verify(publicKey, contracts.DeploymentCapabilitiesSigningPayload(info), signature) {
		if info.CapabilitiesSignature == "" {
			return serverUpgradeRequired()
		}
		return observability.Wrap(errors.New("Coconet Server capability signature verification failed"), contracts.FailureDeploymentIdentity, "deployment_capabilities_signature_invalid", false)
	}
	if !foundConnections {
		return serverUpgradeRequired()
	}
	return nil
}

func validDeploymentCapability(capability string) bool {
	if capability == "" || len(capability) > 64 || capability[0] < 'a' || capability[0] > 'z' {
		return false
	}
	for index := 1; index < len(capability); index++ {
		character := capability[index]
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func serverUpgradeRequired() error {
	return observability.Wrap(errors.New("Coconet Server must be upgraded before it can use lightweight Project connections"), contracts.FailureServerInternal, "server_upgrade_required", false)
}

func validateDeploymentTarget(target deploymentTarget) error {
	if !runtimeDeploymentIDPattern.MatchString(target.DeploymentID) || !publicKeyFingerprintPattern.MatchString(target.ServerPublicKeySHA256) {
		return errors.New("Deployment identity is invalid")
	}
	normalizedURL, err := normalizeServerURL(target.ServerURL)
	if err != nil || normalizedURL != target.ServerURL {
		return errors.New("Deployment Server URL is invalid or not normalized")
	}
	return nil
}

func normalizeServerURL(value string) (string, error) {
	normalized, err := projectlink.ParseServerEndpoint(strings.TrimSpace(value), true)
	if err != nil {
		return "", fmt.Errorf("invalid Coconet Server URL: %w", err)
	}
	return normalized, nil
}

func sameDeploymentTarget(left, right deploymentTarget) bool {
	return left.DeploymentID == right.DeploymentID && left.ServerURL == right.ServerURL && left.ServerPublicKeySHA256 == right.ServerPublicKeySHA256
}

func authenticatedClientForDeployment(ctx context.Context, target deploymentTarget, accessToken string) (*apiClient, error) {
	observed, err := discoverDeployment(ctx, target.ServerURL)
	if err != nil {
		return nil, err
	}
	if !sameDeploymentTarget(target, observed) {
		return nil, observability.Wrap(errors.New("Coconet Server Deployment ID or public key differs from the pinned identity"), contracts.FailureDeploymentIdentity, "deployment_identity_mismatch", false)
	}
	client, err := newAPIClient(target.ServerURL, accessToken)
	if err == nil {
		client.capabilities = observed.Capabilities
	}
	return client, err
}
