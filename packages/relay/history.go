package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func evidence(r contracts.RelayReceipt) contracts.RelayCommitEvidence {
	canonical, _ := json.Marshal(r.Commit)
	return contracts.RelayCommitEvidence{SchemaVersion: 1, ProjectID: r.ProjectID, CommitID: r.Commit.ID, Revision: r.Revision, Writer: r.Writer, AcceptedAt: r.AcceptedAt, PayloadSHA256: r.Commit.SHA256, StateSHA256: r.Commit.StateSHA256, CanonicalSHA256: contentstore.ObjectDigest(canonical)}
}

func receiptEvidence(tx *bolt.Tx, project, id string) (contracts.RelayCommitEvidence, error) {
	var result contracts.RelayCommitEvidence
	data := tx.Bucket([]byte("receipts")).Get(key(project, id))
	if data == nil {
		return result, contracts.ErrRelayReceiptMissing
	}
	if len(data) != 8 {
		return result, json.Unmarshal(data, &result)
	}
	var receipt contracts.RelayReceipt
	if err := json.Unmarshal(tx.Bucket([]byte("entries")).Get(revisionKey(project, number(data))), &receipt); err != nil {
		return result, err
	}
	return evidence(receipt), nil
}

func (s *Store) Receipt(project, id string) (contracts.RelayCommitEvidence, error) {
	var result contracts.RelayCommitEvidence
	if !validID(project) || !validID(id) {
		return result, ErrInvalid
	}
	err := s.db.View(func(tx *bolt.Tx) error { var err error; result, err = receiptEvidence(tx, project, id); return err })
	return result, err
}

// CompactHistory removes only a contiguous prefix covered by a durable
// checkpoint. The checkpoint's own anchor remains readable for publication.
// A persistent cursor bounds empty-project scans as well as actual deletions.
func (s *Store) CompactHistory(ctx context.Context, age time.Duration, limit int) (int, error) {
	if age < 0 || limit < 1 || limit > 1024 {
		return 0, ErrInvalid
	}
	removed := 0
	err := s.reclaim(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("history-maintenance"))
		projects := tx.Bucket([]byte("checkpoint-heads")).Cursor()
		start := meta.Get([]byte("cursor"))
		project, head := projects.Seek(start)
		if project == nil {
			project, head = projects.First()
		}
		for inspected := 0; project != nil && inspected < 32 && removed < limit; inspected++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			var cp contracts.RelayCheckpointAck
			if err := json.Unmarshal(head, &cp); err != nil {
				return err
			}
			name := string(project)
			entries := tx.Bucket([]byte("entries"))
			cursor := entries.Cursor()
			prefix := key(name, "")
			for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix) && removed < limit; {
				if err := ctx.Err(); err != nil {
					return err
				}
				var r contracts.RelayReceipt
				if err := json.Unmarshal(v, &r); err != nil {
					return err
				}
				if r.Revision >= cp.Revision || atTime(r.AcceptedAt).After(s.now().Add(-age)) {
					break
				}
				if r.Revision != number(tx.Bucket([]byte("history-floors")).Get(project))+1 {
					return errors.New("noncontiguous history prefix")
				}
				encoded, err := json.Marshal(evidence(r))
				if err != nil {
					return err
				}
				id := key(name, r.Commit.ID)
				old := tx.Bucket([]byte("receipts")).Get(id)
				if len(old) != 8 || number(old) != r.Revision {
					return errors.New("history receipt differs")
				}
				saved := int64(len(k) + len(v) + len(old) - len(encoded))
				if err := tx.Bucket([]byte("receipts")).Put(id, encoded); err != nil {
					return err
				}
				if err := cursor.Delete(); err != nil {
					return err
				}
				logs := tx.Bucket([]byte("log-usage"))
				if err := logs.Put(project, encodedNumber(max(0, number(logs.Get(project))-saved))); err != nil {
					return err
				}
				if err := tx.Bucket([]byte("history-floors")).Put(project, encodedNumber(r.Revision)); err != nil {
					return err
				}
				removed++
				k, v = cursor.Seek(revisionKey(name, r.Revision+1))
			}
			if removed == limit {
				return meta.Put([]byte("cursor"), project)
			}
			project, head = projects.Next()
			if project == nil {
				return meta.Delete([]byte("cursor"))
			}
		}
		if project != nil {
			return meta.Put([]byte("cursor"), project)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
