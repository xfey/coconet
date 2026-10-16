package relay

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

const collectionGrace = 24 * time.Hour
const readLeaseDuration = 2 * time.Minute
const collectionReceiptRetention = 7 * 24 * time.Hour
const maxCollectionArchiveBytes = 32 << 20

var ErrBusy = errors.New("relay content has an active reader or writer")
var ErrGrace = errors.New("relay collection protection period has not elapsed")

type readLeaseRecord struct {
	Lease  contracts.RelayReadLease `json:"lease"`
	Writer string                   `json:"writer"`
}

func bumpContentEpoch(tx *bolt.Tx, project string) error {
	bucket := tx.Bucket([]byte("content-epochs"))
	return bucket.Put([]byte(project), encodedNumber(number(bucket.Get([]byte(project)))+1))
}
func activeReadLeases(tx *bolt.Tx, project string, now time.Time) (int, error) {
	count := 0
	prefix := key(project, "")
	cursor := tx.Bucket([]byte("read-leases")).Cursor()
	for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cursor.Next() {
		var record readLeaseRecord
		if err := json.Unmarshal(v, &record); err != nil {
			return 0, err
		}
		expires, err := time.Parse(time.RFC3339Nano, record.Lease.ExpiresAt)
		if err != nil {
			return 0, err
		}
		if expires.After(now) {
			count++
		} else if err := cursor.Delete(); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func (s *Store) ReadLease(project, account string, input contracts.RelayReadLeaseRequest) (contracts.RelayReadLease, error) {
	var result contracts.RelayReadLease
	if !validID(project) || !validID(account) || !validID(input.DeviceID) || input.SchemaVersion != 1 || input.Revision < 0 || input.LeaseID != "" && !validID(input.LeaseID) {
		return result, invalid("invalid read lease")
	}
	s.objectMu.Lock()
	defer s.objectMu.Unlock()
	now := s.now().UTC()
	writer := contracts.RelayWriter(account, input.DeviceID)
	err := s.reclaim(func(tx *bolt.Tx) error {
		count, err := activeReadLeases(tx, project, now)
		if err != nil {
			return err
		}
		bucket := tx.Bucket([]byte("read-leases"))
		if input.LeaseID != "" {
			var previous readLeaseRecord
			data := bucket.Get(key(project, input.LeaseID))
			if data == nil {
				return ErrConflict
			}
			if err := json.Unmarshal(data, &previous); err != nil {
				return err
			}
			if previous.Writer != writer || previous.Lease.Revision != input.Revision {
				return invalid("read lease owner differs")
			}
			result = previous.Lease
		} else {
			if number(tx.Bucket([]byte("heads")).Get([]byte(project))) != input.Revision {
				return ErrConflict
			}
			if count >= 256 {
				return ErrBusy
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return err
			}
			result = contracts.RelayReadLease{SchemaVersion: 1, ID: hex.EncodeToString(nonce[:]), Revision: input.Revision}
		}
		result.ExpiresAt = now.Add(readLeaseDuration).Format(time.RFC3339Nano)
		data, _ := json.Marshal(readLeaseRecord{Lease: result, Writer: writer})
		if err := bumpContentEpoch(tx, project); err != nil {
			return err
		}
		return bucket.Put(key(project, result.ID), data)
	})
	return result, err
}
func (s *Store) ReleaseReadLease(project, account, device, id string) error {
	if !validID(project) || !validID(account) || !validID(device) || !validID(id) {
		return invalid("invalid read lease release")
	}
	return s.reclaim(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("read-leases"))
		data := bucket.Get(key(project, id))
		if data == nil {
			return nil
		}
		var record readLeaseRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Writer != contracts.RelayWriter(account, device) {
			return invalid("read lease owner differs")
		}
		return bucket.Delete(key(project, id))
	})
}

