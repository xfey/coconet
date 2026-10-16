package graphstore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

// QueueLibrary records an explicit, deterministic user action and its outbox
// atomically. There is no model outcome to recover between these writes.
func (s *Store) QueueLibrary(run Run, task Task, commit workgraph.Commit) error {
	if commit.Library == nil || commit.Library.Source == nil || run.Kind != "library" || run.ID != commit.RunID || run.ID != task.RunID || run.ProjectID != commit.ProjectID || run.ProjectID != task.ProjectID || run.TaskID != task.ID || run.Status != "ready" || task.Kind != "library" || task.Status != "publish_pending" || len(run.Input) > 16<<10 || !json.Valid(run.Input) || !json.Valid([]byte(run.Output)) {
		return errors.New("invalid Library publication")
	}
	run.Implementation = currentBuildProvenance()
	run.ArchiveReserved = diagnosticFinalReserve
	release := s.PinContent()
	defer release()
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("runs")).Get([]byte(run.ID)) != nil {
			return errors.New("Library request already exists")
		}
		state, err := readState(tx, run.ProjectID)
		if err != nil {
			return err
		}
		if _, _, err := workgraph.Apply(state, commit); err != nil {
			return err
		}
		room, _ := json.Marshal(run)
		if err := pruneDiagnostics(tx, time.Now().UTC(), int64(len(room))+run.ArchiveReserved); err != nil {
			return err
		}
		if err := putRun(tx, run); err != nil {
			return err
		}
		for name, value := range map[string]any{"tasks": task, "pending": commit, "proposals": Proposal{Commit: commit, Status: "pending"}} {
			id := commit.ID
			if name == "tasks" {
				id = task.ID
			}
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if err := tx.Bucket([]byte(name)).Put(key(run.ProjectID, id), data); err != nil {
				return err
			}
		}
		return nil
	})
}
