package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/sessionartifact"
)

const deploymentIdentitySchemaVersion = 1

var (
	deploymentIDPattern        = regexp.MustCompile(`^dpl_[A-Za-z0-9_-]{8,}$`)
	deploymentChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
)

type deploymentIdentityFile struct {
	SchemaVersion int    `json:"schema_version"`
	DeploymentID  string `json:"deployment_id"`
	PrivateKey    string `json:"private_key"`
	CreatedAt     string `json:"created_at"`
}

type deploymentIdentity struct {
	deploymentID string
	privateKey   ed25519.PrivateKey
}

var preserveServerFileOwnership = preserveExistingFileOwnership

func loadOrCreateDeploymentIdentity(path string) (*deploymentIdentity, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		identity, createErr := generateDeploymentIdentity()
		if createErr != nil {
			return nil, createErr
		}
		if saveErr := saveDeploymentIdentity(path, identity); saveErr != nil {
			return nil, saveErr
		}
		return identity, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Deployment identity: %w", err)
	}
	return decodeDeploymentIdentity(path, content)
}

func loadExistingDeploymentIdentity(path string) (*deploymentIdentity, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Deployment identity: %w", err)
	}
	return decodeDeploymentIdentity(path, content)
}

func loadExistingDeploymentIdentityReadOnly(path string) (*deploymentIdentity, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Deployment identity: %w", err)
	}
	return decodeDeploymentIdentityContent(content)
}

func decodeDeploymentIdentity(path string, content []byte) (*deploymentIdentity, error) {
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure Deployment identity: %w", err)
	}
	return decodeDeploymentIdentityContent(content)
}

func decodeDeploymentIdentityContent(content []byte) (*deploymentIdentity, error) {
	var stored deploymentIdentityFile
	if err := decodeStrictJSON(content, &stored); err != nil {
		return nil, fmt.Errorf("decode Deployment identity: %w", err)
	}
	privateKey, err := base64.RawURLEncoding.DecodeString(stored.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize || stored.SchemaVersion != deploymentIdentitySchemaVersion || !deploymentIDPattern.MatchString(stored.DeploymentID) {
		return nil, errors.New("Deployment identity is invalid")
	}
	return &deploymentIdentity{deploymentID: stored.DeploymentID, privateKey: ed25519.PrivateKey(privateKey)}, nil
}

func generateDeploymentIdentity() (*deploymentIdentity, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Deployment signing key: %w", err)
	}
	deploymentID, err := randomID("dpl")
	if err != nil {
		return nil, fmt.Errorf("generate Deployment ID: %w", err)
	}
	return &deploymentIdentity{deploymentID: deploymentID, privateKey: privateKey}, nil
}

func saveDeploymentIdentity(path string, identity *deploymentIdentity) error {
	if identity == nil || !deploymentIDPattern.MatchString(identity.deploymentID) || len(identity.privateKey) != ed25519.PrivateKeySize {
		return errors.New("Deployment identity is invalid")
	}
	return atomicWriteServerJSON(path, deploymentIdentityFile{
		SchemaVersion: deploymentIdentitySchemaVersion,
		DeploymentID:  identity.deploymentID,
		PrivateKey:    base64.RawURLEncoding.EncodeToString(identity.privateKey),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}, 0o600)
}

func (identity *deploymentIdentity) info(serverVersion, challenge string, capabilities ...string) (contracts.DeploymentInfo, error) {
	if identity == nil || !deploymentChallengePattern.MatchString(challenge) {
		return contracts.DeploymentInfo{}, errors.New("Deployment challenge is invalid")
	}
	publicKey := identity.privateKey.Public().(ed25519.PublicKey)
	fingerprint := sha256.Sum256(publicKey)
	result := contracts.DeploymentInfo{
		SchemaVersion:   contracts.SchemaVersion,
		ProtocolVersion: contracts.DeploymentProtocolVersion,
		DeploymentID:    identity.deploymentID,
		ServerVersion:   serverVersion,
		PublicKey:       base64.RawURLEncoding.EncodeToString(publicKey),
		PublicKeySHA256: hex.EncodeToString(fingerprint[:]),
		Challenge:       challenge,
		Capabilities:    []string{contracts.DeploymentCapabilityAtomicSnapshots, sessionartifact.CapabilityCompressedArtifacts, contracts.DeploymentCapabilityWorkspaceRegistry, contracts.DeploymentCapabilityDirectorySetup, contracts.DeploymentCapabilityProjectSetup, contracts.DeploymentCapabilityAccountLogin, contracts.DeploymentCapabilityConnections, contracts.DeploymentCapabilitySessionLibrary, contracts.DeploymentCapabilityLibraryCheckpoint, contracts.DeploymentCapabilityLibraryTags, contracts.DeploymentCapabilityLibraryBrowse, contracts.DeploymentCapabilityVisibleText, contracts.DeploymentCapabilityNativeLineage},
	}
	result.Capabilities = append(result.Capabilities, capabilities...)
	slices.Sort(result.Capabilities)
	result.Capabilities = slices.Compact(result.Capabilities)
	result.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(identity.privateKey, contracts.DeploymentSigningPayload(result)))
	result.CapabilitiesSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(identity.privateKey, contracts.DeploymentCapabilitiesSigningPayload(result)))
	return result, nil
}

func atomicWriteServerJSON(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".deployment-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := preserveServerFileOwnership(path, temporaryPath); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func decodeStrictJSON(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}
