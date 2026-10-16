package agentplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const pluginID = "coconet@coconet"

type cleanupResult struct {
	backupPath      string
	sectionsRemoved int
	cacheRemoved    bool
}

func (manager Manager) Uninstall(ctx context.Context, detection Detection, configRoot string) UninstallResult {
	result := UninstallResult{
		Host: detection.Host, Command: detection.Command, Version: detection.Version,
		Status: StatusSkipped, Warning: detection.Warning,
	}
	if !validHost(detection.Host) || strings.TrimSpace(configRoot) == "" {
		result.Status = StatusFailed
		result.Error = "Agent uninstallation input is invalid"
		return result
	}
	if detection.Host == HostCursor {
		return manager.uninstallCursor(result)
	}
	if !detection.Detected {
		present, err := ownedStatePresent(detection.Host, configRoot)
		if err != nil {
			result.Status = StatusFailed
			result.Error = fmt.Sprintf("inspect Coconet Agent state failed: %v", err)
		} else if present {
			result.Status = StatusFailed
			result.Error = "Agent command was not detected but Coconet integration state remains; provide the correct Agent command"
		}
		return result
	}
	if detection.Path == "" {
		result.Status = StatusFailed
		result.Error = "Agent uninstallation input is invalid"
		return result
	}
	for _, step := range uninstallSteps(detection.Host) {
		output, err := manager.run(ctx, detection.Path, step.arguments...)
		if err != nil && !alreadyAbsent(output) {
			result.Status = StatusFailed
			result.Error = formatCommandError(step.name, output, err)
			return result
		}
	}
	verification, err := manager.run(ctx, detection.Path, verificationArguments(detection.Host)...)
	if err != nil {
		result.Status = StatusFailed
		result.Error = formatCommandError("verify Coconet Plugin removal", verification, err)
		return result
	}
	if err := verifyRemoved(detection.Host, verification.Stdout); err != nil {
		result.Status = StatusFailed
		result.Error = err.Error()
		return result
	}
	cleanup, err := cleanupOwnedState(detection.Host, configRoot)
	if err != nil {
		result.Status = StatusFailed
		result.Error = fmt.Sprintf("clean Coconet Agent state failed: %v", err)
		return result
	}
	result.Status = StatusRemoved
	result.ConfigBackup = cleanup.backupPath
	result.ConfigSectionsRemoved = cleanup.sectionsRemoved
	result.CacheRemoved = cleanup.cacheRemoved
	return result
}

