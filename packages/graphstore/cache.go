package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	bolt "go.etcd.io/bbolt"
)

const defaultLocalObjectLimit int64 = 16 << 30

var ErrLocalObjectQuota = errors.New("local Session replica storage is full; retained copies are protected. Increase the storage --max-mib limit or free local disk space before retrying")

type CacheUsage struct {
	IndexBytes         int64 `json:"index_bytes"`
	IndexDatabaseLimit int64 `json:"index_database_limit"`
	ObjectsBytes       int64 `json:"objects_bytes"`
	ObjectsLimit       int64 `json:"objects_limit"`
	ExportsBytes       int64 `json:"exports_bytes"`
	ExportsLimit       int64 `json:"exports_limit"`
}

// Pins cover complete multi-object operations, not just individual file reads.
// Nested operations are allowed; maintenance skips active work rather than
// waiting behind a model or download with a database transaction open.
func (s *Store) PinContent() func() {
	s.cacheMu.Lock()
	s.contentPins++
	s.cacheMu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.cacheMu.Lock(); s.contentPins--; s.cacheMu.Unlock() }) }
}
func (s *Store) initializeCache() error {
	objects, err := s.objects.Inventory(context.Background())
	if err != nil {
		return err
	}
	var total int64
	for _, object := range objects {
		total += object.Size
	}
	s.objectBytes.Store(total)
	limit := defaultLocalObjectLimit
	if err := s.db.View(func(tx *bolt.Tx) error {
		if data := tx.Bucket([]byte("settings")).Get([]byte("object-limit")); data != nil {
			return json.Unmarshal(data, &limit)
		}
		return nil
	}); err != nil {
		return err
	}
	if limit < 16<<20 || limit > 1<<40 {
		return errors.New("invalid local object limit")
	}
	s.objectLimit.Store(limit)
	return s.initializeMaterialized()
}
func (s *Store) CacheUsage() CacheUsage {
	var indexBytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Lstat(filepath.Join(s.root, "session-index.sqlite"+suffix)); err == nil && info.Mode().IsRegular() {
			indexBytes += info.Size()
		}
	}
	return CacheUsage{IndexBytes: indexBytes, IndexDatabaseLimit: 2 << 30, ObjectsBytes: s.objectBytes.Load(), ObjectsLimit: s.objectLimit.Load(), ExportsBytes: s.materializedBytes.Load(), ExportsLimit: s.materializedLimit.Load()}
}
func (s *Store) SetObjectLimit(limit int64) error {
	if limit < 16<<20 || limit > 1<<40 {
		return errors.New("local object limit must be 16 MiB–1 TiB")
	}
	s.objectQuotaMu.Lock()
	defer s.objectQuotaMu.Unlock()
	data, _ := json.Marshal(limit)
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("settings")).Put([]byte("object-limit"), data) }); err != nil {
		return err
	}
	s.objectLimit.Store(limit)
	return nil
}
