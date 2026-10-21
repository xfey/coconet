package distribution

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssembleAndArchiveBundleDeterministically(t *testing.T) {
	inputs := bundleInputsFixture(t, filepath.Join(t.TempDir(), "coconet-0.3.0-testos-testarch"), "0.3.0", "0.3.0")
	manifest, err := AssembleBundle(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "0.3.0" || len(manifest.Files) < 11 {
		t.Fatalf("unexpected bundle Manifest %#v", manifest)
	}
	loaded, err := LoadManifest(inputs.OutputRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(inputs.OutputRoot, loaded); err != nil {
		t.Fatal(err)
	}

	archiveOne := filepath.Join(t.TempDir(), "coconet.tar.gz")
	hashOne, err := ArchiveBundle(inputs.OutputRoot, archiveOne)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchiveChecksum(archiveOne, strings.ToUpper(hashOne)); err != nil {
		t.Fatal(err)
	}
	checksum, err := os.ReadFile(archiveOne + ".sha256")
	if err != nil || string(checksum) != hashOne+"  coconet.tar.gz\n" {
		t.Fatalf("unexpected checksum %q: %v", checksum, err)
	}
	assertArchiveContains(t, archiveOne, filepath.Base(inputs.OutputRoot)+"/manifest.json")
	assertArchiveContains(t, archiveOne, filepath.Base(inputs.OutputRoot)+"/LICENSE")
	assertArchiveContains(t, archiveOne, filepath.Base(inputs.OutputRoot)+"/licenses/go/modules/example/NOTICE")

	secondInputs := bundleInputsFixture(t, filepath.Join(t.TempDir(), filepath.Base(inputs.OutputRoot)), "0.3.0", "0.3.0")
	if _, err := AssembleBundle(secondInputs); err != nil {
		t.Fatal(err)
	}
	archiveTwo := filepath.Join(t.TempDir(), "coconet.tar.gz")
	hashTwo, err := ArchiveBundle(secondInputs.OutputRoot, archiveTwo)
	if err != nil {
		t.Fatal(err)
	}
	if hashTwo != hashOne {
		t.Fatalf("identical bundle inputs produced different archives: %s != %s", hashOne, hashTwo)
	}
}

func TestBundleName(t *testing.T) {
	name, err := BundleName("0.3.0", "linux", "amd64")
	if err != nil || name != "coconet-0.3.0-linux-amd64" {
		t.Fatalf("unexpected bundle name %q: %v", name, err)
	}
	if _, err := BundleName("../escape", "linux", "amd64"); err == nil {
		t.Fatal("unsafe bundle version was accepted")
	}
}

func TestArchiveDirectoryModesDoNotDependOnStagingPermissions(t *testing.T) {
	inputs := bundleInputsFixture(t, filepath.Join(t.TempDir(), "coconet-0.3.0-testos-testarch"), "0.3.0", "0.3.0")
	if _, err := AssembleBundle(inputs); err != nil {
		t.Fatal(err)
	}
	var expectedHash string
	for _, mode := range []os.FileMode{0o700, 0o755, 0o777} {
		if err := filepath.WalkDir(inputs.OutputRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return err
			}
			return os.Chmod(path, mode)
		}); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(t.TempDir(), "bundle.tar.gz")
		hash, err := ArchiveBundle(inputs.OutputRoot, archive)
		if err != nil {
			t.Fatal(err)
		}
		if expectedHash != "" && hash != expectedHash {
			t.Fatalf("staging directory mode %o changed archive hash", mode)
		}
		expectedHash = hash
		file, err := os.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		reader := tar.NewReader(gz)
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if header.Typeflag != tar.TypeDir {
				continue
			}
			want := int64(0o755)
			if header.Name == filepath.Base(inputs.OutputRoot) {
				want = 0o700
			}
			if header.Mode != want {
				t.Errorf("directory %s: mode %o, want %o", header.Name, header.Mode, want)
			}
			relative, err := filepath.Rel(filepath.Base(inputs.OutputRoot), header.Name)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(inputs.OutputRoot, relative))
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("archiving changed staging directory permissions: %v", err)
			}
		}
		gz.Close()
		file.Close()
	}
}

