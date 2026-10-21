package agentplugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdatePreservesDisabledAndRemovedNativePlugins(t *testing.T) {
	for _, trial := range []struct {
		host    Host
		content string
	}{
		{HostCodex, `{"installed":[{"pluginId":"coconet@coconet","installed":true,"enabled":false}]}`},
		{HostCodex, `{"installed":[]}`},
		{HostClaude, `[{"id":"coconet@coconet","enabled":false}]`},
		{HostClaude, `[]`},
	} {
		runner := &fakeRunner{runs: []fakeRun{{command: "/fixture", args: verificationArguments(trial.host), stdout: trial.content}}}
		result := (Manager{Runner: runner}).Update(context.Background(), Detection{Host: trial.host, Path: "/fixture", Command: "fixture", Detected: true}, "/unused")
		if result.Status != StatusDisabled || runner.call != 1 {
			t.Fatal(result, runner.call)
		}
	}
}

func TestPluginFingerprintAndStableMarketplace(t *testing.T) {
	root := t.TempDir()
	makeSource := func(name, body string) string {
		dir := filepath.Join(root, name)
		for path, content := range map[string]string{".claude-plugin/marketplace.json": `{"name":"coconet","plugins":[{"name":"coconet","source":"./plugins/coconet"}]}`, "plugins/coconet/skill.md": body} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	first, second := makeSource("runtime-1", "one"), makeSource("runtime-2", "one")
	one, err := Fingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Fingerprint(second)
	if err != nil || two != one {
		t.Fatal("runtime path changed content identity", two, err)
	}
	stable := filepath.Join(root, "stable", "claude")
	for _, source := range []string{first, second} {
		path, err := StableMarketplace(source, stable, HostClaude)
		if err != nil || path != stable {
			t.Fatal(path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(second, "plugins/coconet/skill.md"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	two, err = Fingerprint(second)
	if err != nil || two == one {
		t.Fatal("content change ignored", err)
	}
	content, err := os.ReadFile(filepath.Join(stable, "plugins/coconet/skill.md"))
	if err != nil || string(content) != "two" {
		t.Fatal(string(content), err)
	}
}
