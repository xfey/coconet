package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type pluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpConfig struct {
	Servers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		EnvVars []string          `json:"env_vars"`
		Env     map[string]string `json:"env"`
	} `json:"mcpServers"`
}

type hookConfig struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command                string   `json:"command"`
			Args                   []string `json:"args"`
			Timeout                int      `json:"timeout"`
			AdditionalContextLimit int      `json:"additionalContextLimit"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func TestPluginIdentityAndMCPStayAligned(t *testing.T) {
	root := repositoryRoot(t)
	codexRoot := filepath.Join(root, "plugins", "codex", "coconet")
	claudeRoot := filepath.Join(root, "plugins", "claude", "coconet")
	cursorRoot := filepath.Join(root, "plugins", "cursor", "coconet")

	codexManifest := decodeJSON[pluginManifest](t, filepath.Join(codexRoot, ".codex-plugin", "plugin.json"))
	claudeManifest := decodeJSON[pluginManifest](t, filepath.Join(claudeRoot, ".claude-plugin", "plugin.json"))
	cursorManifest := decodeJSON[pluginManifest](t, filepath.Join(cursorRoot, ".cursor-plugin", "plugin.json"))
	if codexManifest != claudeManifest || codexManifest != cursorManifest || codexManifest.Name != "coconet" {
		t.Fatalf("plugin identities diverged: codex=%#v claude=%#v", codexManifest, claudeManifest)
	}
	setupManifest := decodeJSON[pluginManifest](t, filepath.Join(root, "installers", "npm", "package.json"))
	if setupManifest.Name != "coconet" || setupManifest.Version != codexManifest.Version {
		t.Fatalf("npm setup and Plugin versions diverged: setup=%#v plugin=%#v", setupManifest, codexManifest)
	}

	for pluginRoot, agent := range map[string]string{codexRoot: "codex", claudeRoot: "claude", cursorRoot: "cursor"} {
		configName := ".mcp.json"
		if agent == "cursor" {
			configName = "mcp.json"
		}
		config := decodeJSON[mcpConfig](t, filepath.Join(pluginRoot, configName))
		server, ok := config.Servers["coconet"]
		if !ok || server.Command != "/bin/sh" || len(server.Args) != 2 || server.Args[0] != "-c" || !strings.Contains(server.Args[1], "current/bin/coconet") || !strings.Contains(server.Args[1], "mcp --agent "+agent) {
			t.Fatalf("invalid Coconet MCP entry in %s", pluginRoot)
		}
		if agent == "codex" {
			required := []string{
				"HOME", "PATH", "XDG_DATA_HOME", "COCONET_EXECUTABLE", "COCONET_HOME", "COCONET_SERVER_URL",
				"COCONET_CODEX_COMMAND", "CODEX_HOME", "COCONET_CLAUDE_COMMAND", "CLAUDE_CONFIG_DIR",
				"COCONET_CLAUDE_NODE", "COCONET_CLAUDE_HELPER", "COCONET_CLAUDE_SDK_ENTRY",
			}
			for _, variable := range required {
				if !containsString(server.EnvVars, variable) {
					t.Fatalf("Codex MCP entry does not forward %s", variable)
				}
			}
		}
		if agent == "cursor" {
			// Cursor's stdio transport does not inherit custom profile/runtime
			// variables unless the plugin explicitly forwards them.
			for _, key := range []string{"COCONET_EXECUTABLE", "COCONET_HOME", "COCONET_LOCAL_DAG_HOME", "COCONET_SERVER_URL", "COCONET_CURSOR_COMMAND", "CURSOR_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "COCONET_CA_FILE", "CURSOR_API_KEY", "CURSOR_API_ENDPOINT"} {
				if server.Env[key] != "${env:"+key+"}" {
					t.Fatalf("Cursor MCP does not forward %s", key)
				}
			}
		}
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestCursorMCPUnsetsUnresolvedOptionalEnvironment(t *testing.T) {
	config := decodeJSON[mcpConfig](t, filepath.Join(repositoryRoot(t), "plugins", "cursor", "coconet", "mcp.json"))
	server := config.Servers["coconet"]
	for _, placeholders := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolved", true: "missing"}[placeholders], func(t *testing.T) {
			root := t.TempDir()
			runtimePath := filepath.Join(root, "coconet")
			// Values containing shell syntax must remain data through the launcher.
			value := "literal $(exit 91) `exit 92` ; space"
			body := "#!/bin/sh\n[ \"$*\" = 'mcp --agent cursor' ] || exit 93\n"
			for key := range server.Env {
				if key == "COCONET_EXECUTABLE" || key == "COCONET_HOME" {
					continue
				}
				if placeholders {
					body += "[ \"${" + key + "+set}\" != set ] || exit 94\n"
				} else {
					body += "[ \"$" + key + "\" = 'configured' ] || exit 95\n"
				}
			}
			body += "printf '%s' \"$COCONET_HOME\"\n"
			if err := os.WriteFile(runtimePath, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(server.Command, server.Args...)
			command.Env = []string{"PATH=" + root, "COCONET_EXECUTABLE=" + runtimePath, "COCONET_HOME=" + value}
			for key, placeholder := range server.Env {
				if key == "COCONET_EXECUTABLE" || key == "COCONET_HOME" {
					continue
				}
				setting := "configured"
				if placeholders {
					setting = placeholder
				}
				command.Env = append(command.Env, key+"="+setting)
			}
			output, err := command.CombinedOutput()
			if err != nil || string(output) != value {
				t.Fatalf("Cursor environment handling: %v %q", err, output)
			}
		})
	}
}

func TestPluginHooksRemainFailOpenRuntimeEntrypoints(t *testing.T) {
	root := repositoryRoot(t)
	expectedTimeout := map[string]int{
		"Stop": 3, "StopFailure": 3, "UserPromptSubmit": 3, "SessionEnd": 1, "SessionStart": 3,
	}
	cases := []struct {
		path          string
		agent         string
		requiredEvent []string
	}{
		{
			path:          filepath.Join(root, "plugins", "codex", "coconet", "hooks", "hooks.json"),
			agent:         "codex",
			requiredEvent: []string{"Stop", "SessionEnd", "SessionStart"},
		},
		{
			path:          filepath.Join(root, "plugins", "claude", "coconet", "hooks", "hooks.json"),
			agent:         "claude",
			requiredEvent: []string{"Stop", "StopFailure", "SessionEnd", "SessionStart", "UserPromptSubmit"},
		},
	}
	for _, current := range cases {
		config := decodeJSON[hookConfig](t, current.path)
		if len(config.Hooks) != len(current.requiredEvent) {
			t.Fatalf("%s has unexpected Hook events: %#v", current.path, config.Hooks)
		}
		for _, event := range current.requiredEvent {
			groups := config.Hooks[event]
			if len(groups) == 0 {
				t.Fatalf("%s is missing %s", current.path, event)
			}
			for _, group := range groups {
				if group.Matcher != "" {
					t.Fatalf("%s has unexpected matcher for %s: %q", current.path, event, group.Matcher)
				}
				for _, hook := range group.Hooks {
					if !strings.Contains(hook.Command, "scripts/hook.sh") || len(hook.Args) != 0 {
						t.Fatalf("unexpected hook command %q", hook.Command)
					}
					if strings.Contains(hook.Command, " --context") != (event == "SessionStart") {
						t.Fatalf("only SessionStart may forward context: %s", event)
					}
					if hook.Timeout != expectedTimeout[event] {
						t.Fatalf("%s must preserve the %d-second %s budget: %d", current.path, expectedTimeout[event], event, hook.Timeout)
					}
					if hook.AdditionalContextLimit != 0 {
						t.Fatalf("%s retained additionalContext for %s", current.path, event)
					}
				}
			}
		}
		for _, removed := range []string{"UserPromptSubmit", "PreToolUse"} {
			if len(config.Hooks[removed]) != 0 && !(current.agent == "claude" && removed == "UserPromptSubmit") {
				t.Fatalf("%s retained automatic recall Hook %s", current.path, removed)
			}
		}
		wrapperPath := filepath.Join(filepath.Dir(filepath.Dir(current.path)), "scripts", "hook.sh")
		wrapper, err := os.ReadFile(wrapperPath)
		if err != nil {
			t.Fatal(err)
		}
		content := string(wrapper)
		if !strings.HasPrefix(content, "#!/bin/sh\n") || !strings.Contains(content, "current/bin/coconet") || !strings.Contains(content, "command -v coconet") || !strings.Contains(content, `"${runtime}" hook --agent `+current.agent+` >/dev/null 2>&1 || :`) || !strings.HasSuffix(content, "exit 0\n") {
			t.Fatalf("%s does not preserve fail-open forwarding", wrapperPath)
		}
	}
}

func TestPluginSkillsHaveTheSameHostIndependentInstructions(t *testing.T) {
	root := repositoryRoot(t)
	canonicalPath := filepath.Join(root, "prompts", "coconet-team-context.SKILL.md")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "plugins", "codex", "coconet", "skills", "coconet-team-context", "SKILL.md"),
		filepath.Join(root, "plugins", "claude", "coconet", "skills", "coconet-team-context", "SKILL.md"),
	} {
		generated, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(generated) != string(canonical) {
			t.Fatalf("generated Plugin Skill %s diverged from %s; run go generate ./prompts", path, canonicalPath)
		}
	}

}

func TestHookWrappersExitZeroWhenRuntimeIsMissing(t *testing.T) {
	root := repositoryRoot(t)
	for _, wrapper := range []string{
		filepath.Join(root, "plugins", "codex", "coconet", "scripts", "hook.sh"),
		filepath.Join(root, "plugins", "claude", "coconet", "scripts", "hook.sh"),
	} {
		command := exec.Command(wrapper)
		command.Env = []string{"HOME=" + t.TempDir(), "PATH=" + t.TempDir()}
		command.Stdin = strings.NewReader(`{"hook_event_name":"Stop"}`)
		if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
			t.Fatalf("%s did not fail open without Runtime: %v %q", wrapper, err, output)
		}
	}
}

func TestHookWrappersForwardInputButHideRuntimeFailure(t *testing.T) {
	root := repositoryRoot(t)
	for pluginRoot, agent := range map[string]string{
		filepath.Join(root, "plugins", "codex", "coconet"):  "codex",
		filepath.Join(root, "plugins", "claude", "coconet"): "claude",
		filepath.Join(root, "plugins", "cursor", "coconet"): "cursor",
	} {
		dataRoot := t.TempDir()
		runtimePath := filepath.Join(dataRoot, "coconet", "current", "bin", "coconet")
		if err := os.MkdirAll(filepath.Dir(runtimePath), 0o700); err != nil {
			t.Fatal(err)
		}
		runtime := "#!/bin/sh\nprintf '%s\\n' \"$*\" >\"$COCONET_TEST_TRACE\"\n/bin/cat >>\"$COCONET_TEST_TRACE\"\nprintf 'fixture failure\\n' >&2\nexit 127\n"
		if err := os.WriteFile(runtimePath, []byte(runtime), 0o755); err != nil {
			t.Fatal(err)
		}
		trace := filepath.Join(t.TempDir(), "trace")
		wrapper := filepath.Join(pluginRoot, "scripts", "hook.sh")
		command := exec.Command(wrapper)
		command.Env = []string{
			"HOME=" + t.TempDir(), "PATH=" + t.TempDir(), "XDG_DATA_HOME=" + dataRoot,
			"COCONET_TEST_TRACE=" + trace,
		}
		input := `{"hook_event_name":"Stop","cwd":"/unbound"}`
		command.Stdin = strings.NewReader(input)
		if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
			t.Fatalf("%s exposed a Runtime failure: %v %q", wrapper, err, output)
		}
		forwarded, err := os.ReadFile(trace)
		if err != nil || string(forwarded) != "hook --agent "+agent+"\n"+input {
			t.Fatalf("%s did not forward the Hook input: %v %q", wrapper, err, forwarded)
		}
	}
}

func TestHookWrappersForwardContextOnlyForStartupEntry(t *testing.T) {
	root := repositoryRoot(t)
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			payload := `{"systemMessage":"fixed source","hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"source metadata"}}`
			runtimePath := filepath.Join(t.TempDir(), "runtime")
			if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nprintf '%s' '"+payload+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(root, "plugins", agent, "coconet", "scripts", "hook.sh")
			for _, contextOutput := range []bool{false, true} {
				var args []string
				if contextOutput {
					args = []string{"--context"}
				}
				cmd := exec.Command(wrapper, args...)
				cmd.Env = []string{"COCONET_EXECUTABLE=" + runtimePath, "HOME=" + t.TempDir(), "PATH=" + t.TempDir()}
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatal(err)
				}
				if contextOutput && string(output) != payload || !contextOutput && len(output) != 0 {
					t.Fatalf("wrong context forwarding: %t %s", contextOutput, output)
				}
			}
		})
	}
}

