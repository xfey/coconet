package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/distribution"
)

func TestBundleMapsInputsAndReturnsJSON(t *testing.T) {
	restoreReleaseStubs(t)
	var received distribution.BundleInputs
	assembleReleaseBundle = func(input distribution.BundleInputs) (distribution.Manifest, error) {
		received = input
		return distribution.Manifest{Version: input.Version, OS: input.OS, Architecture: input.Architecture, Files: make([]distribution.ManifestFile, 12)}, nil
	}
	archiveReleaseBundle = func(bundleRoot, archivePath string) (string, error) {
		if bundleRoot != received.OutputRoot || archivePath != bundleRoot+".tar.gz" {
			t.Fatalf("unexpected archive paths %q %q", bundleRoot, archivePath)
		}
		return strings.Repeat("a", 64), nil
	}
	output := t.TempDir()
	args := []string{
		"bundle", "--go-licenses", "/inputs/go-licenses", "--version", "0.3.0", "--os", "linux", "--arch", "amd64",
		"--runtime", "/inputs/coconet", "--bootstrap", "/inputs/bootstrap", "--node", "/inputs/node",
		"--node-license", "/inputs/NODE_LICENSE", "--claude-sdk", "/inputs/sdk.mjs",
		"--claude-sdk-license", "/inputs/SDK_LICENSE.md", "--output", output, "--json",
	}
	var stdout, stderr bytes.Buffer
	if exit := run(args, &stdout, &stderr); exit != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected release exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	expectedRoot := filepath.Join(output, "coconet-0.3.0-linux-amd64")
	if received.GoLicenses != "/inputs/go-licenses" || received.OutputRoot != expectedRoot || received.CodexPluginRoot != "plugins/codex/coconet" || received.ClaudePluginRoot != "plugins/claude/coconet" {
		t.Fatalf("unexpected bundle inputs %#v", received)
	}
	if !strings.Contains(stdout.String(), `"bundle":"`+expectedRoot+`"`) || !strings.Contains(stdout.String(), `"files":12`) {
		t.Fatalf("unexpected JSON %q", stdout.String())
	}
}

func TestServerBundleMapsIndependentOperatorInputs(t *testing.T) {
	restoreReleaseStubs(t)
	var received distribution.ServerBundleInputs
	assembleServerBundle = func(input distribution.ServerBundleInputs) (distribution.Manifest, error) {
		received = input
		return distribution.Manifest{Product: "coconet-server", Version: input.Version, OS: input.OS, Architecture: input.Architecture, Files: make([]distribution.ManifestFile, 5)}, nil
	}
	archiveReleaseBundle = func(bundleRoot, archivePath string) (string, error) {
		if bundleRoot != received.OutputRoot || archivePath != bundleRoot+".tar.gz" {
			t.Fatalf("unexpected Server archive paths %q %q", bundleRoot, archivePath)
		}
		return strings.Repeat("b", 64), nil
	}
	output := t.TempDir()
	args := []string{
		"server", "--go-licenses", "/inputs/go-licenses", "--version", "0.5.0", "--arch", "amd64", "--server", "/inputs/coconet-server",
		"--journal", "/inputs/journald@coconet.conf",
		"--systemd", "/inputs/coconet-server.service", "--environment", "/inputs/server.env.example", "--nginx", "/inputs/coconet-server.conf", "--output", output, "--json",
	}
	var stdout, stderr bytes.Buffer
	if exit := run(args, &stdout, &stderr); exit != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected Server release exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	expectedRoot := filepath.Join(output, "coconet-server-0.5.0-linux-amd64")
	if received.GoLicenses != "/inputs/go-licenses" || received.OutputRoot != expectedRoot || received.ServerBinary != "/inputs/coconet-server" || received.JournalExample != "/inputs/journald@coconet.conf" || !strings.Contains(stdout.String(), `"files":5`) {
		t.Fatalf("unexpected Server bundle inputs %#v output=%q", received, stdout.String())
	}
}

func TestBundleRejectsMissingAndUnsafeArguments(t *testing.T) {
	restoreReleaseStubs(t)
	called := false
	assembleReleaseBundle = func(distribution.BundleInputs) (distribution.Manifest, error) {
		called = true
		return distribution.Manifest{}, nil
	}
	var stdout, stderr bytes.Buffer
	if exit := run([]string{"bundle", "--version", "../bad"}, &stdout, &stderr); exit != 2 || called || !strings.Contains(stderr.String(), "required release arguments") {
		t.Fatalf("unexpected missing argument result exit=%d called=%v stderr=%q", exit, called, stderr.String())
	}
	stderr.Reset()
	args := []string{
		"bundle", "--go-licenses", "/inputs/go-licenses", "--version", "../bad", "--os", "linux", "--arch", "amd64", "--runtime", "runtime",
		"--bootstrap", "bootstrap", "--node", "node", "--node-license", "node-license", "--claude-sdk", "sdk", "--claude-sdk-license", "sdk-license",
	}
	if exit := run(args, &stdout, &stderr); exit != 2 || called || !strings.Contains(stderr.String(), "invalid") {
		t.Fatalf("unexpected unsafe argument result exit=%d called=%v stderr=%q", exit, called, stderr.String())
	}
}

func TestBundleReportsAssemblyAndArchiveFailures(t *testing.T) {
	restoreReleaseStubs(t)
	assembleReleaseBundle = func(distribution.BundleInputs) (distribution.Manifest, error) {
		return distribution.Manifest{}, errors.New("assembly failed")
	}
	args := validReleaseArgs()
	var stdout, stderr bytes.Buffer
	if exit := run(args, &stdout, &stderr); exit != 1 || !strings.Contains(stderr.String(), "assembly failed") {
		t.Fatalf("unexpected assembly failure exit=%d stderr=%q", exit, stderr.String())
	}
	assembleReleaseBundle = func(input distribution.BundleInputs) (distribution.Manifest, error) {
		return distribution.Manifest{Version: input.Version, OS: input.OS, Architecture: input.Architecture}, nil
	}
	archiveReleaseBundle = func(string, string) (string, error) { return "", errors.New("archive failed") }
	stderr.Reset()
	if exit := run(args, &stdout, &stderr); exit != 1 || !strings.Contains(stderr.String(), "archive failed") {
		t.Fatalf("unexpected archive failure exit=%d stderr=%q", exit, stderr.String())
	}
}

func validReleaseArgs() []string {
	return []string{
		"bundle", "--go-licenses", "/inputs/go-licenses", "--version", "0.3.0", "--os", "linux", "--arch", "amd64", "--runtime", "runtime",
		"--bootstrap", "bootstrap", "--node", "node", "--node-license", "node-license", "--claude-sdk", "sdk", "--claude-sdk-license", "sdk-license",
	}
}

func restoreReleaseStubs(t *testing.T) {
	t.Helper()
	originalAssemble := assembleReleaseBundle
	originalServer := assembleServerBundle
	originalArchive := archiveReleaseBundle
	t.Cleanup(func() {
		assembleReleaseBundle = originalAssemble
		assembleServerBundle = originalServer
		archiveReleaseBundle = originalArchive
	})
}
