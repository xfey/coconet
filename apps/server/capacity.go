package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/xfey/coconet/packages/storageguard"
	bolt "go.etcd.io/bbolt"
)

func (s *store) guardedUpdate(action func(*bolt.Tx) error) error {
	return s.capacity.Run(false, func() error { return s.db.Update(action) })
}

func capacitySetting(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid server capacity setting: " + name)
	}
	return n, nil
}

func (s *store) configureCapacity() error {
	maximum, err := capacitySetting("COCONET_METADATA_BYTES", 4<<30)
	if err != nil {
		return err
	}
	minimum, err := capacitySetting("COCONET_MIN_FREE_BYTES", 2<<30)
	if err != nil {
		return err
	}
	path := ""
	if s.db != nil {
		path = s.db.Path()
	}
	root, err := relayRootForDatabase(path)
	if err != nil {
		return err
	}
	paths := []string{filepath.Join(root, "relay.db")}
	if path != "" {
		paths = append(paths, path)
	}
	s.capacity, err = storageguard.New(paths, maximum, minimum)
	return err
}
