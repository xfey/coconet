package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/sessionartifact"
	bolt "go.etcd.io/bbolt"
)

func TestMigrationJournalRecoversOnlyUncommittedObjects(t *testing.T) {
	a, _, _, objects := newSyncTestAuthority(t)
	journalPath := a.store.db.Path() + ".migration-objects.json"
	journal := &migrationContentStore{artifactStore: objects, path: journalPath, journal: migrationObjectJournal{Backend: objects.BackendIdentity()}}
	body := []byte("migration output")
	for _, key := range []string{"objects/chunk_orphan", "objects/chunk_committed"} {
		if err := journal.Write(context.Background(), artifactObject{Key: key, SHA256: conversation.Digest(body), Size: int64(len(body))}, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.update(func(s *databaseState) error {
		s.ContentObjects.Set("retained", contentObjectRecord{ObjectKey: "objects/chunk_committed", SHA256: conversation.Digest(body), Size: len(body), Ready: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A crash can leave a partial temporary file; the last renamed journal stays complete.
	if err := os.WriteFile(filepath.Join(filepath.Dir(journalPath), ".migration-journal-interrupted"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.store.recoverMigrationObjects(); err != nil {
		t.Fatal(err)
	}
	objects.mutex.Lock()
	_, orphan := objects.objects["objects/chunk_orphan"]
	_, committed := objects.objects["objects/chunk_committed"]
	objects.mutex.Unlock()
	if orphan || !committed {
		t.Fatal("recovery deleted committed content or retained abandoned content")
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("journal not cleared", err)
	}
	if err := a.store.recoverMigrationObjects(); err != nil {
		t.Fatal(err)
	}
}

func TestBackupInventoryUsesStoredIdentityForPendingCompressedReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native")
	if err := os.WriteFile(path, bytes.Repeat([]byte("tool result\n"), 10000), 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := sessionartifact.BuildVersion(path, "application/x-ndjson", 2)
	if err != nil {
		t.Fatal(err)
	}
	block := artifact.Manifest.Chunks[0]
	state := newDatabaseState()
	state.Chunks.Set(artifactStoredChunkKey("coco_test", block), chunkRecord{ObjectKey: "objects/chunk_compressed", SHA256: block.SHA256, RawSize: block.RawSize})
	state.SyncAttempts.Set("pending", syncAttempt{Request: contracts.PrepareSyncRequest{ProjectID: "coco_test", Manifest: artifact.Manifest}})
	inventory, err := backupInventory(state)
	if err != nil {
		t.Fatal(err)
	}
	got := inventory["objects/chunk_compressed"]
	if got.SHA256 != sessionartifact.StoredHash(block) || got.Size != sessionartifact.StoredSize(block) || got.Size >= block.RawSize {
		t.Fatal("backup used decoded bytes for compressed object", got)
	}
}

func TestMigrationJournalIsCompleteBeforeBackendWrite(t *testing.T) {
	a, _, _, objects := newSyncTestAuthority(t)
	path := a.store.db.Path() + ".migration-objects.json"
	failing := &migrationFailingWriter{artifactStore: objects, path: path, t: t}
	journal := &migrationContentStore{artifactStore: failing, path: path, journal: migrationObjectJournal{Backend: objects.BackendIdentity()}}
	if err := journal.Write(context.Background(), artifactObject{Key: "objects/chunk_failure"}, []byte("body")); err == nil {
		t.Fatal("expected backend failure")
	}
	if err := a.store.recoverMigrationObjects(); err != nil {
		t.Fatal(err)
	}
}

type migrationFailingWriter struct {
	artifactStore
	path string
	t    *testing.T
}

func (w *migrationFailingWriter) Write(_ context.Context, o artifactObject, _ []byte) error {
	data, err := os.ReadFile(w.path)
	if err != nil {
		w.t.Fatal(err)
	}
	var journal migrationObjectJournal
	if err := json.Unmarshal(data, &journal); err != nil || len(journal.Keys) != 1 || journal.Keys[0] != o.Key {
		w.t.Fatal("object write preceded durable journal", err)
	}
	return errors.New("fixture write interrupted")
}

func TestRestoreKeepsObjectWritesBehindMetadataLock(t *testing.T) {
	a, _, _, _ := newSyncTestAuthority(t)
	identity, err := generateDeploymentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.bindDeployment(identity.deploymentID); err != nil {
		t.Fatal(err)
	}
	path := a.store.db.Path()
	backup := filepath.Join(t.TempDir(), "snapshot.db")
	if err := a.store.db.View(func(tx *bolt.Tx) error { return tx.CopyFile(backup, 0600) }); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := restoreBbolt(path, backup, identity.deploymentID, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("live server allowed object restore", called, err)
	}
}
