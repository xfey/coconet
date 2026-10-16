package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/cliui"
	"github.com/xfey/coconet/packages/contracts"
)

const agentIntegrationSchemaVersion = 1

var (
	agentIntegrationIDPattern = regexp.MustCompile(`^agt_[0-9a-f]{12}$`)
	agentIntegrationNow       = time.Now
	resolveAgentMarketplace   = installedAgentMarketplace
	configureAgentIntegration = func(ctx context.Context, integration agentIntegration, marketplaceRoot string) agentplugin.InstallResult {
		manager := agentplugin.NewManager().WithConfigRoot(integration.Type, integration.ConfigRoot)
		detection := manager.Detect(ctx, integration.Type, integration.Command)
		return manager.Install(ctx, detection, marketplaceRoot)
	}
	removeAgentIntegration = func(ctx context.Context, integration agentIntegration) agentplugin.UninstallResult {
		manager := agentplugin.NewManager().WithConfigRoot(integration.Type, integration.ConfigRoot)
		detection := manager.Detect(ctx, integration.Type, integration.Command)
		return manager.Uninstall(ctx, detection, integration.ConfigRoot)
	}
)

type agentIntegrationStatus string

const (
	agentIntegrationPending     agentIntegrationStatus = "pending"
	agentIntegrationConfigured  agentIntegrationStatus = "configured"
	agentIntegrationNotDetected agentIntegrationStatus = "not_detected"
	agentIntegrationFailed      agentIntegrationStatus = "failed"
)

