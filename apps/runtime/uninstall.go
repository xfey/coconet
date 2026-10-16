package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/distribution"
)

var (
	detectUninstallAgent = func(ctx context.Context, host agentplugin.Host, command, configRoot string) agentplugin.Detection {
		return agentplugin.NewManager().WithConfigRoot(host, configRoot).Detect(ctx, host, command)
	}
	uninstallAgent = func(ctx context.Context, detection agentplugin.Detection, configRoot string) agentplugin.UninstallResult {
		return agentplugin.NewManager().WithConfigRoot(detection.Host, configRoot).Uninstall(ctx, detection, configRoot)
	}
	uninstallRuntime = distribution.Uninstall
	purgeRuntimeData = purgeLocalState
)

type uninstallOutput struct {
	Status  string                        `json:"status"`
	Agents  []agentplugin.UninstallResult `json:"agents"`
	Runtime distribution.UninstallResult  `json:"runtime"`
	Data    purgeResult                   `json:"data"`
	Error   string                        `json:"error,omitempty"`
}

type uninstallTarget struct {
	integration    agentIntegration
	integrationIDs []string
	detection      agentplugin.Detection
}

func runUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	defaults, err := defaultUninstallPaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installRoot := flags.String("root", defaults.installRoot, "versioned Coconet install root")
	binDirectory := flags.String("bin-dir", defaults.binDirectory, "user executable directory for archive installs")
	purge := flags.Bool("purge", false, "also remove Coconet local state and credentials")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
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
	integrationState, hasIntegrationRegistry, err := loadAgentIntegrationState(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	integrations := append([]agentIntegration(nil), integrationState.Integrations...)
	targets := make([]uninstallTarget, 0, len(integrations))
	for _, integration := range integrations {
		detection := detectUninstallAgent(ctx, integration.Type, integration.Command, integration.ConfigRoot)
		sharedTarget := -1
		for index, target := range targets {
			if target.integration.Type == integration.Type && samePath(target.integration.ConfigRoot, integration.ConfigRoot) {
				sharedTarget = index
				break
			}
		}
		if sharedTarget < 0 {
			targets = append(targets, uninstallTarget{
				integration: integration, integrationIDs: []string{integration.ID}, detection: detection,
			})
			continue
		}
		targets[sharedTarget].integrationIDs = append(targets[sharedTarget].integrationIDs, integration.ID)
		if !targets[sharedTarget].detection.Detected && detection.Detected {
			targets[sharedTarget].integration = integration
			targets[sharedTarget].detection = detection
		}
	}
	output := uninstallOutput{Status: "ok", Agents: make([]agentplugin.UninstallResult, 0, len(targets))}
	removedIntegrationIDs := map[string]bool{}
	for _, target := range targets {
		integration := target.integration
		result := uninstallAgent(ctx, target.detection, integration.ConfigRoot)
		if result.Status == agentplugin.StatusFailed {
			output.Status = "partial"
		} else {
			for _, integrationID := range target.integrationIDs {
				removedIntegrationIDs[integrationID] = true
			}
		}
		output.Agents = append(output.Agents, result)
	}
	if hasIntegrationRegistry {
		remaining := make([]agentIntegration, 0, len(integrationState.Integrations))
		for _, integration := range integrationState.Integrations {
			if !removedIntegrationIDs[integration.ID] {
				remaining = append(remaining, integration)
			}
		}
		integrationState.Integrations = remaining
		if err := saveAgentIntegrationState(paths, integrationState); err != nil {
			output.Status = "partial"
			output.Error = fmt.Sprintf("save Agent integration state failed: %v", err)
			return finishUninstallOutput(output, *jsonOutput, stdout, stderr, 1)
		}
	}
	if output.Status == "partial" {
		output.Error = "one or more Agent integrations could not be removed; Runtime and local data were preserved"
		return finishUninstallOutput(output, *jsonOutput, stdout, stderr, 1)
	}
	if *purge {
		output.Data, err = purgeRuntimeData(paths)
		if err != nil {
			output.Status = "partial"
			output.Error = fmt.Sprintf("purge Coconet local data failed: %v", err)
			return finishUninstallOutput(output, *jsonOutput, stdout, stderr, 1)
		}
	} else {
		output.Data = purgeResult{Root: paths.root, Preserved: true}
	}
	output.Runtime, err = uninstallRuntime(distribution.UninstallOptions{
		InstallRoot: *installRoot, BinDirectory: *binDirectory,
	})
	if err != nil {
		output.Status = "partial"
		output.Error = fmt.Sprintf("remove Coconet Runtime failed: %v", err)
		return finishUninstallOutput(output, *jsonOutput, stdout, stderr, 1)
	}
	return finishUninstallOutput(output, *jsonOutput, stdout, stderr, 0)
}

func finishUninstallOutput(output uninstallOutput, asJSON bool, stdout, stderr io.Writer, exitCode int) int {
	if err := writeUninstallOutput(output, asJSON, stdout); err != nil {
		fmt.Fprintf(stderr, "write Coconet uninstall result: %v\n", err)
		return 1
	}
	return exitCode
}

func writeUninstallOutput(output uninstallOutput, asJSON bool, stdout io.Writer) error {
	var rendered bytes.Buffer
	if asJSON {
		if err := json.NewEncoder(&rendered).Encode(output); err != nil {
			return err
		}
		_, err := io.Copy(stdout, &rendered)
		return err
	}
	if output.Error != "" {
		fmt.Fprintf(&rendered, "Coconet uninstall incomplete: %s\n", output.Error)
		for _, agent := range output.Agents {
			if agent.Error != "" {
				fmt.Fprintf(&rendered, "- %s: %s\n", displayAgentType(agent.Host), agent.Error)
			}
		}
		_, err := io.Copy(stdout, &rendered)
		return err
	}
	writeCLISuccess(&rendered, "Uninstalled Coconet")
	if output.Data.Preserved {
		fmt.Fprintln(&rendered, "  Your Coconet account, project connections and local session copies were kept.")
	} else if output.Data.Purged {
		fmt.Fprintln(&rendered, "  Local Coconet data was deleted. Original agent sessions were kept.")
	}
	if output.Runtime.Executable == "" || !output.Runtime.ExecutableRemoved {
		fmt.Fprintln(&rendered, "\nRemove the remaining npm launcher:\n  npm uninstall --global coconet")
	}
	fmt.Fprintln(&rendered, "\nRestart your agents to unload the Coconet plugin.")
	_, err := io.Copy(stdout, &rendered)
	return err
}

type uninstallPaths struct {
	installRoot  string
	binDirectory string
}

func defaultUninstallPaths() (uninstallPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return uninstallPaths{}, errors.New("could not resolve the current user home directory")
	}
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".local", "share")
	}
	binDirectory := os.Getenv("XDG_BIN_HOME")
	if binDirectory == "" {
		binDirectory = filepath.Join(home, ".local", "bin")
	}
	return uninstallPaths{installRoot: filepath.Join(dataRoot, "coconet"), binDirectory: binDirectory}, nil
}
