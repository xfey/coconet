package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

type serverStatusReport struct {
	metadata string
	artifact string
	version  string
}

func runServerStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	configPath, code := parseConfigFlag("status", args, stderr)
	if code != 0 {
		return code
	}
	report, config, identity, err := inspectServerStatus(ctx, configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "Coconet Server status: ready")
	fmt.Fprintf(stdout, "Deployment ID: %s\n", identity.deploymentID)
	fmt.Fprintf(stdout, "Listen: %s\n", config.Listen)
	fmt.Fprintf(stdout, "Advertised endpoint: %s\n", config.AdvertisedEndpoint)
	fmt.Fprintf(stdout, "Metadata: %s\n", report.metadata)
	fmt.Fprintf(stdout, "Artifact store: %s\n", report.artifact)
	fmt.Fprintf(stdout, "Process / endpoint: reachable (coconet-server %s)\n", report.version)
	return 0
}

func inspectServerStatus(ctx context.Context, configPath string) (serverStatusReport, serverConfig, *deploymentIdentity, error) {
	if err := inspectPrivateRegularFile(configPath, "Server config"); err != nil {
		return serverStatusReport{}, serverConfig{}, nil, err
	}
	config, err := loadServerConfigReadOnly(configPath)
	if err != nil {
		return serverStatusReport{}, serverConfig{}, nil, err
	}
	if config.AdvertisedEndpoint == "" {
		return serverStatusReport{}, serverConfig{}, nil, errors.New("legacy Server config is missing advertised_endpoint; migrate it to schema 3")
	}
	if err := inspectPrivateRegularFile(config.IdentityPath, "Deployment identity"); err != nil {
		return serverStatusReport{}, serverConfig{}, nil, err
	}
	identity, err := loadExistingDeploymentIdentityReadOnly(config.IdentityPath)
	if err != nil {
		return serverStatusReport{}, serverConfig{}, nil, err
	}
	serverVersion, endpointErr := probeAdvertisedDeployment(ctx, config.AdvertisedEndpoint, identity)
	artifactDriver, backendID, err := inspectConfiguredArtifactStore(ctx, config)
	if err != nil {
		return serverStatusReport{}, serverConfig{}, nil, err
	}
	metadata, err := inspectConfiguredMetadata(ctx, config, identity.deploymentID, backendID, endpointErr == nil)
	if err != nil {
		return serverStatusReport{}, serverConfig{}, nil, err
	}
	if endpointErr != nil {
		return serverStatusReport{}, serverConfig{}, nil, fmt.Errorf("probe advertised endpoint: %w", endpointErr)
	}
	return serverStatusReport{metadata: metadata, artifact: artifactDriver, version: serverVersion}, config, identity, nil
}

func inspectPrivateRegularFile(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a regular non-symlink file", label)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions are broader than 0600", label)
	}
	return nil
}

func inspectConfiguredArtifactStore(ctx context.Context, config serverConfig) (string, string, error) {
	if config.SchemaVersion >= artifactServerConfigSchemaVersion && config.ArtifactDriver == "filesystem" {
		canonical, err := inspectFilesystemArtifactStore(config.ArtifactPath)
		if err != nil {
			return "", "", err
		}
		return "filesystem ready", artifactBackendIdentity("filesystem", canonical), nil
	}
	artifacts, err := configuredArtifactStore(ctx, config)
	if err != nil {
		return "", "", err
	}
	if artifacts == nil {
		return "legacy backend not configured", "", nil
	}
	return artifacts.Driver() + " ready", artifacts.BackendIdentity(), nil
}

