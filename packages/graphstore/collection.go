package graphstore

import (
	"encoding/json"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

type CollectionProgress struct {
	Plan  *contracts.RelayCollectionPlan `json:"plan,omitempty"`
	After string                         `json:"after,omitempty"`
}

func (s *Store) CollectionProgress(project string) (CollectionProgress, error) {
	var result CollectionProgress
	err := s.db.View(func(tx *bolt.Tx) error {
		if data := tx.Bucket([]byte("settings")).Get(key("collection", project)); data != nil {
			return json.Unmarshal(data, &result)
		}
		return nil
	})
	return result, err
}
func (s *Store) SaveCollectionProgress(project string, progress CollectionProgress) error {
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("settings")).Put(key("collection", project), data) })
}
