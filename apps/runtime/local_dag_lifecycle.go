package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type localDAGLaunch struct {
	Configuration string    `json:"configuration"`
	NotBefore     time.Time `json:"not_before"`
}

// The caller holds launch.lock. Failed startup is retried after a short fixed
// cooldown, not on every Hook. Configuration changes bypass the old cooldown.
func reserveLocalDAGLaunch(root, configuration string, now time.Time) (bool, error) {
	path := filepath.Join(root, "launch.json")
	data, err := readLocalPrivateFile(path, 4096, true)
	if err == nil {
		var previous localDAGLaunch
		if err := json.Unmarshal(data, &previous); err != nil {
			return false, err
		}
		// A backward clock jump must not suspend startup indefinitely.
		if previous.Configuration == configuration && previous.NotBefore.After(now) && previous.NotBefore.Sub(now) <= 5*time.Second {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, atomicWriteJSON(path, localDAGLaunch{Configuration: configuration, NotBefore: now.Add(5 * time.Second)}, 0600)
}

// Runtime operational output is bounded independently from durable processing
// archives. Only the process holding managed-daemon.lock opens this writer.
type localDAGLog struct {
	mu    sync.Mutex
	path  string
	file  *os.File
	size  int64
	limit int64
}

func openLocalDAGLog(path string, limit int64) (*localDAGLog, error) {
	if limit < 1 {
		return nil, errors.New("invalid operational log limit")
	}
	log := &localDAGLog{path: path, limit: limit}
	if err := log.open(); err != nil {
		return nil, err
	}
	if log.size > limit {
		// Bound old append-only logs during upgrade; keep only a tail, never
		// copy an arbitrarily large file into the rotation archive.
		if err := log.compact(); err != nil {
			log.Close()
			return nil, err
		}
	}
	if info, err := os.Lstat(path + ".1"); err == nil && (!info.Mode().IsRegular() || info.Size() > limit) {
		// This is the one owned rotation filename, not a recursive cleanup.
		if err := os.Remove(path + ".1"); err != nil {
			log.Close()
			return nil, err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Close()
		return nil, err
	}
	return log, nil
}

func (l *localDAGLog) open() error {
	if info, err := os.Lstat(l.path); err == nil && !info.Mode().IsRegular() {
		return errors.New("Runtime log must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	l.file, l.size = file, info.Size()
	return nil
}

func (l *localDAGLog) compact() error {
	tail := make([]byte, l.limit)
	if _, err := l.file.ReadAt(tail, l.size-l.limit); err != nil {
		return err
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	n, err := l.file.Write(tail)
	l.size = int64(n)
	return err
}

func (l *localDAGLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return 0, os.ErrClosed
	}
	total := 0
	for len(p) > 0 {
		if l.size == l.limit {
			if err := l.file.Close(); err != nil {
				return total, err
			}
			l.file = nil
			if err := os.Rename(l.path, l.path+".1"); err != nil {
				return total, err
			}
			if err := l.open(); err != nil {
				return total, err
			}
		}
		size := min(int64(len(p)), l.limit-l.size)
		n, err := l.file.Write(p[:size])
		total, l.size, p = total+n, l.size+int64(n), p[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func (l *localDAGLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Hooks only launch a replacement process; waiting for shutdown is performed
// by that detached process, never inside the Agent Hook deadline.
func localDAGOlderProcess(root string) (bool, error) {
	data, err := readLocalPrivateFile(filepath.Join(root, "control.json"), 4096, true)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var endpoint localDAGEndpoint
	if err := decodeStrict(data, &endpoint); err != nil {
		return false, err
	}
	return endpoint.Version != version, nil
}
func probeManagedLocalDAG(ctx context.Context, root string) (bool, int) {
	handled, code := localDAGClient(ctx, root, "status", localDAGClientOptions{}, io.Discard, io.Discard)
	if !handled || code != 0 {
		return handled, code
	}
	stale, err := localDAGOlderProcess(root)
	if err != nil {
		return true, 1
	}
	return !stale, 0
}

func resumeConnectedLocalDAGs(ctx context.Context, paths runtimePaths, deployment string) error {
	if os.Getenv("COCONET_DISABLE_BACKGROUND_SYNC") == "1" {
		return nil
	}
	bindings, err := loadBindingState(paths)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, binding := range bindings.Bindings {
		if binding.Workflow != localDAGWorkflow || binding.DeploymentID != deployment {
			continue
		}
		root := localDAGBindingRoot(paths, binding)
		if seen[root] {
			continue
		}
		seen[root] = true
		// Wait for the existing process to close, then start with current sign-in.
		if err := stopConnectedLocalDAG(ctx, root); err != nil {
			return err
		}
		if err := startManagedLocalDAG(root); err != nil {
			return err
		}
	}
	return nil
}

func stopDeploymentLocalDAGs(ctx context.Context, paths runtimePaths, deployment string) error {
	bindings, err := loadBindingState(paths)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, binding := range bindings.Bindings {
		if binding.Workflow != localDAGWorkflow || binding.DeploymentID != deployment {
			continue
		}
		root := localDAGBindingRoot(paths, binding)
		if seen[root] {
			continue
		}
		seen[root] = true
		if err := stopConnectedLocalDAG(ctx, root); err != nil {
			return err
		}
	}
	return nil
}
