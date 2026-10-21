package distribution

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type InstallOptions struct {
	StageOnly    bool
	BundleRoot   string
	InstallRoot  string
	BinDirectory string
	SkipBinLink  bool
	CurrentOS    string
	CurrentArch  string
}

type InstallResult struct {
	Version       string
	ReleasePath   string
	Executable    string
	AlreadyExists bool
}

func Install(options InstallOptions) (InstallResult, error) {
	manifest, err := LoadManifest(options.BundleRoot)
	if err != nil {
		return InstallResult{}, err
	}
	if manifest.Product != "coconet" {
		return InstallResult{}, errors.New("client Bootstrap only installs coconet client bundles")
	}
	if manifest.OS != options.CurrentOS || manifest.Architecture != options.CurrentArch {
		return InstallResult{}, fmt.Errorf("bundle targets %s/%s, current platform is %s/%s", manifest.OS, manifest.Architecture, options.CurrentOS, options.CurrentArch)
	}
	if err := VerifyBundle(options.BundleRoot, manifest); err != nil {
		return InstallResult{}, err
	}
	if options.InstallRoot == "" || (!options.SkipBinLink && options.BinDirectory == "") {
		return InstallResult{}, errors.New("install root and bin directory are required unless the executable link is disabled")
	}
	installRoot, err := filepath.Abs(options.InstallRoot)
	if err != nil {
		return InstallResult{}, err
	}
	binDirectory := ""
	if !options.SkipBinLink {
		binDirectory, err = filepath.Abs(options.BinDirectory)
		if err != nil {
			return InstallResult{}, err
		}
	}
	releaseID := manifest.Version + "-" + manifest.OS + "-" + manifest.Architecture
	versionsRoot := filepath.Join(installRoot, "versions")
	releasePath := filepath.Join(versionsRoot, releaseID)
	currentPath := filepath.Join(installRoot, "current")
	executableTarget := filepath.Join(currentPath, "bin", "coconet")
	executable := executableTarget
	if !options.SkipBinLink {
		executable = filepath.Join(binDirectory, "coconet")
		if err := preflightExecutableLink(executable, executableTarget); err != nil {
			return InstallResult{}, err
		}
	}
	directories := []string{installRoot, versionsRoot}
	if !options.SkipBinLink {
		directories = append(directories, binDirectory)
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return InstallResult{}, err
		}
	}
	alreadyExists := false
	if info, statErr := os.Stat(releasePath); statErr == nil {
		if !info.IsDir() {
			return InstallResult{}, errors.New("release target exists and is not a directory")
		}
		if err := VerifyBundle(releasePath, manifest); err != nil {
			return InstallResult{}, errors.New("installed release differs from the requested bundle")
		}
		alreadyExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return InstallResult{}, statErr
	} else if err := installRelease(options.BundleRoot, installRoot, releasePath, manifest); err != nil {
		return InstallResult{}, err
	}

	if options.StageOnly {
		return InstallResult{Version: manifest.Version, ReleasePath: releasePath, Executable: executable, AlreadyExists: alreadyExists}, nil
	}
	previousCurrent, hadCurrent, err := readSymlink(currentPath)
	if err != nil {
		return InstallResult{}, err
	}
	if err := replaceSymlink(currentPath, filepath.Join("versions", releaseID)); err != nil {
		return InstallResult{}, err
	}
	if !options.SkipBinLink {
		if err := replaceSymlink(executable, executableTarget); err != nil {
			if hadCurrent {
				_ = replaceSymlink(currentPath, previousCurrent)
			} else {
				_ = os.Remove(currentPath)
			}
			return InstallResult{}, err
		}
	}
	return InstallResult{Version: manifest.Version, ReleasePath: releasePath, Executable: executable, AlreadyExists: alreadyExists}, nil
}

func installRelease(bundleRoot, installRoot, releasePath string, manifest Manifest) error {
	staging, err := os.MkdirTemp(installRoot, ".staging-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, entry := range manifest.Files {
		source := filepath.Join(bundleRoot, filepath.FromSlash(entry.Path))
		destination := filepath.Join(staging, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := copyRegularFile(source, destination, os.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("install bundle file %q: %w", entry.Path, err)
		}
	}
	manifestContent, err := os.ReadFile(filepath.Join(bundleRoot, ManifestFilename))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, ManifestFilename), manifestContent, 0o644); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(staging, ManifestFilename), 0o644); err != nil {
		return err
	}
	if err := VerifyBundle(staging, manifest); err != nil {
		return err
	}
	if err := os.Rename(staging, releasePath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("release was installed concurrently; rerun Bootstrap")
		}
		return err
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := output.Chmod(mode); err != nil {
		_ = output.Close()
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func preflightExecutableLink(path, target string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("refusing to replace existing non-symlink executable %q", path)
	}
	existing, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if existing != target {
		return fmt.Errorf("refusing to replace executable symlink not owned by this install: %q", path)
	}
	return nil
}

func readSymlink(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", false, fmt.Errorf("refusing to replace non-symlink %q", path)
	}
	target, err := os.Readlink(path)
	return target, true, err
}

func replaceSymlink(path, target string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".next")
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
