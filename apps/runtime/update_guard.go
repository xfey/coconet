package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"github.com/xfey/coconet/packages/distribution"
	"golang.org/x/sys/unix"
)

// Readers hold work.lock through result persistence. The updater first owns
// intent.lock, so new work cannot starve a drain, then waits for existing work.
func beginRuntimeActivity(ctx context.Context, root string) (func() error, error) {
	gate := runtimeActivityRoot(root)
	intent, err := acquireLocalFileLockMode(ctx, filepath.Join(gate, "update-intent.lock"), unix.LOCK_SH)
	if err != nil {
		return nil, err
	}
	defer intent()
	return acquireLocalFileLockMode(ctx, filepath.Join(gate, "update-work.lock"), unix.LOCK_SH)
}

func runtimeActivityRoot(root string) string {
	if localDAGIsConnectedRoot(root) {
		if paths, err := resolveRuntimePaths(); err == nil {
			return paths.root
		}
	}
	return root
}

func acquireRuntimeUpdate(ctx context.Context, root string) (func(), error) {
	intent, err := acquireLocalFileLock(ctx, filepath.Join(root, "update-intent.lock"))
	if err != nil {
		return nil, err
	}
	work, err := acquireLocalFileLock(ctx, filepath.Join(root, "update-work.lock"))
	if err != nil {
		intent()
		return nil, err
	}
	return func() { work(); intent() }, nil
}

func installationScopeRoots(paths runtimePaths) ([]string, error) {
	roots := []string{filepath.Join(paths.root, "local-dag")}
	entries, err := os.ReadDir(filepath.Join(paths.root, "local-dag", "scopes"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			roots = append(roots, filepath.Join(paths.root, "local-dag", "scopes", entry.Name()))
		}
	}
	if root := os.Getenv("COCONET_LOCAL_DAG_HOME"); root != "" {
		roots = append(roots, root)
	}
	return roots, nil
}

// Bootstrap keeps stdin open while activating the staged bundle. EOF, signals
// or a failed installation all release the barrier and restart stopped scopes
// through the actual current pointer. No task deadline is imposed by update.
func runInstallGuard(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || !filepath.IsAbs(args[0]) {
		return 2
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stdin); close(done); cancel() }()
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	serialize, err := acquireLocalFileLock(ctx, filepath.Join(paths.root, "installation.lock"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer serialize()
	gateRoots := []string{paths.root, filepath.Join(paths.root, "local-dag")}
	if root := os.Getenv("COCONET_LOCAL_DAG_HOME"); root != "" {
		gateRoots = append(gateRoots, filepath.Clean(root))
	}
	for i, root := range gateRoots {
		canonical, err := canonicalPlannedPath(root)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		gateRoots[i] = canonical
	}
	slices.Sort(gateRoots)
	gateRoots = slices.Compact(gateRoots)
	releases := []func(){}
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	// Freeze launches before listing scopes; a concurrently created scope may
	// enqueue work but cannot publish an endpoint during activation.
	for _, root := range gateRoots {
		unlock, err := acquireLocalFileLock(ctx, filepath.Join(root, "update-launch.lock"))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		releases = append(releases, func() { _ = unlock() })
	}
	roots, err := installationScopeRoots(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	live := []string{}
	for _, root := range roots {
		handled, code := localDAGClient(ctx, root, "status", localDAGClientOptions{}, io.Discard, io.Discard)
		if !handled {
			continue
		}
		if code != 0 {
			fmt.Fprintln(stderr, "Cannot verify an existing Runtime. Inspect coconet status --verbose before updating.")
			return 1
		}
		data, err := readLocalPrivateFile(filepath.Join(root, "control.json"), 4096, true)
		var endpoint localDAGEndpoint
		if err != nil || decodeStrict(data, &endpoint) != nil || endpoint.UpdateProtocol != 1 {
			quotedRoot := "'" + strings.ReplaceAll(root, "'", "'\"'\"'") + "'"
			fmt.Fprintln(stderr, "This running Runtime predates safe update draining. Finish ongoing processing and handoffs, stop it with `coconet local-dag stop --root "+quotedRoot+"`, then retry the update. The installed version is unchanged.")
			return 1
		}
		live = append(live, root)
	}
	for _, root := range gateRoots {
		release, err := acquireRuntimeUpdate(ctx, root)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		releases = append(releases, release)
	}
	stopped := []string{}
	defer func() {
		// New processes may wait at the held barrier until this function returns.
		if os.Getenv("COCONET_DISABLE_BACKGROUND_SYNC") == "1" {
			return
		}
		for _, root := range stopped {
			cmd := exec.Command(filepath.Join(args[0], "current", "bin", "coconet"), "_local-daemon", "--root", root)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err == nil {
				_ = cmd.Process.Release()
			} else {
				fmt.Fprintln(stderr, "Could not restart Runtime; the next Coconet command will retry:", err)
			}
		}
	}()
	for _, root := range live {
		if err := stopConnectedLocalDAG(ctx, root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		stopped = append(stopped, root)
	}
	if err := json.NewEncoder(stdout).Encode(map[string]bool{"ready": true}); err != nil {
		return 1
	}
	select {
	case <-done:
		return 0
	case <-ctx.Done():
		select {
		case <-done:
			return 0
		default:
			return 1
		}
	}
}

// Managed startup holds the launch barrier until its endpoint is published.
type runtimeStartupKey struct{}

func holdRuntimeStartup(ctx context.Context, root string) (context.Context, func(), error) {
	unlock, err := acquireLocalFileLockMode(ctx, filepath.Join(runtimeActivityRoot(root), "update-launch.lock"), unix.LOCK_SH)
	if err != nil {
		return ctx, func() {}, err
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = unlock() }) }
	return context.WithValue(ctx, runtimeStartupKey{}, release), release, nil
}

func releaseRuntimeStartup(ctx context.Context) {
	if release, ok := ctx.Value(runtimeStartupKey{}).(func()); ok {
		release()
	}
}

// Follow the installation's current pointer only when this executable belongs
// to a versioned install. Tests and source builds never select a user's binary.
func currentRuntimeExecutable() (string, error) {
	own, err := os.Executable()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(own))))
	if filepath.Base(filepath.Dir(own)) != "bin" || filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(own)))) != "versions" {
		return own, nil
	}
	candidate := filepath.Join(root, "current", "bin", "coconet")
	manifest, err := distribution.LoadManifest(filepath.Join(root, "current"))
	if errors.Is(err, os.ErrNotExist) {
		return own, nil
	}
	if err != nil {
		return "", err
	}
	order, known := distribution.CompareVersions(version, manifest.Version)
	if !known || order >= 0 {
		return own, nil
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	return resolved, nil
}
