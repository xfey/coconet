package contract_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the registered command, not just hook.sh: a running Agent can retain
// this command after uninstall removes the entire Plugin directory.
func TestCachedHookCommandsRemainSafeAfterUninstall(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		source := filepath.Join(repositoryRoot(t), "plugins", agent, "coconet")
		config := decodeJSON[hookConfig](t, filepath.Join(source, "hooks", "hooks.json"))
		wrapper, err := os.ReadFile(filepath.Join(source, "scripts", "hook.sh"))
		if err != nil {
			t.Fatal(err)
		}
		for event, groups := range config.Hooks {
			for _, group := range groups {
				for _, hook := range group.Hooks {
					t.Run(agent+"/"+event, func(t *testing.T) {
						testCachedHookCommand(t, agent, event, hook.Command, wrapper)
					})
				}
			}
		}
	}
}

func testCachedHookCommand(t *testing.T, agent, event, command string, wrapper []byte) {
	t.Helper()
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugin with spaces")
	script := filepath.Join(pluginRoot, "scripts", "hook.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(root, "runtime")
	trace := filepath.Join(root, "trace")
	context := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"fixture context"}}`
	fixture := "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$COCONET_TEST_TRACE\"\n/bin/cat >>\"$COCONET_TEST_TRACE\"\nprintf '%s' '" + context + "'\nprintf 'fixture diagnostic\\n' >&2\nexit \"${COCONET_TEST_EXIT:-0}\"\n"
	if err := os.WriteFile(runtimePath, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"hook_event_name":%q,"cwd":"/fixture"}`, event)
	run := func(want string, extraEnv ...string) {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Env = append([]string{
			"HOME=" + root, "PATH=" + filepath.Join(root, "empty-path"),
			"PLUGIN_ROOT=" + pluginRoot, "CLAUDE_PLUGIN_ROOT=" + pluginRoot,
			"COCONET_EXECUTABLE=" + runtimePath, "COCONET_TEST_TRACE=" + trace,
		}, extraEnv...)
		cmd.Stdin = strings.NewReader(input)
		if output, err := cmd.CombinedOutput(); err != nil || string(output) != want {
			t.Fatalf("registered command returned %v, output %q; want %q", err, output, want)
		}
	}
	want := ""
	if event == "SessionStart" {
		want = context
	}
	for _, exit := range []string{"0", "127"} {
		run(want, "COCONET_TEST_EXIT="+exit)
		forwarded, err := os.ReadFile(trace)
		args := "hook\n--agent\n" + agent + "\n"
		if agent == "codex" && event == "SessionStart" {
			canonical, err := filepath.EvalSymlinks(pluginRoot)
			if err != nil {
				t.Fatal(err)
			}
			args += "--skill-path\n" + filepath.Join(canonical, "skills", "coconet-team-context", "SKILL.md") + "\n"
		}
		if err != nil || string(forwarded) != args+input {
			t.Fatalf("command lost arguments or stdin: %v %q", err, forwarded)
		}
	}
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	run("")
	// An executable file can still fail to start. This also checks
	// the fallback for removal between the check and execution.
	if err := os.WriteFile(script, []byte("#!/nonexistent-coconet-interpreter\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	run("")
	if err := os.Chmod(script, 0o600); err != nil {
		t.Fatal(err)
	}
	run("")
	if err := os.RemoveAll(pluginRoot); err != nil {
		t.Fatal(err)
	}
	run("")
}
