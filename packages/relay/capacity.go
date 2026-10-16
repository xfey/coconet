package relay

import (
	"github.com/xfey/coconet/packages/storageguard"
	bolt "go.etcd.io/bbolt"
)

func (s *Store) update(action func(*bolt.Tx) error) error {
	return s.guard.Run(false, func() error { return s.db.Update(action) })
}
func (s *Store) reclaim(action func(*bolt.Tx) error) error {
	return s.guard.Run(true, func() error { return s.db.Update(action) })
}

// CapacityGuard is installed before serving; it coordinates both databases.
func (s *Store) CapacityGuard() *storageguard.Guard { return s.guard }
