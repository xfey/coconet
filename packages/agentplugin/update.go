package agentplugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const StatusDisabled Status = "disabled"

// Update preserves removals and disabled registrations made in the host. An
// explicit `coconet agent add` still uses Install to restore a requested plugin.
func (m Manager) Update(ctx context.Context, detection Detection, source string) InstallResult {
	if !detection.Detected {
		return m.Install(ctx, detection, source)
	}
	result := InstallResult{Host: detection.Host, Command: detection.Command, Version: detection.Version, Status: StatusDisabled}
	enabled, err := m.integrationEnabled(ctx, detection)
	if err != nil {
		result.Status = StatusFailed
		result.Error = err.Error()
		return result
	}
	if !enabled {
		return result
	}
	return m.Install(ctx, detection, source)
}

func (m Manager) integrationEnabled(ctx context.Context, detection Detection) (bool, error) {
	if detection.Host == HostCursor {
		root, err := m.cursorRoot()
		if err != nil {
			return false, err
		}
		owned, err := cursorOwnedPlugin(filepath.Join(root, "plugins", "local", "coconet"))
		if err != nil || !owned {
			return false, err
		}
		data, err := os.ReadFile(filepath.Join(root, "hooks.json"))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var config struct {
			Hooks map[string][]json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return false, err
		}
		for _, event := range []string{"sessionStart", "beforeSubmitPrompt", "stop", "sessionEnd"} {
			found := false
			for _, entry := range config.Hooks[event] {
				if strings.Contains(string(entry), "/plugins/local/coconet/scripts/hook.sh") {
					found = true
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	}
	output, err := m.run(ctx, detection.Path, verificationArguments(detection.Host)...)
	if err != nil {
		return false, errors.New(formatCommandError("inspect existing Coconet Plugin", output, err))
	}
	if detection.Host == HostCodex {
		var list struct {
			Installed []struct {
				ID        string `json:"pluginId"`
				Installed bool   `json:"installed"`
				Enabled   bool   `json:"enabled"`
			} `json:"installed"`
		}
		if err := decodeAgentJSON(output.Stdout, &list); err != nil {
			return false, err
		}
		for _, p := range list.Installed {
			if p.ID == "coconet@coconet" {
				return p.Installed && p.Enabled, nil
			}
		}
		return false, nil
	}
	type entry struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	var list []entry
	if err := decodeAgentJSON(output.Stdout, &list); err != nil {
		var wrapper struct {
			Installed []entry `json:"installed"`
		}
		if err := decodeAgentJSON(output.Stdout, &wrapper); err != nil {
			return false, err
		}
		list = wrapper.Installed
	}
	for _, p := range list {
		if p.ID == "coconet@coconet" {
			return p.Enabled, nil
		}
	}
	return false, nil
}
