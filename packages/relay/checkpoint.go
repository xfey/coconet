package relay

import (
	"encoding/json"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func (s *Store) Checkpoint(project string) (contracts.RelayCheckpointPage, error) {
	page := contracts.RelayCheckpointPage{SchemaVersion: 1}
	if !validID(project) {
		return page, invalid("invalid project")
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		page.Generation = number(tx.Bucket([]byte("generations")).Get([]byte(project)))
		page.Head = number(tx.Bucket([]byte("heads")).Get([]byte(project)))
		if data := tx.Bucket([]byte("checkpoints")).Get([]byte(project)); data != nil {
			return json.Unmarshal(data, &page.Checkpoint)
		}
		return nil
	})
	return page, err
}

func validateCheckpoint(project, account string, input contracts.RelayCheckpoint) error {
	if !validID(project) || !validID(account) || !validID(input.DeviceID) || !validID(input.CommitID) || input.SchemaVersion != 1 || input.Revision < 1 || input.Revision > 9007199254740991 || (input.Codec != "coconet-state-json-v1" && input.Codec != "coconet-state-json-v2" && input.Codec != "coconet-state-json-v3" && input.Codec != "coconet-state-json-v4") || len(input.Payload) == 0 || len(input.Payload) > contracts.MaxRelayCheckpointBytes || contentstore.ObjectDigest(input.Payload) != input.SHA256 {
		return invalid("invalid checkpoint envelope")
	}
	return nil
}

// PutCheckpoint never advances the commit stream. The latest cache replaces
// its predecessor and is charged only its current size against project quota.
func (s *Store) PutCheckpoint(project, account string, input contracts.RelayCheckpoint) (contracts.RelayCheckpointReceipt, error) {
	var result contracts.RelayCheckpointReceipt
	if err := validateCheckpoint(project, account, input); err != nil {
		return result, err
	}
	err := s.update(func(tx *bolt.Tx) error {
		return s.putCheckpoint(tx, project, account, input, &result)
	})
	return result, err
}

func (s *Store) putCheckpoint(tx *bolt.Tx, project, account string, input contracts.RelayCheckpoint, result *contracts.RelayCheckpointReceipt) error {
	var anchor contracts.RelayReceipt
	data := tx.Bucket([]byte("entries")).Get(revisionKey(project, input.Revision))
	if data == nil {
		return ErrConflict
	}
	if err := json.Unmarshal(data, &anchor); err != nil {
		return err
	}
	if anchor.Commit.ID != input.CommitID || anchor.Commit.StateSHA256 != input.SHA256 {
		return invalid("checkpoint differs from accepted state anchor")
	}
	bucket := tx.Bucket([]byte("checkpoints"))
	oldSize := int64(0)
	if data := bucket.Get([]byte(project)); data != nil {
		var old contracts.RelayCheckpointReceipt
		if err := json.Unmarshal(data, &old); err != nil {
			return err
		}
		if old.Checkpoint.Revision > input.Revision {
			return ErrConflict
		}
		if old.Checkpoint.Revision == input.Revision {
			if old.Checkpoint.SHA256 != input.SHA256 {
				return invalid("checkpoint identity reused")
			}
			*result = old
			return nil
		}
		oldSize = int64(len(data))
	}
	*result = contracts.RelayCheckpointReceipt{SchemaVersion: 1, ProjectID: project, Writer: contracts.RelayWriter(account, input.DeviceID), AcceptedAt: time.Now().UTC().Format(time.RFC3339Nano), Checkpoint: input, Anchor: anchor}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	usage := tx.Bucket([]byte("usage"))
	next := number(usage.Get([]byte(project))) - oldSize + int64(len(encoded))
	if next > MaxProjectBytes {
		return ErrQuota
	}
	if err := usage.Put([]byte(project), encodedNumber(next)); err != nil {
		return err
	}
	if err := bumpGeneration(tx, project); err != nil {
		return err
	}
	summary, _ := json.Marshal(result.Acknowledgement())
	if err := tx.Bucket([]byte("checkpoint-heads")).Put([]byte(project), summary); err != nil {
		return err
	}
	return bucket.Put([]byte(project), encoded)
}

// Generation advances only for a new published checkpoint. Intermediate commits
// do not wake Dashboard readers before a new view is available.
func (s *Store) Generation(project string) (int64, error) {
	var revision int64
	err := s.db.View(func(tx *bolt.Tx) error {
		revision = number(tx.Bucket([]byte("generations")).Get([]byte(project)))
		return nil
	})
	return revision, err
}
func bumpGeneration(tx *bolt.Tx, project string) error {
	bucket := tx.Bucket([]byte("generations"))
	id := []byte(project)
	return bucket.Put(id, encodedNumber(number(bucket.Get(id))+1))
}

// A lightweight preflight avoids fetching or echoing a full checkpoint when
// another device has already published it.
func (s *Store) CheckpointHead(project string) (contracts.RelayCheckpointHead, error) {
	result := contracts.RelayCheckpointHead{SchemaVersion: 1, ProjectID: project}
	if !validID(project) {
		return result, ErrInvalid
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		result.Head = number(tx.Bucket([]byte("heads")).Get([]byte(project)))
		result.Generation = number(tx.Bucket([]byte("generations")).Get([]byte(project)))
		if data := tx.Bucket([]byte("checkpoint-heads")).Get([]byte(project)); data != nil {
			return json.Unmarshal(data, &result.Checkpoint)
		}
		return nil
	})
	return result, err
}