type agentIntegration struct {
	ID             string                 `json:"id"`
	Type           agentplugin.Host       `json:"type"`
	Command        string                 `json:"command"`
	ConfigRoot     string                 `json:"config_root"`
	Status         agentIntegrationStatus `json:"status"`
	RuntimeVersion string                 `json:"runtime_version,omitempty"`
	AgentVersion   string                 `json:"agent_version,omitempty"`
	LastError      string                 `json:"last_error,omitempty"`
	LastWarning    string                 `json:"last_warning,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

type agentIntegrationState struct {
	SchemaVersion int                `json:"schema_version"`
	Integrations  []agentIntegration `json:"integrations"`
}

type agentMutationOutput struct {
	Status      string           `json:"status"`
	Integration agentIntegration `json:"integration"`
}

func runAgent(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: coconet agent <add|list|remove>")
		return 2
	}
	switch args[0] {
	case "add":
		return runAgentAdd(ctx, args[1:], stdout, stderr)
	case "list":
		return runAgentList(args[1:], stdout, stderr)
	case "remove":
		return runAgentRemove(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown agent command %q\n", args[0])
		return 2
	}
}

func runAgentAdd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: coconet agent add <codex|claude|cursor> [--command CMD] [--config-root DIR] [--json]")
		return 2
	}
	host := agentplugin.Host(args[0])
	if !contracts.ValidAgent(contracts.AgentType(host)) {
		fmt.Fprintln(stderr, "Agent type must be codex, claude or cursor")
		return 2
	}
	flags := flag.NewFlagSet("agent add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaultCommand := string(host)
	if host == agentplugin.HostCursor {
		defaultCommand = "agent"
	}
	command := flags.String("command", defaultCommand, "Agent CLI command")
	configRoot := flags.String("config-root", "", "absolute Agent user configuration root")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(stderr, "unexpected positional arguments")
		}
		return 2
	}
	if *configRoot == "" {
		resolved, err := agentplugin.DefaultConfigRoot(host)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*configRoot = resolved
	}
	integration, err := newAgentIntegration(host, *command, *configRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	state, _, err := loadAgentIntegrationState(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if existing, found := findAgentIntegration(state, integration.ID); found {
		integration.CreatedAt = existing.CreatedAt
	}
	state = putAgentIntegration(state, integration)
	if err := saveAgentIntegrationState(paths, state); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	integration = syncOneAgentIntegration(ctx, integration)
	state = putAgentIntegration(state, integration)
	if err := saveAgentIntegrationState(paths, state); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	status := "ok"
	exitCode := 0
	if integration.Status != agentIntegrationConfigured {
		status = "failed"
		exitCode = 1
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(agentMutationOutput{Status: status, Integration: integration}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else if integration.Status == agentIntegrationConfigured {
		writeCLISuccess(stdout, "Coconet plugin installed for "+cliui.Bold(stdout, integration.Command))
		cliui.Note(stdout, "Open a new agent session; accept Coconet's Hook permission prompt if shown.")
	} else {
		cliui.Warning(stderr, "Could not install the Coconet plugin for "+integration.Command)
		if integration.LastError != "" {
			fmt.Fprintln(stderr, integration.LastError)
		}
		if integration.LastWarning != "" {
			cliui.Warning(stderr, integration.LastWarning)
		}
		fmt.Fprintf(stderr, "\nAfter resolving the error, retry:\n  %s\n", agentAddCommand(integration))
	}
	return exitCode
}

func runAgentList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	verbose := flags.Bool("verbose", false, "show integration details")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(stderr, "unexpected positional arguments")
		}
		return 2
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	state, _, err := loadAgentIntegrationState(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sort.Slice(state.Integrations, func(left, right int) bool {
		a, b := state.Integrations[left], state.Integrations[right]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Command != b.Command {
			return a.Command < b.Command
		}
		return a.ConfigRoot < b.ConfigRoot
	})
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(state); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if len(state.Integrations) == 0 {
		fmt.Fprintln(stdout, "No agents are registered with Coconet.\n\nInstall the plugin for an agent:\n  coconet agent add codex\n  coconet agent add claude")
		return 0
	}
	if !*verbose {
		fmt.Fprintln(stdout, cliui.Bold(stdout, "Coconet plugins"))
		writeAgentIntegrationTable(stdout, state.Integrations)
		fmt.Fprintln(stdout, "\nRun `coconet agent list --verbose` for configuration paths and setup errors.")
		return 0
	}
	for _, integration := range state.Integrations {
		fmt.Fprintf(stdout, "%s · %s\n", displayAgentType(integration.Type), integration.Command)
		writeCLIFields(stdout,
			cliField{Label: "ID", Value: integration.ID},
			cliField{Label: "Config", Value: integration.ConfigRoot},
			cliField{Label: "Status", Value: displayAgentStatus(integration.Status)},
			cliField{Label: "Runtime", Value: integration.RuntimeVersion},
			cliField{Label: "Agent", Value: integration.AgentVersion},
		)
		if integration.LastError != "" {
			fmt.Fprintf(stdout, "Error    %s\n", integration.LastError)
		}
		if integration.LastWarning != "" {
			cliui.Warning(stdout, integration.LastWarning)
		}
		if integration.Status != agentIntegrationConfigured {
			fmt.Fprintf(stdout, "Retry:\n  %s\n", agentAddCommand(integration))
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func runAgentRemove(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: coconet agent remove <integration-id> [--json]")
		return 2
	}
	integrationID := args[0]
	if !agentIntegrationIDPattern.MatchString(integrationID) {
		fmt.Fprintln(stderr, "Agent integration ID is invalid")
		return 2
	}
	flags := flag.NewFlagSet("agent remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(stderr, "unexpected positional arguments")
		}
		return 2
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	state, _, err := loadAgentIntegrationState(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	integration, found := findAgentIntegration(state, integrationID)
	if !found {
		fmt.Fprintf(stderr, "Agent integration %s is not registered\n", integrationID)
		return 1
	}
	shared := false
	for _, candidate := range state.Integrations {
		if candidate.ID != integration.ID && candidate.Type == integration.Type && (integration.Type == agentplugin.HostCursor || samePath(candidate.ConfigRoot, integration.ConfigRoot)) {
			shared = true
			break
		}
	}
	result := agentplugin.UninstallResult{Host: integration.Type, Command: integration.Command, Status: agentplugin.StatusRemoved}
	if !shared {
		result = removeAgentIntegration(ctx, integration)
	}
	if result.Status == agentplugin.StatusFailed {
		integration.Status = agentIntegrationFailed
		integration.LastError = result.Error
		integration.UpdatedAt = agentIntegrationNow().UTC()
		state = putAgentIntegration(state, integration)
		if err := saveAgentIntegrationState(paths, state); err != nil {
			fmt.Fprintf(stderr, "%s; save Agent integration state: %v\n", result.Error, err)
			return 1
		}
		if *jsonOutput {
			if err := json.NewEncoder(stdout).Encode(agentMutationOutput{Status: "failed", Integration: integration}); err != nil {
				fmt.Fprintln(stderr, err)
			}
		} else {
			fmt.Fprintln(stderr, result.Error)
		}
		return 1
	}
	state.Integrations = removeAgentIntegrationFromState(state.Integrations, integration.ID)
	if err := saveAgentIntegrationState(paths, state); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(agentMutationOutput{Status: "ok", Integration: integration}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		writeCLISuccess(stdout, "Coconet plugin removed from "+integration.Command)
		cliui.Note(stdout, "Restart this agent to unload the plugin.")
	}
	return 0
}

func agentAddCommand(integration agentIntegration) string {
	return shellCommand("coconet", []string{"agent", "add", string(integration.Type), "--command", integration.Command, "--config-root", integration.ConfigRoot})
}

func writeAgentIntegrationTable(output io.Writer, integrations []agentIntegration) {
	typeWidth := len("TYPE")
	commandWidth := len("COMMAND")
	for _, integration := range integrations {
		if value := len(displayAgentType(integration.Type)); value > typeWidth {
			typeWidth = value
		}
		if value := len(integration.Command); value > commandWidth {
			commandWidth = value
		}
	}
	fmt.Fprintf(output, "%-*s  %-*s  %s\n", typeWidth, "TYPE", commandWidth, "COMMAND", "STATUS")
	for _, integration := range integrations {
		fmt.Fprintf(output, "%-*s  %-*s  %s\n", typeWidth, displayAgentType(integration.Type), commandWidth, integration.Command, displayAgentStatus(integration.Status))
	}
}

func newAgentIntegration(host agentplugin.Host, command, configRoot string) (agentIntegration, error) {
	if !contracts.ValidAgent(contracts.AgentType(host)) {
		return agentIntegration{}, errors.New("Agent type must be codex, claude or cursor")
	}
	command = strings.TrimSpace(command)
	if command == "" || len(command) > 512 || strings.ContainsAny(command, "\x00\r\n") {
		return agentIntegration{}, errors.New("Agent command is invalid")
	}
	if !filepath.IsAbs(command) && filepath.Base(command) != command {
		return agentIntegration{}, errors.New("Agent command must be an executable name or an absolute path")
	}
	if filepath.IsAbs(command) {
		command = filepath.Clean(command)
	}
	if !filepath.IsAbs(configRoot) {
		return agentIntegration{}, errors.New("Agent configuration root must be an absolute path")
	}
	configRoot = filepath.Clean(configRoot)
	if configRoot == string(filepath.Separator) {
		return agentIntegration{}, errors.New("Agent configuration root is too broad")
	}
	identity := string(host) + "\x00" + command + "\x00" + configRoot
	digest := sha256.Sum256([]byte(identity))
	now := agentIntegrationNow().UTC()
	return agentIntegration{
		ID: "agt_" + hex.EncodeToString(digest[:6]), Type: host, Command: command, ConfigRoot: configRoot,
		Status: agentIntegrationPending, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func loadAgentIntegrationState(paths runtimePaths) (agentIntegrationState, bool, error) {
	content, err := os.ReadFile(paths.integrationPath)
	if errors.Is(err, os.ErrNotExist) {
		return agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{}}, false, nil
	}
	if err != nil {
		return agentIntegrationState{}, false, fmt.Errorf("read Agent integrations: %w", err)
	}
	if err := os.Chmod(paths.integrationPath, 0o600); err != nil {
		return agentIntegrationState{}, false, fmt.Errorf("secure Agent integrations: %w", err)
	}
	var state agentIntegrationState
	if err := decodeStrict(content, &state); err != nil {
		return agentIntegrationState{}, false, fmt.Errorf("decode Agent integrations: %w", err)
	}
	if err := validateAgentIntegrationState(state); err != nil {
		return agentIntegrationState{}, false, err
	}
	return state, true, nil
}

func saveAgentIntegrationState(paths runtimePaths, state agentIntegrationState) error {
	state.SchemaVersion = agentIntegrationSchemaVersion
	if state.Integrations == nil {
		state.Integrations = []agentIntegration{}
	}
	if err := validateAgentIntegrationState(state); err != nil {
		return err
	}
	return atomicWriteJSON(paths.integrationPath, state, 0o600)
}

func validateAgentIntegrationState(state agentIntegrationState) error {
	if state.SchemaVersion != agentIntegrationSchemaVersion {
		return errors.New("unsupported Agent integration schema version")
	}
	seen := map[string]bool{}
	for _, integration := range state.Integrations {
		if !agentIntegrationIDPattern.MatchString(integration.ID) || seen[integration.ID] || integration.CreatedAt.IsZero() || integration.UpdatedAt.IsZero() {
			return errors.New("Agent integration state contains an invalid or duplicate entry")
		}
		expected, err := newAgentIntegration(integration.Type, integration.Command, integration.ConfigRoot)
		if err != nil || expected.ID != integration.ID || !validAgentIntegrationStatus(integration.Status) {
			return errors.New("Agent integration state contains an invalid entry")
		}
		seen[integration.ID] = true
	}
	return nil
}

func validAgentIntegrationStatus(status agentIntegrationStatus) bool {
	return status == agentIntegrationPending || status == agentIntegrationConfigured || status == agentIntegrationNotDetected || status == agentIntegrationFailed
}

func putAgentIntegration(state agentIntegrationState, integration agentIntegration) agentIntegrationState {
	updated := make([]agentIntegration, 0, len(state.Integrations)+1)
	for _, existing := range state.Integrations {
		if existing.ID != integration.ID {
			updated = append(updated, existing)
		}
	}
	state.Integrations = append(updated, integration)
	return state
}

func findAgentIntegration(state agentIntegrationState, id string) (agentIntegration, bool) {
	for _, integration := range state.Integrations {
		if integration.ID == id {
			return integration, true
		}
	}
	return agentIntegration{}, false
}

func removeAgentIntegrationFromState(integrations []agentIntegration, id string) []agentIntegration {
	result := make([]agentIntegration, 0, len(integrations))
	for _, integration := range integrations {
		if integration.ID != id {
			result = append(result, integration)
		}
	}
	return result
}

func syncOneAgentIntegration(ctx context.Context, integration agentIntegration) agentIntegration {
	integration.RuntimeVersion = version
	integration.UpdatedAt = agentIntegrationNow().UTC()
	integration.LastError = ""
	integration.LastWarning = ""
	marketplaceRoot, err := resolveAgentMarketplace(integration.Type)
	if err != nil {
		integration.Status = agentIntegrationFailed
		integration.LastError = err.Error()
		return integration
	}
	result := configureAgentIntegration(ctx, integration, marketplaceRoot)
	integration.AgentVersion = result.Version
	integration.LastWarning = result.Warning
	integration.LastError = result.Error
	switch result.Status {
	case agentplugin.StatusConfigured:
		integration.Status = agentIntegrationConfigured
	case agentplugin.StatusSkipped:
		integration.Status = agentIntegrationNotDetected
	default:
		integration.Status = agentIntegrationFailed
	}
	return integration
}

func syncRegisteredAgentIntegrations(ctx context.Context, paths runtimePaths, onlyOutdated bool) error {
	return syncAgentIntegrationsWithProgress(ctx, paths, onlyOutdated, io.Discard)
}

func syncAgentIntegrationsWithProgress(ctx context.Context, paths runtimePaths, onlyOutdated bool, progress io.Writer) error {
	state, exists, err := loadAgentIntegrationState(paths)
	if err != nil || !exists {
		return err
	}
	for index, integration := range state.Integrations {
		if onlyOutdated && integration.RuntimeVersion == version {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Persist the attempted version before invoking a host. If setup is
		// interrupted, the following connect must not silently start it again.
		attempt := integration
		attempt.RuntimeVersion, attempt.Status = version, agentIntegrationFailed
		attempt.LastError = "Agent setup did not finish. Inspect `coconet agent list --verbose` and retry this Agent."
		attempt.UpdatedAt = agentIntegrationNow().UTC()
		state.Integrations[index] = attempt
		if err := saveAgentIntegrationState(paths, state); err != nil {
			return err
		}
		fmt.Fprintf(progress, "Configuring Coconet plugin for %s…\n", integration.Command)
		state.Integrations[index] = syncOneAgentIntegration(ctx, integration)
		if err := saveAgentIntegrationState(paths, state); err != nil {
			return err
		}
		result := state.Integrations[index]
		fmt.Fprintf(progress, "  %s: %s\n", integration.Command, displayAgentStatus(result.Status))
		if result.LastError != "" {
			fmt.Fprintln(progress, result.LastError)
		}
	}
	return ctx.Err()
}

func autoSyncRegisteredAgentIntegrations(ctx context.Context, progress io.Writer) {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return
	}
	_ = syncAgentIntegrationsWithProgress(ctx, paths, true, progress)
}

func installedAgentMarketplace(host agentplugin.Host) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve Coconet Runtime: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	root := filepath.Dir(filepath.Dir(executable))
	marketplace := filepath.Join(root, "marketplaces", string(host))
	info, err := os.Stat(marketplace)
	if err != nil {
		return "", fmt.Errorf("resolve %s Plugin marketplace: %w", host, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("resolve %s Plugin marketplace: path is not a directory", host)
	}
	return marketplace, nil
}
