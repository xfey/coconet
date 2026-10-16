package agentplugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallCodexRemovesPluginMarketplaceTrustAndCache(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	original := strings.Join([]string{
		`[projects."/work"]`,
		`trust_level = "trusted"`,
		``,
		`[hooks.state."coconet@coconet:hooks/hooks.json:stop:0:0"]`,
		`trusted_hash = "sha256:fixture"`,
		``,
		`[plugins."coconet@coconet"]`,
		`enabled = true`,
		``,
		`[marketplaces.coconet]`,
		`source_type = "local"`,
		`source = "/install/current/marketplaces/codex"`,
		``,
		`[plugins."other@other"]`,
		`enabled = true`,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "plugins", "cache", "coconet", "coconet", "0.4.0")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{runs: []fakeRun{
		{command: "/bin/codex-fixture", args: []string{"plugin", "remove", "coconet@coconet", "--json"}},
		{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "remove", "coconet", "--json"}},
		{command: "/bin/codex-fixture", args: []string{"plugin", "list", "--available", "--json"}, stdout: `{"installed":[]}`},
	}}
	result := (Manager{Runner: runner}).Uninstall(context.Background(), Detection{
		Host: HostCodex, Command: "codex-fixture", Path: "/bin/codex-fixture", Detected: true,
	}, root)
	if result.Status != StatusRemoved || result.ConfigSectionsRemoved != 3 || !result.CacheRemoved || result.ConfigBackup == "" {
		t.Fatalf("unexpected uninstall result %#v", result)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), "coconet") || !strings.Contains(string(updated), `[plugins."other@other"]`) || !strings.Contains(string(updated), `[projects."/work"]`) {
		t.Fatalf("unexpected cleaned config %q", updated)
	}
	backup, err := os.ReadFile(result.ConfigBackup)
	if err != nil || string(backup) != original {
		t.Fatalf("unexpected backup %q %v", backup, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "plugins", "cache", "coconet")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Coconet cache remains: %v", err)
	}
}

func TestUninstallClaudeAcceptsAlreadyAbsentAndRemovesExactCache(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "plugins", "cache", "coconet")
	other := filepath.Join(root, "plugins", "cache", "other")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	missing := errors.New("missing")
	runner := &fakeRunner{runs: []fakeRun{
		{command: "/bin/tclaude", args: []string{"plugin", "uninstall", "coconet@coconet", "--scope", "user"}, stderr: "Plugin not found", err: missing},
		{command: "/bin/tclaude", args: []string{"plugin", "marketplace", "remove", "coconet", "--scope", "user"}, stderr: "Marketplace not installed", err: missing},
		{command: "/bin/tclaude", args: []string{"plugin", "list", "--json"}, stdout: `[]`},
	}}
	result := (Manager{Runner: runner}).Uninstall(context.Background(), Detection{
		Host: HostClaude, Command: "tclaude", Path: "/bin/tclaude", Detected: true,
	}, root)
	if result.Status != StatusRemoved || !result.CacheRemoved || result.ConfigBackup != "" {
		t.Fatalf("unexpected uninstall result %#v", result)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated cache was removed: %v", err)
	}
}

func TestUninstallFailureKeepsOwnedStateAndReportsBoundedError(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("[plugins.\"coconet@coconet\"]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{runs: []fakeRun{{
		command: "/bin/codex-fixture", args: []string{"plugin", "remove", "coconet@coconet", "--json"},
		stderr: strings.Repeat("failure ", 100), err: errors.New("failed"),
	}}}
	result := (Manager{Runner: runner}).Uninstall(context.Background(), Detection{
		Host: HostCodex, Command: "codex-fixture", Path: "/bin/codex-fixture", Detected: true,
	}, root)
	if result.Status != StatusFailed || len(result.Error) > 550 || !strings.Contains(result.Error, "remove Codex Plugin") {
		t.Fatalf("unexpected failure %#v", result)
	}
	content, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(content), "coconet@coconet") {
		t.Fatalf("failure changed config %q %v", content, err)
	}
}

