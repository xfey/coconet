package relay

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
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	bolt "go.etcd.io/bbolt"
)

// Backup describes an opaque Relay snapshot. The database is the authoritative
// inventory; duplicating every object descriptor in a second manifest would
// create another unbounded in-memory catalog.
type Backup struct {
	DatabaseSHA256     string `json:"database_sha256"`
	References         int64  `json:"object_references"`
	ReferenceBytes     int64  `json:"object_reference_bytes"`
	ExternalReferences int64  `json:"external_references,omitempty"`
}

// BackupLiveMetadata is called only while the shared storage guard excludes
// writers in BOTH authority and Relay. External content is temporary and is
// not copied. Filesystem backends continue to use the offline backup path.
func (s *Store) BackupLiveMetadata(ctx context.Context, target string) (*Backup, error) {
	if s.backend == nil {
		return nil, errors.New("online backup requires external Relay content; use offline complete backup for filesystem content")
	}
	descriptor, err := inspectBackup(ctx, s.db, s.objects, nil)
	if err != nil {
		return nil, err
	}
	if descriptor.References != 0 {
		return nil, errors.New("online metadata backup cannot omit local Relay objects")
	}
	if err := copyBackup(ctx, s.db, s.objects, target); err != nil {
		return nil, err
	}
	descriptor.DatabaseSHA256, err = hashDatabase(filepath.Join(target, "relay.db"))
	return &descriptor, err
}

// BackupOffline holds the source database's OS read lock until every live
// catalog object has been verified and copied. Open/GC cannot start meanwhile.
// It does not run recovery or mutate the source. The caller must keep the
// authority database locked too, and write its completion marker last.
func BackupOffline(ctx context.Context, root, target string) (*Backup, error) {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil // Relay has never been initialized.
	}
	db, objects, err := openBackupSource(root)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	descriptor, err := inspectBackup(ctx, db, objects, nil)
	if err != nil {
		return nil, err
	}
	if err := copyBackup(ctx, db, objects, target); err != nil {
		return nil, err
	}
	descriptor.DatabaseSHA256, err = hashDatabase(filepath.Join(target, "relay.db"))
	return &descriptor, err
}

// RestoreOffline validates the complete snapshot before creating a fresh
// destination. It never swaps a live Relay independently of its authority.
func RestoreOffline(ctx context.Context, source, target string, expected Backup) error {
	db, objects, err := openBackupSource(source)
	if err != nil {
		return err
	}
	defer db.Close()
	digest, err := hashDatabase(db.Path())
	if err != nil || !validDigest(expected.DatabaseSHA256) || digest != expected.DatabaseSHA256 {
		return errors.New("Relay backup database failed integrity verification")
	}
	actual, err := inspectBackup(ctx, db, objects, nil)
	if err != nil {
		return err
	}
	if actual.References != expected.References || actual.ReferenceBytes != expected.ReferenceBytes || actual.ExternalReferences != expected.ExternalReferences {
		return errors.New("Relay backup catalog differs from its completion marker")
	}
	return copyBackup(ctx, db, objects, target)
}

func openBackupSource(root string) (*bolt.DB, *contentstore.Store, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("Relay backup source must be an existing real directory")
	}
	path := filepath.Join(root, "relay.db")
	info, err = os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("Relay backup source database must be a regular file")
	}
	db, err := bolt.Open(path, 0400, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, nil, fmt.Errorf("stop the Relay before backup or restore: %w", err)
	}
	objects, err := contentstore.Open(root)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, objects, nil
}

func inspectBackup(ctx context.Context, db *bolt.DB, objects *contentstore.Store, target *contentstore.Store) (Backup, error) {
	result := Backup{}
	err := db.View(func(tx *bolt.Tx) error {
		// Drain Check even after the first error; it produces on a goroutine.
		var structural error
		for err := range tx.Check() {
			if structural == nil {
				structural = err
			}
		}
		if structural != nil {
			return structural
		}
		for _, name := range []string{"heads", "entries", "receipts", "objects", "usage", "checkpoints", "generations", "read-leases", "collections", "content-epochs", "log-usage"} {
			if tx.Bucket([]byte(name)) == nil {
				return errors.New("Relay backup is missing metadata buckets")
			}
		}
		return tx.Bucket([]byte("objects")).ForEach(func(k, v []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			project, digest, ok := strings.Cut(string(k), "\x00")
			var record objectRecord
			if !ok || !validID(project) || !validDigest(digest) || json.Unmarshal(v, &record) != nil || record.Size < 0 || record.Size > contentstore.MaxObjectBytes {
				return errors.New("Relay backup contains an invalid object descriptor")
			}
			// These bytes are already inaccessible. Preserve their metadata so
			// normal startup/upload/GC recovery finishes the original operation.
			if !record.Ready || record.Deleting != "" {
				return nil
			}
			if record.ExternalKey != "" {
				result.ExternalReferences++
				return nil // Metadata-only reference; temporary COS bytes are not a backup.
			}
			file, err := objects.OpenObject(digest)
			if err != nil {
				return fmt.Errorf("Relay backup object failed verification: %w", err)
			}
			info, err := file.Stat()
			if err == nil && info.Size() != record.Size {
				err = errors.New("Relay backup object size differs from catalog")
			}
			if err == nil && target != nil {
				err = target.PutStream(&backupReader{ctx, file}, digest, record.Size)
			}
			if err := errors.Join(err, file.Close()); err != nil {
				return err
			}
			result.References++
			result.ReferenceBytes += record.Size
			return nil
		})
	})
	return result, err
}

func copyBackup(ctx context.Context, source *bolt.DB, objects *contentstore.Store, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return fmt.Errorf("Relay backup destination must be new: %w", err)
	}
	contents, err := contentstore.Open(target)
	if err != nil {
		return err
	}
	if _, err := inspectBackup(ctx, source, objects, contents); err != nil {
		return err
	}
	db, err := bolt.Open(filepath.Join(target, "relay.db"), 0600, nil)
	if err != nil {
		return err
	}
	// Reclaim reusable database pages in the copy, never in the live source.
	err = bolt.Compact(db, source, 16<<20)
	if err := errors.Join(err, db.Close()); err != nil {
		return err
	}
	directory, err := os.Open(target)
	if err != nil {
		return err
	}
	return errors.Join(ctx.Err(), directory.Sync(), directory.Close())
}

type backupReader struct {
	ctx context.Context
	io.Reader
}

func (r *backupReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func hashDatabase(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), errors.Join(err, f.Close())
}
