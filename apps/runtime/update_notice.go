package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xfey/coconet/packages/distribution"
	"golang.org/x/term"
)

type updateNotice struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
}

func readUpdateNotice(paths runtimePaths) updateNotice {
	var value updateNotice
	data, err := readLocalPrivateFile(filepath.Join(paths.root, "update-notice.json"), 4096, true)
	if err == nil {
		_ = json.Unmarshal(data, &value)
	}
	return value
}
func noticeDue(at, now time.Time) bool {
	return at.IsZero() || at.After(now) || now.Sub(at) >= 24*time.Hour
}

// Human commands only read the cache. Metadata checks run detached, at most
// daily; Hook, MCP and structured command output never carry update messages.
func maybeNotifyUpdate(args []string, stderr io.Writer) {
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "connect", "status", "login", "whoami", "version", "--version", "-v":
	default:
		return
	}
	for _, arg := range args {
		if arg == "--json" {
			return
		}
	}
	file, ok := stderr.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		return
	}
	cache, now := readUpdateNotice(paths), time.Now()
	var notified time.Time
	if data, err := readLocalPrivateFile(filepath.Join(paths.root, "update-reminded.json"), 256, true); err == nil {
		_ = json.Unmarshal(data, &notified)
	}
	if order, known := distribution.CompareVersions(version, cache.Latest); known && order < 0 && noticeDue(notified, now) {
		fmt.Fprintf(stderr, "Coconet %s is available. Run `coconet update` to update.\n", cache.Latest)
		// Concurrent reminders are harmless; no download is triggered here.
		_ = atomicWriteJSON(filepath.Join(paths.root, "update-reminded.json"), now, 0600)
	}
	if !noticeDue(cache.CheckedAt, now) {
		return
	}
	executable, err := currentRuntimeExecutable()
	if err != nil {
		return
	}
	command := exec.Command(executable, "_update-check")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if command.Start() == nil {
		_ = command.Process.Release()
	}
}
func runUpdateCheck(ctx context.Context) int {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	release, err := acquireLocalFileLock(ctx, filepath.Join(paths.root, "update-notice.lock"))
	if err != nil {
		return 1
	}
	defer release()
	cache, now := readUpdateNotice(paths), time.Now()
	if !noticeDue(cache.CheckedAt, now) {
		return 0
	}
	// Failed checks also wait a day, preventing offline retry storms.
	cache.CheckedAt = now
	if err := atomicWriteJSON(filepath.Join(paths.root, "update-notice.json"), cache, 0600); err != nil {
		return 1
	}
	latest, err := latestClientVersion(ctx)
	if err != nil {
		return 0
	}
	cache.Latest = latest
	if err := atomicWriteJSON(filepath.Join(paths.root, "update-notice.json"), cache, 0600); err != nil {
		return 1
	}
	return 0
}
