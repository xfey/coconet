package distribution

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed LICENSE
var coconetLicense []byte

type BundleInputs struct {
	GoLicenses       string
	Version          string
	OS               string
	Architecture     string
	OutputRoot       string
	RuntimeBinary    string
	BootstrapBinary  string
	NodeBinary       string
	NodeLicense      string
	ClaudeHelper     string
	ClaudeSDK        string
	ClaudeSDKLicense string
	CodexPluginRoot  string
	ClaudePluginRoot string
	CursorPluginRoot string
}

func BundleName(version, targetOS, architecture string) (string, error) {
	if !safeReleaseValue.MatchString(version) || !safeReleaseValue.MatchString(targetOS) || !safeReleaseValue.MatchString(architecture) {
		return "", errors.New("bundle version or platform is invalid")
	}
	return fmt.Sprintf("coconet-%s-%s-%s", version, targetOS, architecture), nil
}

func AssembleBundle(input BundleInputs) (Manifest, error) {
	if _, err := os.Lstat(input.OutputRoot); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Manifest{}, errors.New("bundle output already exists")
		}
		return Manifest{}, err
	}
	parent, err := filepath.Abs(filepath.Dir(input.OutputRoot))
	if err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Manifest{}, err
	}
	staging, err := os.MkdirTemp(parent, ".bundle-*")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(staging)
	if err := os.WriteFile(filepath.Join(staging, "LICENSE"), coconetLicense, 0o644); err != nil {
		return Manifest{}, err
	}
	if err := copyGoLicenses(input.GoLicenses, staging); err != nil {
		return Manifest{}, err
	}
	files := []struct {
		source      string
		destination string
		mode        os.FileMode
	}{
		{input.RuntimeBinary, "bin/coconet", 0o755},
		{input.BootstrapBinary, "bin/coconet-bootstrap", 0o755},
		{input.NodeBinary, "bin/lib/node/node", 0o755},
		{input.ClaudeHelper, "bin/lib/claude/helper.mjs", 0o644},
		{input.ClaudeSDK, "bin/lib/claude/sdk.mjs", 0o644},
		{input.NodeLicense, "licenses/NODE_LICENSE", 0o644},
		{input.ClaudeSDKLicense, "licenses/CLAUDE_AGENT_SDK_LICENSE.md", 0o644},
	}
	for _, file := range files {
		if file.source == "" {
			return Manifest{}, fmt.Errorf("bundle input for %q is missing", file.destination)
		}
		destination := filepath.Join(staging, filepath.FromSlash(file.destination))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return Manifest{}, err
		}
		if err := copyRegularFile(file.source, destination, file.mode); err != nil {
			return Manifest{}, fmt.Errorf("copy %q: %w", file.destination, err)
		}
	}
	pluginTargets := []struct {
		source      string
		destination string
	}{
		{input.CodexPluginRoot, filepath.Join(staging, "marketplaces", "codex", "plugins", "coconet")},
		{input.ClaudePluginRoot, filepath.Join(staging, "marketplaces", "claude", "plugins", "coconet")},
		{input.CursorPluginRoot, filepath.Join(staging, "marketplaces", "cursor", "plugins", "coconet")},
	}
	for _, plugin := range pluginTargets {
		if err := copyDirectory(plugin.source, plugin.destination); err != nil {
			return Manifest{}, err
		}
	}
	if err := writeMarketplaces(staging, input.Version); err != nil {
		return Manifest{}, err
	}
	manifest, err := CreateManifest(staging, input.Version, input.OS, input.Architecture)
	if err != nil {
		return Manifest{}, err
	}
	if err := WriteManifest(staging, manifest); err != nil {
		return Manifest{}, err
	}
	if err := VerifyBundle(staging, manifest); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(staging, input.OutputRoot); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ArchiveBundle(bundleRoot, archivePath string) (string, error) {
	manifest, err := LoadManifest(bundleRoot)
	if err != nil {
		return "", err
	}
	if err := VerifyBundle(bundleRoot, manifest); err != nil {
		return "", err
	}
	if _, err := os.Lstat(archivePath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", errors.New("bundle archive already exists")
		}
		return "", err
	}
	checksumPath := archivePath + ".sha256"
	if _, err := os.Lstat(checksumPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", errors.New("bundle checksum already exists")
		}
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(archivePath), ".archive-*")
	if err != nil {
		return "", err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	gzipWriter := gzip.NewWriter(io.MultiWriter(temporary, hasher))
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := filepath.Base(bundleRoot)
	err = filepath.WalkDir(bundleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(bundleRoot, path)
		if err != nil {
			return err
		}
		name := prefix
		if relative != "." {
			name = filepath.ToSlash(filepath.Join(prefix, relative))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("bundle archive refuses non-regular file %q", relative)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		// Archive permissions are part of the distribution contract, not the
		// operator's staging umask. Keep staging private without leaking its
		// directory modes into a bundle consumed by the npm installer.
		if info.IsDir() {
			header.Mode = 0o755
			if relative == "." {
				header.Mode = 0o700
			}
		}
		header.ModTime = time.Unix(0, 0)
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := tarWriter.Close(); err == nil {
		err = closeErr
	}
	if closeErr := gzipWriter.Close(); err == nil {
		err = closeErr
	}
	if syncErr := temporary.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	if err := os.Rename(temporaryPath, archivePath); err != nil {
		return "", err
	}
	checksum := []byte(hash + "  " + filepath.Base(archivePath) + "\n")
	if err := os.WriteFile(checksumPath, checksum, 0o644); err != nil {
		_ = os.Remove(archivePath)
		return "", err
	}
	if err := os.Chmod(checksumPath, 0o644); err != nil {
		_ = os.Remove(checksumPath)
		_ = os.Remove(archivePath)
		return "", err
	}
	return hash, nil
}

func copyDirectory(source, destination string) error {
	if source == "" {
		return errors.New("plugin source directory is required")
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("plugin source contains unsupported file %q", path)
		}
		mode := os.FileMode(0o644)
		if info, err := entry.Info(); err != nil {
			return err
		} else if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		return copyRegularFile(path, target, mode)
	})
}

