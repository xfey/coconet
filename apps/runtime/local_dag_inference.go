package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

type localInferenceSource struct {
	Schema  int    `json:"schema"`
	Agent   string `json:"agent"`
	Profile string `json:"profile"`
}

func localInferenceSourcePath(root, project, session string) string {
	return filepath.Join(root, "inference-sources", workgraph.Digest([]string{project, session})+".json")
}

// Capture fixes the local Agent profile before a task can become runnable.
// Executables are resolved from local registrations; none come from the Relay.
func rememberLocalInferenceSource(root string, engine *localdag.Engine, request localDAGObservationRequest) error {
	profile, err := canonicalPath(request.ConfigRoot)
	if err != nil {
		return err
	}
	session := localdag.ObservationSessionID(request.ProjectID, engine.Actor, string(request.Agent), request.NativeID)
	path := localInferenceSourcePath(root, request.ProjectID, session)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := acquireLocalFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	source := localInferenceSource{Schema: 1, Agent: string(request.Agent), Profile: profile}
	data, err := readLocalPrivateFile(path, 16<<10, true)
	if err == nil {
		var old localInferenceSource
		if err = decodeStrict(data, &old); err != nil {
			return err
		}
		if old != source {
			return errors.New("this conversation belongs to a different Agent profile")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWriteJSON(path, source, 0600)
}

func automaticInferenceResolver(root string) func(string, workgraph.Session) (inference.Runner, error) {
	return func(project string, session workgraph.Session) (inference.Runner, error) {
		var source localInferenceSource
		data, err := readLocalPrivateFile(localInferenceSourcePath(root, project, session.ID), 16<<10, true)
		if err != nil {
			return nil, fmt.Errorf("source Agent profile is unavailable; continue the original conversation to capture its source again: %w", err)
		}
		if err = decodeStrict(data, &source); err != nil {
			return nil, err
		}
		if source.Schema != 1 || source.Agent != session.Agent || !filepath.IsAbs(source.Profile) {
			return nil, errors.New("invalid source Agent profile")
		}
		paths, err := resolveRuntimePaths()
		if err != nil {
			return nil, err
		}
		state, _, err := loadAgentIntegrationState(paths)
		if err != nil {
			return nil, err
		}
		matches := []agentIntegration{}
		for _, entry := range state.Integrations {
			if entry.Type != agentplugin.Host(source.Agent) {
				continue
			}
			profile, err := canonicalPath(entry.ConfigRoot)
			if err != nil {
				continue
			}
			if profile == source.Profile {
				matches = append(matches, entry)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("cannot identify the %s installation for this conversation; inspect coconet agent list --verbose", source.Agent)
		}
		executable, err := exec.LookPath(matches[0].Command)
		if err != nil {
			return nil, fmt.Errorf("the source %s executable is unavailable; check its installation", source.Agent)
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return nil, err
		}
		return inference.Local{Config: inference.Config{Provider: source.Agent, Command: executable, ConfigRoot: source.Profile, WorkRoot: filepath.Join(root, "inference-work"), Timeout: 120 * time.Second}}, nil
	}
}
