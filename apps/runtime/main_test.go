package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/adapters/claude"
	"github.com/xfey/coconet/adapters/codex"
	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/contracts"
)

func TestHelpDoesNotCreateLocalState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "unused-state")
	t.Setenv("COCONET_HOME", root)
	for _, args := range [][]string{{"--help"}, {"help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Connect this folder; choose or create a project in your browser") {
			t.Fatalf("help failed: %d %q %q", code, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "\x1b") {
			t.Fatal("redirected help contains ANSI controls")
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("help touched local state: %v", err)
	}
}

func TestAgentListDoesNotReconfigureOutdatedIntegrations(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	t.Setenv("COCONET_HOME", t.TempDir())
	paths, _ := resolveRuntimePaths()
	entry, _ := newAgentIntegration("claude", "fixture-agent", t.TempDir())
	entry.RuntimeVersion = "old"
	state := agentIntegrationState{SchemaVersion: 1, Integrations: []agentIntegration{entry}}
	if err := saveAgentIntegrationState(paths, state); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(paths.integrationPath)
	resolveAgentMarketplace = func(_ agentplugin.Host) (string, error) {
		t.Fatal("agent list attempted plugin configuration")
		return "", nil
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"agent", "list", "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 || !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
		t.Fatalf("list failed: %d %s %s", code, stdout.String(), stderr.String())
	}
	after, _ := os.ReadFile(paths.integrationPath)
	if !bytes.Equal(before, after) {
		t.Fatal("agent list changed the integration registry")
	}
}

func TestHealthCommandProducesContract(t *testing.T) {
	t.Setenv("COCONET_HOME", t.TempDir())
	t.Setenv("COCONET_CODEX_COMMAND", "command-that-does-not-exist")
	t.Setenv("COCONET_CLAUDE_COMMAND", "command-that-does-not-exist")
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"health", "--json"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	var report contracts.HealthReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != contracts.SchemaVersion || report.Status != contracts.StatusDegraded {
		t.Fatalf("unexpected report %#v", report)
	}
}

func TestRuntimeHealthAcceptsInjectedAdapters(t *testing.T) {
	t.Setenv("COCONET_HOME", t.TempDir())
	codexAdapter := codex.New("codex-fixture")
	codexAdapter.Locate = func(string) (string, error) { return "/fixture/codex-fixture", nil }
	claudeAdapter := claude.New("tclaude")
	claudeAdapter.Locate = func(string) (string, error) { return "/fixture/tclaude", nil }
	report := runtimeHealth(context.Background(), []adapters.Adapter{codexAdapter, claudeAdapter})
	if report.Status != contracts.StatusOK {
		t.Fatalf("unexpected status %q", report.Status)
	}
}

func TestRuntimeHealthAcceptsBindingLocalEndpointOverride(t *testing.T) {
	repositoryRoot := initTestRepository(t, "health-binding-override")
	configureRuntimeTest(t, t.TempDir(), repositoryRoot, "https://old-route.coconet.test")
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	account := fixtureAccount("acct_healthoverride01", "Health Override")
	credentialTarget := fixtureDeploymentTarget("https://old-route.coconet.test")
	if err := saveCredential(paths, credentialTarget, account, "fixture-token-with-enough-entropy-health"); err != nil {
		t.Fatal(err)
	}
	bindingTarget := credentialTarget
	bindingTarget.ServerURL = "https://new-route.coconet.test"
	binding := fixtureBinding(repositoryRoot, "coco_healthoverride01", account.AccountID, bindingTarget)
	binding.Workflow = localDAGWorkflow
	if err := saveBinding(paths, binding); err != nil {
		t.Fatal(err)
	}
	state, components := runtimeLocalHealth(paths, nil)
	if state.BindingCount != 1 || state.InvalidBindingCount != 0 || len(components) != 1 || components[0].Status != contracts.StatusOK {
		t.Fatalf("verified local endpoint override degraded health: state=%#v components=%#v", state, components)
	}
}

func TestHookIsSilentAndFailOpen(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"hook", "--agent", "claude"}, strings.NewReader(`{"session_id":"fixture"}`), &stdout, &stderr)
	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected hook result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestMCPInitializeListAndCall(t *testing.T) {
	t.Setenv("COCONET_CODEX_COMMAND", "command-that-does-not-exist")
	t.Setenv("COCONET_CLAUDE_COMMAND", "command-that-does-not-exist")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"coconet_health","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := serveMCP(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(lines))
	}
	for _, line := range lines {
		var response rpcResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil {
			t.Fatalf("unexpected RPC error %#v", response.Error)
		}
	}
}