func DefaultConfigRoot(host Host) (string, error) {
	if !validHost(host) {
		return "", errors.New("unsupported Agent host")
	}
	if host == HostCursor {
		if root := strings.TrimSpace(os.Getenv("CURSOR_CONFIG_DIR")); root != "" {
			return filepath.Abs(root)
		}
		if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
			return filepath.Abs(filepath.Join(root, "cursor"))
		}
		home, err := os.UserHomeDir()
		return filepath.Join(home, ".cursor"), err
	}
	if host == HostCodex {
		if root := strings.TrimSpace(os.Getenv("CODEX_HOME")); root != "" {
			return filepath.Abs(root)
		}
	} else if root := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); root != "" {
		return filepath.Abs(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	directory := ".codex"
	if host == HostClaude {
		directory = ".claude"
	}
	return filepath.Join(home, directory), nil
}

func uninstallSteps(host Host) []installStep {
	if host == HostCodex {
		return []installStep{
			{name: "remove Codex Plugin", arguments: []string{"plugin", "remove", pluginID, "--json"}},
			{name: "remove Codex Marketplace", arguments: []string{"plugin", "marketplace", "remove", "coconet", "--json"}},
		}
	}
	return []installStep{
		{name: "uninstall Claude Plugin", arguments: []string{"plugin", "uninstall", pluginID, "--scope", "user"}},
		{name: "remove Claude Marketplace", arguments: []string{"plugin", "marketplace", "remove", "coconet", "--scope", "user"}},
	}
}

func alreadyAbsent(output CommandOutput) bool {
	detail := strings.ToLower(string(combinedOutput(output)))
	return strings.Contains(detail, "not found") ||
		strings.Contains(detail, "not installed") ||
		strings.Contains(detail, "not configured or installed")
}

func verifyRemoved(host Host, content []byte) error {
	if host == HostCodex {
		var list struct {
			Installed []struct {
				PluginID  string `json:"pluginId"`
				Installed bool   `json:"installed"`
				Enabled   bool   `json:"enabled"`
			} `json:"installed"`
		}
		if err := decodeAgentJSON(content, &list); err != nil {
			return errors.New("Codex Plugin removal verification returned invalid JSON")
		}
		for _, plugin := range list.Installed {
			if plugin.PluginID == pluginID && (plugin.Installed || plugin.Enabled) {
				return errors.New("Codex still reports coconet@coconet as installed or enabled")
			}
		}
		return nil
	}
	type claudePlugin struct {
		ID string `json:"id"`
	}
	var installed []claudePlugin
	if err := decodeAgentJSON(content, &installed); err != nil {
		var list struct {
			Installed []claudePlugin `json:"installed"`
		}
		if objectErr := decodeAgentJSON(content, &list); objectErr != nil {
			return errors.New("Claude Plugin removal verification returned invalid JSON")
		}
		installed = list.Installed
	}
	for _, plugin := range installed {
		if plugin.ID == pluginID {
			return errors.New("Claude still reports coconet@coconet as installed")
		}
	}
	return nil
}

func cleanupOwnedState(host Host, configRoot string) (cleanupResult, error) {
	root, err := filepath.Abs(configRoot)
	if err != nil {
		return cleanupResult{}, err
	}
	result := cleanupResult{}
	if host == HostCodex {
		result.backupPath, result.sectionsRemoved, err = cleanupCodexConfig(filepath.Join(root, "config.toml"))
		if err != nil {
			return cleanupResult{}, err
		}
	}
	cachePath := filepath.Join(root, "plugins", "cache", "coconet")
	if _, err := os.Lstat(cachePath); err == nil {
		if err := os.RemoveAll(cachePath); err != nil {
			return cleanupResult{}, err
		}
		result.cacheRemoved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return cleanupResult{}, err
	}
	return result, nil
}

func ownedStatePresent(host Host, configRoot string) (bool, error) {
	root, err := filepath.Abs(configRoot)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(filepath.Join(root, "plugins", "cache", "coconet")); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if host == HostCodex {
		content, err := os.ReadFile(filepath.Join(root, "config.toml"))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		_, removed := stripOwnedTOMLTables(content)
		return removed != 0, nil
	}
	installed, err := readJSONObject(filepath.Join(root, "plugins", "installed_plugins.json"))
	if err != nil {
		return false, err
	}
	if plugins, ok := installed["plugins"].(map[string]any); ok {
		if _, exists := plugins[pluginID]; exists {
			return true, nil
		}
	}
	marketplaces, err := readJSONObject(filepath.Join(root, "plugins", "known_marketplaces.json"))
	if err != nil {
		return false, err
	}
	_, exists := marketplaces["coconet"]
	return exists, nil
}

func readJSONObject(path string) (map[string]any, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(content, &object); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return object, nil
}

func cleanupCodexConfig(path string) (string, int, error) {
	target, info, err := resolveCodexConfig(path)
	if err != nil {
		return "", 0, err
	}
	if info == nil {
		return "", 0, nil
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return "", 0, err
	}
	updated, removed := stripOwnedTOMLTables(content)
	if removed == 0 {
		return "", 0, nil
	}
	backup, err := createConfigBackup(target, content, info.Mode().Perm())
	if err != nil {
		return "", 0, err
	}
	currentTarget, currentInfo, err := resolveCodexConfig(path)
	if err != nil || currentTarget != target || currentInfo == nil || !os.SameFile(info, currentInfo) {
		return backup, 0, errors.New("Codex config or its symlink changed during Coconet cleanup; retry after closing active configuration editors")
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return backup, 0, err
	}
	if !bytes.Equal(current, content) {
		return backup, 0, errors.New("Codex config changed during Coconet cleanup; retry after closing active configuration editors")
	}
	// Replace the resolved file, never the user's link (e.g. Mackup/dotfiles).
	if err := atomicWrite(target, updated, info.Mode().Perm()); err != nil {
		return backup, 0, err
	}
	return backup, removed, nil
}

// A missing config is valid for a new profile. A dangling or circular link is
// not: do not silently replace it or write a new unrelated configuration.
func resolveCodexConfig(path string) (string, os.FileInfo, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil, nil
	} else if err != nil {
		return "", nil, fmt.Errorf("inspect Codex config: %w", err)
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("resolve Codex config link: %w; restore its target or reconnect the Mackup/dotfiles directory, then retry", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", nil, fmt.Errorf("read Codex config target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("Codex config must be a regular file or a symlink to one")
	}
	return target, info, nil
}

func stripOwnedTOMLTables(content []byte) ([]byte, int) {
	lines := bytes.SplitAfter(content, []byte("\n"))
	result := make([]byte, 0, len(content))
	skipping := false
	removed := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(string(line))
		if isTOMLTableHeader(trimmed) {
			if ownedTOMLTable(trimmed) {
				skipping = true
				removed++
				continue
			}
			skipping = false
		}
		if !skipping {
			result = append(result, line...)
		}
	}
	return result, removed
}

func isTOMLTableHeader(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}

func ownedTOMLTable(table string) bool {
	return table == `[plugins."coconet@coconet"]` ||
		table == `[marketplaces.coconet]` ||
		(strings.HasPrefix(table, `[hooks.state."coconet@coconet:`) && strings.HasSuffix(table, `"]`))
}

func createConfigBackup(path string, content []byte, mode os.FileMode) (string, error) {
	for index := 0; index < 1000; index++ {
		suffix := ".coconet-uninstall.bak"
		if index != 0 {
			suffix = fmt.Sprintf(".coconet-uninstall.bak.%d", index)
		}
		candidate := path + suffix
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := file.Write(content); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", errors.New("could not allocate a Codex config backup path")
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".coconet-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
