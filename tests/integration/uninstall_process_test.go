package integration

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xfey/coconet/packages/distribution"
)

func TestRuntimeCanRemoveItsVerifiedInstallWhileRunning(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Coconet MVP supports macOS and Linux")
	}
	base := t.TempDir()
	installRoot := filepath.Join(base, "data", "coconet")
	releaseName := "0.9.0-test-" + runtime.GOOS + "-" + runtime.GOARCH
	releaseRoot := filepath.Join(installRoot, "versions", releaseName)
	runtimeBinary := filepath.Join(releaseRoot, "bin", "coconet")
	if err := os.MkdirAll(filepath.Dir(runtimeBinary), 0o700); err != nil {
		t.Fatal(err)
	}
	buildBinary(t, projectRoot(t), runtimeBinary, "./apps/runtime")
	files := map[string]os.FileMode{
		"bin/coconet-bootstrap":                                          0o755,
		"bin/lib/node/node":                                              0o755,
		"bin/lib/claude/helper.mjs":                                      0o644,
		"bin/lib/claude/sdk.mjs":                                         0o644,
		"licenses/NODE_LICENSE":                                          0o644,
		"licenses/CLAUDE_AGENT_SDK_LICENSE.md":                           0o644,
		"marketplaces/codex/.agents/plugins/marketplace.json":            0o644,
		"marketplaces/codex/plugins/coconet/.codex-plugin/plugin.json":   0o644,
		"marketplaces/claude/.claude-plugin/marketplace.json":            0o644,
		"marketplaces/claude/plugins/coconet/.claude-plugin/plugin.json": 0o644,
		"marketplaces/cursor/plugins/coconet/.cursor-plugin/plugin.json": 0o644,
		"marketplaces/cursor/plugins/coconet/mcp.json":                   0o644,
	}
	for relative, mode := range files {
		path := filepath.Join(releaseRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := distribution.CreateManifest(releaseRoot, "0.9.0-test", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	manifestContent, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestContent = append(manifestContent, '\n')
	if err := os.WriteFile(filepath.Join(releaseRoot, distribution.ManifestFilename), manifestContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("versions", releaseName), filepath.Join(installRoot, "current")); err != nil {
		t.Fatal(err)
	}
	binDirectory := filepath.Join(base, "bin")
	if err := os.MkdirAll(binDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(binDirectory, "coconet")
	if err := os.Symlink(filepath.Join(installRoot, "current", "bin", "coconet"), launcher); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(launcher,
		"uninstall", "--json", "--root", installRoot, "--bin-dir", binDirectory,
	)
	command.Env = append(os.Environ(),
		"HOME="+base,
		"XDG_CONFIG_HOME="+filepath.Join(base, "config"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_BIN_HOME="+binDirectory,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("running Runtime could not uninstall itself: %v: %s", err, output)
	}
	var result struct {
		Status  string                       `json:"status"`
		Runtime distribution.UninstallResult `json:"runtime"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode uninstall output: %v: %s", err, output)
	}
	if result.Status != "ok" || !result.Runtime.RuntimeRemoved || !result.Runtime.ExecutableRemoved {
		t.Fatalf("unexpected uninstall result %#v", result)
	}
	for _, path := range []string{installRoot, launcher} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("uninstall target remains %q: %v", path, err)
		}
	}
}
