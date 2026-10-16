package distribution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type UninstallOptions struct {
	InstallRoot  string
	BinDirectory string
}

type UninstallResult struct {
	InstallRoot       string `json:"install_root"`
	Executable        string `json:"executable,omitempty"`
	RuntimeRemoved    bool   `json:"runtime_removed"`
	ExecutableRemoved bool   `json:"executable_removed"`
	AlreadyAbsent     bool   `json:"already_absent"`
}

func Uninstall(options UninstallOptions) (UninstallResult, error) {
	if options.InstallRoot == "" {
		return UninstallResult{}, errors.New("install root is required")
	}
	installRoot, err := filepath.Abs(options.InstallRoot)
	if err != nil {
		return UninstallResult{}, err
	}
	if err := rejectBroadRemovalRoot(installRoot); err != nil {
		return UninstallResult{}, err
	}
	result := UninstallResult{InstallRoot: installRoot}
	executable := ""
	if options.BinDirectory != "" {
		binDirectory, err := filepath.Abs(options.BinDirectory)
		if err != nil {
			return UninstallResult{}, err
		}
		executable = filepath.Join(binDirectory, "coconet")
		result.Executable = executable
	}
	info, err := os.Lstat(installRoot)
	if errors.Is(err, os.ErrNotExist) {
		result.AlreadyAbsent = true
		if executable != "" {
			removed, err := removeOwnedExecutable(executable, installRoot)
			if err != nil {
				return UninstallResult{}, err
			}
			result.ExecutableRemoved = removed
		}
		return result, nil
	}
	if err != nil {
		return UninstallResult{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return UninstallResult{}, errors.New("install root is not a regular directory")
	}
	if err := validateInstalledRuntime(installRoot); err != nil {
		return UninstallResult{}, err
	}
	if err := os.RemoveAll(installRoot); err != nil {
		return UninstallResult{}, fmt.Errorf("remove Coconet Runtime: %w", err)
	}
	result.RuntimeRemoved = true
	if executable != "" {
		removed, err := removeOwnedExecutable(executable, installRoot)
		if err != nil {
			return result, err
		}
		result.ExecutableRemoved = removed
	}
	return result, nil
}

func validateInstalledRuntime(installRoot string) error {
	canonicalRoot, err := filepath.EvalSymlinks(installRoot)
	if err != nil {
		return fmt.Errorf("resolve Coconet install root: %w", err)
	}
	current := filepath.Join(installRoot, "current")
	info, err := os.Lstat(current)
	if err != nil {
		return fmt.Errorf("inspect active Coconet Runtime: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return errors.New("active Coconet Runtime is not an installer-owned symlink")
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return fmt.Errorf("resolve active Coconet Runtime: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	versionsRoot := filepath.Join(canonicalRoot, "versions")
	if !pathInside(versionsRoot, resolved) {
		return errors.New("active Coconet Runtime points outside the install root")
	}
	manifest, err := LoadManifest(resolved)
	if err != nil {
		return fmt.Errorf("validate active Coconet Runtime identity: %w", err)
	}
	if manifest.Product != "coconet" {
		return errors.New("active install is not a Coconet client Runtime")
	}
	return nil
}

func ownedExecutable(path, installRoot string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	target = filepath.Clean(target)
	if !pathInside(installRoot, target) {
		return false, nil
	}
	return true, nil
}

func removeOwnedExecutable(path, installRoot string) (bool, error) {
	owned, err := ownedExecutable(path, installRoot)
	if err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func rejectBroadRemovalRoot(path string) error {
	volumeRoot := filepath.Clean(filepath.VolumeName(path) + string(filepath.Separator))
	if path == volumeRoot {
		return errors.New("refusing to remove a filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if canonicalHome, err := filepath.Abs(home); err == nil && path == canonicalHome {
			return errors.New("refusing to remove the user home directory")
		}
	}
	return nil
}
