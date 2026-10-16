package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/agentplugin"
)

func TestAgentAddSupportsMultipleInstancesOfOneType(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	resolveAgentMarketplace = func(host agentplugin.Host) (string, error) {
		return "/runtime/marketplaces/" + string(host), nil
	}
	configured := []agentIntegration{}
	configureAgentIntegration = func(_ context.Context, integration agentIntegration, marketplace string) agentplugin.InstallResult {
		if marketplace != "/runtime/marketplaces/codex" {
			t.Fatalf("unexpected marketplace %q", marketplace)
		}
		configured = append(configured, integration)
		return agentplugin.InstallResult{Host: integration.Type, Command: integration.Command, Version: "fixture", Status: agentplugin.StatusConfigured}
	}
	root := t.TempDir()
	for _, fixture := range []struct{ command, config string }{
		{"codex", filepath.Join(root, "codex")},
		{"codex-fixture", filepath.Join(root, "codex-fixture")},
	} {
		var stdout, stderr bytes.Buffer
		exit := runAgent(context.Background(), []string{"add", "codex", "--command", fixture.command, "--config-root", fixture.config, "--json"}, &stdout, &stderr)
		if exit != 0 || stderr.Len() != 0 {
			t.Fatalf("add failed exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
		}
		var output agentMutationOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Integration.Status != agentIntegrationConfigured {
			t.Fatalf("unexpected add output %#v err=%v", output, err)
		}
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	state, exists, err := loadAgentIntegrationState(paths)
	if err != nil || !exists || len(state.Integrations) != 2 || len(configured) != 2 || state.Integrations[0].ID == state.Integrations[1].ID {
		t.Fatalf("unexpected registry state=%#v configured=%#v exists=%v err=%v", state, configured, exists, err)
	}
}

func TestAgentListSeparatesSummaryAndVerboseDetails(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	integration, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(t.TempDir(), "codex-fixture"))
	integration.Status = agentIntegrationConfigured
	integration.RuntimeVersion = "0.8.0"
	integration.AgentVersion = "fixture"
	paths, _ := resolveRuntimePaths()
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{integration}}); err != nil {
		t.Fatal(err)
	}

	var summary, verbose, stderr bytes.Buffer
	if exit := runAgentList(nil, &summary, &stderr); exit != 0 {
		t.Fatalf("agent list failed exit=%d stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(summary.String(), "TYPE") || !strings.Contains(summary.String(), "codex-fixture") || !strings.Contains(summary.String(), "Plugin installed") || strings.Contains(summary.String(), integration.ID) || strings.Contains(summary.String(), integration.ConfigRoot) {
		t.Fatalf("unexpected agent summary %q", summary.String())
	}
	if exit := runAgentList([]string{"--verbose"}, &verbose, &stderr); exit != 0 {
		t.Fatalf("verbose agent list failed exit=%d stderr=%q", exit, stderr.String())
	}
	for _, detail := range []string{integration.ID, integration.ConfigRoot, integration.RuntimeVersion, integration.AgentVersion} {
		if !strings.Contains(verbose.String(), detail) {
			t.Fatalf("verbose Agent list omitted %q: %q", detail, verbose.String())
		}
	}
}

