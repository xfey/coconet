package agentplugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func cursorFixture(t *testing.T) (Manager, string) {
	t.Helper()
	root := t.TempDir()
	market := t.TempDir()
	plugin := filepath.Join(market, "plugins", "coconet")
	os.MkdirAll(filepath.Join(plugin, ".cursor-plugin"), 0700)
	os.MkdirAll(filepath.Join(plugin, "scripts"), 0700)
	os.WriteFile(filepath.Join(plugin, ".cursor-plugin/plugin.json"), []byte(`{"name":"coconet"}`), 0600)
	os.WriteFile(filepath.Join(plugin, "scripts/hook.sh"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	return Manager{CursorRoot: root}, market
}
func TestCursorInstallUpdateRemovePreservesOtherHooksAndSymlink(t *testing.T) {
	m, market := cursorFixture(t)
	shared := filepath.Join(t.TempDir(), "hooks.json")
	os.WriteFile(shared, []byte(`{"version":1,"extra":"preserve","hooks":{"stop":[{"command":"other-hook","custom":true}]}}`), 0640)
	os.Symlink(shared, filepath.Join(m.CursorRoot, "hooks.json"))
	for i := 0; i < 2; i++ {
		r := m.installCursor(InstallResult{Host: HostCursor}, market)
		if r.Status != StatusConfigured {
			t.Fatal(r)
		}
	}
	b, _ := os.ReadFile(shared)
	var obj struct {
		Extra string                       `json:"extra"`
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	json.Unmarshal(b, &obj)
	if len(obj.Hooks["stop"]) != 2 || len(obj.Hooks["sessionStart"]) != 1 || obj.Extra != "preserve" {
		t.Fatal(string(b))
	}
	r := m.uninstallCursor(UninstallResult{Host: HostCursor})
	if r.Status != StatusRemoved || r.ConfigSectionsRemoved != 4 {
		t.Fatal(r)
	}
	b, _ = os.ReadFile(shared)
	json.Unmarshal(b, &obj)
	if len(obj.Hooks["stop"]) != 1 || obj.Extra != "preserve" {
		t.Fatal(string(b))
	}
	link, err := os.Readlink(filepath.Join(m.CursorRoot, "hooks.json"))
	if err != nil || link != shared {
		t.Fatal("replaced symlink", err)
	}
	info, _ := os.Stat(shared)
	if info.Mode().Perm() != 0640 {
		t.Fatal("changed permissions")
	}
	r = m.uninstallCursor(UninstallResult{Host: HostCursor})
	if r.Status != StatusRemoved || r.CacheRemoved || r.ConfigSectionsRemoved != 0 {
		t.Fatal(r)
	}
}
func TestCursorInvalidHooksRollBackPluginInstall(t *testing.T) {
	for _, content := range []string{`invalid`, `null`, `{"version":2}`, `{"hooks":null}`, `{"hooks":{"stop":{}}}`} {
		t.Run(content, func(t *testing.T) {
			m, market := cursorFixture(t)
			os.WriteFile(filepath.Join(m.CursorRoot, "hooks.json"), []byte(content), 0600)
			r := m.installCursor(InstallResult{Host: HostCursor}, market)
			if r.Status != StatusFailed {
				t.Fatal(r)
			}
			if _, err := os.Stat(filepath.Join(m.CursorRoot, "plugins/local/coconet")); !os.IsNotExist(err) {
				t.Fatal("failed installation left plugin")
			}
		})
	}
}
func TestCursorDoesNotOverwriteUnmanagedPlugin(t *testing.T) {
	m, market := cursorFixture(t)
	path := filepath.Join(m.CursorRoot, "plugins/local/coconet")
	os.MkdirAll(path, 0700)
	os.WriteFile(filepath.Join(path, "user-file"), []byte("keep"), 0600)
	if r := m.installCursor(InstallResult{Host: HostCursor}, market); r.Status != StatusFailed {
		t.Fatal(r)
	}
	if r := m.uninstallCursor(UninstallResult{Host: HostCursor}); r.Status != StatusFailed {
		t.Fatal(r)
	}
	if _, err := os.Stat(filepath.Join(path, "user-file")); err != nil {
		t.Fatal(err)
	}
}