func TestAssembleAndArchiveIndependentServerBundle(t *testing.T) {
	inputsRoot := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(inputsRoot, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	output := filepath.Join(t.TempDir(), "coconet-server-0.5.0-linux-amd64")
	manifest, err := AssembleServerBundle(ServerBundleInputs{
		Version: "0.5.0", OS: "linux", Architecture: "amd64", OutputRoot: output,
		GoLicenses:          licenseFixture(t),
		OperationsDirectory: filepath.Join("..", "..", "deploy"),
		ServerBinary:        write("coconet-server", "server", 0o755),
		SystemdExample:      write("coconet-server.service", "[Service]\n", 0o644),
		JournalExample:      write("journald@coconet.conf", "[Journal]\nSystemMaxUse=128M\n", 0o644),
		EnvironmentExample:  write("server.env.example", "COCONET_S3_ENDPOINT=\n", 0o644),
		NginxExample:        write("coconet-server.conf", "server {}\n", 0o644),
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Product != "coconet-server" || len(manifest.Files) != 18 {
		t.Fatalf("unexpected Server Manifest %#v", manifest)
	}
	loaded, err := LoadManifest(output)
	if err != nil || VerifyBundle(output, loaded) != nil {
		t.Fatalf("Server bundle verification failed: %#v %v", loaded, err)
	}
	if _, err := Install(InstallOptions{BundleRoot: output, InstallRoot: t.TempDir(), BinDirectory: t.TempDir(), CurrentOS: "linux", CurrentArch: "amd64"}); err == nil {
		t.Fatal("client installer accepted a Server operator bundle")
	}
	archive := output + ".tar.gz"
	if _, err := ArchiveBundle(output, archive); err != nil {
		t.Fatal(err)
	}
	assertArchiveContains(t, archive, filepath.Base(output)+"/bin/coconet-server")
	assertArchiveContains(t, archive, filepath.Base(output)+"/LICENSE")
	assertArchiveContains(t, archive, filepath.Base(output)+"/licenses/go/modules/example/NOTICE")
	assertArchiveContains(t, archive, filepath.Base(output)+"/examples/systemd/journald@coconet.conf")
	for _, name := range []string{"backup.py", "storage-check.py", "STORAGE.md", "backup.example.json", "storage-check.example.json", "systemd/coconet-backup.service", "systemd/coconet-backup.timer", "systemd/coconet-storage-check.service", "systemd/coconet-storage-check.timer"} {
		assertArchiveContains(t, archive, filepath.Base(output)+"/examples/operations/"+name)
	}
	if _, err := ServerBundleName("0.5.0", "darwin", "arm64"); err == nil {
		t.Fatal("unsupported macOS Server bundle was accepted")
	}
}

func TestAssembleBundleRejectsVersionMismatchAndSymlink(t *testing.T) {
	mismatched := bundleInputsFixture(t, filepath.Join(t.TempDir(), "bundle"), "0.3.0", "0.4.0")
	if _, err := AssembleBundle(mismatched); err == nil || !strings.Contains(err.Error(), "versions must match") {
		t.Fatalf("Plugin version mismatch was not rejected: %v", err)
	}
	cursorMismatch := bundleInputsFixture(t, filepath.Join(t.TempDir(), "bundle"), "0.3.0", "0.3.0")
	if err := os.WriteFile(filepath.Join(cursorMismatch.CursorPluginRoot, ".cursor-plugin", "plugin.json"), []byte(`{"name":"coconet","version":"0.4.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := AssembleBundle(cursorMismatch); err == nil || !strings.Contains(err.Error(), "versions must match") {
		t.Fatalf("Cursor Plugin version mismatch was not rejected: %v", err)
	}

	symlinked := bundleInputsFixture(t, filepath.Join(t.TempDir(), "bundle"), "0.3.0", "0.3.0")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(symlinked.CodexPluginRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := AssembleBundle(symlinked); err == nil || !strings.Contains(err.Error(), "unsupported file") {
		t.Fatalf("Plugin symlink was not rejected: %v", err)
	}
}

func TestVerifyBundleRejectsUndeclaredAndSymlinkFiles(t *testing.T) {
	bundle := fixtureBundle(t, "0.7.0", "runtime")
	manifest, err := LoadManifest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(bundle, "extra")
	if err := os.WriteFile(extra, []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundle, manifest); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("undeclared file was not rejected: %v", err)
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(bundle, "bin", "coconet"), extra); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundle, manifest); err == nil || !strings.Contains(err.Error(), "unsupported file") {
		t.Fatalf("symlink was not rejected: %v", err)
	}
}

func bundleInputsFixture(t *testing.T, output, bundleVersion, claudeVersion string) BundleInputs {
	t.Helper()
	inputsRoot := t.TempDir()
	write := func(relative, content string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(inputsRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	codexPlugin := filepath.Join(inputsRoot, "codex-plugin")
	claudePlugin := filepath.Join(inputsRoot, "claude-plugin")
	cursorPlugin := filepath.Join(inputsRoot, "cursor-plugin")
	write("cursor-plugin/.cursor-plugin/plugin.json", `{"name":"coconet","version":"`+bundleVersion+`"}`, 0644)
	write("codex-plugin/.codex-plugin/plugin.json", `{"name":"coconet","version":"`+bundleVersion+`"}`, 0o644)
	write("codex-plugin/scripts/hook.sh", "#!/bin/sh\n", 0o755)
	write("claude-plugin/.claude-plugin/plugin.json", `{"name":"coconet","version":"`+claudeVersion+`"}`, 0o644)
	write("claude-plugin/.mcp.json", "{}", 0o644)
	write("cursor-plugin/mcp.json", "{}", 0o644)
	return BundleInputs{
		GoLicenses: licenseFixture(t), Version: bundleVersion, OS: "testos", Architecture: "testarch", OutputRoot: output,
		RuntimeBinary: write("inputs/coconet", "runtime", 0o755), BootstrapBinary: write("inputs/coconet-bootstrap", "bootstrap", 0o755),
		NodeBinary: write("inputs/node", "node", 0o755), ClaudeHelper: write("inputs/helper.mjs", "helper", 0o644), ClaudeSDK: write("inputs/sdk.mjs", "sdk", 0o644),
		NodeLicense: write("inputs/NODE_LICENSE", "node-license", 0o644), ClaudeSDKLicense: write("inputs/SDK_LICENSE.md", "sdk-license", 0o644),
		CodexPluginRoot: codexPlugin, ClaudePluginRoot: claudePlugin, CursorPluginRoot: cursorPlugin,
	}
}

func assertArchiveContains(t *testing.T, archivePath, expected string) {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == expected {
			return
		}
	}
	t.Fatalf("archive does not contain %q", expected)
}

func licenseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"index.json", "go/LICENSE", "modules/example/NOTICE"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture license"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBundleRequiresDependencyNotices(t *testing.T) {
	input := bundleInputsFixture(t, filepath.Join(t.TempDir(), "bundle"), "0.3.0", "0.3.0")
	input.GoLicenses = ""
	if _, err := AssembleBundle(input); err == nil {
		t.Fatal("bundle omitted Go licenses")
	}
	input.GoLicenses = t.TempDir()
	if _, err := AssembleBundle(input); err == nil {
		t.Fatal("bundle accepted empty license inventory")
	}
}

func TestPluginVersionCanDifferFromRuntimeRelease(t *testing.T) {
	options := bundleInputsFixture(t, filepath.Join(t.TempDir(), "bundle"), "0.3.0", "0.3.0")
	for _, plugin := range []struct{ root, host string }{{options.CodexPluginRoot, "codex"}, {options.ClaudePluginRoot, "claude"}, {options.CursorPluginRoot, "cursor"}} {
		if err := os.WriteFile(filepath.Join(plugin.root, "."+plugin.host+"-plugin", "plugin.json"), []byte(`{"name":"coconet","version":"0.2.0"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AssembleBundle(options); err != nil {
		t.Fatal(err)
	}
}
