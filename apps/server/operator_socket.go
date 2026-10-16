package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/storageguard"
)

// The socket sits inside the private deployment directory, mode 0600. It is
// never mounted on the public HTTP router or authorized with project tokens.
func startOperatorSocket(ctx context.Context, config serverConfig, s *store) (func(), error) {
	if s.db == nil {
		return func() {}, nil
	}
	root := filepath.Dir(config.DataPath)
	path := filepath.Join(root, "operator.sock")
	if configured := os.Getenv("COCONET_OPERATOR_SOCKET"); configured != "" {
		if !filepath.IsAbs(configured) || filepath.Dir(configured) != root {
			return nil, errors.New("operator socket must be inside the private deployment directory")
		}
		path = configured
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("operator path is not a socket")
		}
		conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil, errors.New("operator socket is already active")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	var backupMu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/storage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		capacity, err := s.capacity.Status()
		if err != nil {
			http.Error(w, "capacity inspection failed", 500)
			return
		}
		relay, err := s.relayStore()
		if err != nil {
			http.Error(w, "Relay unavailable", 500)
			return
		}
		maintenance, err := relay.MaintenanceStatus()
		if err != nil {
			http.Error(w, "maintenance inspection failed", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "capacity": capacity, "maintenance": maintenance})
	})
	mux.HandleFunc("/backup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if !backupMu.TryLock() {
			http.Error(w, "backup already running", 409)
			return
		}
		defer backupMu.Unlock()
		var input struct {
			Name string `json:"name"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !safeBackupName(input.Name) {
			http.Error(w, "invalid backup name", 400)
			return
		}
		backupRoot := filepath.Join(root, "backups", "automatic")
		if err := privateBackupDirectory(backupRoot); err != nil {
			http.Error(w, "backup directory unavailable", 500)
			return
		}
		directory := filepath.Join(backupRoot, input.Name)
		if err := os.Mkdir(directory, 0700); err != nil {
			http.Error(w, "backup destination already exists or is unavailable", 409)
			return
		}
		attempt, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := s.backupOnline(attempt, config, filepath.Join(directory, "server.db")); err != nil {
			// Only this newly-created incomplete private directory is disposable.
			_ = os.RemoveAll(directory)
			http.Error(w, "online backup failed; inspect server operator logs", 503)
			fmt.Fprintf(os.Stderr, "online backup failed: %v\n", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "name": input.Name, "complete": true})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = server.Serve(listener) }()
	return func() { _ = server.Close(); _ = listener.Close() }, nil
}

func safeBackupName(name string) bool {
	if len(name) < 1 || len(name) > 80 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func privateBackupDirectory(path string) error {
	// Validate each component before walking it; deployment parent is private.
	parent := filepath.Dir(path)
	for _, p := range []string{parent, path} {
		if err := os.Mkdir(p, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("backup directory must be private and non-symlink")
		}
	}
	return nil
}

func (s *store) backupOnline(ctx context.Context, config serverConfig, output string) error {
	if s.db == nil || s.capacity == nil {
		return errors.New("online backup requires a guarded local authority database")
	}
	relay, err := s.relayStore()
	if err != nil {
		return err
	}
	status, err := s.capacity.Status()
	if err != nil {
		return err
	}
	// Reserve for the copy and bbolt compaction overhead before pausing writes.
	release, err := s.capacity.Reserve(2*status.MetadataBytes + storageguard.Headroom)
	if err != nil {
		return err
	}
	defer release()
	return s.capacity.Exclusive(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The current relay-only server has no legacy artifact inventory. Keep
		// old artifact GC/copy semantics on the offline path instead of claiming
		// an online complete backup while external legacy objects can disappear.
		if err := s.view(func(state databaseState) error {
			inventory, err := backupInventory(state)
			if err != nil {
				return err
			}
			if len(inventory) > 0 {
				return errors.New("legacy content requires offline complete backup")
			}
			return nil
		}); err != nil {
			return err
		}
		return backupCompleteLocked(ctx, config, s.contents, output, s.db, relay.BackupLiveMetadata)
	})
}