func TestAgentSetupFailureKeepsRetryInstanceAndJSONOutput(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	resolveAgentMarketplace = func(agentplugin.Host) (string, error) { return "/fixture/marketplace", nil }
	configureAgentIntegration = func(_ context.Context, integration agentIntegration, _ string) agentplugin.InstallResult {
		return agentplugin.InstallResult{Host: integration.Type, Command: integration.Command, Status: agentplugin.StatusFailed, Error: "fixture setup failure"}
	}
	root := filepath.Join(t.TempDir(), "team's agent config")
	args := []string{"add", "codex", "--command", "codex-fixture", "--config-root", root}
	var stdout, stderr bytes.Buffer
	if code := runAgent(context.Background(), args, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("human failure mixed with command output: %d %q %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "'--command' 'codex-fixture'") || !strings.Contains(stderr.String(), "'--config-root' '"+strings.ReplaceAll(root, "'", "'\"'\"'")+"'") {
		t.Fatalf("retry lost the explicit agent identity: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runAgent(context.Background(), append(args, "--json"), &stdout, &stderr); code != 1 || stderr.Len() != 0 {
		t.Fatalf("JSON failure changed: %d %q", code, stderr.String())
	}
	var result agentMutationOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Integration.Command != "codex-fixture" || result.Integration.ConfigRoot != root {
		t.Fatalf("invalid JSON failure: %s %v", stdout.String(), err)
	}
}

func TestAgentAddRetainsFailureForInspectionAndRetry(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	resolveAgentMarketplace = func(agentplugin.Host) (string, error) { return "/marketplace", nil }
	configureAgentIntegration = func(_ context.Context, integration agentIntegration, _ string) agentplugin.InstallResult {
		return agentplugin.InstallResult{Host: integration.Type, Command: integration.Command, Status: agentplugin.StatusFailed, Error: "fixture install failed"}
	}
	var stdout, stderr bytes.Buffer
	exit := runAgent(context.Background(), []string{"add", "claude", "--command", "tclaude", "--config-root", filepath.Join(t.TempDir(), "tclaude"), "--json"}, &stdout, &stderr)
	if exit != 1 || stderr.Len() != 0 {
		t.Fatalf("unexpected failure exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	paths, _ := resolveRuntimePaths()
	state, exists, err := loadAgentIntegrationState(paths)
	if err != nil || !exists || len(state.Integrations) != 1 || state.Integrations[0].Status != agentIntegrationFailed || state.Integrations[0].LastError != "fixture install failed" {
		t.Fatalf("failure was not retained: %#v exists=%v err=%v", state, exists, err)
	}
}

func TestAutomaticAgentSyncOnlyUpdatesOlderRuntimeEntries(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	resolveAgentMarketplace = func(agentplugin.Host) (string, error) { return "/marketplace", nil }
	now := time.Date(2026, 8, 31, 1, 2, 3, 0, time.UTC)
	agentIntegrationNow = func() time.Time { return now }
	old, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(t.TempDir(), "codex-fixture"))
	current, _ := newAgentIntegration(agentplugin.HostClaude, "tclaude", filepath.Join(t.TempDir(), "tclaude"))
	old.Status, old.RuntimeVersion = agentIntegrationConfigured, "0.5.0"
	current.Status, current.RuntimeVersion = agentIntegrationConfigured, version
	paths, _ := resolveRuntimePaths()
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{old, current}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	configureAgentIntegration = func(_ context.Context, integration agentIntegration, _ string) agentplugin.InstallResult {
		calls++
		return agentplugin.InstallResult{Host: integration.Type, Command: integration.Command, Status: agentplugin.StatusConfigured}
	}
	if err := syncRegisteredAgentIntegrations(context.Background(), paths, true); err != nil {
		t.Fatal(err)
	}
	state, _, _ := loadAgentIntegrationState(paths)
	if calls != 1 || state.Integrations[0].RuntimeVersion != version || state.Integrations[1].RuntimeVersion != version {
		t.Fatalf("unexpected automatic sync calls=%d state=%#v", calls, state)
	}
}

func TestInterruptedSetupPersistsCompletedAndAttemptedEntries(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	resolveAgentMarketplace = func(agentplugin.Host) (string, error) { return "/marketplace", nil }
	paths, _ := resolveRuntimePaths()
	entries := []agentIntegration{}
	for _, command := range []string{"first", "interrupted", "later"} {
		entry, _ := newAgentIntegration(agentplugin.HostClaude, command, filepath.Join(t.TempDir(), command))
		entry.RuntimeVersion = "old"
		entries = append(entries, entry)
	}
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: 1, Integrations: entries}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	configureAgentIntegration = func(_ context.Context, entry agentIntegration, _ string) agentplugin.InstallResult {
		stored, _, err := loadAgentIntegrationState(paths)
		if err != nil {
			t.Fatal(err)
		}
		attempt, _ := findAgentIntegration(stored, entry.ID)
		if attempt.Status != agentIntegrationFailed || attempt.RuntimeVersion != version || attempt.LastError == "" {
			t.Fatal("attempt was not recorded before starting the host")
		}
		if entry.Command == "interrupted" {
			if stored.Integrations[0].Status != agentIntegrationConfigured {
				t.Fatal("completed predecessor was not persisted")
			}
			cancel()
			return agentplugin.InstallResult{Status: agentplugin.StatusFailed, Error: "cancelled"}
		}
		return agentplugin.InstallResult{Status: agentplugin.StatusConfigured}
	}
	var progress bytes.Buffer
	if err := syncAgentIntegrationsWithProgress(ctx, paths, false, &progress); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !strings.Contains(progress.String(), "interrupted") || !strings.Contains(progress.String(), "cancelled") {
		t.Fatal("missing setup progress and failure")
	}
	configureAgentIntegration = func(_ context.Context, entry agentIntegration, _ string) agentplugin.InstallResult {
		if entry.Command != "later" {
			t.Fatalf("automatic sync repeated a completed/failed attempt: %s", entry.Command)
		}
		return agentplugin.InstallResult{Status: agentplugin.StatusConfigured}
	}
	if err := syncRegisteredAgentIntegrations(context.Background(), paths, true); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := loadAgentIntegrationState(paths)
	if stored.Integrations[1].Status != agentIntegrationFailed || stored.Integrations[2].Status != agentIntegrationConfigured {
		t.Fatalf("recovery lost failed attempt or skipped remaining host: %#v", stored)
	}
}

func TestAgentRemovePreservesIntegrationSharingTheSameConfigRoot(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	root := filepath.Join(t.TempDir(), "codex")
	first, _ := newAgentIntegration(agentplugin.HostCodex, "codex", root)
	second, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", root)
	first.Status, second.Status = agentIntegrationConfigured, agentIntegrationConfigured
	paths, _ := resolveRuntimePaths()
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{first, second}}); err != nil {
		t.Fatal(err)
	}
	removals := 0
	removeAgentIntegration = func(_ context.Context, integration agentIntegration) agentplugin.UninstallResult {
		removals++
		return agentplugin.UninstallResult{Host: integration.Type, Command: integration.Command, Status: agentplugin.StatusRemoved}
	}
	if exit := runAgent(context.Background(), []string{"remove", first.ID}, &bytes.Buffer{}, &bytes.Buffer{}); exit != 0 || removals != 0 {
		t.Fatalf("shared removal unexpectedly uninstalled the Plugin: exit=%d removals=%d", exit, removals)
	}
	if exit := runAgent(context.Background(), []string{"remove", second.ID}, &bytes.Buffer{}, &bytes.Buffer{}); exit != 0 || removals != 1 {
		t.Fatalf("last removal did not uninstall the Plugin: exit=%d removals=%d", exit, removals)
	}
}

