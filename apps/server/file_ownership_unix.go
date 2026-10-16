//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func preserveExistingFileOwnership(existingPath, replacementPath string) error {
	info, err := os.Stat(existingPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing file ownership: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("existing file ownership is unavailable")
	}
	if err := os.Chown(replacementPath, int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("preserve existing file ownership: %w", err)
	}
	return nil
}
