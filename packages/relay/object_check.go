package relay

import (
	"encoding/json"
	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
	"time"
)

func (s *Store) Missing(project string, input contracts.RelayObjectCheck) (contracts.RelayMissingObjects, error) {
	result := contracts.RelayMissingObjects{SchemaVersion: 1, Missing: []string{}}
	if !validID(project) || input.SchemaVersion != 1 || input.Objects == nil || len(input.Objects) > 8192 {
		return result, invalid("invalid object check")
	}
	err := s.update(func(tx *bolt.Tx) error {
		seen := map[string]bool{}
		for _, object := range input.Objects {
			if !validDigest(object.SHA256) || object.Size < 0 || object.Size > contentstore.MaxObjectBytes || seen[object.SHA256] {
				return invalid("invalid or duplicate object descriptor")
			}
			seen[object.SHA256] = true
			data := tx.Bucket([]byte("objects")).Get(key(project, object.SHA256))
			if data == nil {
				result.Missing = append(result.Missing, object.SHA256)
				continue
			}
			var record objectRecord
			if err := json.Unmarshal(data, &record); err != nil {
				return err
			}
			if record.Size != object.Size {
				return invalid("object size differs from identity")
			}
			if !record.Ready || record.Deleting != "" {
				result.Missing = append(result.Missing, object.SHA256)
			} else if s.contentTTL > 0 {
				record.ExpiresAt = s.now().Add(s.contentTTL).UTC().Format(time.RFC3339Nano)
				if err := saveTemporary(tx, "objects", key(project, object.SHA256), record, objectDue(record)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return result, err
}
