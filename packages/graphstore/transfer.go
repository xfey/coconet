package graphstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

// Transfer records transport attempts without storing bearer credentials.
// Validated receipts are archived before applying graph state; History records
// whether their deterministic business transaction was accepted locally.
type Transfer struct {
	Bytes      int64                   `json:"bytes,omitempty"`
	DurationMS int64                   `json:"duration_ms,omitempty"`
	Source     *workgraph.Source       `json:"source,omitempty"`
	ID         uint64                  `json:"id"`
	ProjectID  string                  `json:"project_id"`
	Operation  string                  `json:"operation"`
	Status     string                  `json:"status"`
	At         string                  `json:"at"`
	Error      string                  `json:"error,omitempty"`
	CommitID   string                  `json:"commit_id,omitempty"`
	Revision   int64                   `json:"revision,omitempty"`
	SHA256     string                  `json:"sha256,omitempty"`
	Receipt    *contracts.RelayReceipt `json:"receipt,omitempty"`
}

func (s *Store) WatchProject(project string) error {
	if project == "" {
		return errors.New("project is required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("states"))
		if bucket.Get([]byte(project)) != nil {
			return nil
		}
		data, err := json.Marshal(workgraph.New(project))
		if err != nil {
			return err
		}
		return bucket.Put([]byte(project), data)
	})
}

func (s *Store) RecordTransfer(record Transfer) error {
	if record.ProjectID == "" || record.Operation == "" || record.Status == "" || len(record.Error) > 8000 {
		return errors.New("invalid transfer record")
	}
	record.At = time.Now().UTC().Format(time.RFC3339Nano)
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("transfers"))
		id, err := bucket.NextSequence()
		if err != nil {
			return err
		}
		record.ID = id
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(data) > 2<<20 {
			return errors.New("transfer record exceeds archive limit")
		}
		rowKey, orderKey := key(record.ProjectID, fmt.Sprintf("%020d", id)), transferOrderKey(id)
		amount := transferArchiveSize(orderKey, rowKey, data)
		usage, err := pruneTransfers(tx, time.Now().UTC(), amount)
		if err != nil {
			return err
		}
		if err := bucket.Put(rowKey, data); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("transfer-order")).Put(orderKey, rowKey); err != nil {
			return err
		}
		usage.Bytes += amount
		usage.Rows++
		return putTransferUsage(tx, usage)
	})
}

func (s *Store) Transfers(project string, limit int) ([]Transfer, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("transfer page limit must be 1–1000")
	}
	result := []Transfer{}
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := key(project, "")
		cursor := tx.Bucket([]byte("transfers")).Cursor()
		k, v := cursor.Seek(append(bytes.Clone(prefix), 255))
		if k == nil {
			k, v = cursor.Last()
		} else {
			k, v = cursor.Prev()
		}
		for ; k != nil && bytes.HasPrefix(k, prefix) && len(result) < limit; k, v = cursor.Prev() {
			var record Transfer
			if err := json.Unmarshal(v, &record); err != nil {
				return err
			}
			result = append(result, record)
		}
		return nil
	})
	return result, err
}

// BindMode prevents a private offline graph or another Relay identity from
// silently sharing the same revision space. Credentials are not persisted here.
func (s *Store) BindMode(identity string) error {
	if identity == "" {
		return errors.New("store mode identity is required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("settings"))
		old := bucket.Get([]byte("mode"))
		if old != nil && string(old) != identity {
			return errors.New("local store belongs to another mode or Relay identity; use its original configuration")
		}
		if old == nil && identity != "offline" && tx.Bucket([]byte("states")).Stats().KeyN > 0 {
			return errors.New("cannot attach a pre-existing offline graph to a Relay revision space")
		}
		return bucket.Put([]byte("mode"), []byte(identity))
	})
}
