package graphstore

import (
	"encoding/json"
	"errors"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

func MatchesEvidence(c workgraph.Commit, e contracts.RelayCommitEvidence) bool {
	return e.SchemaVersion == 1 && e.ProjectID == c.ProjectID && e.CommitID == c.ID && e.Revision == c.Base+1 && e.Writer == c.Actor && e.PayloadSHA256 == workgraph.Digest(c) && len(e.StateSHA256) == 64
}

// RecoverCheckpoint replaces only accepted graph state. Native content,
// unfinished tasks, runs and unaccepted proposals remain device-owned. Matching
// durable receipt evidence finalizes accepted outbox entries without replaying
// their effects over a newer graph or triggering another model call.
func (s *Store) RecoverCheckpoint(state workgraph.State, receipt contracts.RelayCheckpointReceipt, anchor workgraph.Commit, expected int64, accepted map[string]contracts.RelayCommitEvidence) error {
	release := s.PinContent()
	defer release()
	if err := workgraph.ValidateState(state); err != nil {
		return err
	}
	if state.Revision <= expected || state.ProjectID != receipt.ProjectID || state.Revision != receipt.Checkpoint.Revision || workgraph.Digest(state) != receipt.Checkpoint.SHA256 || ObjectDigest(receipt.Checkpoint.Payload) != receipt.Checkpoint.SHA256 || anchor.ID != receipt.Checkpoint.CommitID || anchor.ProjectID != state.ProjectID || anchor.Base+1 != state.Revision {
		return errors.New("recovery checkpoint differs")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		current, err := readState(tx, state.ProjectID)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return workgraph.ErrConflict
		}
		for id, evidence := range accepted {
			data := tx.Bucket([]byte("pending")).Get(key(state.ProjectID, id))
			var c workgraph.Commit
			if json.Unmarshal(data, &c) != nil || !MatchesEvidence(c, evidence) || evidence.Revision > state.Revision {
				return errors.New("pending commit changed during recovery")
			}
			if err := tx.Bucket([]byte("commits")).Put(key(state.ProjectID, id), data); err != nil {
				return err
			}
			change, _ := json.Marshal(workgraph.Change{CommitID: id, RunID: c.RunID, Before: c.Base, After: c.Base + 1, StateDigest: evidence.StateSHA256, Nodes: []string{}, Sessions: []string{}})
			if err := tx.Bucket([]byte("changes")).Put(key(state.ProjectID, id), change); err != nil {
				return err
			}
			if err := publishRun(tx, c); err != nil {
				return err
			}
			if err := tx.Bucket([]byte("pending")).Delete(key(state.ProjectID, id)); err != nil {
				return err
			}
		}
		data, _ := json.Marshal(receipt)
		if err := tx.Bucket([]byte("baselines")).Put([]byte(state.ProjectID), data); err != nil {
			return err
		}
		// A prior local archive baseline must not override the newer verified one.
		if err := tx.Bucket([]byte("replica-baselines")).Delete([]byte(state.ProjectID)); err != nil {
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
