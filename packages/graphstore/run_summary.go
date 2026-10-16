package graphstore

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

type RunSummary struct {
	SharedTaskID string             `json:"shared_task_id,omitempty"`
	InputSHA256  string             `json:"input_sha256,omitempty"`
	Executor     string             `json:"executor,omitempty"`
	SourceOwner  string             `json:"source_owner,omitempty"`
	ID           string             `json:"id"`
	TaskID       string             `json:"task_id"`
	ProjectID    string             `json:"project_id"`
	Kind         string             `json:"kind"`
	Status       string             `json:"status"`
	StartedAt    string             `json:"started_at"`
	FinishedAt   string             `json:"finished_at,omitempty"`
	Error        string             `json:"error,omitempty"`
	Template     string             `json:"template,omitempty"`
	CommitID     string             `json:"commit_id,omitempty"`
	Pinned       bool               `json:"pinned,omitempty"`
	Archive      *DiagnosticArchive `json:"archive,omitempty"`
}
type RunPage struct {
	Runs []RunSummary `json:"runs"`
	Next string       `json:"next,omitempty"`
}

func runBefore(a, b RunSummary) bool {
	at, _ := time.Parse(time.RFC3339Nano, a.StartedAt)
	bt, _ := time.Parse(time.RFC3339Nano, b.StartedAt)
	if at.Equal(bt) {
		return a.ID > b.ID
	}
	return at.After(bt)
}

// Only metadata is decoded and retained. Pagination does not materialize all
// provider inputs and responses, and pinned/expired traces remain discoverable.
func (s *Store) RunSummaries(project, before string, limit int) (RunPage, error) {
	page := RunPage{Runs: []RunSummary{}}
	if project == "" || limit < 1 || limit > 100 {
		return page, errors.New("run pages require a project and a limit within 1–100")
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("runs"))
		var boundary RunSummary
		if before != "" {
			data := bucket.Get([]byte(before))
			if data == nil {
				return errors.New("diagnostic cursor is unavailable")
			}
			if err := json.Unmarshal(data, &boundary); err != nil {
				return err
			}
			if boundary.ProjectID != project {
				return errors.New("diagnostic cursor belongs to another project")
			}
		}
		return bucket.ForEach(func(_, data []byte) error {
			var summary RunSummary
			if err := json.Unmarshal(data, &summary); err != nil {
				return err
			}
			if summary.ProjectID != project || before != "" && !runBefore(boundary, summary) {
				return nil
			}
			index := sort.Search(len(page.Runs), func(i int) bool { return runBefore(summary, page.Runs[i]) })
			if index > limit {
				return nil
			}
			page.Runs = append(page.Runs, RunSummary{})
			copy(page.Runs[index+1:], page.Runs[index:])
			page.Runs[index] = summary
			if len(page.Runs) > limit+1 {
				page.Runs = page.Runs[:limit+1]
			}
			return nil
		})
	})
	if len(page.Runs) > limit {
		page.Runs = page.Runs[:limit]
		page.Next = page.Runs[limit-1].ID
	}
	return page, err
}
