package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/distribution"
)

func TestBootstrapAutomaticallyConfiguresDefaultAgents(t *testing.T) {
	base, repository := t.TempDir(), projectRoot(t)
	bootstrap, runtimeBinary := filepath.Join(base, "bootstrap"), filepath.Join(base, "runtime")
	buildBinary(t, repository, bootstrap, "./apps/bootstrap")
	buildBinary(t, repository, runtimeBinary, "./apps/runtime")
	fixture := filepath.Join(base, "unused-input")
	if err := os.WriteFile(fixture, []byte("fixture\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(base, "bundle")
	pluginContent, err := os.ReadFile(filepath.Join(repository, "plugins/codex/coconet/.codex-plugin/plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(pluginContent, &plugin); err != nil {
		t.Fatal(err)
	}
	notices := filepath.Join(base, "notices")
	if err := os.MkdirAll(filepath.Join(notices, "go"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.json", "go/LICENSE"} {
		if err := os.WriteFile(filepath.Join(notices, name), []byte("fixture"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	_, err = distribution.AssembleBundle(distribution.BundleInputs{
		GoLicenses: notices, Version: plugin.Version, OS: runtime.GOOS, Architecture: runtime.GOARCH, OutputRoot: bundle,
		RuntimeBinary: runtimeBinary, BootstrapBinary: bootstrap, NodeBinary: fixture, NodeLicense: fixture,
		ClaudeHelper: fixture, ClaudeSDK: fixture, ClaudeSDKLicense: fixture,
		CodexPluginRoot: filepath.Join(repository, "plugins/codex/coconet"), ClaudePluginRoot: filepath.Join(repository, "plugins/claude/coconet"), CursorPluginRoot: filepath.Join(repository, "plugins/cursor/coconet"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"both", "missing-claude", "failed-claude", "skip"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			commands, state := filepath.Join(root, "commands"), filepath.Join(root, "state/coconet")
			codexRoot, claudeRoot := filepath.Join(root, "agents/codex"), filepath.Join(root, "agents/claude")
			for _, dir := range []string{commands, codexRoot, claudeRoot} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// These are inert fixture executables. PATH excludes all real Agents.
			for _, host := range []string{"codex", "claude", "agent"} {
				if host == "claude" && scenario == "missing-claude" {
					continue
				}
				script := defaultAgentFixtureScript(host)
				if host == "agent" {
					script = "#!/bin/sh\nprintf '2026.10.14-fixture\\n'\n"
				}
				if err := os.WriteFile(filepath.Join(commands, host), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{
				"PATH=" + commands, "HOME=" + root, "XDG_CONFIG_HOME=" + filepath.Dir(state),
				"CODEX_HOME=" + codexRoot, "CLAUDE_CONFIG_DIR=" + claudeRoot,
				"COCONET_TEST_FAIL_CLAUDE=" + scenario,
			}
			installRoot := filepath.Join(root, "install")
			args := []string{"install", "--bundle", bundle, "--root", installRoot, "--no-bin-link", "--json"}
			if scenario == "skip" {
				args = append(args, "--skip-agents")
			}
			var first []byte
			for attempt := 0; attempt < 2; attempt++ {
				command := exec.Command(bootstrap, args...)
				command.Env = env
				var progress bytes.Buffer
				command.Stderr = &progress
				output, err := command.Output()
				if err != nil {
					t.Fatalf("Bootstrap failed: %v %s %s", err, output, progress.String())
				}
				var result struct {
					Status        string                  `json:"status"`
					Agents        agentplugin.SetupReport `json:"agents"`
					AlreadyExists bool                    `json:"already_exists"`
				}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("invalid output: %s: %v", output, err)
				}
				if result.Status != "ok" || result.AlreadyExists != (attempt == 1) {
					t.Fatalf("unexpected Runtime install: %s", output)
				}
				if scenario == "skip" {
					if result.Agents.Status != "skipped" || len(result.Agents.Results) != 0 {
						t.Fatalf("skip installed Agents: %s", output)
					}
					if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("skip created Agent state: %v", err)
					}
					continue
				}
				if !bytes.Contains(progress.Bytes(), []byte("Configuring Coconet plugin for")) {
					t.Fatal("missing live Agent setup progress on stderr")
				}
				wantClaude := agentplugin.StatusConfigured
				if scenario == "missing-claude" {
					wantClaude = agentplugin.StatusSkipped
				}
				if scenario == "failed-claude" {
					wantClaude = agentplugin.StatusFailed
				}
				if len(result.Agents.Results) != 3 || result.Agents.Results[0].Status != agentplugin.StatusConfigured || result.Agents.Results[1].Status != wantClaude || result.Agents.Results[2].Status != agentplugin.StatusConfigured {
					t.Fatalf("wrong host outcomes: %s", output)
				}
				if (result.Agents.Status == "partial") != (scenario == "failed-claude") {
					t.Fatalf("wrong partial status: %s", output)
				}
				registry, err := os.ReadFile(filepath.Join(state, "agent-integrations.json"))
				if err != nil {
					t.Fatal(err)
				}
				var stored struct {
					Integrations []struct {
						ID         string `json:"id"`
						Status     string `json:"status"`
						ConfigRoot string `json:"config_root"`
					} `json:"integrations"`
				}
				if err := json.Unmarshal(registry, &stored); err != nil || len(stored.Integrations) != 3 {
					t.Fatalf("invalid registry: %s %v", registry, err)
				}
				identity, _ := json.Marshal(stored)
				if attempt == 0 {
					first = identity
				} else if !bytes.Equal(first, identity) {
					t.Fatal("repeat install changed integration identities")
				}
				if stored.Integrations[0].ConfigRoot != codexRoot || stored.Integrations[1].ConfigRoot != claudeRoot {
					t.Fatal("Agent configuration escaped fixture roots")
				}
				for _, path := range []string{"bindings.json", "credentials.json"} {
					if _, err := os.Stat(filepath.Join(state, path)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("installation created collaboration state: %s %v", path, err)
					}
				}
			}
		})
	}
}

func defaultAgentFixtureScript(host string) string {
	configVariable, manifest := "CODEX_HOME", ".agents/plugins/marketplace.json"
	list := `{"installed":[{"pluginId":"coconet@coconet","installed":true,"enabled":true}]}`
	if host == "claude" {
		configVariable, manifest, list = "CLAUDE_CONFIG_DIR", ".claude-plugin/marketplace.json", `[{"id":"coconet@coconet","enabled":true}]`
	}
	return `#!/bin/sh
set -eu
config="$` + configVariable + `"
if [ "$1" = "--version" ]; then printf 'fixture-host\n'; exit 0; fi
if [ "` + host + `" = "claude" ] && [ "$COCONET_TEST_FAIL_CLAUDE" = "failed-claude" ]; then printf 'fixture installation failure\n' >&2; exit 1; fi
case "$1 $2" in
  'plugin marketplace')
    [ "$3" = "add" ]
    [ -f "$4/` + manifest + `" ]
    [ -f "$4/plugins/coconet/hooks/hooks.json" ]
    [ -x "$4/plugins/coconet/scripts/hook.sh" ]
    printf '%s\n' "$4" > "$config/source"
    printf '{}\n' ;;
  'plugin add'|'plugin install')
    [ -f "$config/source" ]
    : > "$config/installed"
    printf '{}\n' ;;
  'plugin update') [ -f "$config/installed" ]; printf '{}\n' ;;
  'plugin list') [ -f "$config/installed" ]; printf '%s\n' '` + list + `' ;;
  *) exit 91 ;;
esac
`
}
