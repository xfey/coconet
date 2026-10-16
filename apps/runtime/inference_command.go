package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func runInferenceCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runInferenceCommandWithInput(ctx, args, nil, stdout, stderr)
}
func runInferenceCommandWithInput(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "status" && args[0] != "pause" && args[0] != "resume") {
		fmt.Fprintln(stderr, "usage: coconet inference <status|pause|resume>. Conversations use their source Agent's default model and sign-in automatically.")
		return 2
	}
	_, binding, paths, err := currentProjectBinding()
	if err != nil || binding == nil || binding.Workflow != localDAGWorkflow {
		fmt.Fprintln(stderr, "Connect this folder to a project first.")
		return 1
	}
	root := localDAGBindingRoot(paths, *binding)
	control, err := loadInferenceControl(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if args[0] == "status" {
		config, err := loadLocalDAGService(root)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		state, _, err := loadAgentIntegrationState(paths)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		agents := []map[string]string{}
		for _, entry := range state.Integrations {
			agents = append(agents, map[string]string{"agent": string(entry.Type), "command": entry.Command, "status": string(entry.Status)})
		}
		if err = json.NewEncoder(stdout).Encode(map[string]any{"mode": "automatic", "model": "Agent default", "paused": config.PauseReason != "", "reason": config.PauseReason, "agents": agents}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	release, err := acquireLocalFileLock(ctx, filepath.Join(root, "settings.lock"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer release()
	if err = stopConnectedLocalDAG(ctx, root); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	control.Paused = args[0] == "pause"
	if err = atomicWriteJSON(filepath.Join(root, "inference-control.json"), control, 0600); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if os.Getenv("COCONET_DISABLE_BACKGROUND_SYNC") != "1" {
		if err = startManagedLocalDAG(root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if control.Paused {
		writeCLISuccess(stdout, "Local processing paused")
	} else {
		writeCLISuccess(stdout, "Local processing resumed with each Agent's default model")
	}
	return 0
}

func stopConnectedLocalDAG(ctx context.Context, root string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if handled, code := localDAGClient(ctx, root, "stop", localDAGClientOptions{}, io.Discard, io.Discard); handled && code != 0 {
		return errors.New("could not stop local Runtime; inspect its control endpoint")
	}
	unlock, err := acquireLocalFileLock(ctx, filepath.Join(root, "managed-daemon.lock"))
	if err != nil {
		return fmt.Errorf("waiting for Runtime shutdown: %w", err)
	}
	unlock()
	return nil
}

// Resolve existing ancestors without creating or reading an Agent profile.
func canonicalPlannedPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
		return "", err
	}
	parent, err := canonicalPlannedPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}