// The authenticated client derives candidates from its accepted semantic state.
// Relay validates anchors, leases and bytes, never interprets the opaque DAG.
func (s *Store) MarkCollection(project, account string, plan contracts.RelayCollectionPlan) (contracts.RelayCollection, error) {
	if s.backend != nil || s.contentTTL > 0 {
		return contracts.RelayCollection{}, invalid("temporary content uses server-owned expiry")
	}
	var result contracts.RelayCollection
	if !validID(project) || !validID(account) || !validID(plan.ID) || !validID(plan.DeviceID) || plan.SchemaVersion != 1 || plan.Revision < 1 || !validDigest(plan.CheckpointSHA256) || len(plan.Objects) < 1 || len(plan.Objects) > 8192 {
		return result, invalid("invalid collection plan")
	}
	seen := map[string]bool{}
	for _, digest := range plan.Objects {
		if !validDigest(digest) || seen[digest] {
			return result, invalid("invalid collection object")
		}
		seen[digest] = true
	}
	plan.Objects = slices.Clone(plan.Objects)
	slices.Sort(plan.Objects)
	writer := contracts.RelayWriter(account, plan.DeviceID)
	now := s.now().UTC()
	s.objectMu.Lock()
	defer s.objectMu.Unlock()
	err := s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("collections"))
		if data := bucket.Get(key(project, plan.ID)); data != nil {
			if err := json.Unmarshal(data, &result); err != nil {
				return err
			}
			left, _ := json.Marshal(plan)
			right, _ := json.Marshal(result.Plan)
			if result.Writer != writer || !bytes.Equal(left, right) {
				return invalid("collection identity reused")
			}
			return nil
		}
		if err := collectionAnchor(tx, project, plan); err != nil {
			return err
		}
		if count, err := activeReadLeases(tx, project, now); err != nil {
			return err
		} else if count > 0 {
			return ErrBusy
		}
		notBefore := now
		objects := tx.Bucket([]byte("objects"))
		for _, digest := range plan.Objects {
			if s.active[digest] > 0 {
				return ErrBusy
			}
			var record objectRecord
			data := objects.Get(key(project, digest))
			if data == nil {
				return ErrObject
			}
			if err := json.Unmarshal(data, &record); err != nil {
				return err
			}
			if !record.Ready || record.Deleting != "" {
				return ErrBusy
			}
			if record.RetireAfter == "" {
				record.RetireAfter = now.Add(collectionGrace).Format(time.RFC3339Nano)
			}
			due, err := time.Parse(time.RFC3339Nano, record.RetireAfter)
			if err != nil {
				return err
			}
			if due.After(notBefore) {
				notBefore = due
			}
			encoded, _ := json.Marshal(record)
			if err := objects.Put(key(project, digest), encoded); err != nil {
				return err
			}
		}
		used, err := pruneCollectionArchive(tx, project, now)
		if err != nil {
			return err
		}
		result = contracts.RelayCollection{SchemaVersion: 1, Plan: plan, Writer: writer, Epoch: number(tx.Bucket([]byte("content-epochs")).Get([]byte(project))), NotBefore: notBefore.Format(time.RFC3339Nano), Status: "marked", UpdatedAt: now.Format(time.RFC3339Nano)}
		encoded, _ := json.Marshal(result)
		if used+len(encoded) > maxCollectionArchiveBytes {
			return ErrQuota
		}
		return bucket.Put(key(project, plan.ID), encoded)
	})
	return result, err
}
func collectionAnchor(tx *bolt.Tx, project string, plan contracts.RelayCollectionPlan) error {
	if number(tx.Bucket([]byte("heads")).Get([]byte(project))) != plan.Revision {
		return ErrConflict
	}
	var cp contracts.RelayCheckpointReceipt
	data := tx.Bucket([]byte("checkpoints")).Get([]byte(project))
	if data == nil {
		return ErrConflict
	}
	if err := json.Unmarshal(data, &cp); err != nil {
		return err
	}
	if cp.Checkpoint.Revision != plan.Revision || cp.Checkpoint.SHA256 != plan.CheckpointSHA256 {
		return ErrConflict
	}
	return nil
}