func TestCodexHookComputesInstalledSkillPathWithRepeatedDirectories(t *testing.T) {
	pluginRoot := filepath.Join(t.TempDir(), "coconet", "coconet", "version with spaces")
	scripts := filepath.Join(pluginRoot, "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "plugins", "codex", "coconet", "scripts", "hook.sh"))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(scripts, "hook.sh")
	if err := os.WriteFile(wrapper, content, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n/bin/cat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(wrapper, "--context")
	cmd.Env = []string{"COCONET_EXECUTABLE=" + runtimePath, "HOME=" + t.TempDir(), "PATH=" + t.TempDir()}
	input := `{"hook_event_name":"SessionStart"}`
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	canonical, pathErr := filepath.EvalSymlinks(pluginRoot)
	expected := "hook\n--agent\ncodex\n--skill-path\n" + filepath.Join(canonical, "skills", "coconet-team-context", "SKILL.md") + "\n" + input
	if err != nil || pathErr != nil || string(output) != expected {
		t.Fatalf("wrapper lost installed directory or argument boundaries: %v %v %q", err, pathErr, output)
	}
}

func TestPluginMCPWrappersResolveUserRuntimeWithoutPATH(t *testing.T) {
	root := repositoryRoot(t)
	dataRoot := t.TempDir()
	runtimePath := filepath.Join(dataRoot, "coconet", "current", "bin", "coconet")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for pluginRoot, agent := range map[string]string{
		filepath.Join(root, "plugins", "codex", "coconet"):  "codex",
		filepath.Join(root, "plugins", "claude", "coconet"): "claude",
		filepath.Join(root, "plugins", "cursor", "coconet"): "cursor",
	} {
		configName := ".mcp.json"
		if agent == "cursor" {
			configName = "mcp.json"
		}
		config := decodeJSON[mcpConfig](t, filepath.Join(pluginRoot, configName))
		server := config.Servers["coconet"]
		command := exec.Command(server.Command, server.Args...)
		command.Env = []string{"HOME=" + t.TempDir(), "XDG_DATA_HOME=" + dataRoot, "PATH=" + t.TempDir()}
		output, err := command.CombinedOutput()
		if err != nil || string(output) != "mcp --agent "+agent+"\n" {
			t.Fatalf("%s did not resolve the user Runtime: %v %q", pluginRoot, err, output)
		}
	}
}

func TestPluginMCPWrappersPreferExplicitRuntime(t *testing.T) {
	root := repositoryRoot(t)
	runtimePath := filepath.Join(t.TempDir(), "custom-runtime", "coconet")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for pluginRoot, agent := range map[string]string{
		filepath.Join(root, "plugins", "codex", "coconet"):  "codex",
		filepath.Join(root, "plugins", "claude", "coconet"): "claude",
		filepath.Join(root, "plugins", "cursor", "coconet"): "cursor",
	} {
		configName := ".mcp.json"
		if agent == "cursor" {
			configName = "mcp.json"
		}
		config := decodeJSON[mcpConfig](t, filepath.Join(pluginRoot, configName))
		server := config.Servers["coconet"]
		command := exec.Command(server.Command, server.Args...)
		command.Env = []string{
			"HOME=" + t.TempDir(),
			"PATH=" + t.TempDir(),
			"XDG_DATA_HOME=" + t.TempDir(),
			"COCONET_EXECUTABLE=" + runtimePath,
		}
		output, err := command.CombinedOutput()
		if err != nil || string(output) != "mcp --agent "+agent+"\n" {
			t.Fatalf("%s did not prefer the explicit Runtime: %v %q", pluginRoot, err, output)
		}
	}
}

func decodeJSON[T any](t *testing.T, path string) T {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
