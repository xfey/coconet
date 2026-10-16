package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xfey/coconet/packages/agentplugin"
)

const defaultAgentsFilename = "agent-defaults.json"

// Track default discovery separately from the registry. Upgrades seed newly
// supported hosts once and never restore an integration the user removed.
type defaultAgentSetupState struct {
	SchemaVersion int `json:"schema_version"`
}

func runAgentSetup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "unexpected Agent setup arguments")
		return 2
	}
	report := agentplugin.SetupReport{Status: "ok", Results: []agentplugin.InstallResult{}}
	paths, err := resolveRuntimePaths()
	if err == nil {
		report.Results, err = seedDefaultAgentIntegrations(paths)
	}
	if err == nil {
		err = syncAgentIntegrationsWithProgress(ctx, paths, false, stderr)
	}
	if err == nil {
		var state agentIntegrationState
		state, _, err = loadAgentIntegrationState(paths)
		for _, integration := range state.Integrations {
			report.Results = append(report.Results, agentplugin.InstallResult{
				Host: integration.Type, Command: integration.Command, Version: integration.AgentVersion,
				Status: agentplugin.Status(integration.Status), Error: integration.LastError, Warning: integration.LastWarning,
			})
		}
	}
	if err != nil {
		report.Status = "partial"
		report.Error = err.Error()
	}
	for _, result := range report.Results {
		if result.Status != agentplugin.StatusConfigured && result.Status != agentplugin.StatusSkipped {
			report.Status = "partial"
		}
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if report.Status == "partial" {
		return 1
	}
	return 0
}

func seedDefaultAgentIntegrations(paths runtimePaths) ([]agentplugin.InstallResult, error) {
	failures := []agentplugin.InstallResult{}
	marker := filepath.Join(paths.root, defaultAgentsFilename)
	priorSchema := 0
	content, err := os.ReadFile(marker)
	if err == nil {
		var state defaultAgentSetupState
		if err := decodeStrict(content, &state); err != nil || (state.SchemaVersion != 1 && state.SchemaVersion != 2) {
			return failures, errors.New("invalid default Agent setup state")
		}
		if state.SchemaVersion == 2 {
			return failures, nil
		}
		priorSchema = state.SchemaVersion
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failures, fmt.Errorf("read default Agent setup state: %w", err)
	}
	state, _, err := loadAgentIntegrationState(paths)
	if err != nil {
		return failures, err
	}
	hosts := []agentplugin.Host{agentplugin.HostCodex, agentplugin.HostClaude, agentplugin.HostCursor}
	if priorSchema == 1 {
		hosts = []agentplugin.Host{agentplugin.HostCursor}
	}
	for _, host := range hosts {
		command := string(host)
		if host == agentplugin.HostCursor {
			command = "agent"
		}
		root, err := agentplugin.DefaultConfigRoot(host)
		var integration agentIntegration
		if err == nil {
			integration, err = newAgentIntegration(host, command, root)
		}
		if err != nil {
			failures = append(failures, agentplugin.InstallResult{Host: host, Command: command, Status: agentplugin.StatusFailed, Error: err.Error()})
			continue
		}
		// Preserve an explicit command that already owns this host/config pair.
		registered := false
		for _, existing := range state.Integrations {
			if existing.Type == host && samePath(existing.ConfigRoot, integration.ConfigRoot) {
				registered = true
				break
			}
		}
		if !registered {
			state = putAgentIntegration(state, integration)
		}
	}
	// Save before configuring hosts. Retries reuse these identities, while the
	// marker prevents later upgrades from reversing an explicit Agent removal.
	if err := saveAgentIntegrationState(paths, state); err != nil {
		return failures, err
	}
	if len(failures) == 0 {
		if err := atomicWriteJSON(marker, defaultAgentSetupState{SchemaVersion: 2}, 0o600); err != nil {
			return failures, err
		}
	}
	return failures, nil
}
