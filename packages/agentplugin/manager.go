package agentplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/subprocess"
)

type Host string

const (
	HostCodex  Host = "codex"
	HostClaude Host = "claude"
	HostCursor Host = "cursor"

	StatusConfigured Status = "configured"
	StatusRemoved    Status = "removed"
	StatusSkipped    Status = "not_detected"
	StatusFailed     Status = "failed"
)

type Status string

type Detection struct {
	Host     Host   `json:"host"`
	Command  string `json:"command"`
	Path     string `json:"path,omitempty"`
	Version  string `json:"version,omitempty"`
	Detected bool   `json:"detected"`
	Warning  string `json:"warning,omitempty"`
}

type InstallResult struct {
	Changed bool   `json:"changed,omitempty"`
	Host    Host   `json:"host"`
	Command string `json:"command"`
	Version string `json:"version,omitempty"`
	Status  Status `json:"status"`
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
}

type UninstallResult struct {
	Host                  Host   `json:"host"`
	Command               string `json:"command"`
	Version               string `json:"version,omitempty"`
	Status                Status `json:"status"`
	ConfigBackup          string `json:"config_backup,omitempty"`
	ConfigSectionsRemoved int    `json:"config_sections_removed,omitempty"`
	CacheRemoved          bool   `json:"cache_removed,omitempty"`
	Warning               string `json:"warning,omitempty"`
	Error                 string `json:"error,omitempty"`
}

// SetupReport separates Agent configuration from a successful Runtime install.
type SetupReport struct {
	Status  string          `json:"status"`
	Results []InstallResult `json:"results"`
	Error   string          `json:"error,omitempty"`
}

type Runner interface {
	LookPath(command string) (string, error)
	Run(ctx context.Context, command string, args ...string) (CommandOutput, error)
}

type CommandOutput struct {
	Stdout []byte
	Stderr []byte
}

type systemRunner struct {
	environment map[string]string
}

func (systemRunner) LookPath(command string) (string, error) {
	return exec.LookPath(command)
}

