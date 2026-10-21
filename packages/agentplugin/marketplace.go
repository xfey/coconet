package agentplugin

import (
	"errors"
	"os"
	"path/filepath"
)

// StableMarketplace keeps the registered source directory unchanged across
// Runtime releases. The plugin itself stays in the immutable verified bundle.
func StableMarketplace(source, destination string, host Host) (string, error) {
	if host == HostCursor {
		return source, nil
	} // Cursor's local plugin path is already stable.
	manifest := filepath.Join(".agents", "plugins", "marketplace.json")
	if host == HostClaude {
		manifest = filepath.Join(".claude-plugin", "marketplace.json")
	}
	data, err := os.ReadFile(filepath.Join(source, manifest))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(destination, manifest)), 0700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(destination, "plugins"), 0700); err != nil {
		return "", err
	}
	target := filepath.Join(destination, "plugins", "coconet")
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("stable plugin source is not a managed link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	link := target + ".next"
	_ = os.Remove(link)
	if err := os.Symlink(filepath.Join(source, "plugins", "coconet"), link); err != nil {
		return "", err
	}
	if err := os.Rename(link, target); err != nil {
		_ = os.Remove(link)
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(filepath.Join(destination, manifest)), ".marketplace-")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(file.Name(), filepath.Join(destination, manifest)); err != nil {
		return "", err
	}
	return destination, nil
}
