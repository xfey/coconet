package contentstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type ObjectInfo struct {
	SHA256   string
	Size     int64
	Modified time.Time
}

// Inventory reports validated regular content objects. It never traverses
// symlinks or includes native histories, profile data or arbitrary directories.
func (s *Store) Inventory(ctx context.Context) ([]ObjectInfo, error) {
	result := []ObjectInfo{}
	directory := filepath.Join(s.root, "objects")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("content inventory requires a real directory")
	}
	prefixes, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	for _, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !prefix.IsDir() || len(prefix.Name()) != 2 {
			return nil, errors.New("unexpected content directory entry")
		}
		entries, err := os.ReadDir(filepath.Join(directory, prefix.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// In-flight temporary streams are not publication identities.
			if len(entry.Name()) > 9 && entry.Name()[:9] == ".pending-" {
				continue
			}
			path, err := s.objectPath(entry.Name())
			if err != nil || filepath.Dir(path) != filepath.Join(directory, prefix.Name()) {
				return nil, errors.New("unexpected content object name")
			}
			info, err := s.Stat(entry.Name())
			if err != nil {
				return nil, err
			}
			result = append(result, ObjectInfo{SHA256: entry.Name(), Size: info.Size(), Modified: info.ModTime()})
			if len(result) > 1000000 {
				return nil, errors.New("local content inventory exceeds one million objects")
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SHA256 < result[j].SHA256 })
	return result, nil
}