func (runner systemRunner) Run(ctx context.Context, command string, args ...string) (CommandOutput, error) {
	var stdout, stderr bytes.Buffer
	process := exec.Command(command, args...)
	process.Env = mergedEnvironment(os.Environ(), runner.environment)
	process.Stdout = &stdout
	process.Stderr = &stderr
	err := subprocess.Run(ctx, process)
	return CommandOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

type Manager struct {
	Runner      Runner
	Timeout     time.Duration
	Environment map[string]string
	// CursorRoot overrides its fixed extension directory for isolated tests.
	CursorRoot string
}

func NewManager() Manager {
	return Manager{Runner: systemRunner{}, Timeout: 15 * time.Second}
}

// WithConfigRoot returns a Manager whose Agent subprocesses are isolated to
// one explicit user configuration root. It does not mutate the parent process.
func (manager Manager) WithConfigRoot(host Host, configRoot string) Manager {
	manager.Environment = ConfigEnvironment(host, configRoot)
	if _, ok := manager.Runner.(systemRunner); ok {
		manager.Runner = systemRunner{environment: manager.Environment}
	}
	return manager
}

func ConfigEnvironment(host Host, configRoot string) map[string]string {
	if strings.TrimSpace(configRoot) == "" {
		return nil
	}
	if host == HostCodex {
		return map[string]string{"CODEX_HOME": configRoot}
	}
	if host == HostCursor {
		return map[string]string{"CURSOR_CONFIG_DIR": configRoot}
	}
	if host == HostClaude {
		return map[string]string{"CLAUDE_CONFIG_DIR": configRoot}
	}
	return nil
}

func (manager Manager) Detect(ctx context.Context, host Host, command string) Detection {
	detection := Detection{Host: host, Command: command}
	if !validHost(host) {
		detection.Warning = "unsupported Agent host"
		return detection
	}
	if strings.TrimSpace(command) == "" {
		detection.Warning = "Agent command is empty"
		return detection
	}
	runner := manager.runner()
	path, err := runner.LookPath(command)
	if err != nil {
		var executableError *exec.Error
		if !errors.As(err, &executableError) {
			detection.Warning = compactOutput(err.Error())
		}
		return detection
	}
	detection.Path = path
	detection.Detected = true
	output, err := manager.run(ctx, path, "--version")
	if err != nil {
		detection.Warning = formatCommandError("read Agent version", output, err)
		return detection
	}
	detection.Version = compactOutput(string(combinedOutput(output)))
	return detection
}

func (manager Manager) Install(ctx context.Context, detection Detection, marketplaceRoot string) InstallResult {
	result := InstallResult{
		Host: detection.Host, Command: detection.Command, Version: detection.Version,
		Status: StatusSkipped, Warning: detection.Warning,
	}
	if !detection.Detected {
		return result
	}
	if !validHost(detection.Host) || detection.Path == "" || strings.TrimSpace(marketplaceRoot) == "" {
		result.Status = StatusFailed
		result.Error = "Agent installation input is invalid"
		return result
	}
	if detection.Host == HostCodex && manager.Environment["CODEX_HOME"] != "" {
		if _, _, err := resolveCodexConfig(filepath.Join(manager.Environment["CODEX_HOME"], "config.toml")); err != nil {
			result.Status, result.Error = StatusFailed, err.Error()
			return result
		}
	}
	if detection.Host == HostCursor {
		return manager.installCursor(result, marketplaceRoot)
	}
	steps := installSteps(detection.Host, marketplaceRoot)
	for _, step := range steps {
		output, err := manager.run(ctx, detection.Path, step.arguments...)
		if err != nil && detection.Host == HostCodex && step.name == "add Codex Marketplace" && codexMarketplaceSourceConflict(output) {
			output, err = manager.replaceCodexMarketplace(ctx, detection.Path, marketplaceRoot)
		}
		if err != nil {
			result.Status = StatusFailed
			result.Error = formatCommandError(step.name, output, err)
			return result
		}
	}
	verification, err := manager.run(ctx, detection.Path, verificationArguments(detection.Host)...)
	if err != nil {
		result.Status = StatusFailed
		result.Error = formatCommandError("verify Coconet Plugin", verification, err)
		return result
	}
	if err := verifyInstalled(detection.Host, verification.Stdout); err != nil {
		result.Status = StatusFailed
		result.Error = err.Error()
		return result
	}
	result.Status = StatusConfigured
	return result
}

func (manager Manager) replaceCodexMarketplace(ctx context.Context, command, marketplaceRoot string) (CommandOutput, error) {
	previousRoot := ""
	if output, err := manager.run(ctx, command, "plugin", "marketplace", "list", "--json"); err == nil {
		previousRoot = codexMarketplaceRoot(output.Stdout, "coconet")
	}
	output, err := manager.run(ctx, command, "plugin", "marketplace", "remove", "coconet", "--json")
	if err != nil {
		return output, err
	}
	output, err = manager.run(ctx, command, "plugin", "marketplace", "add", marketplaceRoot, "--json")
	if err == nil || previousRoot == "" {
		return output, err
	}
	_, _ = manager.run(ctx, command, "plugin", "marketplace", "remove", "coconet", "--json")
	_, _ = manager.run(ctx, command, "plugin", "marketplace", "add", previousRoot, "--json")
	return output, err
}

func codexMarketplaceSourceConflict(output CommandOutput) bool {
	return strings.Contains(string(combinedOutput(output)), "marketplace 'coconet' is already added from a different source")
}

func codexMarketplaceRoot(content []byte, name string) string {
	var list struct {
		Marketplaces []struct {
			Name string `json:"name"`
			Root string `json:"root"`
		} `json:"marketplaces"`
	}
	if decodeAgentJSON(content, &list) != nil {
		return ""
	}
	for _, marketplace := range list.Marketplaces {
		if marketplace.Name == name {
			return marketplace.Root
		}
	}
	return ""
}

type installStep struct {
	name      string
	arguments []string
}

func installSteps(host Host, marketplaceRoot string) []installStep {
	if host == HostCodex {
		return []installStep{
			{name: "add Codex Marketplace", arguments: []string{"plugin", "marketplace", "add", marketplaceRoot, "--json"}},
			{name: "install Codex Plugin", arguments: []string{"plugin", "add", "coconet@coconet", "--json"}},
		}
	}
	return []installStep{
		{name: "add Claude Marketplace", arguments: []string{"plugin", "marketplace", "add", marketplaceRoot, "--scope", "user"}},
		{name: "install Claude Plugin", arguments: []string{"plugin", "install", "coconet@coconet", "--scope", "user"}},
		{name: "update Claude Plugin", arguments: []string{"plugin", "update", "coconet@coconet", "--scope", "user"}},
	}
}

func verificationArguments(host Host) []string {
	if host == HostCodex {
		return []string{"plugin", "list", "--available", "--json"}
	}
	return []string{"plugin", "list", "--json"}
}

func verifyInstalled(host Host, content []byte) error {
	if host == HostCodex {
		var list struct {
			Installed []struct {
				PluginID  string `json:"pluginId"`
				Installed bool   `json:"installed"`
				Enabled   bool   `json:"enabled"`
			} `json:"installed"`
		}
		if err := decodeAgentJSON(content, &list); err != nil {
			return errors.New("Codex Plugin verification returned invalid JSON")
		}
		for _, plugin := range list.Installed {
			if plugin.PluginID == "coconet@coconet" && plugin.Installed && plugin.Enabled {
				return nil
			}
		}
		return errors.New("Codex did not report coconet@coconet as installed and enabled")
	}
	type claudePlugin struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	var installed []claudePlugin
	if err := decodeAgentJSON(content, &installed); err != nil {
		var list struct {
			Installed []claudePlugin `json:"installed"`
		}
		if objectErr := decodeAgentJSON(content, &list); objectErr != nil {
			return errors.New("Claude Plugin verification returned invalid JSON")
		}
		installed = list.Installed
	}
	for _, plugin := range installed {
		if plugin.ID == "coconet@coconet" && plugin.Enabled {
			return nil
		}
	}
	return errors.New("Claude did not report coconet@coconet as installed and enabled")
}

func (manager Manager) run(ctx context.Context, command string, args ...string) (CommandOutput, error) {
	timeout := manager.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := manager.runner().Run(commandContext, command, args...)
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		return output, context.DeadlineExceeded
	}
	return output, err
}

