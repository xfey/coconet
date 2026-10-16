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

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/distribution"
)

func TestUninstallRunsWithoutConfirmation(t *testing.T) {
	restoreUninstallStubs(t)
	configureUninstallTestEnvironment(t)
	called := false
	uninstallRuntime = func(distribution.UninstallOptions) (distribution.UninstallResult, error) {
		called = true
		return distribution.UninstallResult{}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"uninstall"}, strings.NewReader("ignored\n"), &stdout, &stderr)
	if exit != 0 || !called || stderr.Len() != 0 || !strings.Contains(stdout.String(), "✓ Uninstalled Coconet") {
		t.Fatalf("unexpected uninstall exit=%d called=%v stdout=%q stderr=%q", exit, called, stdout.String(), stderr.String())
	}
}

func TestUninstallRemovesRegisteredAgentsAndRuntimeButPreservesData(t *testing.T) {
	restoreUninstallStubs(t)
	base := configureUninstallTestEnvironment(t)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	codex, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(base, "codex"))
	claude, _ := newAgentIntegration(agentplugin.HostClaude, "tclaude", filepath.Join(base, "claude"))
	codex.Status, claude.Status = agentIntegrationConfigured, agentIntegrationConfigured
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{codex, claude}}); err != nil {
		t.Fatal(err)
	}
	detectUninstallAgent = func(_ context.Context, host agentplugin.Host, command, _ string) agentplugin.Detection {
		return agentplugin.Detection{Host: host, Command: command, Path: "/bin/" + command, Detected: true}
	}
	uninstallAgent = func(_ context.Context, detection agentplugin.Detection, configRoot string) agentplugin.UninstallResult {
		expected := filepath.Join(base, string(detection.Host))
		if configRoot != expected {
			t.Fatalf("unexpected %s config root %q", detection.Host, configRoot)
		}
		return agentplugin.UninstallResult{Host: detection.Host, Command: detection.Command, Status: agentplugin.StatusRemoved}
	}
	uninstallRuntime = func(options distribution.UninstallOptions) (distribution.UninstallResult, error) {
		if options.InstallRoot != filepath.Join(base, "install") || options.BinDirectory != filepath.Join(base, "bin") {
			t.Fatalf("unexpected Runtime options %#v", options)
		}
		return distribution.UninstallResult{InstallRoot: options.InstallRoot, RuntimeRemoved: true}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{
		"uninstall", "--json", "--root", filepath.Join(base, "install"), "--bin-dir", filepath.Join(base, "bin"),
	}, strings.NewReader(""), &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected uninstall exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var output uninstallOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Status != "ok" || len(output.Agents) != 2 || !output.Runtime.RuntimeRemoved || !output.Data.Preserved || output.Data.Purged {
		t.Fatalf("unexpected uninstall output %#v", output)
	}
}

func TestUninstallUsesEveryRegisteredAgentIntegration(t *testing.T) {
	restoreUninstallStubs(t)
	base := configureUninstallTestEnvironment(t)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	first, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(base, "codex-fixture"))
	second, _ := newAgentIntegration(agentplugin.HostCodex, "codex-team-two", filepath.Join(base, "codex-two"))
	first.Status, second.Status = agentIntegrationConfigured, agentIntegrationConfigured
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{first, second}}); err != nil {
		t.Fatal(err)
	}
	detectUninstallAgent = func(_ context.Context, host agentplugin.Host, command, configRoot string) agentplugin.Detection {
		return agentplugin.Detection{Host: host, Command: command, Path: filepath.Join(configRoot, command), Detected: true}
	}
	removed := map[string]string{}
	uninstallAgent = func(_ context.Context, detection agentplugin.Detection, configRoot string) agentplugin.UninstallResult {
		removed[detection.Command] = configRoot
		return agentplugin.UninstallResult{Host: detection.Host, Command: detection.Command, Status: agentplugin.StatusRemoved}
	}
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"uninstall", "--json", "--root", filepath.Join(base, "install"), "--bin-dir", filepath.Join(base, "bin")}, strings.NewReader(""), &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 || removed["codex-fixture"] != first.ConfigRoot || removed["codex-team-two"] != second.ConfigRoot {
		t.Fatalf("unexpected registered uninstall exit=%d removed=%#v stdout=%q stderr=%q", exit, removed, stdout.String(), stderr.String())
	}
	state, exists, err := loadAgentIntegrationState(paths)
	if err != nil || !exists || len(state.Integrations) != 0 {
		t.Fatalf("registered integrations were not cleared: %#v exists=%v err=%v", state, exists, err)
	}
}

