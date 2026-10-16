package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xfey/coconet/packages/distribution"
)

var version = "dev"

var (
	assembleReleaseBundle = distribution.AssembleBundle
	assembleServerBundle  = distribution.AssembleServerBundle
	archiveReleaseBundle  = distribution.ArchiveBundle
)

type releaseOutput struct {
	Status       string `json:"status"`
	Version      string `json:"version"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Bundle       string `json:"bundle"`
	Archive      string `json:"archive"`
	SHA256       string `json:"sha256"`
	Files        int    `json:"files"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version" || args[0] == "-v") {
		fmt.Fprintf(stdout, "coconet-release %s\n", version)
		return 0
	}
	if len(args) > 0 && args[0] == "server" {
		return runServerBundle(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "bundle" {
		printUsage(stderr)
		return 2
	}
	flags := flag.NewFlagSet("bundle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	releaseVersion := flags.String("version", "", "release and Plugin version")
	targetOS := flags.String("os", "", "target operating system")
	architecture := flags.String("arch", "", "target architecture")
	runtimeBinary := flags.String("runtime", "", "target coconet Runtime binary")
	bootstrapBinary := flags.String("bootstrap", "", "target coconet-bootstrap binary")
	nodeBinary := flags.String("node", "", "target Node executable")
	goLicenses := flags.String("go-licenses", "", "generated Go dependency license directory")
	nodeLicense := flags.String("node-license", "", "Node distribution license")
	claudeHelper := flags.String("claude-helper", "adapters/claude/helper/helper.mjs", "fixed Claude helper module")
	claudeSDK := flags.String("claude-sdk", "", "fixed Claude Agent SDK entry module")
	claudeSDKLicense := flags.String("claude-sdk-license", "", "Claude Agent SDK license")
	codexPlugin := flags.String("codex-plugin", "plugins/codex/coconet", "Codex Plugin source root")
	cursorPlugin := flags.String("cursor-plugin", "plugins/cursor/coconet", "Cursor Plugin source root")
	claudePlugin := flags.String("claude-plugin", "plugins/claude/coconet", "Claude Plugin source root")
	outputDirectory := flags.String("output", "dist", "release output directory")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(stderr, "unexpected positional arguments")
		}
		return 2
	}
	required := []struct {
		name  string
		value string
	}{
		{"--go-licenses", *goLicenses}, {"--version", *releaseVersion}, {"--os", *targetOS}, {"--arch", *architecture},
		{"--runtime", *runtimeBinary}, {"--bootstrap", *bootstrapBinary}, {"--node", *nodeBinary}, {"--node-license", *nodeLicense},
		{"--claude-helper", *claudeHelper}, {"--claude-sdk", *claudeSDK}, {"--claude-sdk-license", *claudeSDKLicense},
		{"--codex-plugin", *codexPlugin}, {"--claude-plugin", *claudePlugin}, {"--output", *outputDirectory},
	}
	missing := make([]string, 0)
	for _, argument := range required {
		if strings.TrimSpace(argument.value) == "" {
			missing = append(missing, argument.name)
		}
	}
	if len(missing) != 0 {
		fmt.Fprintf(stderr, "required release arguments are missing: %s\n", strings.Join(missing, ", "))
		return 2
	}
	name, err := distribution.BundleName(*releaseVersion, *targetOS, *architecture)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	outputRoot, err := filepath.Abs(*outputDirectory)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	bundleRoot := filepath.Join(outputRoot, name)
	archivePath := bundleRoot + ".tar.gz"
	manifest, err := assembleReleaseBundle(distribution.BundleInputs{
		GoLicenses: *goLicenses, Version: *releaseVersion, OS: *targetOS, Architecture: *architecture, OutputRoot: bundleRoot,
		RuntimeBinary: *runtimeBinary, BootstrapBinary: *bootstrapBinary, NodeBinary: *nodeBinary, NodeLicense: *nodeLicense,
		ClaudeHelper: *claudeHelper, ClaudeSDK: *claudeSDK, ClaudeSDKLicense: *claudeSDKLicense,
		CodexPluginRoot: *codexPlugin, ClaudePluginRoot: *claudePlugin, CursorPluginRoot: *cursorPlugin,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	hash, err := archiveReleaseBundle(bundleRoot, archivePath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result := releaseOutput{
		Status: "ok", Version: manifest.Version, OS: manifest.OS, Architecture: manifest.Architecture,
		Bundle: bundleRoot, Archive: archivePath, SHA256: hash, Files: len(manifest.Files),
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Coconet %s %s/%s bundle created with %d files.\nBundle: %s\nArchive: %s\nSHA-256: %s\n", result.Version, result.OS, result.Architecture, result.Files, result.Bundle, result.Archive, result.SHA256)
	return 0
}

func runServerBundle(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	releaseVersion := flags.String("version", "", "release version")
	targetOS := flags.String("os", "linux", "target operating system")
	architecture := flags.String("arch", "", "target architecture")
	goLicenses := flags.String("go-licenses", "", "generated Go dependency license directory")
	serverBinary := flags.String("server", "", "target coconet-server binary")
	systemdExample := flags.String("systemd", "deploy/systemd/coconet-server.service", "systemd unit example")
	journalExample := flags.String("journal", "deploy/systemd/journald@coconet.conf", "Coconet journal retention example")
	environmentExample := flags.String("environment", "deploy/systemd/server.env.example", "root-owned environment-file example")
	operations := flags.String("operations", "deploy", "storage operations scripts and templates directory")
	nginxExample := flags.String("nginx", "deploy/nginx/coconet-server.conf", "Nginx virtual-host example")
	outputDirectory := flags.String("output", "dist", "release output directory")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(stderr, "unexpected positional arguments")
		}
		return 2
	}
	missing := make([]string, 0)
	for _, argument := range []struct{ name, value string }{
		{"--go-licenses", *goLicenses}, {"--version", *releaseVersion}, {"--os", *targetOS}, {"--arch", *architecture},
		{"--server", *serverBinary}, {"--systemd", *systemdExample}, {"--journal", *journalExample}, {"--environment", *environmentExample}, {"--nginx", *nginxExample}, {"--output", *outputDirectory},
	} {
		if strings.TrimSpace(argument.value) == "" {
			missing = append(missing, argument.name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "required Server release arguments are missing: %s\n", strings.Join(missing, ", "))
		return 2
	}
	name, err := distribution.ServerBundleName(*releaseVersion, *targetOS, *architecture)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	outputRoot, err := filepath.Abs(*outputDirectory)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	bundleRoot := filepath.Join(outputRoot, name)
	archivePath := bundleRoot + ".tar.gz"
	manifest, err := assembleServerBundle(distribution.ServerBundleInputs{
		GoLicenses: *goLicenses, Version: *releaseVersion, OS: *targetOS, Architecture: *architecture, OutputRoot: bundleRoot,
		OperationsDirectory: *operations, ServerBinary: *serverBinary, SystemdExample: *systemdExample, JournalExample: *journalExample, EnvironmentExample: *environmentExample, NginxExample: *nginxExample,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	hash, err := archiveReleaseBundle(bundleRoot, archivePath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result := releaseOutput{
		Status: "ok", Version: manifest.Version, OS: manifest.OS, Architecture: manifest.Architecture,
		Bundle: bundleRoot, Archive: archivePath, SHA256: hash, Files: len(manifest.Files),
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Coconet Server %s %s/%s bundle created with %d files.\nBundle: %s\nArchive: %s\nSHA-256: %s\n", result.Version, result.OS, result.Architecture, result.Files, result.Bundle, result.Archive, result.SHA256)
	return 0
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: coconet-release <bundle|server> [arguments]")
}