func (manager Manager) runner() Runner {
	if manager.Runner == nil {
		return systemRunner{environment: manager.Environment}
	}
	return manager.Runner
}

func mergedEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := overrides[name]; replaced {
				continue
			}
		}
		result = append(result, entry)
	}
	for name, value := range overrides {
		result = append(result, name+"="+value)
	}
	return result
}

// Some Agent wrappers print an update notice before an otherwise valid JSON
// result. Accept exactly one complete JSON value with optional leading text;
// command failures still remain failures and trailing output is rejected.
func decodeAgentJSON(content []byte, target any) error {
	if err := json.Unmarshal(content, target); err == nil {
		return nil
	}
	found := false
	var candidate []byte
	for index, value := range content {
		if value != '{' && value != '[' {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(content[index:]))
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			continue
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			continue
		}
		if found {
			return errors.New("Agent verification returned more than one JSON value")
		}
		found = true
		candidate = append([]byte(nil), raw...)
	}
	if !found {
		return errors.New("Agent verification returned invalid JSON")
	}
	return json.Unmarshal(candidate, target)
}

func validHost(host Host) bool {
	return host == HostCodex || host == HostClaude || host == HostCursor
}

func formatCommandError(action string, output CommandOutput, err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return action + " timed out; the Agent command was stopped. Check the Agent CLI before retrying."
	}
	if errors.Is(err, context.Canceled) {
		return action + " was cancelled."
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return action + " failed: the wrapper exited but a child process kept its output open. The command was stopped."
	}
	detail := compactOutput(string(combinedOutput(output)))
	if detail == "" {
		detail = compactOutput(err.Error())
	}
	return fmt.Sprintf("%s failed: %s", action, detail)
}

func combinedOutput(output CommandOutput) []byte {
	combined := make([]byte, 0, len(output.Stdout)+len(output.Stderr)+1)
	combined = append(combined, output.Stdout...)
	if len(output.Stdout) != 0 && len(output.Stderr) != 0 {
		combined = append(combined, '\n')
	}
	return append(combined, output.Stderr...)
}

func compactOutput(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 500 {
		return value[:500] + "…"
	}
	return value
}
