package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/agentplugin"
)

func defaultSetupFixture(t *testing.T) runtimePaths {
	t.Helper()
	restoreAgentIntegrationStubs(t)
	base := t.TempDir()
	t.Setenv("COCONET_HOME", filepath.Join(base, "coconet"))
	t.Setenv("CODEX_HOME", filepath.Join(base, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "claude"))
	t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(base, "cursor"))
	resolveAgentMarketplace = func(agentplugin.Host) (string, error) { return "/fixture/marketplace", nil }
	configureAgentIntegration = func(_ context.Context, entry agentIntegration, _ string) agentplugin.InstallResult {
		return agentplugin.InstallResult{Host: entry.Type, Command: entry.Command, Status: agentplugin.StatusConfigured}
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func setupReport(t *testing.T, expectedExit int) agentplugin.SetupReport {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if exit := runAgentSetup(context.Background(), nil, &stdout, &stderr); exit != expectedExit {
		t.Fatalf("unexpected setup exit=%d stderr=%s stdout=%s", exit, stderr.String(), stdout.String())
	}
	var report agentplugin.SetupReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestDefaultAgentSetupRegistersHostsAndPreservesRemoval(t *testing.T) {
	paths := defaultSetupFixture(t)
	report := setupReport(t, 0)
	if report.Status != "ok" || len(report.Results) != 3 {
		t.Fatalf("unexpected report: %#v", report)
	}
	state, _, err := loadAgentIntegrationState(paths)
	if err != nil || len(state.Integrations) != 3 {
		t.Fatalf("unexpected registry: %#v %v", state, err)
	}
	for _, entry := range state.Integrations {
		if (entry.Command != string(entry.Type) && !(entry.Type == agentplugin.HostCursor && entry.Command == "agent")) || entry.Status != agentIntegrationConfigured {
			t.Fatalf("unexpected default: %#v", entry)
		}
	}
	for _, path := range []string{paths.integrationPath, filepath.Join(paths.root, defaultAgentsFilename)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("unsafe setup state %s: %v", path, err)
		}
	}
	removeAgentIntegration = func(_ context.Context, entry agentIntegration) agentplugin.UninstallResult {
		return agentplugin.UninstallResult{Host: entry.Type, Status: agentplugin.StatusRemoved}
	}
	if runAgentRemove(context.Background(), []string{state.Integrations[0].ID}, &bytes.Buffer{}, &bytes.Buffer{}) != 0 {
		t.Fatal("remove failed")
	}
	report = setupReport(t, 0)
	if len(report.Results) != 2 || report.Results[0].Host != agentplugin.HostClaude || report.Results[1].Host != agentplugin.HostCursor {
		t.Fatalf("setup restored a removed default: %#v", report)
	}
}

func TestDefaultAgentSetupUpgradesLegacyWrapperRegistry(t *testing.T) {
	paths := defaultSetupFixture(t)
	root := t.TempDir()
	wrapper, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(root, "codex-fixture"))
	wrapper.Status, wrapper.RuntimeVersion = agentIntegrationConfigured, "old"
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: 1, Integrations: []agentIntegration{wrapper}}); err != nil {
		t.Fatal(err)
	}
	report := setupReport(t, 0)
	if len(report.Results) != 4 {
		t.Fatalf("defaults not added alongside wrapper: %#v", report)
	}
	state, _, _ := loadAgentIntegrationState(paths)
	updated, found := findAgentIntegration(state, wrapper.ID)
	if !found || updated.Command != wrapper.Command || updated.ConfigRoot != wrapper.ConfigRoot || !updated.CreatedAt.Equal(wrapper.CreatedAt) || updated.RuntimeVersion != version {
		t.Fatalf("wrapper changed: %#v", updated)
	}
	if len(setupReport(t, 0).Results) != 4 {
		t.Fatal("repeat setup duplicated entries")
	}
}

func TestDefaultAgentSetupRespectsExplicitConfigOwner(t *testing.T) {
	paths := defaultSetupFixture(t)
	wrapper, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", os.Getenv("CODEX_HOME"))
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: 1, Integrations: []agentIntegration{wrapper}}); err != nil {
		t.Fatal(err)
	}
	report := setupReport(t, 0)
	if len(report.Results) != 3 || report.Results[0].Command != "codex-fixture" {
		t.Fatalf("explicit config owner replaced: %#v", report)
	}
}

func TestDefaultAgentSetupIsolatesMissingHostsAndInstallFailures(t *testing.T) {
	for _, status := range []agentplugin.Status{agentplugin.StatusSkipped, agentplugin.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			paths := defaultSetupFixture(t)
			configureAgentIntegration = func(_ context.Context, entry agentIntegration, _ string) agentplugin.InstallResult {
				if entry.Type == agentplugin.HostCodex {
					return agentplugin.InstallResult{Status: status, Error: "fixture host failure"}
				}
				return agentplugin.InstallResult{Status: agentplugin.StatusConfigured}
			}
			exit := 0
			if status == agentplugin.StatusFailed {
				exit = 1
			}
			report := setupReport(t, exit)
			if len(report.Results) != 3 || report.Results[1].Status != agentplugin.StatusConfigured {
				t.Fatalf("one host blocked the other: %#v", report)
			}
			state, _, _ := loadAgentIntegrationState(paths)
			if state.Integrations[0].Status != agentIntegrationStatus(status) || state.Integrations[0].LastError != "fixture host failure" {
				t.Fatalf("host outcome not retained: %#v", state)
			}
		})
	}
}

func TestDefaultAgentSetupContinuesAfterInvalidHostConfig(t *testing.T) {
	defaultSetupFixture(t)
	t.Setenv("CODEX_HOME", string(filepath.Separator))
	report := setupReport(t, 1)
	if len(report.Results) != 3 || report.Results[0].Status != agentplugin.StatusFailed || report.Results[1].Status != agentplugin.StatusConfigured {
		t.Fatalf("invalid config blocked other host: %#v", report)
	}
}

func TestDefaultAgentSetupUsesNativeCommandsAndConventionalRoots(t *testing.T) {
	paths := defaultSetupFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CURSOR_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("COCONET_CODEX_COMMAND", "codex-fixture")
	t.Setenv("COCONET_CLAUDE_COMMAND", "tclaude")
	setupReport(t, 0)
	state, _, err := loadAgentIntegrationState(paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range state.Integrations {
		if (entry.Command != string(entry.Type) && !(entry.Type == agentplugin.HostCursor && entry.Command == "agent")) || entry.ConfigRoot != filepath.Join(home, "."+string(entry.Type)) {
			t.Fatalf("unexpected default: %#v", entry)
		}
	}
}

func TestDefaultAgentSetupRejectsCorruptPolicyWithoutChangingRegistry(t *testing.T) {
	paths := defaultSetupFixture(t)
	setupReport(t, 0)
	before, _ := os.ReadFile(paths.integrationPath)
	if err := os.WriteFile(filepath.Join(paths.root, defaultAgentsFilename), []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configureAgentIntegration = func(context.Context, agentIntegration, string) agentplugin.InstallResult {
		t.Fatal("corrupt setup state configured an Agent")
		return agentplugin.InstallResult{}
	}
	if report := setupReport(t, 1); report.Error == "" {
		t.Fatal("missing policy failure")
	}
	after, _ := os.ReadFile(paths.integrationPath)
	if !bytes.Equal(before, after) {
		t.Fatal("corrupt setup changed registry")
	}
}
