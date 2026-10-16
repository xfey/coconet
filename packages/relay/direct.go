package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/storageguard"
	bolt "go.etcd.io/bbolt"
)

// DirectBackend keeps bulk bytes outside Relay. Seal copies a staging object to
// a distinct immutable key; an unexpired PUT URL cannot modify published data.
type DirectBackend interface {
	Identity() string
	UploadURL(context.Context, string, int64, time.Duration) (string, error)
	Seal(context.Context, string, string, int64) error
	DownloadURL(context.Context, string, time.Duration) (string, error)
	Remove(context.Context, string) error
}

type Options struct {
	HistoryRetention time.Duration
	Guard            *storageguard.Guard
	Backend          DirectBackend
	// Zero leaves expiry disabled until replica recovery has been validated.
	ContentTTL       time.Duration
	TransferCapacity int64
}

const uploadLease = 15 * time.Minute
const downloadURLLease = time.Minute

func (s *Store) ContentPolicy() contracts.RelayContentPolicy {
	return contracts.RelayContentPolicy{SchemaVersion: 1, Direct: s.backend != nil, Temporary: s.backend != nil || s.contentTTL > 0, TTLSeconds: int64(s.contentTTL / time.Second)}
}

func (s *Store) bindBackend() error {
	identity := "filesystem"
	if s.backend != nil {
		identity = s.backend.Identity()
	}
	if identity == "" {
		return errors.New("content backend identity is required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("backend"))
		if previous := bucket.Get([]byte("identity")); previous != nil && string(previous) != identity {
			return errors.New("Relay content backend differs; use a separate Relay directory for a new backend")
		}
		return bucket.Put([]byte("identity"), []byte(identity))
	})
}

func directKey(project, lease, kind string) string { return project + "/" + lease + "/" + kind }

func (s *Store) BeginUpload(ctx context.Context, project, account string, input contracts.RelayTransferRequest) (contracts.RelayTransferTarget, error) {
	result := contracts.RelayTransferTarget{SchemaVersion: 1, SHA256: input.SHA256, Size: input.Size}
	if !validID(project) || !validID(account) || input.SchemaVersion != 1 || !validDigest(input.SHA256) || input.Size < 0 || input.Size > contentstore.MaxObjectBytes || input.UploadID != "" {
		return result, invalid("invalid upload reservation")
	}
	if s.backend == nil {
		result.Proxy = true
		return result, nil
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, err
	}
	now := s.now().UTC()
	lease := hex.EncodeToString(nonce[:])
	var record objectRecord
	err := s.update(func(tx *bolt.Tx) error {
		objects, usage := tx.Bucket([]byte("objects")), tx.Bucket([]byte("usage"))
		id := key(project, input.SHA256)
		if data := objects.Get(id); data != nil {
			if err := json.Unmarshal(data, &record); err != nil {
				return err
			}
			if record.Size != input.Size {
				return invalid("object size differs from identity")
			}
			if record.Deleting != "" {
				return ErrBusy
			}
			if record.Ready {
				result.Ready = true
				return nil
			}
			if record.Writer != account {
				return ErrUploading
			}
			// Expired staging reservations are reclaimed by maintenance before
			// accepting another generation; never abandon billable objects here.
			until, err := time.Parse(time.RFC3339Nano, record.UploadUntil)
			if err != nil || !now.Before(until) {
				return ErrBusy
			}
			return nil
		}
		if err := changeTransferUsage(tx, project, 2*max(input.Size, 4096), s.transferCapacity); err != nil {
			return err
		}
		used := number(usage.Get([]byte(project)))
		if input.Size > MaxProjectBytes-used {
			return ErrQuota
		}
		record = objectRecord{Size: input.Size, Lease: lease, Writer: account, ReservedAt: now.Format(time.RFC3339Nano), UploadUntil: now.Add(uploadLease).Format(time.RFC3339Nano), StagingKey: directKey(project, lease, "upload"), ExternalKey: directKey(project, lease, "content")}
		if err := saveTemporary(tx, "objects", id, record, objectDue(record)); err != nil {
			return err
		}
		if err := usage.Put([]byte(project), encodedNumber(used+input.Size)); err != nil {
			return err
		}
		return bumpContentEpoch(tx, project)
	})
	if err != nil || result.Ready {
		return result, err
	}
	until, _ := time.Parse(time.RFC3339Nano, record.UploadUntil)
	result.URL, err = s.backend.UploadURL(ctx, record.StagingKey, record.Size, min(uploadLease, until.Sub(now)))
	result.UploadID, result.ExpiresAt = record.Lease, record.UploadUntil
	return result, err
}