func TestUninstallVerificationRejectsRemainingPlugin(t *testing.T) {
	runner := &fakeRunner{runs: []fakeRun{
		{command: "/bin/codex-fixture", args: []string{"plugin", "remove", "coconet@coconet", "--json"}},
		{command: "/bin/codex-fixture", args: []string{"plugin", "marketplace", "remove", "coconet", "--json"}},
		{command: "/bin/codex-fixture", args: []string{"plugin", "list", "--available", "--json"}, stdout: `{"installed":[{"pluginId":"coconet@coconet","installed":true,"enabled":true}]}`},
	}}
	result := (Manager{Runner: runner}).Uninstall(context.Background(), Detection{
		Host: HostCodex, Command: "codex-fixture", Path: "/bin/codex-fixture", Detected: true,
	}, t.TempDir())
	if result.Status != StatusFailed || !strings.Contains(result.Error, "still reports") {
		t.Fatalf("unexpected verification result %#v", result)
	}
}

func TestMissingAgentWithOwnedStateFailsSafe(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugins", "cache", "coconet"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := (Manager{Runner: &fakeRunner{}}).Uninstall(context.Background(), Detection{
		Host: HostCodex, Command: "missing", Detected: false,
	}, root)
	if result.Status != StatusFailed || !strings.Contains(result.Error, "integration state remains") {
		t.Fatalf("unexpected missing Agent result %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "cache", "coconet")); err != nil {
		t.Fatalf("missing Agent failure removed state: %v", err)
	}
}

func TestCodexCleanupPreservesMackupLinks(t *testing.T) {
	for _, kind := range []string{"absolute", "relative", "chain", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			shared := filepath.Join(root, "Mackup")
			if err := os.Mkdir(shared, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(shared, "config.toml")
			original := []byte("# personal configuration\nmodel = \"gpt-5.6-sol\"\n[plugins.\"coconet@coconet\"]\nenabled = true\n[plugins.\"other@other\"]\nenabled = true\n")
			if err := os.WriteFile(target, original, 0640); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "config.toml")
			linkTarget := target
			switch kind {
			case "relative":
				linkTarget = "Mackup/config.toml"
			case "chain":
				linkTarget = filepath.Join(root, "middle.toml")
				if err := os.Symlink("Mackup/config.toml", linkTarget); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Symlink("Mackup", filepath.Join(root, "profile")); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(root, "profile", "config.toml")
			}
			if kind != "directory" {
				if err := os.Symlink(linkTarget, path); err != nil {
					t.Fatal(err)
				}
			}
			backup, count, err := cleanupCodexConfig(path)
			if err != nil || count != 1 {
				t.Fatalf("cleanup: count=%d err=%v", count, err)
			}
			link := path
			if kind == "directory" {
				link = filepath.Dir(path)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("link replaced: %v", err)
			}
			updated, err := os.ReadFile(target)
			if err != nil || strings.Contains(string(updated), "coconet") || !strings.Contains(string(updated), "other@other") || !strings.Contains(string(updated), "# personal configuration") {
				t.Fatalf("unrelated config changed: %s, %v", updated, err)
			}
			if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0640 {
				t.Fatalf("target permissions changed: %v", err)
			}
			if saved, err := os.ReadFile(backup); err != nil || string(saved) != string(original) {
				t.Fatalf("backup differs: %v", err)
			}
			if _, count, err := cleanupCodexConfig(path); err != nil || count != 0 {
				t.Fatalf("repeat cleanup: %d %v", count, err)
			}
		})
	}
}

func TestCodexInstallRejectsBrokenConfigWithoutTouchingIt(t *testing.T) {
	for _, kind := range []string{"dangling", "circular", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.toml")
			target := "missing.toml"
			if kind == "circular" {
				target = "config.toml"
			}
			if kind == "directory" {
				target = root
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{}
			manager := (Manager{Runner: runner}).WithConfigRoot(HostCodex, root)
			result := manager.Install(context.Background(), Detection{Host: HostCodex, Command: "codex", Path: "/bin/codex-fixture", Detected: true}, "/fixture/marketplace")
			if result.Status != StatusFailed || !strings.Contains(result.Error, "Codex config") || runner.call != 0 {
				t.Fatalf("unexpected result %#v", result)
			}
			if actual, err := os.Readlink(path); err != nil || actual != target {
				t.Fatalf("broken link changed: %v", err)
			}
		})
	}
}
