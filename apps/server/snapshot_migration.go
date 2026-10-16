package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Schema migration may create immutable objects before its metadata transaction
// commits. This durable journal makes a crash between those writes recoverable.
type migrationObjectJournal struct {
	Backend string   `json:"backend"`
	Keys    []string `json:"keys"`
}
type migrationContentStore struct {
	artifactStore
	path    string
	journal migrationObjectJournal
}

func (m *migrationContentStore) Write(ctx context.Context, object artifactObject, data []byte) error {
	writer, ok := m.artifactStore.(artifactWriter)
	if !ok {
		return errors.New("migration content backend is not writable")
	}
	if !validArtifactObjectKey(object.Key) {
		return errors.New("invalid migration object")
	}
	m.journal.Keys = append(m.journal.Keys, object.Key)
	dataJournal, err := json.Marshal(m.journal)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".migration-journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(dataJournal)
	if err = errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), m.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(m.path))
	if err != nil {
		return err
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return err
	}
	return writer.Write(ctx, object, data)
}
func (s *store) recoverMigrationObjects() error {
	path := s.db.Path() + ".migration-objects.json"
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal migrationObjectJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return err
	}
	if s.contents == nil || journal.Backend != s.contents.BackendIdentity() {
		return errors.New("migration journal belongs to another content backend")
	}
	referenced := map[string]bool{}
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(stateBucket)
		if bucket == nil {
			return nil
		}
		content := bucket.Get(stateKey)
		if content == nil && tx.Bucket(recordsBucket) != nil {
			var err error
			content, err = exportRecordJSON(tx)
			if err != nil {
				return err
			}
		}
		if content == nil {
			return nil
		}
		state, err := decodeStateJSON(content)
		if err != nil {
			return err
		}
		for _, o := range state.ContentObjects.All() {
			referenced[o.ObjectKey] = true
		}
		return nil
	}); err != nil {
		return err
	}
	deleter, ok := s.contents.(artifactDeletionStore)
	if !ok {
		return errors.New("migration recovery requires content deletion support")
	}
	for _, key := range journal.Keys {
		if !validArtifactObjectKey(key) {
			return errors.New("migration journal has invalid object identity")
		}
		if referenced[key] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := deleter.Delete(ctx, key)
		cancel()
		if err != nil {
			return err
		}
	}
	return os.Remove(path)
}
