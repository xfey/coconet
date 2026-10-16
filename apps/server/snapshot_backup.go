package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/sessionartifact"
	bolt "go.etcd.io/bbolt"
)

type backupObject struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type completeBackupManifest struct {
	Config         serverConfig    `json:"config"`
	Identity       json.RawMessage `json:"identity"`
	Format         int             `json:"format"`
	DeploymentID   string          `json:"deployment_id"`
	BackendID      string          `json:"backend_id"`
	MetadataSHA256 string          `json:"metadata_sha256"`
	Objects        []backupObject  `json:"objects"`
	Relay          *relay.Backup   `json:"relay,omitempty"`
}

// Backup is deliberately offline for bbolt. Keep the database's OS read lock
// for the whole metadata AND object copy: a server/GC writer cannot start in
// the middle of a backup. No schema migration is performed on the source.
func backupComplete(ctx context.Context, config serverConfig, objects artifactStore, output string) error {
	database, err := bolt.Open(config.DataPath, 0400, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("stop coconet-server before complete backup: %w", err)
	}
	defer database.Close()
	return backupCompleteLocked(ctx, config, objects, output, database)
}

// The caller holds the database lock through backup and any subsequent reset.
func backupCompleteLocked(ctx context.Context, config serverConfig, objects artifactStore, output string, database *bolt.DB, live ...func(context.Context, string) (*relay.Backup, error)) error {
	if objects == nil {
		return errors.New("complete backup requires the configured artifact backend")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("backup output already exists or cannot be inspected")
	}
	directory := output + ".objects"
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	var err error
	manifest := completeBackupManifest{Format: 1, BackendID: objects.BackendIdentity(), Objects: []backupObject{}}
	inventory := map[string]backupObject{}
	manifest.Config = config
	manifest.Identity, err = os.ReadFile(config.IdentityPath)
	if err != nil {
		return err
	}
	identity, err := decodeDeploymentIdentityContent(manifest.Identity)
	if err != nil {
		return err
	}

	err = database.View(func(tx *bolt.Tx) error {
		if tx.Bucket(metaBucket) == nil || tx.Bucket(stateBucket) == nil {
			return errors.New("backup metadata is incomplete")
		}
		manifest.DeploymentID = string(tx.Bucket(metaBucket).Get(deploymentKey))
		if manifest.DeploymentID != identity.deploymentID {
			return errors.New("backup identity differs from metadata")
		}
		bound := string(tx.Bucket(metaBucket).Get(artifactBackendKey))
		if bound != "" && bound != manifest.BackendID {
			return errors.New("backup backend does not match metadata")
		}
		var content []byte
		if legacy := tx.Bucket(stateBucket).Get(stateKey); legacy != nil {
			content = legacy
		} else {
			var err error
			content, err = exportRecordJSON(tx)
			if err != nil {
				return err
			}
		}
		state, err := decodeStateJSON(content)
		if err != nil {
			return err
		}
		inventory, err = backupInventory(state)
		if err != nil {
			return err
		}

		return tx.CopyFile(output, 0600)
	})
	if err != nil {
		return err
	}
	if err := validateBboltDatabase(database, manifest.DeploymentID); err != nil {
		return err
	}
	keys := make([]string, 0, len(inventory))
	for key := range inventory {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		object := inventory[key]
		if !validArtifactObjectKey(key) {
			return errors.New("invalid backup object identity")
		}
		reader, err := objects.Open(ctx, key)
		if err != nil {
			return err
		}
		target := filepath.Join(directory, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			reader.Close()
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			reader.Close()
			return err
		}
		hasher := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(reader, object.Size+1))
		err = errors.Join(copyErr, reader.Close(), file.Sync(), file.Close())
		if err != nil {
			return err
		}
		if n != object.Size || hex.EncodeToString(hasher.Sum(nil)) != object.SHA256 {
			return errors.New("backup object failed integrity verification")
		}
		manifest.Objects = append(manifest.Objects, object)
	}
	manifest.MetadataSHA256, err = hashBackupFile(output)
	if err != nil {
		return err
	}
	root, err := relayRootForDatabase(config.DataPath)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		manifest.Relay, err = live[0](ctx, output+".relay")
	} else {
		manifest.Relay, err = relay.BackupOffline(ctx, root, output+".relay")
	}
	if err != nil {
		return err
	}
	if manifest.Relay != nil {
		manifest.Format = 2
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// This completion marker is written last. Incomplete bundles cannot restore.
	return writeBackupMarker(output+".manifest.json", data)
}
func hashBackupFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func writeBackupMarker(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func restoreCompleteObjects(ctx context.Context, input, deployment string, objects artifactStore) error {
	m, err := readBackupManifest(input)
	if err != nil {
		return err
	}
	if (m.Format != 1 && m.Format != 2) || (m.Format == 2) != (m.Relay != nil) || m.DeploymentID != deployment || objects == nil || m.BackendID != objects.BackendIdentity() {
		return errors.New("backup identity does not match the configured Deployment and artifact backend")
	}
	identity, err := decodeDeploymentIdentityContent(m.Identity)
	if err != nil || identity.deploymentID != deployment {
		return errors.New("backup Deployment identity is incomplete or invalid")
	}
	hash, err := hashBackupFile(input)
	if err != nil || hash != m.MetadataSHA256 {
		return errors.New("backup metadata failed integrity verification")
	}
	if err := validateBboltSnapshotReadOnly(input, deployment); err != nil {
		return err
	}
	writer, ok := objects.(artifactWriter)
	if !ok {
		return errors.New("artifact backend does not support restore")
	}
	seen := map[string]bool{}
	// Validate every file before writing anything to the configured backend.
	for _, object := range m.Objects {
		if !validArtifactObjectKey(object.Key) || seen[object.Key] || object.Size < 1 || object.Size > 16<<20 {
			return errors.New("invalid backup object descriptor")
		}
		seen[object.Key] = true
		path := filepath.Join(input+".objects", filepath.FromSlash(object.Key))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != object.Size {
			return errors.New("backup object is missing or changed")
		}
		hash, err := hashBackupFile(path)
		if err != nil || hash != object.SHA256 {
			return errors.New("backup object failed integrity verification")
		}
	}
	// Require exact closure, not merely integrity of the listed files.
	db, err := bolt.Open(input, 0400, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	err = db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(stateBucket).Get(stateKey)
		if data == nil {
			var err error
			data, err = exportRecordJSON(tx)
			if err != nil {
				return err
			}
		}
		state, err := decodeStateJSON(data)
		if err != nil {
			return err
		}
		inventory, err := backupInventory(state)
		if err != nil {
			return err
		}
		if len(inventory) != len(m.Objects) {
			return errors.New("backup object closure is incomplete")
		}
		for _, object := range m.Objects {
			if inventory[object.Key] != object {
				return errors.New("backup object closure differs from metadata")
			}
		}
		return nil
	})
	err = errors.Join(err, db.Close())
	if err != nil {
		return err
	}
	for _, object := range m.Objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(input+".objects", filepath.FromSlash(object.Key)))
		if err != nil {
			return err
		}
		if err := writer.Write(ctx, artifactObject{Key: object.Key, SHA256: object.SHA256, Size: object.Size}, data); err != nil {
			return err
		}
	}
	return nil
}

