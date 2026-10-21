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
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/cliui"
	"github.com/xfey/coconet/packages/distribution"
	"github.com/xfey/coconet/packages/subprocess"
)

var version = "dev"

var (
	loadBundleManifest = distribution.LoadManifest
	verifyBundle       = distribution.VerifyBundle
	installBundle      = distribution.Install
	setupAgents        = setupInstalledAgents
	guardInstallation  = startInstallationGuard
)

type installOutput struct {
	Status        string                  `json:"status"`
	Version       string                  `json:"version"`
	ReleasePath   string                  `json:"release_path"`
	Executable    string                  `json:"executable"`
	AlreadyExists bool                    `json:"already_exists"`
	Agents        agentplugin.SetupReport `json:"agents"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version" || args[0] == "-v") {
		fmt.Fprintf(stdout, "coconet-bootstrap %s\n", version)
		return 0
	}
	if len(args) == 0 || args[0] != "install" {
		fmt.Fprintln(stderr, "usage: coconet-bootstrap install [--bundle DIR] [--root DIR] [--bin-dir DIR] [--no-bin-link] [--skip-agents] [--json]")
		return 2
	}
	defaults, err := defaultInstallPaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	bundleRoot := flags.String("bundle", defaultBundleRoot(), "extracted Coconet bundle directory")
	installRoot := flags.String("root", defaults.installRoot, "versioned Coconet install root")
	binDirectory := flags.String("bin-dir", defaults.binDirectory, "user executable directory")
	noBinLink := flags.Bool("no-bin-link", false, "do not create a separate coconet command link")
	onlyIfNewer := flags.Bool("if-newer", false, "activate only if no same or newer Runtime is already installed")
	skipAgents := flags.Bool("skip-agents", os.Getenv("COCONET_SKIP_AGENT_SETUP") == "1", "install Runtime without configuring Agent integrations")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(stderr, "unexpected positional arguments")
		}
		return 2
	}
	manifest, err := loadBundleManifest(*bundleRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := verifyBundle(*bundleRoot, manifest); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options := distribution.InstallOptions{
		BundleRoot: *bundleRoot, InstallRoot: *installRoot, BinDirectory: *binDirectory,
		SkipBinLink: *noBinLink,
		CurrentOS:   runtime.GOOS, CurrentArch: runtime.GOARCH, StageOnly: true,
	}
	result, err := installBundle(options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Stage and verify all bytes before pausing any running work. The guard
	// remains alive until activation succeeds or fails; EOF always resumes it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lease, err := guardInstallation(ctx, filepath.Join(result.ReleasePath, "bin", "coconet"), *installRoot, *onlyIfNewer, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var activated distribution.InstallResult
	var installErr error
	if ctx.Err() != nil {
		installErr = ctx.Err()
	} else if lease.Current {
		// Another launcher may have completed a newer install while this one
		// downloaded. Read its result while the installation lease is held.
		current := filepath.Join(*installRoot, "current")
		var installedVersion string
		installedVersion, installErr = distribution.InstalledVersion(current)
		activated = distribution.InstallResult{Version: installedVersion, ReleasePath: current,
			Executable: filepath.Join(current, "bin", "coconet"), AlreadyExists: true}
	} else {
		options.StageOnly = false
		activated, installErr = installBundle(options)
		activated.AlreadyExists = result.AlreadyExists
	}
	resumeErr := lease.Finish()
	if installErr != nil {
		fmt.Fprintln(stderr, installErr)
		return 1
	}
	if resumeErr != nil {
		fmt.Fprintln(stderr, resumeErr)
		return 1
	}
	result = activated
	output := installOutput{
		Status: "ok", Version: result.Version, ReleasePath: result.ReleasePath, Executable: result.Executable,
		AlreadyExists: result.AlreadyExists,
		Agents:        agentplugin.SetupReport{Status: "skipped", Results: []agentplugin.InstallResult{}},
	}
	if !*skipAgents && !lease.Current {
		output.Agents = setupAgents(filepath.Join(result.ReleasePath, "bin", "coconet"), stderr)
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(output); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		state := "Installed"
		if result.AlreadyExists {
			state = "Verified"
		}
		cliui.Success(stdout, fmt.Sprintf("%s Coconet %s", state, result.Version))
		configured := []string{}
		for _, result := range output.Agents.Results {
			switch result.Status {
			case agentplugin.StatusConfigured:
				if result.Changed {
					configured = append(configured, result.Command)
				}
			case agentplugin.StatusDisabled:
				cliui.Note(stdout, result.Command+" plugin remains disabled or removed.")
			case agentplugin.StatusSkipped:
				cliui.Note(stdout, result.Command+" was not found; plugin setup skipped.")
			default:
				cliui.Warning(stderr, "Could not configure the Coconet plugin for "+result.Command)
			}
		}
		if output.Agents.Status == "partial" {
			if output.Agents.Error != "" {
				fmt.Fprintln(stderr, output.Agents.Error)
			}
			fmt.Fprintln(stderr, "Coconet is installed. Run `coconet agent list --verbose` for setup errors and retry commands.")
		}
		if len(configured) > 0 {
			cliui.Note(stdout, "Coconet plugin configured for: "+strings.Join(configured, ", "))
			cliui.Note(stdout, "Open a new agent session; accept Coconet's Hook permission prompt if shown.")
		}
	}
	return 0
}

func setupInstalledAgents(executable string, progress io.Writer) agentplugin.SetupReport {
	interrupted, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(interrupted, 3*time.Minute)
	defer cancel()
	command := exec.Command(executable, "_agent-setup")
	// Allow Runtime to stop its independently grouped host command and save
	// the interrupted attempt before Bootstrap enforces termination.
	command.WaitDelay = 3 * time.Second
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, progress
	err := subprocess.Run(ctx, command)
	content := output.Bytes()
	var report agentplugin.SetupReport
	if json.Unmarshal(content, &report) == nil && (report.Status == "ok" || report.Status == "partial") && report.Results != nil {
		if err == nil || report.Status == "partial" {
			return report
		}
	}
	return agentplugin.SetupReport{
		Status: "partial", Results: []agentplugin.InstallResult{},
		Error: "Agent setup did not complete; inspect coconet agent list --verbose and retry Agent registration.",
	}
}

type installPaths struct {
	installRoot  string
	binDirectory string
}

func defaultInstallPaths() (installPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return installPaths{}, errors.New("could not resolve the current user home directory")
	}
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".local", "share")
	}
	binDirectory := os.Getenv("XDG_BIN_HOME")
	if binDirectory == "" {
		binDirectory = filepath.Join(home, ".local", "bin")
	}
	return installPaths{installRoot: filepath.Join(dataRoot, "coconet"), binDirectory: binDirectory}, nil
}

func defaultBundleRoot() string {
	if executable, err := os.Executable(); err == nil {
		return filepath.Dir(filepath.Dir(executable))
	}
	return "."
}
