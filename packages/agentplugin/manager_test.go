package agentplugin

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	paths map[string]string
	runs  []fakeRun
	call  int
}

type fakeRun struct {
	command string
	args    []string
	stdout  string
	stderr  string
	err     error
}

func (runner *fakeRunner) LookPath(command string) (string, error) {
	if path := runner.paths[command]; path != "" {
		return path, nil
	}
	return "", &exec.Error{Name: command, Err: exec.ErrNotFound}
}

func (runner *fakeRunner) Run(_ context.Context, command string, args ...string) (CommandOutput, error) {
	if runner.call >= len(runner.runs) {
		return CommandOutput{}, errors.New("unexpected command")
	}
	expected := runner.runs[runner.call]
	runner.call++
	if expected.command != command || !reflect.DeepEqual(expected.args, args) {
		return CommandOutput{}, errors.New("unexpected command arguments")
	}
	return CommandOutput{Stdout: []byte(expected.stdout), Stderr: []byte(expected.stderr)}, expected.err
}

func TestDetectAndInstallCodex(t *testing.T) {
	runner := &fakeRunner{
		paths: map[string]string{"codex-fixture": "/bin/codex-fixture"},
		runs: []fakeRun{
			{command: "/bin/codex-fixture", args: []string{"--version"}, stdout: "codex-fixture 1.0\ncodex 2.0\n"},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "add", "/install/current/marketplaces/codex", "--json"}},
			{command: "/bin/codex-fixture", args: []string{"plugin", "add", "coconet@coconet", "--json"}},
			{command: "/bin/codex-fixture", args: []string{"plugin", "list", "--available", "--json"}, stdout: `{"installed":[{"pluginId":"coconet@coconet","installed":true,"enabled":true}]}`, stderr: "non-JSON warning"},
		},
	}
	manager := Manager{Runner: runner}
	detection := manager.Detect(context.Background(), HostCodex, "codex-fixture")
	if !detection.Detected || detection.Version != "codex-fixture 1.0 codex 2.0" {
		t.Fatalf("unexpected detection %#v", detection)
	}
	result := manager.Install(context.Background(), detection, "/install/current/marketplaces/codex")
	if result.Status != StatusConfigured || result.Error != "" || runner.call != len(runner.runs) {
		t.Fatalf("unexpected install result %#v calls=%d", result, runner.call)
	}
}

func TestDetectAndInstallClaude(t *testing.T) {
	runner := &fakeRunner{
		paths: map[string]string{"tclaude": "/bin/tclaude"},
		runs: []fakeRun{
			{command: "/bin/tclaude", args: []string{"--version"}, stdout: "tclaude 1.0"},
			{command: "/bin/tclaude", args: []string{"plugin", "marketplace", "add", "/install/current/marketplaces/claude", "--scope", "user"}},
			{command: "/bin/tclaude", args: []string{"plugin", "install", "coconet@coconet", "--scope", "user"}},
			{command: "/bin/tclaude", args: []string{"plugin", "update", "coconet@coconet", "--scope", "user"}},
			{command: "/bin/tclaude", args: []string{"plugin", "list", "--json"}, stdout: `[{"id":"coconet@coconet","enabled":true}]`},
		},
	}
	manager := Manager{Runner: runner}
	detection := manager.Detect(context.Background(), HostClaude, "tclaude")
	result := manager.Install(context.Background(), detection, "/install/current/marketplaces/claude")
	if result.Status != StatusConfigured || runner.call != len(runner.runs) {
		t.Fatalf("unexpected install result %#v calls=%d", result, runner.call)
	}
}

func TestClaudeVerificationAcceptsOneJSONValueAfterWrapperNotice(t *testing.T) {
	content := []byte("TClaude update available; restart later.\n[{\"id\":\"coconet@coconet\",\"enabled\":true}]\n")
	if err := verifyInstalled(HostClaude, content); err != nil {
		t.Fatalf("wrapper notice hid a valid Plugin result: %v", err)
	}
	if err := verifyInstalled(HostClaude, append(content, []byte("trailing failure")...)); err == nil {
		t.Fatal("trailing non-JSON output was accepted")
	}
}