// Compaction writes a new database, leaving the original untouched. Adoption
// uses the same validated, rollback-preserving restore boundary as backups.
func compactBbolt(dataPath, output string) error {
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("compaction output must be new")
	}
	source, err := bolt.Open(dataPath, 0400, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := bolt.Open(output, 0600, nil)
	if err != nil {
		return err
	}
	err = bolt.Compact(target, source, 16<<20)
	return errors.Join(err, target.Close())
}

// Include ready catalog objects and verify every durable manifest reference.
// Pending attempts can reuse these objects after restoration.
func backupInventory(state databaseState) (map[string]backupObject, error) {
	inventory := map[string]backupObject{}
	expected := map[string]backupObject{}
	for _, snapshot := range state.Snapshots.All() {
		project := state.Sessions.Get(snapshot.ServerSessionID).ProjectID
		for _, b := range snapshot.Manifest.Chunks {
			c, ok := state.Chunks.Lookup(artifactStoredChunkKey(project, b))
			if !ok {
				return nil, errors.New("backup Snapshot has a missing raw block")
			}
			expected[c.ObjectKey] = backupObject{Key: c.ObjectKey, SHA256: sessionartifact.StoredHash(b), Size: sessionartifact.StoredSize(b)}
		}
	}
	for _, attempt := range state.SyncAttempts.All() {
		if attempt.Committed != nil || attempt.Expired {
			continue
		}
		for _, block := range attempt.Request.Manifest.Chunks {
			if chunk, ok := state.Chunks.Lookup(artifactStoredChunkKey(attempt.Request.ProjectID, block)); ok {
				expected[chunk.ObjectKey] = backupObject{Key: chunk.ObjectKey, SHA256: sessionartifact.StoredHash(block), Size: sessionartifact.StoredSize(block)}
			}
		}
	}
	for _, c := range state.Chunks.All() {
		item, ok := expected[c.ObjectKey]
		if !ok {
			item = backupObject{Key: c.ObjectKey, SHA256: c.StoredSHA256, Size: c.StoredSize}
			if c.StoredSHA256 == "" {
				item.SHA256, item.Size = c.SHA256, c.RawSize
			}
		}
		inventory[c.ObjectKey] = item
	}
	for _, c := range state.ContentObjects.All() {
		if c.Ready {
			inventory[c.ObjectKey] = backupObject{Key: c.ObjectKey, SHA256: c.SHA256, Size: int64(c.Size)}
		}
	}
	check := func(project string, manifest *contracts.ConversationManifest) error {
		if manifest == nil {
			return errors.New("backup compact manifest is missing")
		}
		if err := conversation.Validate(*manifest); err != nil {
			return err
		}
		for _, b := range manifest.Blocks {
			c, ok := state.ContentObjects.Lookup(contentObjectKey(project, b.SHA256))
			if !ok || !c.Ready || c.Size != b.Size {
				return errors.New("backup compact block is missing")
			}
		}
		return nil
	}
	for _, snapshot := range state.Snapshots.All() {
		if snapshot.Conversation != nil {
			if err := check(state.Sessions.Get(snapshot.ServerSessionID).ProjectID, snapshot.Conversation.Content); err != nil {
				return nil, err
			}
		}
	}
	for _, session := range state.Sessions.All() {
		if session.SyncCheckpoint != nil {
			if err := check(session.ProjectID, &session.SyncCheckpoint.Native); err != nil {
				return nil, err
			}
		}
	}
	return inventory, nil
}
