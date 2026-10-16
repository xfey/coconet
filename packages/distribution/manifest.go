package distribution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	ManifestSchemaVersion = 1
	ManifestFilename      = "manifest.json"
)

var safeReleaseValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	Product       string         `json:"product"`
	Version       string         `json:"version"`
	OS            string         `json:"os"`
	Architecture  string         `json:"architecture"`
	Files         []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

func LoadManifest(bundleRoot string) (Manifest, error) {
	manifestPath := filepath.Join(bundleRoot, ManifestFilename)
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect bundle Manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		return Manifest{}, errors.New("bundle Manifest must be a regular file with mode 0644")
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("read bundle Manifest: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode bundle Manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("bundle Manifest contains trailing JSON data")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Product != "coconet" && manifest.Product != "coconet-server" {
		return errors.New("bundle Manifest identity is unsupported")
	}
	if !safeReleaseValue.MatchString(manifest.Version) || !safeReleaseValue.MatchString(manifest.OS) || !safeReleaseValue.MatchString(manifest.Architecture) {
		return errors.New("bundle version or platform is invalid")
	}
	if len(manifest.Files) == 0 {
		return errors.New("bundle Manifest has no files")
	}
	required := map[string]bool{}
	if manifest.Product == "coconet" {
		required = map[string]bool{
			"bin/coconet":                                                    false,
			"bin/coconet-bootstrap":                                          false,
			"bin/lib/node/node":                                              false,
			"bin/lib/claude/helper.mjs":                                      false,
			"bin/lib/claude/sdk.mjs":                                         false,
			"licenses/NODE_LICENSE":                                          false,
			"licenses/CLAUDE_AGENT_SDK_LICENSE.md":                           false,
			"marketplaces/codex/.agents/plugins/marketplace.json":            false,
			"marketplaces/codex/plugins/coconet/.codex-plugin/plugin.json":   false,
			"marketplaces/claude/.claude-plugin/marketplace.json":            false,
			"marketplaces/claude/plugins/coconet/.claude-plugin/plugin.json": false,
			"marketplaces/cursor/plugins/coconet/.cursor-plugin/plugin.json": false,
			"marketplaces/cursor/plugins/coconet/mcp.json":                   false,
		}
	} else {
		required = map[string]bool{
			"bin/coconet-server":                      false,
			"examples/systemd/coconet-server.service": false,
			"examples/systemd/server.env.example":     false,
			"examples/nginx/coconet-server.conf":      false,
		}
	}
	previous := ""
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, file := range manifest.Files {
		if !validRelativePath(file.Path) || file.Path == ManifestFilename {
			return fmt.Errorf("bundle Manifest contains unsafe path %q", file.Path)
		}
		if _, exists := seen[file.Path]; exists {
			return fmt.Errorf("bundle Manifest repeats path %q", file.Path)
		}
		if previous != "" && file.Path < previous {
			return errors.New("bundle Manifest files must be sorted by path")
		}
		if len(file.SHA256) != 64 {
			return fmt.Errorf("bundle file %q has an invalid SHA-256", file.Path)
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return fmt.Errorf("bundle file %q has an invalid SHA-256", file.Path)
		}
		if file.Size < 0 || file.Mode != 0o644 && file.Mode != 0o755 {
			return fmt.Errorf("bundle file %q has invalid size or mode", file.Path)
		}
		seen[file.Path] = struct{}{}
		if _, exists := required[file.Path]; exists {
			required[file.Path] = true
		}
		previous = file.Path
	}
	for path, present := range required {
		if !present {
			return fmt.Errorf("bundle Manifest is missing %q", path)
		}
	}
	return nil
}

func VerifyBundle(bundleRoot string, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	root, err := filepath.Abs(bundleRoot)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() {
		return errors.New("bundle root must be a directory, not a symbolic link")
	}
	expectedFiles := make(map[string]ManifestFile, len(manifest.Files))
	for _, expected := range manifest.Files {
		expectedFiles[expected.Path] = expected
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ManifestFilename {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
				return errors.New("bundle Manifest must be a regular file with mode 0644")
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("bundle contains unsupported file %q", relative)
		}
		if _, exists := expectedFiles[relative]; !exists {
			return fmt.Errorf("bundle contains file not declared by the Manifest %q", relative)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, expected := range manifest.Files {
		path := filepath.Join(root, filepath.FromSlash(expected.Path))
		if !pathInside(root, path) {
			return fmt.Errorf("bundle file %q escaped its root", expected.Path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect bundle file %q: %w", expected.Path, err)
		}
		if !info.Mode().IsRegular() || uint32(info.Mode().Perm()) != expected.Mode || info.Size() != expected.Size {
			return fmt.Errorf("bundle file %q metadata does not match the Manifest", expected.Path)
		}
		hash, _, err := hashFile(path)
		if err != nil || hash != expected.SHA256 {
			return fmt.Errorf("bundle file %q failed SHA-256 verification", expected.Path)
		}
	}
	return nil
}

func CreateManifest(bundleRoot, version, targetOS, architecture string) (Manifest, error) {
	return createProductManifest(bundleRoot, "coconet", version, targetOS, architecture)
}

func createProductManifest(bundleRoot, product, version, targetOS, architecture string) (Manifest, error) {
	rootInfo, err := os.Lstat(bundleRoot)
	if err != nil {
		return Manifest{}, err
	}
	if !rootInfo.IsDir() {
		return Manifest{}, errors.New("bundle root must be a directory, not a symbolic link")
	}
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, Product: product, Version: version, OS: targetOS, Architecture: architecture}
	err = filepath.WalkDir(bundleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == bundleRoot || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("bundle source contains unsupported file %q", path)
		}
		relative, err := filepath.Rel(bundleRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ManifestFilename {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := uint32(info.Mode().Perm())
		if mode != 0o644 && mode != 0o755 {
			return fmt.Errorf("bundle source file %q must use mode 0644 or 0755", relative)
		}
		hash, size, err := hashFile(path)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, ManifestFile{Path: relative, SHA256: hash, Size: size, Mode: mode})
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func WriteManifest(bundleRoot string, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(bundleRoot, ".manifest-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(bundleRoot, ManifestFilename))
}

func validRelativePath(value string) bool {
	if value == "" || strings.Contains(value, `\`) || filepath.IsAbs(value) || strings.HasPrefix(value, "/") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func pathInside(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}
