package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func TestOfflineBackupRestoresOpaqueHistoryObjectsAndCompactsCopy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("original source bytes")
	digest := contentstore.ObjectDigest(body)
	for _, project := range []string{"one", "two"} {
		if err := s.PutObject(project, digest, int64(len(body)), bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := []byte("opaque checkpoint; no server-side DAG interpretation")
	commit := proposal("commit", 0)
	commit.Objects, commit.StateSHA256 = []string{digest}, contentstore.ObjectDigest(checkpoint)
	receipt, err := s.Append("one", "owner", commit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCheckpoint("one", "owner", contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "device", Revision: 1, CommitID: commit.ID, Codec: "coconet-state-json-v1", Payload: checkpoint, SHA256: commit.StateSHA256}); err != nil {
		t.Fatal(err)
	}
	// Create free pages and interrupted states that must remain recoverable.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("collections")).Put([]byte("temporary"), bytes.Repeat([]byte("x"), 2<<20))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte("collections")).Delete([]byte("temporary")); err != nil {
			return err
		}
		data, _ := json.Marshal(objectRecord{Size: 16, Lease: "interrupted"})
		return tx.Bucket([]byte("objects")).Put(key("one", contentstore.ObjectDigest([]byte("unfinished"))), data)
	}); err != nil {
		t.Fatal(err)
	}
	activeTarget := filepath.Join(t.TempDir(), "busy")
	if _, err := BackupOffline(ctx, root, activeTarget); err == nil {
		t.Fatal("backed up an active Relay")
	}
	if _, err := os.Stat(activeTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("active backup created a target")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := hashDatabase(filepath.Join(root, "relay.db"))
	backup := filepath.Join(t.TempDir(), "backup")
	descriptor, err := BackupOffline(ctx, root, backup)
	if err != nil || descriptor.References != 2 || descriptor.ReferenceBytes != 2*int64(len(body)) {
		t.Fatal(descriptor, err)
	}
	after, _ := hashDatabase(filepath.Join(root, "relay.db"))
	if before != after {
		t.Fatal("backup modified its source")
	}
	original, _ := os.Stat(filepath.Join(root, "relay.db"))
	compacted, _ := os.Stat(filepath.Join(backup, "relay.db"))
	if compacted.Size() >= original.Size()/2 {
		t.Fatal("backup did not reclaim free pages", original.Size(), compacted.Size())
	}
	t.Logf("relay database source=%d compact_backup=%d", original.Size(), compacted.Size())
	destination := filepath.Join(t.TempDir(), "restore")
	if err := RestoreOffline(ctx, backup, destination, *descriptor); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	page, err := restored.Checkpoint("one")
	if err != nil || page.Head != 1 || page.Checkpoint == nil || !bytes.Equal(page.Checkpoint.Checkpoint.Payload, checkpoint) {
		t.Fatal(page, err)
	}
	again, err := restored.Append("one", "owner", commit)
	if err != nil || again.AcceptedAt != receipt.AcceptedAt {
		t.Fatal("restore lost the accepted idempotent receipt", err)
	}
	file, err := restored.Object("two", digest)
	if err != nil {
		t.Fatal("restore lost shared content", err)
	}
	file.Close()
	if err := RestoreOffline(ctx, backup, destination, *descriptor); err == nil {
		t.Fatal("restore overwrote an existing destination")
	}
}

func TestOfflineBackupFailsClosedOnMissingCorruptAndSymlinkedContent(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt", "symlink", "database", "catalog-count", "cancelled"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			s, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("recoverable content")
			digest := contentstore.ObjectDigest(body)
			if err := s.PutObject("project", digest, int64(len(body)), bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
			s.Close()
			backup := filepath.Join(t.TempDir(), "backup")
			info, err := BackupOffline(ctx, root, backup)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(backup, "objects", digest[:2], digest)
			switch damage {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, []byte("wrong"), 0600)
			case "symlink":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(filepath.Join(root, "objects", digest[:2], digest), path)
				}
			case "database":
				info.DatabaseSHA256 = contentstore.ObjectDigest([]byte("wrong database"))
			case "catalog-count":
				info.References++
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "restore")
			if err := RestoreOffline(ctx, backup, target, *info); err == nil {
				t.Fatal("invalid backup restored")
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid backup created a destination")
			}
		})
	}
}