func TestUninstallCleansSharedConfigRootOnce(t *testing.T) {
	restoreUninstallStubs(t)
	base := configureUninstallTestEnvironment(t)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	sharedRoot := filepath.Join(base, "shared-codex")
	first, _ := newAgentIntegration(agentplugin.HostCodex, "missing-codex", sharedRoot)
	second, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", sharedRoot)
	first.Status, second.Status = agentIntegrationConfigured, agentIntegrationConfigured
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{first, second}}); err != nil {
		t.Fatal(err)
	}
	detectUninstallAgent = func(_ context.Context, host agentplugin.Host, command, _ string) agentplugin.Detection {
		return agentplugin.Detection{Host: host, Command: command, Path: "/bin/" + command, Detected: command == "codex-fixture"}
	}
	removals := 0
	uninstallAgent = func(_ context.Context, detection agentplugin.Detection, configRoot string) agentplugin.UninstallResult {
		removals++
		if detection.Command != "codex-fixture" || configRoot != sharedRoot {
			t.Fatalf("unexpected shared-root target detection=%#v root=%q", detection, configRoot)
		}
		return agentplugin.UninstallResult{Host: detection.Host, Command: detection.Command, Status: agentplugin.StatusRemoved}
	}
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"uninstall", "--json", "--root", filepath.Join(base, "install"), "--bin-dir", filepath.Join(base, "bin")}, strings.NewReader(""), &stdout, &stderr)
	if exit != 0 || removals != 1 || stderr.Len() != 0 {
		t.Fatalf("unexpected shared-root uninstall exit=%d removals=%d stdout=%q stderr=%q", exit, removals, stdout.String(), stderr.String())
	}
	state, exists, err := loadAgentIntegrationState(paths)
	if err != nil || !exists || len(state.Integrations) != 0 {
		t.Fatalf("shared integrations were not cleared: %#v exists=%v err=%v", state, exists, err)
	}
}

