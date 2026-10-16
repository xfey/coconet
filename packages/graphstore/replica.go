package graphstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

// ReplicaState is durable progress for one immutable source, not graph truth.
// Missing progress is reconstructed from accepted snapshots after a crash.
type ReplicaState struct {
	Source      workgraph.Source `json:"source"`
	Digest      string           `json:"digest"`
	TextReady   bool             `json:"text_ready"`
	NativeReady bool             `json:"native_ready"`
	Uploaded    bool             `json:"uploaded"`
	Attempts    int              `json:"attempts"`
	Error       string           `json:"error,omitempty"`
	NextAt      string           `json:"next_at,omitempty"`
	UpdatedAt   string           `json:"updated_at,omitempty"`
}

type ReplicaCoverage struct {
	Revision       int64 `json:"revision"`
	Expected       int   `json:"expected"`
	TextReady      int   `json:"text_ready"`
	NativeReady    int   `json:"native_ready"`
	PendingUploads int   `json:"pending_uploads"`
	Failed         int   `json:"failed"`
}

func (s *Store) Replica(project string, snapshot workgraph.Snapshot) (ReplicaState, error) {
	result := ReplicaState{Source: snapshot.Source, Digest: snapshot.Digest}
	err := s.db.View(func(tx *bolt.Tx) error {
		if data := tx.Bucket([]byte("replicas")).Get(key(project, snapshot.Source.Key())); data != nil {
			if err := json.Unmarshal(data, &result); err != nil {
				return err
			}
			if result.Digest != snapshot.Digest {
				return errors.New("replica identity differs from fixed source")
			}
		}
		return nil
	})
	return result, err
}

// SaveReplica rechecks the source in the same transaction as the progress write.
// A download completing after retirement must not resurrect a source or task.
func (s *Store) SaveReplica(project string, progress ReplicaState) error {
	if len(progress.Error) > 4000 || progress.Attempts < 0 {
		return errors.New("invalid replica progress")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		state, err := readState(tx, project)
		if err != nil {
			return err
		}
		snapshot, ok := state.Snapshots[progress.Source.Key()]
		if !ok || snapshot.Digest != progress.Digest {
			return workgraph.ErrSource
		}
		bucket := tx.Bucket([]byte("replicas"))
		id := key(project, progress.Source.Key())
		data, err := json.Marshal(progress)
		if err != nil {
			return err
		}
		if bytes.Equal(data, bucket.Get(id)) {
			return nil
		}
		progress.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		data, err = json.Marshal(progress)
		if err != nil {
			return err
		}
		return bucket.Put(id, data)
	})
}

func (s *Store) ReplicaCoverage(project, actor string) (ReplicaCoverage, error) {
	var result ReplicaCoverage
	err := s.db.View(func(tx *bolt.Tx) error {
		state, err := readState(tx, project)
		if err != nil {
			return err
		}
		retained := workgraph.RetainedSources(state)
		result.Revision, result.Expected = state.Revision, len(retained)
		for id, snapshot := range state.Snapshots {
			if !retained[id] {
				continue
			}
			var progress ReplicaState
			if data := tx.Bucket([]byte("replicas")).Get(key(project, snapshot.Source.Key())); data != nil {
				if err := json.Unmarshal(data, &progress); err != nil {
					return err
				}
				if progress.Digest != snapshot.Digest {
					return errors.New("replica identity differs")
				}
			}
			if progress.TextReady {
				result.TextReady++
			}
			if progress.NativeReady {
				result.NativeReady++
			}
			if snapshot.Owner == actor && !progress.Uploaded {
				result.PendingUploads++
			}
			if progress.Error != "" {
				result.Failed++
			}
		}
		return nil
	})
	return result, err
}

func (s *Store) PruneReplicaProgress(project string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		state, err := readState(tx, project)
		if err != nil {
			return err
		}
		prefix := key(project, "")
		cursor := tx.Bucket([]byte("replicas")).Cursor()
		for k, _ := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
			if _, exists := state.Snapshots[string(k[len(prefix):])]; !exists {
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
