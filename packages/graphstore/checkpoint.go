package graphstore

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

var ErrBootstrapBusy = errors.New("checkpoint bootstrap requires an empty project without local work")

// CanBootstrap avoids fetching a remote cache when the local project needs log
// recovery. InstallCheckpoint repeats this check atomically before writing.
func (s *Store) CanBootstrap(project string) (bool, error) {
	err := s.db.View(func(tx *bolt.Tx) error { return bootstrapReady(tx, project) })
	if errors.Is(err, ErrBootstrapBusy) {
		return false, nil
	}
	return err == nil, err
}

func bootstrapReady(tx *bolt.Tx, project string) error {
	if tx.Bucket([]byte("heads")).Get([]byte(project)) != nil {
		return ErrBootstrapBusy
	}
	current, err := readState(tx, project)
	if err != nil {
		return err
	}
	if current.Revision != 0 || len(current.Sessions)+len(current.Nodes)+len(current.Snapshots)+len(current.Library) != 0 {
		return ErrBootstrapBusy
	}
	prefix := key(project, "")
	for _, name := range []string{"pending", "tasks", "commits", "proposals"} {
		k, _ := tx.Bucket([]byte(name)).Cursor().Seek(prefix)
		if bytes.HasPrefix(k, prefix) {
			return ErrBootstrapBusy
		}
	}
	return tx.Bucket([]byte("runs")).ForEach(func(_, data []byte) error {
		var r Run
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.ProjectID == project {
			return ErrBootstrapBusy
		}
		return nil
	})
}

// AcceptedRevision reads the small accepted head without decoding the DAG.
func (s *Store) AcceptedRevision(project string) (int64, error) {
	var revision int64
	err := s.db.View(func(tx *bolt.Tx) error {
		if data := tx.Bucket([]byte("heads")).Get([]byte(project)); data != nil {
			var head struct {
				Base int64 `json:"base"`
			}
			if err := json.Unmarshal(data, &head); err != nil {
				return err
			}
			revision = head.Base + 1
		}
		return nil
	})
	return revision, err
}

// CheckpointState reads state and its last accepted commit atomically.
func (s *Store) CheckpointState(project string) (workgraph.State, workgraph.Commit, error) {
	var state workgraph.State
	var head workgraph.Commit
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		state, err = readState(tx, project)
		if err != nil || state.Revision == 0 {
			return err
		}
		if data := tx.Bucket([]byte("heads")).Get([]byte(project)); data != nil {
			return json.Unmarshal(data, &head)
		}
		// Stores made before checkpoints still have complete immutable history.
		cursor := tx.Bucket([]byte("commits")).Cursor()
		prefix := key(project, "")
		for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cursor.Next() {
			var c workgraph.Commit
			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}
			if c.Base+1 == state.Revision {
				head = c
				return nil
			}
		}
		return errors.New("accepted head is missing")
	})
	return state, head, err
}

// InstallCheckpoint preserves the bootstrap receipt and baseline for replay.
// It cannot hide unfinished local attempts, even when revision is still zero.
func (s *Store) InstallCheckpoint(state workgraph.State, receipt contracts.RelayCheckpointReceipt, anchor workgraph.Commit) error {
	release := s.PinContent()
	defer release()
	if err := workgraph.ValidateState(state); err != nil {
		return err
	}
	if state.ProjectID != receipt.ProjectID || state.Revision != receipt.Checkpoint.Revision || workgraph.Digest(state) != receipt.Checkpoint.SHA256 || ObjectDigest(receipt.Checkpoint.Payload) != receipt.Checkpoint.SHA256 || anchor.ProjectID != state.ProjectID || anchor.Base+1 != state.Revision || anchor.ID != receipt.Checkpoint.CommitID {
		return errors.New("checkpoint baseline differs")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := bootstrapReady(tx, state.ProjectID); err != nil {
			return err
		}
		data, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if err := tx.Bucket([]byte("baselines")).Put([]byte(state.ProjectID), data); err != nil {
			return err
		}
		data, _ = json.Marshal(anchor)
		if err := tx.Bucket([]byte("heads")).Put([]byte(state.ProjectID), data); err != nil {
			return err
		}
		data, _ = json.Marshal(state)
		return tx.Bucket([]byte("states")).Put([]byte(state.ProjectID), data)
	})
}

// ReplayBase returns the authenticated bootstrap baseline, or an empty graph
// for stores retaining the complete log. This is not historical revalidation.
func (s *Store) ReplayBase(project string) (workgraph.State, error) {
	state := workgraph.New(project)
	err := s.db.View(func(tx *bolt.Tx) error {
		backup, err := replicaReplayBase(tx, project)
		if err != nil {
			return err
		}
		if backup != nil {
			state = *backup
			return nil
		}
		if data := tx.Bucket([]byte("baselines")).Get([]byte(project)); data != nil {
			var receipt contracts.RelayCheckpointReceipt
			if err := json.Unmarshal(data, &receipt); err != nil {
				return err
			}
			if ObjectDigest(receipt.Checkpoint.Payload) != receipt.Checkpoint.SHA256 {
				return errors.New("checkpoint archive corrupted")
			}
			if err := json.Unmarshal(receipt.Checkpoint.Payload, &state); err != nil {
				return err
			}
			return workgraph.ValidateState(state)
		}
		return nil
	})
	return state, err
}

func (s *Store) PublishedCheckpoint(project string) (int64, error) {
	var revision int64
	err := s.db.View(func(tx *bolt.Tx) error {
		if data := tx.Bucket([]byte("settings")).Get(key("checkpoint_published", project)); data != nil {
			return json.Unmarshal(data, &revision)
		}
		return nil
	})
	return revision, err
}
func (s *Store) MarkCheckpointPublished(project string, revision int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("settings"))
		id := key("checkpoint_published", project)
		var previous int64
		if data := bucket.Get(id); data != nil {
			if err := json.Unmarshal(data, &previous); err != nil {
				return err
			}
		}
		if previous >= revision {
			return nil
		}
		data, _ := json.Marshal(revision)
		return bucket.Put(id, data)
	})
}