func writeMarketplaces(root, bundleVersion string) error {
	codexVersion, err := pluginVersion(filepath.Join(root, "marketplaces", "codex", "plugins", "coconet", ".codex-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	claudeVersion, err := pluginVersion(filepath.Join(root, "marketplaces", "claude", "plugins", "coconet", ".claude-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	cursorVersion, err := pluginVersion(filepath.Join(root, "marketplaces", "cursor", "plugins", "coconet", ".cursor-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	if codexVersion != claudeVersion || codexVersion != cursorVersion {
		return errors.New("Codex, Claude and Cursor Plugin versions must match in one bundle")
	}
	codex := map[string]any{
		"name": "coconet", "interface": map[string]string{"displayName": "Coconet"},
		"plugins": []any{map[string]any{
			"name": "coconet", "source": map[string]string{"source": "local", "path": "./plugins/coconet"},
			"policy": map[string]string{"installation": "AVAILABLE", "authentication": "ON_INSTALL"}, "category": "Productivity",
		}},
	}
	claude := map[string]any{
		"name": "coconet", "owner": map[string]string{"name": "Coconet"},
		"metadata": map[string]string{"description": "Coconet team Session collaboration Plugin."},
		"plugins": []any{map[string]any{
			"name": "coconet", "source": "./plugins/coconet", "description": "Connect Claude Code to Coconet team Session collaboration.", "version": claudeVersion,
		}},
	}
	for path, value := range map[string]any{
		filepath.Join(root, "marketplaces", "codex", ".agents", "plugins", "marketplace.json"): codex,
		filepath.Join(root, "marketplaces", "claude", ".claude-plugin", "marketplace.json"):    claude,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		content, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, append(content, '\n'), 0o644); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func pluginVersion(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return "", err
	}
	if manifest.Name != "coconet" || !safeReleaseValue.MatchString(manifest.Version) {
		return "", errors.New("Plugin manifest has an invalid Coconet name or version")
	}
	return manifest.Version, nil
}

func VerifyArchiveChecksum(archivePath, expected string) error {
	actual, _, err := hashFile(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return errors.New("bundle archive SHA-256 mismatch")
	}
	return nil
}

// New bundles must carry linked Go dependency terms. Reading older bundles stays
// compatible because the manifest validates every file actually distributed.
func copyGoLicenses(source, bundle string) error {
	if source == "" {
		return errors.New("Go dependency license directory is required")
	}
	for _, name := range []string{"index.json", "go/LICENSE"} {
		info, err := os.Lstat(filepath.Join(source, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("Go dependency license file %s is missing or invalid", name)
		}
	}
	return copyDirectory(source, filepath.Join(bundle, "licenses", "go"))
}
