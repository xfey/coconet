package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xfey/coconet/packages/relay"
	bolt "go.etcd.io/bbolt"
)

func readBackupManifest(input string) (completeBackupManifest, error) {
	var manifest completeBackupManifest
	info, err := os.Lstat(input + ".manifest.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return manifest, errors.New("a complete, bounded regular backup manifest is required")
	}
	data, err := os.ReadFile(input + ".manifest.json")
	if err == nil {
		err = json.Unmarshal(data, &manifest)
	}
	return manifest, err
}

func requireLegacyRestore(config serverConfig, input string) error {
	manifest, err := readBackupManifest(input)
	if err != nil {
		return err
	}
	root, err := relayRootForDatabase(config.DataPath)
	if err != nil {
		return err
	}
	_, existsErr := os.Lstat(root)
	if manifest.Relay != nil || !errors.Is(existsErr, os.ErrNotExist) {
		return errors.New("Relay deployments must restore identity, authority and Relay together into a new --data-dir; refusing a partial in-place restore")
	}
	return nil
}

// Restoring to a fresh directory keeps both databases in one adoption unit.
// server.json is the final completion marker, so serve cannot open a partial
// restore using this directory. No configured live files or identities change.
func restoreDeployment(ctx context.Context, input, output string) (string, error) {
	if os.Getenv("COCONET_RELAY_HOME") != "" {
		return "", errors.New("unset COCONET_RELAY_HOME for a fresh Deployment restore and its first startup; Relay is restored beside the new authority database")
	}
	manifest, err := readBackupManifest(input)
	if err != nil {
		return "", err
	}
	if (manifest.Format != 1 && manifest.Format != 2) || (manifest.Format == 2) != (manifest.Relay != nil) {
		return "", errors.New("unsupported or incomplete Deployment backup")
	}
	identity, err := decodeDeploymentIdentityContent(manifest.Identity)
	if err != nil || identity.deploymentID != manifest.DeploymentID {
		return "", errors.New("backup Deployment identity is invalid")
	}
	digest, err := hashBackupFile(input)
	if err != nil || digest != manifest.MetadataSHA256 {
		return "", errors.New("backup authority database failed integrity verification")
	}
	if err := validateBboltSnapshotReadOnly(input, manifest.DeploymentID); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(absolute, 0700); err != nil {
		return "", fmt.Errorf("restore destination must be a new directory: %w", err)
	}
	// A failure deliberately leaves an incomplete directory for inspection;
	// it has no startup configuration and is never adopted automatically.
	config := manifest.Config
	config.SchemaVersion, config.MetadataDriver = serverConfigSchemaVersion, "bbolt"
	config.DataPath, config.IdentityPath = filepath.Join(absolute, "server.db"), filepath.Join(absolute, "deployment.json")
	config.ArtifactDriver, config.ArtifactPath = "filesystem", filepath.Join(absolute, "artifacts")
	config.RequireArtifactStore = false
	if err := validateServerConfig(config); err != nil {
		return "", err
	}
	objects, err := openFilesystemArtifactStore(config.ArtifactPath)
	if err != nil {
		return "", err
	}
	// Validate the original backend identity while copying verified bytes to
	// the fresh local backend. Only the restored database is rebound below.
	target := &restoreArtifactTarget{filesystemArtifactStore: objects, identity: manifest.BackendID}
	if err := restoreCompleteObjects(ctx, input, manifest.DeploymentID, target); err != nil {
		return "", err
	}
	if manifest.Relay != nil {
		if err := relay.RestoreOffline(ctx, input+".relay", config.DataPath+".relay", *manifest.Relay); err != nil {
			return "", err
		}
	}
	if _, err := restoreBbolt(config.DataPath, input, manifest.DeploymentID); err != nil {
		return "", err
	}
	db, err := bolt.Open(config.DataPath, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return "", err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(metaBucket).Put(artifactBackendKey, []byte(objects.BackendIdentity()))
	})
	if err == nil {
		err = validateBboltDatabase(db, manifest.DeploymentID)
	}
	if err := errors.Join(err, db.Close()); err != nil {
		return "", err
	}
	if err := writeBackupMarker(config.IdentityPath, manifest.Identity); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := filepath.Join(absolute, "server.json")
	if err := writeBackupMarker(path, append(data, '\n')); err != nil {
		return "", err
	}
	directory, err := os.Open(absolute)
	if err != nil {
		return "", err
	}
	return path, errors.Join(directory.Sync(), directory.Close())
}

type restoreArtifactTarget struct {
	*filesystemArtifactStore
	identity string
}

func (s *restoreArtifactTarget) BackendIdentity() string { return s.identity }
