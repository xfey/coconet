package graphstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/xfey/coconet/packages/modelusage"
	bolt "go.etcd.io/bbolt"
)

func modelAttempt(run Run) bool {
	return (run.Kind == "stage" || run.Kind == "relations") && run.Provider != "" && run.Status != "deferred"
}

func retainModelUsage(run *Run) {
	if run.TokenUsage == nil && modelAttempt(*run) {
		usage := modelusage.Normalize(run.Provider, run.Model, run.Usage, run.Output)
		run.TokenUsage = &usage
	}
}

func (s *Store) ModelUsage(since time.Time) ([]modelusage.Attempt, error) {
	return readModelUsage(s.db, since)
}

// ReadModelUsage never starts a worker, creates a database or migrates user
// data. Live stores must be queried through their authenticated local endpoint.
func ReadModelUsage(root string, since time.Time) ([]modelusage.Attempt, error) {
	path := filepath.Join(root, "graph.db")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("Runtime database must be a regular file")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return readModelUsage(db, since)
}

func readModelUsage(db *bolt.DB, since time.Time) ([]modelusage.Attempt, error) {
	rows := []modelusage.Attempt{}
	err := db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("runs"))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, data []byte) error {
			var run Run
			if err := json.Unmarshal(data, &run); err != nil {
				return err
			}
			if !modelAttempt(run) {
				return nil
			}
			retainModelUsage(&run)
			task := run.TaskID
			if run.SharedTaskID != "" {
				task = run.SharedTaskID
			}
			rows = append(rows, modelusage.Attempt{ID: run.ID, TaskID: task, ProjectID: run.ProjectID, Provider: run.Provider, Kind: run.Kind, Status: run.Status, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Usage: *run.TokenUsage})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, rows[i].StartedAt)
		b, _ := time.Parse(time.RFC3339Nano, rows[j].StartedAt)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return rows[i].ID < rows[j].ID
	})
	seen := map[string]bool{}
	selected := make([]modelusage.Attempt, 0, len(rows))
	for _, row := range rows {
		identity := row.ProjectID + "\x00" + row.TaskID
		row.Retry = seen[identity]
		seen[identity] = true
		at, err := time.Parse(time.RFC3339Nano, row.StartedAt)
		// Unknown dates remain visible in all-time reports only.
		if !since.IsZero() && (err != nil || at.Before(since)) {
			continue
		}
		selected = append(selected, row)
	}
	return selected, nil
}
