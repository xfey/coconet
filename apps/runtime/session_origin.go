package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

// Local routing metadata only. Neither cwd changes nor a resumed invocation
// may silently transfer an existing Session to another connected Project.
type sessionOrigin struct {
	AgentType       contracts.AgentType    `json:"agent_type"`
	NativeSessionID string                 `json:"native_session_id"`
	ConfigRoot      string                 `json:"config_root"`
	CWD             string                 `json:"cwd"`
	Binding         contracts.LocalBinding `json:"binding"`
}

func hookSessionBinding(paths runtimePaths, agent contracts.AgentType, event hookEvent) (*contracts.LocalBinding, string, string, error) {
	configRoot, err := filepath.Abs(libraryConfigRoot(agent))
	if err != nil {
		return nil, "", "", err
	}
	if canonical, err := filepath.EvalSymlinks(configRoot); err == nil {
		configRoot = canonical
	}
	if agent != contracts.AgentClaude || !adapters.ValidNativeSessionID(event.nativeID()) {
		binding, err := bindingForAnyCWD(paths, event.CWD)
		return binding, event.CWD, configRoot, err
	}
	key := adapters.Digest(string(agent) + "\x00" + configRoot + "\x00" + event.nativeID())
	path := filepath.Join(paths.root, "session-origins", key+".json")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	unlock, err := acquireLocalFileLock(ctx, path+".lock")
	if err != nil {
		return nil, "", "", err
	}
	defer unlock()
	var origin sessionOrigin
	content, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(content, &origin); err != nil {
			return nil, "", "", err
		}
		if origin.AgentType != agent || origin.NativeSessionID != event.nativeID() || origin.ConfigRoot != configRoot || !filepath.IsAbs(origin.CWD) || !pathContains(origin.Binding.CollaborationRoot, origin.CWD) {
			return nil, "", "", errors.New("invalid local Session origin")
		}
		binding, err := bindingAtRoot(paths, origin.Binding.CollaborationRoot)
		if err != nil || binding == nil {
			return nil, "", "", err
		}
		if binding.DeploymentID != origin.Binding.DeploymentID || binding.ProjectID != origin.Binding.ProjectID || binding.AccountID != origin.Binding.AccountID || binding.ServerPublicKeySHA256 != origin.Binding.ServerPublicKeySHA256 {
			return nil, "", "", errors.New("Session origin no longer matches its connected Project")
		}
		return binding, origin.CWD, configRoot, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", "", err
	}
	cwd := event.CWD
	confirmed := strings.EqualFold(event.HookEventName, "SessionStart") || strings.EqualFold(event.EventName, "SessionStart")
	// Startup root is a fallback for sessions already running during upgrade.
	// Once pinned, the persisted origin wins even across resume/worktree moves.
	if startup := os.Getenv("CLAUDE_PROJECT_DIR"); filepath.IsAbs(startup) {
		cwd = startup
		confirmed = true
	}
	cwd, err = canonicalPath(cwd)
	if err != nil {
		return nil, "", "", err
	}
	binding, err := bindingForAnyCWD(paths, cwd)
	if err != nil || binding == nil {
		return nil, "", "", err
	}
	if !confirmed {
		// Without a startup hint, the Adapter must still validate SDK cwd against
		// this provisional binding; do not bless a dynamic directory as origin.
		return binding, "", configRoot, nil
	}
	origin = sessionOrigin{AgentType: agent, NativeSessionID: event.nativeID(), ConfigRoot: configRoot, CWD: cwd, Binding: *binding}
	if err := atomicWriteJSON(path, origin, 0o600); err != nil {
		return nil, "", "", err
	}
	return binding, cwd, configRoot, nil
}
