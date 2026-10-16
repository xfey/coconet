package distribution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateLoadAndVerifyManifest(t *testing.T) {
	bundle := fixtureBundle(t, "0.7.0", "runtime-v1")
	manifest, err := LoadManifest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "0.7.0" || manifest.OS != "testos" || manifest.Architecture != "testarch" {
		t.Fatalf("unexpected Manifest %#v", manifest)
	}
	if err := VerifyBundle(bundle, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "bin", "coconet"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundle, manifest); err == nil {
		t.Fatal("tampered bundle passed verification")
	}
}

func TestManifestRejectsUnsafeOrIncompleteFiles(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, Product: "coconet", Version: "0.7.0", OS: "linux", Architecture: "amd64",
		Files: []ManifestFile{{Path: "../escape", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Mode: 0o644}},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("unsafe path passed validation")
	}
	manifest.Files[0].Path = "bin/coconet"
	if err := manifest.Validate(); err == nil {
		t.Fatal("incomplete bundle passed validation")
	}
}

func fixtureBundle(t *testing.T, version, runtimeContent string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"bin/coconet":                                                    runtimeContent,
		"bin/coconet-bootstrap":                                          "bootstrap",
		"bin/lib/node/node":                                              "node",
		"bin/lib/claude/helper.mjs":                                      "helper",
		"bin/lib/claude/sdk.mjs":                                         "sdk",
		"licenses/NODE_LICENSE":                                          "node-license",
		"licenses/CLAUDE_AGENT_SDK_LICENSE.md":                           "sdk-license",
		"marketplaces/codex/.agents/plugins/marketplace.json":            "{}",
		"marketplaces/codex/plugins/coconet/.codex-plugin/plugin.json":   "{}",
		"marketplaces/claude/.claude-plugin/marketplace.json":            "{}",
		"marketplaces/claude/plugins/coconet/.claude-plugin/plugin.json": "{}",
		"marketplaces/cursor/plugins/coconet/.cursor-plugin/plugin.json": "{}",
		"marketplaces/cursor/plugins/coconet/mcp.json":                   "{}",
	}
	for relative, content := range files {
		mode := os.FileMode(0o644)
		if relative == "bin/coconet" || relative == "bin/coconet-bootstrap" || relative == "bin/lib/node/node" {
			mode = 0o755
		}
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := CreateManifest(root, version, "testos", "testarch")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	return root
}
