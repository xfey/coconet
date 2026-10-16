package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const drainLockRetryInterval = 20 * time.Millisecond

// Retain the prior lock filename during upgrade without opening its database.
func acquireAccountLock(ctx context.Context, paths runtimePaths, deploymentID string) (func() error, error) {
	return acquireLocalFileLock(ctx, deploymentSyncPath(paths, deploymentID)+".drain.lock")
}
func acquireLocalFileLock(ctx context.Context, path string) (func() error, error) {
	return acquireLocalFileLockMode(ctx, path, unix.LOCK_EX)
}

func acquireLocalFileLockMode(ctx context.Context, path string, mode int) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return nil, err
	}
	for {
		err = unix.Flock(int(lock.Fd()), mode|unix.LOCK_NB)
		if err == nil {
			return func() error {
				unlockErr := unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				closeErr := lock.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = lock.Close()
			return nil, err
		}
		timer := time.NewTimer(drainLockRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = lock.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
