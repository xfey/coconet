package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

const maxRepairs = 8192
const repairLifetime = 7 * 24 * time.Hour

// Missing requests are coalesced by project/hash. A live claim is never reset
// by another reader. Periodic expiry bounds abandoned requests and claims.
func (s *Store) RequestRepair(project, digest string) error {
	if !validID(project) || !validDigest(digest) {
		return ErrInvalid
	}
	return s.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("repairs"))
		id := key(project, digest)
		var r contracts.RelayRepair
		if data := b.Get(id); data != nil {
			if err := json.Unmarshal(data, &r); err != nil {
				return err
			}
			if atTime(r.ExpiresAt).After(s.now()) {
				return nil
			}
		}
		counts := tx.Bucket([]byte("repair-counts"))
		count := number(counts.Get([]byte(project)))
		if b.Get(id) == nil {
			if count >= maxRepairs {
				return ErrQuota
			}
			if err := counts.Put([]byte(project), encodedNumber(count+1)); err != nil {
				return err
			}
		} else {
			if err := tx.Bucket([]byte("repair-expiry")).Delete(dueKey(atTime(r.ExpiresAt).UnixNano(), id)); err != nil {
				return err
			}
		}
		r = contracts.RelayRepair{SHA256: digest, ExpiresAt: s.now().Add(repairLifetime).UTC().Format(time.RFC3339Nano)}
		data, _ := json.Marshal(r)
		if err := tx.Bucket([]byte("repair-expiry")).Put(dueKey(atTime(r.ExpiresAt).UnixNano(), id), []byte{1}); err != nil {
			return err
		}
		return b.Put(id, data)
	})
}
func (s *Store) Repairs(project, after string) (contracts.RelayRepairPage, error) {
	result := contracts.RelayRepairPage{SchemaVersion: 1, Requests: []contracts.RelayRepair{}}
	if !validID(project) || after != "" && !validDigest(after) {
		return result, ErrInvalid
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := key(project, "")
		c := tx.Bucket([]byte("repairs")).Cursor()
		for k, v := c.Seek(key(project, after)); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			digest := string(k[len(prefix):])
			if digest <= after {
				continue
			}
			var r contracts.RelayRepair
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if !atTime(r.ExpiresAt).After(s.now()) {
				continue
			}
			if len(result.Requests) == 128 {
				result.Next = result.Requests[len(result.Requests)-1].SHA256
				break
			}
			result.Requests = append(result.Requests, r)
		}
		return nil
	})
	return result, err
}
func (s *Store) ClaimRepair(project, account string, input contracts.RelayRepairRequest) (contracts.RelayRepair, error) {
	var result contracts.RelayRepair
	if !validID(project) || !validID(account) || input.SchemaVersion != 1 || !validID(input.DeviceID) || !validDigest(input.SHA256) {
		return result, ErrInvalid
	}
	err := s.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("repairs"))
		id := key(project, input.SHA256)
		data := b.Get(id)
		if data == nil {
			return ErrObject
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if !atTime(result.ExpiresAt).After(s.now()) {
			return ErrObject
		}
		writer := contracts.RelayWriter(account, input.DeviceID)
		if result.Claimant != writer && atTime(result.ClaimedUntil).After(s.now()) {
			return ErrBusy
		}
		result.Claimant = writer
		result.ClaimedUntil = s.now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
		encoded, _ := json.Marshal(result)
		return b.Put(id, encoded)
	})
	return result, err
}

// This index also prunes requests for projects whose devices never return.
func (s *Store) pruneRepairs(limit int) error {
	return s.reclaim(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("repair-expiry")).Cursor()
		now := s.now().UnixNano()
		for n := 0; n < limit; n++ {
			k, _ := c.First()
			if k == nil || string(k[:20]) > fmt.Sprintf("%020d", now) {
				break
			}
			k = bytes.Clone(k)
			id := bytes.Clone(k[21:])
			b := tx.Bucket([]byte("repairs"))
			if data := b.Get(id); data != nil {
				var r contracts.RelayRepair
				if err := json.Unmarshal(data, &r); err != nil {
					return err
				}
				if atTime(r.ExpiresAt).UnixNano() <= now {
					if err := deleteRepair(tx, id); err != nil {
						return err
					}
				}
			}
			if err := tx.Bucket([]byte("repair-expiry")).Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteRepair(tx *bolt.Tx, id []byte) error {
	b := tx.Bucket([]byte("repairs"))
	data := b.Get(id)
	if data == nil {
		return nil
	}
	var r contracts.RelayRepair
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if err := tx.Bucket([]byte("repair-expiry")).Delete(dueKey(atTime(r.ExpiresAt).UnixNano(), id)); err != nil {
		return err
	}
	project, _, _ := strings.Cut(string(id), "\x00")
	counts := tx.Bucket([]byte("repair-counts"))
	if err := counts.Put([]byte(project), encodedNumber(max(0, number(counts.Get([]byte(project)))-1))); err != nil {
		return err
	}
	return b.Delete(id)
}
