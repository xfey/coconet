// Package storageguard serializes capacity admission across local metadata stores.
package storageguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrCapacity = errors.New("server metadata capacity reached")
var ErrSpace = errors.New("server disk reserve reached")

// Headroom covers the largest bounded checkpoint transaction, COW pages and
// bbolt's 16 MiB file growth increment. This is an admission budget, not a
// filesystem quota; other host processes can still consume disk concurrently.
const Headroom int64 = 64 << 20

type Guard struct {
	mu                     sync.Mutex
	reserved               int64
	Paths                  []string
	MaxBytes, MinFreeBytes int64
	space                  func(string) (int64, error)
}

type Status struct {
	MetadataBytes  int64 `json:"metadata_bytes"`
	MaxBytes       int64 `json:"metadata_limit_bytes"`
	MinFreeBytes   int64 `json:"minimum_free_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

func New(paths []string, maximum, minimum int64) (*Guard, error) {
	if len(paths) == 0 || maximum < 2*Headroom || minimum < Headroom {
		return nil, errors.New("invalid metadata capacity policy")
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || seen[filepath.Clean(path)] {
			return nil, errors.New("capacity paths must be distinct absolute paths")
		}
		seen[filepath.Clean(path)] = true
	}
	return &Guard{Paths: append([]string(nil), paths...), MaxBytes: maximum, MinFreeBytes: minimum, space: available}, nil
}

func (g *Guard) Status() (Status, error) {
	status := Status{MaxBytes: g.MaxBytes, MinFreeBytes: g.MinFreeBytes, AvailableBytes: 1<<63 - 1}
	for _, path := range g.Paths {
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return status, errors.New("metadata path is not a regular file")
			}
			status.MetadataBytes += info.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			return status, err
		}
		root := filepath.Dir(path)
		for {
			_, err := os.Stat(root)
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) || root == filepath.Dir(root) {
				return status, err
			}
			root = filepath.Dir(root)
		}
		free, err := g.space(root)
		if err != nil {
			return status, fmt.Errorf("inspect storage capacity: %w", err)
		}
		status.AvailableBytes = min(status.AvailableBytes, free)
	}
	return status, nil
}

// Run checks before invoking any mutation. Reclamation bypasses the metadata
// ceiling and operating reserve but still requires transaction headroom.
func (g *Guard) Run(reclaim bool, action func() error) error {
	if g == nil {
		return action()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s, err := g.Status()
	if err != nil {
		return err
	}
	minimum := g.MinFreeBytes + Headroom
	if reclaim {
		minimum = Headroom
	}
	if s.AvailableBytes-g.reserved < minimum {
		return ErrSpace
	}
	if !reclaim && s.MetadataBytes > g.MaxBytes-Headroom {
		return ErrCapacity
	}
	return action()
}

// Reserve accounts for concurrent filesystem uploads while their bytes are
// streamed outside database locks. External object stores do not call this.
func (g *Guard) Reserve(size int64) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	if size < 0 {
		return nil, errors.New("invalid disk reservation")
	}
	err := g.Run(false, func() error {
		s, err := g.Status()
		if err != nil {
			return err
		}
		if size > s.AvailableBytes-g.reserved-g.MinFreeBytes-Headroom {
			return ErrSpace
		}
		g.reserved += size
		return nil
	})
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); defer g.mu.Unlock(); g.reserved -= size }) }, nil
}

// Exclusive provides a common snapshot boundary for both metadata databases.
// Its callback must not call Run recursively.
func (g *Guard) Exclusive(action func() error) error {
	if g == nil {
		return errors.New("coordinated storage guard is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return action()
}
