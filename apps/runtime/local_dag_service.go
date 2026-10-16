package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
)

type localDAGWorkspace struct {
	Path      string `json:"path"`
	ProjectID string `json:"project_id"`
}
type localDAGServiceConfig struct {
	Managed        bool                `json:"-"`
	PauseReason    string              `json:"pause_reason,omitempty"`
	Schema         int                 `json:"schema"`
	Actor          string              `json:"actor"`
	Provider       string              `json:"provider"`
	Command        string              `json:"command"`
	Profile        string              `json:"profile"`
	Model          string              `json:"model"`
	MaxCalls       int                 `json:"max_calls"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	Workspaces     []localDAGWorkspace `json:"workspaces"`
}

func managedLocalDAGRoot() (string, error) { return localDAGRootAt(currentDirectory()) }

func explicitLocalDAGRoot() (string, error) {
	if root := os.Getenv("COCONET_LOCAL_DAG_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("COCONET_LOCAL_DAG_HOME must be absolute")
		}
		return filepath.Clean(root), nil
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.root, "local-dag"), nil
}

func loadLocalDAGService(root string) (localDAGServiceConfig, error) {
	if config, managed, err := loadConnectedLocalDAG(root); managed {
		return config, err
	}
	var config localDAGServiceConfig
	data, err := readLocalPrivateFile(filepath.Join(root, "runtime.json"), 64<<10, false)
	if err != nil {
		return config, err
	}
	if err := decodeStrict(data, &config); err != nil {
		return config, err
	}
	if config.Schema != 1 || config.Actor == "" || !contracts.ValidAgent(contracts.AgentType(config.Provider)) || config.Command == "" || !filepath.IsAbs(config.Profile) || config.Model == "" || config.MaxCalls < 1 || config.MaxCalls > 100 || config.TimeoutSeconds < 1 || config.TimeoutSeconds > 900 || len(config.Workspaces) > 100 {
		return config, errors.New("invalid managed Runtime inference configuration")
	}
	seen := map[string]bool{}
	for i, workspace := range config.Workspaces {
		if !filepath.IsAbs(workspace.Path) || workspace.ProjectID == "" {
			return config, errors.New("invalid managed workspace")
		}
		canonical, err := canonicalPath(workspace.Path)
		if err != nil {
			return config, err
		}
		if seen[canonical] {
			return config, errors.New("managed workspace has more than one project binding")
		}
		seen[canonical] = true
		config.Workspaces[i].Path = canonical
	}
	return config, nil
}

// Configured local workspaces use this inbox exclusively. The old entry is
// retained only during the migration until installation and bindings switch.
func enqueueManagedLocalHook(agent contracts.AgentType, event hookEvent) (bool, error) {
	cwd := event.CWD
	if agent == contracts.AgentClaude && filepath.IsAbs(os.Getenv("CLAUDE_PROJECT_DIR")) {
		cwd = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	if os.Getenv("COCONET_LOCAL_DAG_HOME") == "" {
		paths, err := resolveRuntimePaths()
		if err != nil {
			return true, err
		}
		binding, origin, _, err := hookSessionBinding(paths, agent, event)
		if err != nil {
			return true, err
		}
		if binding != nil && binding.Workflow == localDAGWorkflow {
			cwd = binding.CollaborationRoot
			if origin != "" {
				event.CWD = origin
			}
		}
	}
	root, err := localDAGRootAt(cwd)
	if err != nil {
		return true, err
	}
	config, err := loadLocalDAGService(root)
	if errors.Is(err, os.ErrNotExist) && !localDAGIsConnectedRoot(root) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	request, found, err := localDAGHookSource(root, config, agent, event)
	if err != nil || !found {
		return found, err
	}
	request.TranscriptPath, request.Event, request.ObservedAt = event.TranscriptPath, event.HookEventName, event.ObservedAt
	if request.Event == "" {
		request.Event = event.EventName
	}
	if strings.EqualFold(request.Event, "SessionStart") || !isSyncHookEvent(request.Event) {
		return true, nil
	}
	if _, err := time.Parse(time.RFC3339Nano, request.ObservedAt); err != nil {
		request.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := enqueueLocalDAGObservation(root, request); err != nil {
		return true, err
	}
	if os.Getenv("COCONET_DISABLE_BACKGROUND_SYNC") == "1" {
		return true, nil
	}
	return true, startManagedLocalDAG(root)
}

func localDAGHookSource(root string, config localDAGServiceConfig, agent contracts.AgentType, event hookEvent) (localDAGObservationRequest, bool, error) {
	var source localDAGObservationRequest
	if !adapters.ValidNativeSessionID(event.nativeID()) {
		return source, false, nil
	}
	profile, err := canonicalPath(libraryConfigRoot(agent))
	if err != nil {
		return source, true, err
	}
	path := localDAGOriginPath(root, config.Actor, agent, profile, event.nativeID())
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	release, err := acquireLocalFileLock(ctx, path+".lock")
	if err != nil {
		return source, true, err
	}
	defer release()
	data, err := readLocalPrivateFile(path, 64<<10, false)
	if err == nil {
		if err := decodeStrict(data, &source); err != nil {
			return source, true, err
		}
		if source.NativeID != event.nativeID() || source.Agent != agent || source.ConfigRoot != profile || !pathContains(source.Root, source.CWD) {
			return source, true, errors.New("invalid managed Session origin")
		}
		for _, workspace := range config.Workspaces {
			if workspace.ProjectID == source.ProjectID && samePath(workspace.Path, source.Root) {
				if source.EmptyStart && endsEmptyStart(event) {
					source.EmptyStart = false
					if err := atomicWriteJSON(path, source, 0600); err != nil {
						return source, true, err
					}
				}
				return source, true, nil
			}
		}
		return source, true, errors.New("Session origin is no longer connected; reconnect its original workspace")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return source, true, err
	}
	cwd := event.CWD
	if agent == contracts.AgentClaude && filepath.IsAbs(os.Getenv("CLAUDE_PROJECT_DIR")) {
		cwd = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	cwd, err = canonicalPath(cwd)
	if err != nil {
		return source, false, err
	}
	var selected *localDAGWorkspace
	for _, workspace := range config.Workspaces {
		if pathContains(workspace.Path, cwd) && (selected == nil || len(workspace.Path) > len(selected.Path)) {
			copy := workspace
			selected = &copy
		}
	}
	if selected == nil {
		return source, false, nil
	}
	source = localDAGObservationRequest{ProjectID: selected.ProjectID, Agent: agent, NativeID: event.nativeID(), Root: selected.Path, CWD: cwd, ConfigRoot: profile, EmptyStart: trackedEmptyStart(agent, profile, event)}
	if err := atomicWriteJSON(path, source, 0600); err != nil {
		return source, true, err
	}
	return source, true, nil
}

func startManagedLocalDAG(root string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if handled, code := probeManagedLocalDAG(ctx, root); handled {
		if code != 0 {
			return errors.New("local Runtime status check failed; inspect its control endpoint")
		}
		return nil
	}
	config, err := loadLocalDAGService(root)
	if err != nil {
		return err
	}
	// Hooks remain bounded. A concurrent launcher owns the responsibility for
	// starting the service; MCP readers wait for its authenticated endpoint.
	lockCtx, unlockCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer unlockCancel()
	release, err := acquireLocalFileLock(lockCtx, filepath.Join(root, "launch.lock"))
	if errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	if handled, code := probeManagedLocalDAG(ctx, root); handled {
		if code != 0 {
			return errors.New("local Runtime status check failed; inspect its control endpoint")
		}
		return nil
	}
	allowed, err := reserveLocalDAGLaunch(root, workgraph.Digest(struct {
		Config  localDAGServiceConfig
		Version string
	}{config, version}), time.Now())
	if err != nil || !allowed {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "_local-daemon", "--root", root)
	// The child owns rotation after acquiring its lifetime lock. Detached stdio
	// and process session also prevent an Agent's exit/Ctrl+C from killing it.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func runManagedLocalDAG(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "--root" || !filepath.IsAbs(args[1]) {
		return 2
	}
	root := args[1]
	if stale, err := localDAGOlderProcess(root); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	} else if stale {
		if err := stopConnectedLocalDAG(ctx, root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	lockCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	release, err := acquireLocalFileLock(lockCtx, filepath.Join(root, "managed-daemon.lock"))
	if errors.Is(err, context.DeadlineExceeded) {
		return 0 // another managed process already owns this root
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer release()
	log, err := openLocalDAGLog(filepath.Join(root, "daemon.log"), 2<<20)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer log.Close()
	stdout, stderr = io.MultiWriter(log, stdout), io.MultiWriter(log, stderr)
	config, err := loadLocalDAGService(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if config.Managed {
		for ctx.Err() == nil {
			code := serveConnectedLocalDAG(ctx, root, config, stdout, stderr)
			if code != localDAGReloadExit {
				return code
			}
			config, err = loadLocalDAGService(root)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		return 0
	}
	arguments := []string{"serve", "--root", root, "--actor", config.Actor, "--provider", config.Provider, "--command", config.Command, "--profile", config.Profile, "--model", config.Model, "--max-calls", strconv.Itoa(config.MaxCalls), "--timeout", strconv.Itoa(config.TimeoutSeconds) + "s"}
	return runLocalDAG(ctx, arguments, stdout, stderr)
}