func TestUninstallAgentFailurePreservesRuntimeAndData(t *testing.T) {
	restoreUninstallStubs(t)
	base := configureUninstallTestEnvironment(t)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	codex, _ := newAgentIntegration(agentplugin.HostCodex, "codex-fixture", filepath.Join(base, "codex"))
	claude, _ := newAgentIntegration(agentplugin.HostClaude, "tclaude", filepath.Join(base, "claude"))
	codex.Status, claude.Status = agentIntegrationConfigured, agentIntegrationConfigured
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: agentIntegrationSchemaVersion, Integrations: []agentIntegration{codex, claude}}); err != nil {
		t.Fatal(err)
	}
	detectUninstallAgent = func(_ context.Context, host agentplugin.Host, command, _ string) agentplugin.Detection {
		return agentplugin.Detection{Host: host, Command: command, Path: "/bin/" + command, Detected: true}
	}
	uninstallAgent = func(_ context.Context, detection agentplugin.Detection, _ string) agentplugin.UninstallResult {
		status := agentplugin.StatusRemoved
		message := ""
		if detection.Host == agentplugin.HostCodex {
			status = agentplugin.StatusFailed
			message = "Codex removal failed"
		}
		return agentplugin.UninstallResult{Host: detection.Host, Command: detection.Command, Status: status, Error: message}
	}
	uninstallRuntime = func(distribution.UninstallOptions) (distribution.UninstallResult, error) {
		t.Fatal("Runtime removal ran after Agent failure")
		return distribution.UninstallResult{}, nil
	}
	purgeRuntimeData = func(runtimePaths) (purgeResult, error) {
		t.Fatal("purge ran after Agent failure")
		return purgeResult{}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"uninstall", "--purge", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Runtime and local data were preserved") {
		t.Fatalf("unexpected partial result exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestUninstallPurgeRunsBeforeRuntimeRemoval(t *testing.T) {
	restoreUninstallStubs(t)
	configureUninstallTestEnvironment(t)
	order := []string{}
	purgeRuntimeData = func(paths runtimePaths) (purgeResult, error) {
		order = append(order, "purge")
		return purgeResult{Root: paths.root, Purged: true, RootRemoved: true}, nil
	}
	uninstallRuntime = func(options distribution.UninstallOptions) (distribution.UninstallResult, error) {
		order = append(order, "runtime")
		return distribution.UninstallResult{InstallRoot: options.InstallRoot, RuntimeRemoved: true}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"uninstall", "--purge", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 || strings.Join(order, ",") != "purge,runtime" {
		t.Fatalf("unexpected purge result exit=%d order=%v stdout=%q stderr=%q", exit, order, stdout.String(), stderr.String())
	}
}

func TestUninstallHelpIsReadOnlyAndSuccessful(t *testing.T) {
	restoreUninstallStubs(t)
	configureUninstallTestEnvironment(t)
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"uninstall", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "--purge") {
		t.Fatalf("unexpected help result exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestUninstallJSONWriteFailureReturnsFailure(t *testing.T) {
	restoreUninstallStubs(t)
	configureUninstallTestEnvironment(t)
	var stderr bytes.Buffer
	exit := run(context.Background(), []string{"uninstall", "--json"}, strings.NewReader(""), failingWriter{}, &stderr)
	if exit != 1 || !strings.Contains(stderr.String(), "write Coconet uninstall result") {
		t.Fatalf("unexpected output failure exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestPurgeRemovesOnlyOwnedEntriesAndPreservesForeignFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coconet")
	paths := runtimePaths{
		root: root, credentialPath: filepath.Join(root, "credentials.json"),
		bindingPath: filepath.Join(root, "bindings.json"), operationPath: filepath.Join(root, "operations.json"), explicitHome: true,
	}
	for _, path := range []string{paths.credentialPath, paths.bindingPath, filepath.Join(root, defaultAgentsFilename), filepath.Join(root, "keep.txt")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "deployments", "fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := purgeLocalState(paths)
	if err != nil || !result.Purged || result.RootRemoved || result.EntriesRemoved != 4 {
		t.Fatalf("unexpected purge result %#v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("foreign file was removed: %v", err)
	}
	for _, path := range []string{paths.credentialPath, paths.bindingPath, filepath.Join(root, defaultAgentsFilename), filepath.Join(root, "deployments")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned state remains %q: %v", path, err)
		}
	}
}

func TestPurgeRejectsBroadCustomHome(t *testing.T) {
	root := t.TempDir()
	if _, err := purgeLocalState(runtimePaths{root: root, explicitHome: true}); err == nil || !strings.Contains(err.Error(), "not named coconet") {
		t.Fatalf("unsafe purge root was accepted: %v", err)
	}
}

func TestPurgeKeychainCredentialsStopsAtExactNotFound(t *testing.T) {
	original := deleteNextKeychainCredential
	calls := 0
	deleteNextKeychainCredential = func() ([]byte, error) {
		calls++
		if calls <= 2 {
			return []byte("deleted"), nil
		}
		return []byte("The specified item could not be found in the keychain."), errors.New("exit 44")
	}
	t.Cleanup(func() { deleteNextKeychainCredential = original })
	removed, err := purgeKeychainCredentials()
	if err != nil || removed != 2 || calls != 3 {
		t.Fatalf("unexpected Keychain purge removed=%d calls=%d err=%v", removed, calls, err)
	}
}

func configureUninstallTestEnvironment(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_BIN_HOME", filepath.Join(base, "bin"))
	t.Setenv("COCONET_HOME", filepath.Join(base, "config", "coconet"))
	t.Setenv("CODEX_HOME", filepath.Join(base, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "claude"))
	return base
}

func restoreUninstallStubs(t *testing.T) {
	t.Helper()
	originalDetect := detectUninstallAgent
	originalAgent := uninstallAgent
	originalRuntime := uninstallRuntime
	originalPurge := purgeRuntimeData
	detectUninstallAgent = func(_ context.Context, host agentplugin.Host, command, _ string) agentplugin.Detection {
		return agentplugin.Detection{Host: host, Command: command}
	}
	uninstallAgent = func(_ context.Context, detection agentplugin.Detection, _ string) agentplugin.UninstallResult {
		return agentplugin.UninstallResult{Host: detection.Host, Command: detection.Command, Status: agentplugin.StatusSkipped}
	}
	uninstallRuntime = func(options distribution.UninstallOptions) (distribution.UninstallResult, error) {
		return distribution.UninstallResult{InstallRoot: options.InstallRoot, RuntimeRemoved: true}, nil
	}
	purgeRuntimeData = func(paths runtimePaths) (purgeResult, error) {
		return purgeResult{Root: paths.root, Purged: true, RootRemoved: true}, nil
	}
	t.Cleanup(func() {
		detectUninstallAgent = originalDetect
		uninstallAgent = originalAgent
		uninstallRuntime = originalRuntime
		purgeRuntimeData = originalPurge
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("fixture write failure")
}
