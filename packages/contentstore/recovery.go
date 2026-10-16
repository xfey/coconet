package contentstore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CleanupInterruptedStreams requires the caller to hold its store's exclusive
// database/process lock. It must never run concurrently with receive.
func (s *Store) CleanupInterruptedStreams() error {
	directory := filepath.Join(s.root, "objects")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("content recovery requires a real object directory")
	}
	prefixes, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(prefixes) > 256 {
		return errors.New("invalid content prefix inventory")
	}
	for _, prefix := range prefixes {
		if !prefix.IsDir() || len(prefix.Name()) != 2 || strings.Trim(prefix.Name(), "0123456789abcdef") != "" {
			return errors.New("invalid content prefix directory")
		}
		if err := CleanupPrivateTemps(filepath.Join(directory, prefix.Name()), []string{".pending-"}); err != nil {
			return err
		}
	}
	return nil
}

// CleanupPrivateTemps removes only regular files matching the exact numeric
// names emitted by os.CreateTemp in a caller-owned directory. Unknown entries
// and subdirectories are never recursively deleted. Caller excludes writers.
func CleanupPrivateTemps(directory string, prefixes []string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("temporary recovery requires a real owned directory")
	}
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	changed, count := false, 0
	for {
		entries, err := file.ReadDir(512)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 1000000 {
				return errors.New("temporary recovery inventory exceeds capacity")
			}
			match := false
			for _, prefix := range prefixes {
				if prefix == "" {
					return errors.New("temporary prefix must not be empty")
				}
				if strings.HasPrefix(entry.Name(), prefix) {
					suffix := strings.TrimPrefix(entry.Name(), prefix)
					match = suffix != "" && strings.Trim(suffix, "0123456789") == ""
					break
				}
			}
			if !match {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("interrupted temporary is not a regular file")
			}
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
			changed = true
		}
		if err == io.EOF {
			break
		}
	}
	if changed {
		return file.Sync()
	}
	return nil
}