func (s *Store) FinishUpload(ctx context.Context, project, account string, input contracts.RelayTransferRequest) (contracts.RelayTransferTarget, error) {
	result := contracts.RelayTransferTarget{SchemaVersion: 1, SHA256: input.SHA256, Size: input.Size}
	if s.backend == nil || !validID(project) || !validID(account) || input.SchemaVersion != 1 || !validDigest(input.SHA256) || !validID(input.UploadID) {
		return result, invalid("invalid upload completion")
	}
	id := key(project, input.SHA256)
	if _, busy := s.sealing.LoadOrStore(string(id), true); busy {
		return result, ErrBusy
	}
	defer s.sealing.Delete(string(id))
	s.objectMu.Lock()
	s.active[input.SHA256]++
	s.objectMu.Unlock()
	defer s.releaseObject(input.SHA256)
	var record objectRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte("objects")).Get(id)
		if data == nil {
			return ErrObject
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Lease != input.UploadID || record.Writer != account || record.Size != input.Size || record.Deleting != "" {
			return invalid("upload reservation differs")
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if record.Ready {
		result.Ready = true
		return result, nil
	}
	until, err := time.Parse(time.RFC3339Nano, record.UploadUntil)
	if err != nil || !s.now().Before(until) {
		return result, ErrBusy
	}
	if err := s.backend.Seal(ctx, record.StagingKey, record.ExternalKey, record.Size); err != nil {
		return result, err
	}
	err = s.reclaim(func(tx *bolt.Tx) error {
		var current objectRecord
		if err := json.Unmarshal(tx.Bucket([]byte("objects")).Get(id), &current); err != nil {
			return err
		}
		if current.Lease != record.Lease || current.Deleting != "" {
			return ErrBusy
		}
		current.Ready = true
		if s.contentTTL > 0 {
			current.ExpiresAt = s.now().Add(s.contentTTL).UTC().Format(time.RFC3339Nano)
		}
		if err := saveTemporary(tx, "objects", id, current, objectDue(current)); err != nil {
			return err
		}
		if err := deleteRepair(tx, id); err != nil {
			return err
		}
		return bumpContentEpoch(tx, project)
	})
	result.Ready = err == nil
	// Staging remains until its PUT capability expires. Maintenance then
	// removes it, including writes made after successful finalization.
	return result, err
}

func (s *Store) DownloadTarget(ctx context.Context, project, digest string) (contracts.RelayTransferTarget, error) {
	result := contracts.RelayTransferTarget{SchemaVersion: 1, SHA256: digest}
	if !validID(project) || !validDigest(digest) {
		return result, invalid("invalid download identity")
	}
	var record objectRecord
	err := s.reclaim(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte("objects")).Get(key(project, digest))
		if data == nil {
			return ErrObject
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if !record.Ready || record.Deleting != "" {
			return ErrObject
		}
		if s.backend != nil {
			guards := tx.Bucket([]byte("download-guards"))
			until := number(guards.Get([]byte(project)))
			if until < s.now().Add(downloadURLLease).UnixNano() {
				return guards.Put([]byte(project), encodedNumber(s.now().Add(2*downloadURLLease).UnixNano()))
			}
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Size = record.Size
	if s.backend == nil {
		result.Proxy = true
		return result, nil
	}
	result.ExpiresAt = s.now().Add(downloadURLLease).UTC().Format(time.RFC3339Nano)
	result.URL, err = s.backend.DownloadURL(ctx, record.ExternalKey, downloadURLLease)
	if errors.Is(err, ErrObject) {
		err = errors.Join(err, s.invalidateTemporary(project, digest, record.Lease))
	}
	return result, err
}
