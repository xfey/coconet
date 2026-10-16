package distribution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ServerBundleInputs struct {
	GoLicenses          string
	OperationsDirectory string
	Version             string
	OS                  string
	Architecture        string
	OutputRoot          string
	ServerBinary        string
	SystemdExample      string
	JournalExample      string
	EnvironmentExample  string
	NginxExample        string
}

func ServerBundleName(version, targetOS, architecture string) (string, error) {
	if !safeReleaseValue.MatchString(version) || !safeReleaseValue.MatchString(targetOS) || !safeReleaseValue.MatchString(architecture) {
		return "", errors.New("Server bundle version or platform is invalid")
	}
	if targetOS != "linux" {
		return "", errors.New("coconet-server Preview supports Linux operator bundles only")
	}
	return fmt.Sprintf("coconet-server-%s-%s-%s", version, targetOS, architecture), nil
}

func AssembleServerBundle(input ServerBundleInputs) (Manifest, error) {
	if _, err := ServerBundleName(input.Version, input.OS, input.Architecture); err != nil {
		return Manifest{}, err
	}
	if _, err := os.Lstat(input.OutputRoot); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Manifest{}, errors.New("Server bundle output already exists")
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
	staging, err := os.MkdirTemp(parent, ".server-bundle-*")
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
		{input.ServerBinary, "bin/coconet-server", 0o755},
		{input.SystemdExample, "examples/systemd/coconet-server.service", 0o644},
		{input.JournalExample, "examples/systemd/journald@coconet.conf", 0o644},
		{input.EnvironmentExample, "examples/systemd/server.env.example", 0o644},
		{input.NginxExample, "examples/nginx/coconet-server.conf", 0o644},
	}
	if input.OperationsDirectory != "" {
		for _, name := range []string{"storage-check.py", "backup.py", "STORAGE.md", "storage-check.example.json", "backup.example.json", "systemd/coconet-storage-check.service", "systemd/coconet-storage-check.timer", "systemd/coconet-backup.service", "systemd/coconet-backup.timer"} {
			files = append(files, struct {
				source      string
				destination string
				mode        os.FileMode
			}{filepath.Join(input.OperationsDirectory, filepath.FromSlash(name)), "examples/operations/" + name, 0644})
		}
	}
	for _, file := range files {
		if file.source == "" {
			return Manifest{}, fmt.Errorf("Server bundle input for %q is missing", file.destination)
		}
		destination := filepath.Join(staging, filepath.FromSlash(file.destination))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return Manifest{}, err
		}
		if err := copyRegularFile(file.source, destination, file.mode); err != nil {
			return Manifest{}, fmt.Errorf("copy %q: %w", file.destination, err)
		}
	}
	manifest, err := createProductManifest(staging, "coconet-server", input.Version, input.OS, input.Architecture)
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
