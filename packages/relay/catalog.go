package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func (s *Store) ObjectCatalog(project, after string, limit int) (contracts.RelayObjectPage, error) {
	page := contracts.RelayObjectPage{SchemaVersion: 1, Objects: []contracts.RelayObject{}}
	if !validID(project) || after != "" && !validDigest(after) || limit < 1 || limit > 512 {
		return page, invalid("invalid content catalog page")
	}
	// A restarted or replacement device can finish a deletion whose logical
	// decision was already committed. This cannot authorize new candidates.
	if err := s.resumeDeleting(project); err != nil && !errors.Is(err, ErrBusy) {
		return page, err
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		page.Head = number(tx.Bucket([]byte("heads")).Get([]byte(project)))
		prefix := key(project, "")
		cursor := tx.Bucket([]byte("objects")).Cursor()
		k, v := cursor.Seek(key(project, after))
		if after != "" && bytes.Equal(k, key(project, after)) {
			k, v = cursor.Next()
		}
		scanned := 0
		for ; k != nil && bytes.HasPrefix(k, prefix); k, v = cursor.Next() {
			if scanned == limit {
				return nil
			}
			scanned++
			page.Next = string(k[len(prefix):])
			var record objectRecord
			if err := json.Unmarshal(v, &record); err != nil {
				return err
			}
			if record.Ready && record.Deleting == "" {
				page.Objects = append(page.Objects, contracts.RelayObject{SHA256: page.Next, Size: record.Size})
			}
		}
		page.Next = ""
		return nil
	})
	return page, err
}

func (s *Store) resumeDeleting(project string) error {
	var pending contracts.RelayCollection
	if err := s.db.View(func(tx *bolt.Tx) error {
		prefix := key(project, "")
		cursor := tx.Bucket([]byte("collections")).Cursor()
		for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cursor.Next() {
			var record contracts.RelayCollection
			if err := json.Unmarshal(v, &record); err != nil {
				return err
			}
			if record.Status == "deleting" {
				pending = record
				break
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if pending.Status == "" {
		return nil
	}
	account := strings.TrimSuffix(pending.Writer, "."+pending.Plan.DeviceID)
	_, err := s.Collect(project, account, pending.Plan.DeviceID, pending.Plan.ID)
	return err
}
