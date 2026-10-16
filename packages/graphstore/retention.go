package graphstore

import (
	"encoding/json"

	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

// ProtectedSources returns only unfinished local work, not every historical
// diagnostic reference. Completed trace archives cannot pin raw sessions forever.
func (s *Store) ProtectedSources(project string) (map[string]bool, error) {
	protected := map[string]bool{}
	add := func(source *workgraph.Source) {
		if source != nil {
			protected[source.Key()] = true
		}
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte("tasks")).ForEach(func(_, data []byte) error {
			var task Task
			if err := json.Unmarshal(data, &task); err != nil {
				return err
			}
			if task.ProjectID == project {
				add(task.Source)
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket([]byte("runs")).ForEach(func(_, data []byte) error {
			var run Run
			if err := json.Unmarshal(data, &run); err != nil {
				return err
			}
			if run.ProjectID == project && (run.Status == "running" || run.Status == "responded" || run.Status == "ready") {
				add(run.Source)
				for _, source := range run.Protected {
					add(&source)
				}
			}
			return nil
		})
	})
	return protected, err
}
