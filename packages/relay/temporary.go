package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const defaultTransferCapacity int64 = 100 << 30
const stagingGrace = time.Minute

// Capacity reserves both staging and final bytes, including failed generations.
// Small objects have a minimum charge so metadata cannot grow without bound.
func transferCharge(r objectRecord) int64 { return 2 * max(r.Size, 4096) }
func dueKey(at int64, id []byte) []byte   { return []byte(fmt.Sprintf("%020d/%s", at, id)) }
func atTime(value string) time.Time       { t, _ := time.Parse(time.RFC3339Nano, value); return t }
func objectDue(r objectRecord) time.Time {
	var due time.Time
	if r.StagingKey != "" {
		due = atTime(r.UploadUntil).Add(stagingGrace)
	}
	if r.Ready && r.ExpiresAt != "" {
		expiry := atTime(r.ExpiresAt)
		if due.IsZero() || expiry.Before(due) {
			due = expiry
		}
	}
	return due
}
func saveTemporary(tx *bolt.Tx, bucket string, id []byte, r objectRecord, due time.Time) error {
	b := tx.Bucket([]byte(bucket))
	index := tx.Bucket([]byte("expiry"))
	indexID := append([]byte(bucket+"/"), id...)
	if old := b.Get(id); old != nil {
		var previous objectRecord
		if err := json.Unmarshal(old, &previous); err != nil {
			return err
		}
		if previous.MaintenanceAt > 0 {
			if err := index.Delete(dueKey(previous.MaintenanceAt, indexID)); err != nil {
				return err
			}
		}
	}
	r.MaintenanceAt = 0
	if !due.IsZero() {
		r.MaintenanceAt = due.UnixNano()
		if err := index.Put(dueKey(r.MaintenanceAt, indexID), []byte{1}); err != nil {
			return err
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return b.Put(id, data)
}
func deleteTemporary(tx *bolt.Tx, bucket string, id []byte, r objectRecord) error {
	if r.MaintenanceAt > 0 {
		if err := tx.Bucket([]byte("expiry")).Delete(dueKey(r.MaintenanceAt, append([]byte(bucket+"/"), id...))); err != nil {
			return err
		}
	}
	return tx.Bucket([]byte(bucket)).Delete(id)
}
func changeTransferUsage(tx *bolt.Tx, project string, delta int64, limit int64) error {
	b := tx.Bucket([]byte("transfer-usage"))
	used, total := number(b.Get([]byte(project))), number(b.Get([]byte("\x00")))
	if delta > 0 && (delta > MaxProjectBytes-used || delta > limit-total) {
		return ErrQuota
	}
	if err := b.Put([]byte(project), encodedNumber(max(0, used+delta))); err != nil {
		return err
	}
	return b.Put([]byte("\x00"), encodedNumber(max(0, total+delta)))
}

// Forget only a generation whose absence was checked by the trusted backend.
// Keep its keys and capacity charged until maintenance removes all remnants.
func (s *Store) invalidateTemporary(project, digest, lease string) error {
	return s.reclaim(func(tx *bolt.Tx) error {
		id := key(project, digest)
		var r objectRecord
		data := tx.Bucket([]byte("objects")).Get(id)
		if data == nil {
			return nil
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.Lease != lease || r.Deleting != "" {
			return nil
		}
		r.Ready = true
		r.ExpiresAt = s.now().UTC().Format(time.RFC3339Nano)
		if err := saveTemporary(tx, "garbage", key(project, r.Lease), r, objectDue(r)); err != nil {
			return err
		}
		if err := deleteTemporary(tx, "objects", id, r); err != nil {
			return err
		}
		return tx.Bucket([]byte("usage")).Put([]byte(project), encodedNumber(max(0, number(tx.Bucket([]byte("usage")).Get([]byte(project)))-r.Size)))
	})
}

// MaintainTemporary is restartable and indexed by due time. It performs at most
// limit object operations; no database transaction or global lock spans COS I/O.
func (s *Store) MaintainTemporary(ctx context.Context, limit int) (int, error) {
	if s.backend == nil {
		return 0, nil
	}
	if limit < 1 || limit > 256 {
		return 0, ErrInvalid
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	completed := 0
	var failure error
	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		var bucket, project string
		var id []byte
		now := s.now().UTC()
		err := s.db.View(func(tx *bolt.Tx) error {
			k, _ := tx.Bucket([]byte("expiry")).Cursor().First()
			if k == nil || bytes.Compare(k[:20], []byte(fmt.Sprintf("%020d", now.UnixNano()))) > 0 {
				return nil
			}
			var rest string
			var ok bool
			bucket, rest, ok = strings.Cut(string(k[21:]), "/")
			if !ok {
				return errors.New("invalid transfer expiry index")
			}
			id = []byte(rest)
			project, _, _ = strings.Cut(rest, "\x00")
			return nil
		})
		if err != nil {
			return completed, err
		}
		if id == nil {
			break
		}
		lockID := string(id)
		if bucket == "garbage" {
			lockID = "garbage/" + lockID
		}
		if _, busy := s.sealing.LoadOrStore(lockID, true); busy {
			err = s.deferTemporary(bucket, id, now.Add(time.Minute))
			if err != nil {
				return completed, err
			}
			continue
		}
		changed, err := s.maintainGeneration(ctx, bucket, id, project, now)
		s.sealing.Delete(lockID)
		if err != nil {
			if failure == nil {
				failure = err
			}
			if deferErr := s.deferTemporary(bucket, id, now.Add(time.Minute)); deferErr != nil {
				return completed, errors.Join(failure, deferErr)
			}
			continue
		}
		if changed {
			completed++
		}
	}
	return completed, failure
}
func (s *Store) deferTemporary(bucket string, id []byte, due time.Time) error {
	return s.reclaim(func(tx *bolt.Tx) error {
		var r objectRecord
		data := tx.Bucket([]byte(bucket)).Get(id)
		if data == nil {
			return nil
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		return saveTemporary(tx, bucket, id, r, due)
	})
}
func (s *Store) maintainGeneration(ctx context.Context, bucket string, id []byte, project string, now time.Time) (bool, error) {
	var r objectRecord
	removeAll := false
	selected := false
	err := s.reclaim(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(bucket)).Get(id)
		if data == nil {
			return nil
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.MaintenanceAt > now.UnixNano() {
			return nil
		}
		stageUntil := atTime(r.UploadUntil).Add(stagingGrace)
		if r.StagingKey != "" && now.Before(stageUntil) {
			return saveTemporary(tx, bucket, id, r, stageUntil)
		}
		removeAll = !r.Ready || r.ExpiresAt != "" && !now.Before(atTime(r.ExpiresAt))
		if removeAll && bucket == "objects" && r.Deleting == "" {
			readers, err := activeReadLeases(tx, project, now)
			if err != nil {
				return err
			}
			guard := number(tx.Bucket([]byte("download-guards")).Get([]byte(project)))
			if readers > 0 || guard > now.UnixNano() {
				return saveTemporary(tx, bucket, id, r, now.Add(time.Minute))
			}
		}
		if removeAll {
			r.Deleting = "expiry"
		}
		selected = true
		return saveTemporary(tx, bucket, id, r, now.Add(time.Minute))
	})
	if err != nil || !selected {
		return false, err
	}
	if r.StagingKey != "" {
		if err := s.backend.Remove(ctx, r.StagingKey); err != nil {
			return false, err
		}
	}
	if removeAll {
		if err := s.backend.Remove(ctx, r.ExternalKey); err != nil {
			return false, err
		}
	}
	err = s.reclaim(func(tx *bolt.Tx) error {
		var current objectRecord
		if err := json.Unmarshal(tx.Bucket([]byte(bucket)).Get(id), &current); err != nil {
			return err
		}
		if current.Lease != r.Lease {
			return ErrConflict
		}
		if !removeAll {
			current.StagingKey = ""
			return saveTemporary(tx, bucket, id, current, objectDue(current))
		}
		if err := deleteTemporary(tx, bucket, id, current); err != nil {
			return err
		}
		if err := changeTransferUsage(tx, project, -transferCharge(current), s.transferCapacity); err != nil {
			return err
		}
		if bucket == "objects" {
			usage := tx.Bucket([]byte("usage"))
			if err := usage.Put([]byte(project), encodedNumber(max(0, number(usage.Get([]byte(project)))-current.Size))); err != nil {
				return err
			}
		}
		return nil
	})
	return err == nil, err
}

// The server owns expiry even when every client is offline.
func (s *Store) RunMaintenance(ctx context.Context, report func(error)) {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		attempt, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := s.MaintenanceCycle(attempt)
		cancel()
		if report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