func TestConfigEnvironmentMapsEachAgentType(t *testing.T) {
	if environment := ConfigEnvironment(HostCodex, "/tmp/codex-fixture"); !reflect.DeepEqual(environment, map[string]string{"CODEX_HOME": "/tmp/codex-fixture"}) {
		t.Fatalf("unexpected Codex environment %#v", environment)
	}
	if environment := ConfigEnvironment(HostClaude, "/tmp/tclaude"); !reflect.DeepEqual(environment, map[string]string{"CLAUDE_CONFIG_DIR": "/tmp/tclaude"}) {
		t.Fatalf("unexpected Claude environment %#v", environment)
	}
}

func TestInstallCodexRelocatesItsVersionedMarketplace(t *testing.T) {
	conflict := errors.New("different source")
	runner := &fakeRunner{
		runs: []fakeRun{
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "add", "/install/current/marketplaces/codex", "--json"}, stderr: "marketplace 'coconet' is already added from a different source", err: conflict},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "list", "--json"}, stdout: `{"marketplaces":[{"name":"coconet","root":"/install/versions/old/marketplaces/codex"}]}`},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "remove", "coconet", "--json"}},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "add", "/install/current/marketplaces/codex", "--json"}},
			{command: "/bin/codex-fixture", args: []string{"plugin", "add", "coconet@coconet", "--json"}},
			{command: "/bin/codex-fixture", args: []string{"plugin", "list", "--available", "--json"}, stdout: `{"installed":[{"pluginId":"coconet@coconet","installed":true,"enabled":true}]}`},
		},
	}
	detection := Detection{Host: HostCodex, Command: "codex-fixture", Path: "/bin/codex-fixture", Detected: true}
	result := (Manager{Runner: runner}).Install(context.Background(), detection, "/install/current/marketplaces/codex")
	if result.Status != StatusConfigured || runner.call != len(runner.runs) {
		t.Fatalf("Codex marketplace relocation failed: %#v calls=%d", result, runner.call)
	}
}

func TestInstallCodexRestoresPreviousMarketplaceWhenRelocationFails(t *testing.T) {
	conflict := errors.New("different source")
	replacementFailure := errors.New("replacement failed")
	runner := &fakeRunner{
		runs: []fakeRun{
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "add", "/install/current/marketplaces/codex", "--json"}, stderr: "marketplace 'coconet' is already added from a different source", err: conflict},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "list", "--json"}, stdout: `{"marketplaces":[{"name":"coconet","root":"/install/versions/old/marketplaces/codex"}]}`},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "remove", "coconet", "--json"}},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "add", "/install/current/marketplaces/codex", "--json"}, stderr: "replacement failed", err: replacementFailure},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "remove", "coconet", "--json"}},
			{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "add", "/install/versions/old/marketplaces/codex", "--json"}},
		},
	}
	detection := Detection{Host: HostCodex, Command: "codex-fixture", Path: "/bin/codex-fixture", Detected: true}
	result := (Manager{Runner: runner}).Install(context.Background(), detection, "/install/current/marketplaces/codex")
	if result.Status != StatusFailed || !strings.Contains(result.Error, "replacement failed") || runner.call != len(runner.runs) {
		t.Fatalf("Codex marketplace rollback failed: %#v calls=%d", result, runner.call)
	}
}

func TestMissingAgentIsSkippedAndFailureIsBounded(t *testing.T) {
	manager := Manager{Runner: &fakeRunner{paths: map[string]string{}}}
	detection := manager.Detect(context.Background(), HostCodex, "missing")
	if detection.Detected || detection.Warning != "" {
		t.Fatalf("unexpected missing detection %#v", detection)
	}
	if result := manager.Install(context.Background(), detection, "/marketplace"); result.Status != StatusSkipped {
		t.Fatalf("unexpected missing result %#v", result)
	}

	longOutput := strings.Repeat("failure ", 100)
	runner := &fakeRunner{
		paths: map[string]string{"codex": "/bin/codex"},
		runs: []fakeRun{
			{command: "/bin/codex", args: []string{"--version"}, stdout: "codex"},
			{command: "/bin/codex", args: []string{"plugin", "marketplace", "add", "/marketplace", "--json"}, stderr: longOutput, err: errors.New("failed")},
		},
	}
	manager = Manager{Runner: runner}
	detection = manager.Detect(context.Background(), HostCodex, "codex")
	result := manager.Install(context.Background(), detection, "/marketplace")
	if result.Status != StatusFailed || len(result.Error) > 550 || !strings.Contains(result.Error, "add Codex Marketplace") {
		t.Fatalf("unexpected failure %#v", result)
	}
}