func TestAgentIntegrationInputAndStateAreStrict(t *testing.T) {
	if _, err := newAgentIntegration(agentplugin.HostCodex, "bin/codex-fixture", "/tmp/codex-fixture"); err == nil {
		t.Fatal("relative command path was accepted")
	}
	if _, err := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", "relative"); err == nil {
		t.Fatal("relative config root was accepted")
	}
	if _, err := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", string(filepath.Separator)); err == nil {
		t.Fatal("broad config root was accepted")
	}
	absoluteCommand := filepath.Join(t.TempDir(), "bin", "..", "codex-fixture")
	normalized, err := newAgentIntegration(agentplugin.HostCodex, absoluteCommand, "/tmp/codex-fixture")
	if err != nil || normalized.Command != filepath.Clean(absoluteCommand) {
		t.Fatalf("absolute command was not normalized: %#v %v", normalized, err)
	}
	paths := runtimePaths{root: t.TempDir()}
	paths.integrationPath = filepath.Join(paths.root, "agent-integrations.json")
	invalid := `{"schema_version":1,"integrations":[],"unknown":true}`
	if err := os.WriteFile(paths.integrationPath, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAgentIntegrationState(paths); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("invalid registry was accepted: %v", err)
	}
}

func TestAgentRemoveReportsJSONWriteFailure(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", filepath.Join(t.TempDir(), "coconet"))
	integration, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(t.TempDir(), "codex-fixture"))
	integration.Status = agentIntegrationConfigured
	paths, _ := resolveRuntimePaths()
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{integration}}); err != nil {
		t.Fatal(err)
	}
	removeAgentIntegration = func(_ context.Context, integration agentIntegration) agentplugin.UninstallResult {
		return agentplugin.UninstallResult{Host: integration.Type, Command: integration.Command, Status: agentplugin.StatusFailed, Error: "fixture removal failed"}
	}
	var stderr bytes.Buffer
	exit := runAgent(context.Background(), []string{"remove", integration.ID, "--json"}, failingWriter{}, &stderr)
	if exit != 1 || !strings.Contains(stderr.String(), "fixture write failure") {
		t.Fatalf("unexpected JSON write failure exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestAgentMarketplaceResolutionFailureIsRecorded(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	resolveAgentMarketplace = func(agentplugin.Host) (string, error) { return "", errors.New("marketplace missing") }
	integration, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(t.TempDir(), "codex-fixture"))
	updated := syncOneAgentIntegration(context.Background(), integration)
	if updated.Status != agentIntegrationFailed || updated.LastError != "marketplace missing" {
		t.Fatalf("unexpected resolution failure %#v", updated)
	}
}

func restoreAgentIntegrationStubs(t *testing.T) {
	t.Helper()
	originalNow := agentIntegrationNow
	originalResolve := resolveAgentMarketplace
	originalConfigure := configureAgentIntegration
	originalRemove := removeAgentIntegration
	t.Cleanup(func() {
		agentIntegrationNow = originalNow
		resolveAgentMarketplace = originalResolve
		configureAgentIntegration = originalConfigure
		removeAgentIntegration = originalRemove
	})
}
