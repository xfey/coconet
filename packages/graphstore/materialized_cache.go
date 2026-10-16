package graphstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const defaultMaterializedLimit int64 = 1 << 30
const maxMaterializedEntries = 4096

var ErrMaterializedQuota = errors.New("local derived session files reached their cache limit; inspect coconet storage status, run storage prune, or increase --exports-mib")

type materializedEntry struct {
	path      string
	info      os.FileInfo
	temporary bool
}

func (s *Store) materializedDirectory() (string, error) {
	path := filepath.Join(s.root, "materialized")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("derived session files require a real cache directory")
	}
	return path, nil
}
func (s *Store) materializedInventory() ([]materializedEntry, error) {
	// Merely opening the Runtime or querying a graph must not materialize a
	// conversation. The directory is created only by an explicit fixed read.
	if _, err := os.Lstat(filepath.Join(s.root, "materialized")); errors.Is(err, os.ErrNotExist) {
		return []materializedEntry{}, nil
	} else if err != nil {
		return nil, err
	}
	directory, err := s.materializedDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	result := []materializedEntry{}
	for _, entry := range entries {
		name := entry.Name()
		temporary := strings.HasPrefix(name, ".artifact-") || strings.HasPrefix(name, ".conversation-")
		digest := strings.TrimSuffix(name, ".jsonl")
		decoded, decodeErr := hex.DecodeString(digest)
		if !temporary && (decodeErr != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest) {
			return nil, errors.New("unexpected derived cache filename")
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("derived cache entries must be regular files")
		}
		result = append(result, materializedEntry{path: filepath.Join(directory, name), info: info, temporary: temporary})
	}
	return result, nil
}
func (s *Store) initializeMaterialized() error {
	entries, err := s.materializedInventory()
	if err != nil {
		return err
	}
	var total int64
	for _, entry := range entries {
		// Exclusive graph database ownership proves these incomplete derived files
		// belonged to a previous process. They are never original Agent artifacts.
		if entry.temporary {
			if err := os.Remove(entry.path); err != nil {
				return err
			}
			continue
		}
		total += entry.info.Size()
	}
	s.materializedBytes.Store(total)
	limit := defaultMaterializedLimit
	if err := s.db.View(func(tx *bolt.Tx) error {
		if data := tx.Bucket([]byte("settings")).Get([]byte("materialized-limit")); data != nil {
			return json.Unmarshal(data, &limit)
		}
		return nil
	}); err != nil {
		return err
	}
	if limit < 16<<20 || limit > 1<<40 {
		return errors.New("invalid derived session cache limit")
	}
	s.materializedLimit.Store(limit)
	return nil
}

// Called with materializedMu and a content pin, before linking a new file.
func (s *Store) allowMaterialized(path string, size int64) (bool, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("derived cache is not a regular file")
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, err := s.materializedInventory()
	if err != nil {
		return false, err
	}
	count := 0
	var total int64
	for _, entry := range entries {
		if !entry.temporary {
			total += entry.info.Size()
			count++
		}
	}
	s.materializedBytes.Store(total)
	if count >= maxMaterializedEntries || size > s.materializedLimit.Load()-total {
		return false, ErrMaterializedQuota
	}
	return true, nil
}
func (s *Store) touchMaterialized(path string) error { return os.Chtimes(path, time.Now(), time.Now()) }
func (s *Store) pruneMaterialized(ctx context.Context, now time.Time) (CacheCollection, error) {
	var result CacheCollection
	s.materializedMu.Lock()
	defer s.materializedMu.Unlock()
	entries, err := s.materializedInventory()
	if err != nil {
		return result, err
	}
	var total int64
	for _, entry := range entries {
		if !entry.temporary {
			total += entry.info.Size()
		}
	}
	s.materializedBytes.Store(total)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !entry.temporary && now.Sub(entry.info.ModTime()) < localObjectProtection {
			continue
		}
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		if !entry.temporary {
			s.materializedBytes.Add(-entry.info.Size())
		}
		result.Removed++
		result.Bytes += entry.info.Size()
		if result.Removed >= 512 {
			break
		}
	}
	if result.Removed > 0 {
		dir, err := os.Open(filepath.Join(s.root, "materialized"))
		if err != nil {
			return result, err
		}
		defer dir.Close()
		if err := dir.Sync(); err != nil {
			return result, err
		}
	}
	return result, nil
}
func (s *Store) SetStorageLimits(objects, exports int64) error {
	if objects != 0 && (objects < 16<<20 || objects > 1<<40) || exports != 0 && (exports < 16<<20 || exports > 1<<40) {
		return errors.New("storage limits must be within 16 MiB–1 TiB")
	}
	s.objectQuotaMu.Lock()
	defer s.objectQuotaMu.Unlock()
	s.materializedMu.Lock()
	defer s.materializedMu.Unlock()
	if objects == 0 {
		objects = s.objectLimit.Load()
	}
	if exports == 0 {
		exports = s.materializedLimit.Load()
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		for key, value := range map[string]int64{"object-limit": objects, "materialized-limit": exports} {
			data, _ := json.Marshal(value)
			if err := tx.Bucket([]byte("settings")).Put([]byte(key), data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.objectLimit.Store(objects)
	s.materializedLimit.Store(exports)
	return nil
}