// Logical deletion is atomic. After it starts, retries finish that exact plan
// even after restart; uploads cannot revive its records while deletion runs.
func (s *Store) Collect(project, account, device, id string) (contracts.RelayCollection, error) {
	var result contracts.RelayCollection
	if !validID(project) || !validID(account) || !validID(device) || !validID(id) {
		return result, invalid("invalid collection identity")
	}
	claim := "collection/" + project + "/" + id
	if _, busy := s.sealing.LoadOrStore(claim, true); busy {
		return result, ErrBusy
	}
	defer s.sealing.Delete(claim)
	s.objectMu.Lock()
	now := s.now().UTC()
	err := s.reclaim(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("collections"))
		data := bucket.Get(key(project, id))
		if data == nil {
			return ErrObject
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if result.Writer != contracts.RelayWriter(account, device) {
			return invalid("collection owner differs")
		}
		if result.Status == "deleted" || result.Status == "cancelled" {
			return nil
		}
		for _, digest := range result.Plan.Objects {
			if s.active[digest] > 0 || s.deleting[digest] {
				return ErrBusy
			}
		}
		if result.Status == "marked" {
			due, err := time.Parse(time.RFC3339Nano, result.NotBefore)
			if err != nil {
				return err
			}
			if err := collectionAnchor(tx, project, result.Plan); err != nil || number(tx.Bucket([]byte("content-epochs")).Get([]byte(project))) != result.Epoch {
				result.Status = "cancelled"
			} else if count, err := activeReadLeases(tx, project, now); err != nil {
				return err
			} else if count > 0 {
				return ErrBusy
			} else if now.Before(due) {
				return ErrGrace
			} else {
				objects := tx.Bucket([]byte("objects"))
				for _, digest := range result.Plan.Objects {
					var record objectRecord
					data := objects.Get(key(project, digest))
					if data == nil {
						return ErrObject
					}
					if err := json.Unmarshal(data, &record); err != nil {
						return err
					}
					if !record.Ready || record.Deleting != "" {
						return ErrBusy
					}
					record.Deleting = id
					if err := tx.Bucket([]byte("object-references")).Delete(key(digest, project)); err != nil {
						return err
					}
					encoded, _ := json.Marshal(record)
					if err := objects.Put(key(project, digest), encoded); err != nil {
						return err
					}
				}
				result.Status = "deleting"
			}
			result.UpdatedAt = now.Format(time.RFC3339Nano)
			encoded, _ := json.Marshal(result)
			return bucket.Put(key(project, id), encoded)
		}
		if result.Status != "deleting" {
			return invalid("invalid collection state")
		}
		return nil
	})
	if err != nil || result.Status != "deleting" {
		s.objectMu.Unlock()
		return result, err
	}
	// Durable references are removed atomically with the logical tombstone.
	// Under a short lock pin only physical deletions; unrelated hashes can be
	// read/uploaded while disk removal and fsync run outside this lock.
	remove := []string{}
	err = s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte("object-references")).Cursor()
		for _, digest := range result.Plan.Objects {
			prefix := key(digest, "")
			k, _ := cursor.Seek(prefix)
			if k == nil || !bytes.HasPrefix(k, prefix) {
				remove = append(remove, digest)
			}
		}
		return nil
	})
	if err == nil {
		for _, digest := range remove {
			s.deleting[digest] = true
		}
	}
	s.objectMu.Unlock()
	if err != nil {
		return result, err
	}
	defer func() {
		s.objectMu.Lock()
		for _, digest := range remove {
			delete(s.deleting, digest)
		}
		s.objectMu.Unlock()
	}()
	for _, digest := range remove {
		if err := s.objects.Remove(digest); err != nil {
			return result, err
		}
	}
	err = s.reclaim(func(tx *bolt.Tx) error {
		objects, usage := tx.Bucket([]byte("objects")), tx.Bucket([]byte("usage"))
		removed := int64(0)
		for _, digest := range result.Plan.Objects {
			var record objectRecord
			if err := json.Unmarshal(objects.Get(key(project, digest)), &record); err != nil {
				return err
			}
			if record.Deleting != id {
				return invalid("collection marker changed")
			}
			removed += record.Size
			if err := objects.Delete(key(project, digest)); err != nil {
				return err
			}
		}
		if err := usage.Put([]byte(project), encodedNumber(max(0, number(usage.Get([]byte(project)))-removed))); err != nil {
			return err
		}
		result.Status = "deleted"
		result.Deleted = len(result.Plan.Objects)
		result.UpdatedAt = now.Format(time.RFC3339Nano)
		data, _ := json.Marshal(result)
		return tx.Bucket([]byte("collections")).Put(key(project, id), data)
	})
	return result, err
}

// Never expire logically started deletion. Unstarted or terminal plans have a
// bounded retry window; future maintenance recomputes from the current graph.
func pruneCollectionArchive(tx *bolt.Tx, project string, now time.Time) (int, error) {
	used := 0
	prefix := key(project, "")
	cursor := tx.Bucket([]byte("collections")).Cursor()
	for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cursor.Next() {
		var record contracts.RelayCollection
		if err := json.Unmarshal(v, &record); err != nil {
			return 0, err
		}
		at, err := time.Parse(time.RFC3339Nano, record.UpdatedAt)
		if err != nil {
			return 0, err
		}
		if record.Status != "deleting" && now.Sub(at) > collectionReceiptRetention {
			if err := cursor.Delete(); err != nil {
				return 0, err
			}
		} else {
			used += len(v)
		}
	}
	return used, nil
}
