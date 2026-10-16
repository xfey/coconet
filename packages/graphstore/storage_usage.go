package graphstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	bolt "go.etcd.io/bbolt"
)

type StorageCategory struct {
	Files int   `json:"files"`
	Bytes int64 `json:"file_bytes"`
}
type DatabaseBucketUsage struct {
	Records int   `json:"records"`
	Bytes   int64 `json:"key_value_bytes"`
}
type StorageUsage struct {
	CacheUsage
	Layout                   map[string]StorageCategory     `json:"layout"`
	TotalFileBytes           int64                          `json:"total_file_bytes"`
	Symlinks                 int                            `json:"symlinks_not_followed"`
	DatabaseFreeBytes        int64                          `json:"database_free_bytes"`
	DatabasePendingFreeBytes int64                          `json:"database_pending_free_bytes"`
	Buckets                  map[string]DatabaseBucketUsage `json:"database_buckets"`
	Note                     string                         `json:"note"`
}

// StorageUsage measures the complete private scope, not just content blobs.
// This explicit inspection is bounded and never follows links into Agent homes.
// Concurrent file sizes are an observation, not a transactionally frozen backup.
func (s *Store) StorageUsage(ctx context.Context) (StorageUsage, error) {
	result := StorageUsage{CacheUsage: s.CacheUsage(), Layout: map[string]StorageCategory{}, Buckets: map[string]DatabaseBucketUsage{}, Note: "Logical file sizes, not allocated filesystem blocks; concurrent writes can change them. Bucket bytes count keys/values and exclude bbolt page overhead. Free database pages are reusable and remain inside graph.db until offline compaction."}
	count := 0
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 1000000 {
			return errors.New("storage inspection exceeds one million entries")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			result.Symlinks++
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		category, _, _ := strings.Cut(relative, string(os.PathSeparator))
		switch category {
		case "graph.db", "objects", "materialized", "inbox", "history", "forks", "inference-sources", "inference-work":
		default:
			category = "runtime_files"
		}
		usage := result.Layout[category]
		usage.Files++
		usage.Bytes += info.Size()
		result.Layout[category] = usage
		result.TotalFileBytes += info.Size()
		return nil
	})
	if err != nil {
		return result, err
	}
	stats := s.db.Stats()
	result.DatabaseFreeBytes = int64(stats.FreePageN) * int64(s.db.Info().PageSize)
	result.DatabasePendingFreeBytes = int64(stats.PendingPageN) * int64(s.db.Info().PageSize)
	err = s.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, bucket *bolt.Bucket) error {
			usage := DatabaseBucketUsage{}
			if err := bucket.ForEach(func(k, v []byte) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				usage.Records++
				usage.Bytes += int64(len(k) + len(v))
				return nil
			}); err != nil {
				return err
			}
			result.Buckets[string(name)] = usage
			return nil
		})
	})
	return result, err
}