func inspectFilesystemArtifactStore(root string) (string, error) {
	for _, path := range []string{root, filepath.Join(root, "objects"), filepath.Join(root, ".tmp")} {
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("inspect filesystem artifact directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return "", errors.New("filesystem artifact directory is missing, replaced, or has unsafe permissions")
		}
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize filesystem artifact root: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func inspectConfiguredMetadata(ctx context.Context, config serverConfig, deploymentID, backendID string, running bool) (string, error) {
	if config.MetadataDriver == "postgres" {
		if err := inspectPostgresMetadata(ctx, os.Getenv("COCONET_DATABASE_URL"), deploymentID, backendID); err != nil {
			return "", err
		}
		return "postgres ready (read-only probe)", nil
	}
	if err := inspectBboltMetadata(config.DataPath, deploymentID, backendID); err != nil {
		if running && errors.Is(err, bolt.ErrTimeout) {
			return "bbolt locked by the verified running process", nil
		}
		return "", err
	}
	return "bbolt ready (read-only probe)", nil
}

func inspectBboltMetadata(path, deploymentID, backendID string) error {
	if err := inspectPrivateRegularFile(path, "bbolt metadata"); err != nil {
		return err
	}
	database, err := bolt.Open(path, 0o400, &bolt.Options{ReadOnly: true, Timeout: 100 * time.Millisecond})
	if err != nil {
		return fmt.Errorf("open bbolt metadata read-only: %w", err)
	}
	defer database.Close()
	return database.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		state := tx.Bucket(stateBucket)
		if meta == nil || state == nil {
			return errors.New("bbolt metadata is incomplete")
		}
		marker := meta.Get(schemaKey)
		if len(marker) != 1 || marker[0] < 1 || marker[0] > databaseSchemaVersion {
			return errors.New("bbolt metadata schema is unsupported")
		}
		if string(meta.Get(deploymentKey)) != deploymentID {
			return errors.New("metadata database belongs to a different Coconet Deployment")
		}
		if string(meta.Get(artifactBackendKey)) != backendID {
			return errors.New("metadata database belongs to a different artifact backend")
		}
		if int(marker[0]) == databaseSchemaVersion {
			return validateRecordSnapshot(tx)
		}
		if int(marker[0]) >= 23 {
			content, err := exportRecordJSON(tx)
			if err != nil {
				return err
			}
			_, err = decodeStateJSON(content)
			return err
		}
		_, err := decodeState(tx)
		return err
	})
}

func inspectPostgresMetadata(ctx context.Context, databaseURL, deploymentID, backendID string) error {
	if databaseURL == "" {
		return errors.New("PostgreSQL database URL is required")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL read-only probe: %w", err)
	}
	defer database.Close()
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := database.PingContext(probeCtx); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	var schemaVersion int
	var storedDeploymentID string
	var storedBackendID sql.NullString
	var document []byte
	if err := database.QueryRowContext(probeCtx, `SELECT schema_version, deployment_id, artifact_backend_id, document FROM coconet_state WHERE singleton = TRUE`).Scan(&schemaVersion, &storedDeploymentID, &storedBackendID, &document); err != nil {
		return fmt.Errorf("read PostgreSQL state: %w", err)
	}
	if schemaVersion != databaseSchemaVersion || storedDeploymentID != deploymentID || storedBackendID.String != backendID {
		return errors.New("PostgreSQL metadata identity or schema does not match Server configuration")
	}
	if _, err := decodeStateJSON(document); err != nil {
		return err
	}
	return nil
}

func probeAdvertisedDeployment(ctx context.Context, endpoint string, identity *deploymentIdentity) (string, error) {
	challengeBytes := make([]byte, 24)
	if _, err := rand.Read(challengeBytes); err != nil {
		return "", fmt.Errorf("create status challenge: %w", err)
	}
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/deployment?challenge="+url.QueryEscape(challenge), nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Deployment probe returned HTTP %d", response.StatusCode)
	}
	var info contracts.DeploymentInfo
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&info); err != nil {
		return "", fmt.Errorf("decode Deployment probe: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", errors.New("Deployment probe returned trailing content")
	}
	publicKey := identity.privateKey.Public().(ed25519.PublicKey)
	fingerprint := sha256.Sum256(publicKey)
	signature, signatureErr := base64.RawURLEncoding.DecodeString(info.Signature)
	capabilitySignature, capabilityErr := base64.RawURLEncoding.DecodeString(info.CapabilitiesSignature)
	if info.SchemaVersion != contracts.SchemaVersion || info.ProtocolVersion != contracts.DeploymentProtocolVersion || info.DeploymentID != identity.deploymentID || info.Challenge != challenge || info.PublicKey != base64.RawURLEncoding.EncodeToString(publicKey) || info.PublicKeySHA256 != hex.EncodeToString(fingerprint[:]) || strings.TrimSpace(info.ServerVersion) == "" || signatureErr != nil || !ed25519.Verify(publicKey, contracts.DeploymentSigningPayload(info), signature) || capabilityErr != nil || !slices.Contains(info.Capabilities, contracts.DeploymentCapabilityConnections) || !ed25519.Verify(publicKey, contracts.DeploymentCapabilitiesSigningPayload(info), capabilitySignature) {
		return "", errors.New("advertised endpoint returned a different or invalid Coconet Deployment identity")
	}
	return info.ServerVersion, nil
}
