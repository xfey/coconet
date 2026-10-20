package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type purgeResult struct {
	Root                       string `json:"root"`
	Preserved                  bool   `json:"preserved"`
	Purged                     bool   `json:"purged"`
	EntriesRemoved             int    `json:"entries_removed,omitempty"`
	KeychainCredentialsRemoved int    `json:"keychain_credentials_removed,omitempty"`
	RootRemoved                bool   `json:"root_removed,omitempty"`
}

var deleteNextKeychainCredential = func() ([]byte, error) {
	return exec.Command("security", "delete-generic-password", "-s", keychainService).CombinedOutput()
}

func purgeLocalState(paths runtimePaths) (purgeResult, error) {
	result := purgeResult{Root: paths.root}
	if filepath.Base(filepath.Clean(paths.root)) != "coconet" {
		return result, errors.New("refusing to purge a configuration directory whose final directory is not named coconet")
	}
	info, err := os.Lstat(paths.root)
	if errors.Is(err, os.ErrNotExist) {
		result.Purged = true
		result.RootRemoved = true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("Coconet local state root is not a regular directory")
	}
	if runtime.GOOS == "darwin" && !paths.explicitHome {
		removed, err := purgeKeychainCredentials()
		if err != nil {
			return result, err
		}
		result.KeychainCredentialsRemoved = removed
	}
	entries := []string{"credentials.json", "bindings.json", "operations.json", "agent-integrations.json", defaultAgentsFilename, "deployments", "artifacts", "session-origins"}
	for _, name := range entries {
		path := filepath.Join(paths.root, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return result, err
		}
		if err := os.RemoveAll(path); err != nil {
			return result, fmt.Errorf("remove Coconet local state entry %q: %w", name, err)
		}
		result.EntriesRemoved++
	}
	remaining, err := os.ReadDir(paths.root)
	if err != nil {
		return result, err
	}
	if len(remaining) == 0 {
		if err := os.Remove(paths.root); err != nil {
			return result, err
		}
		result.RootRemoved = true
	}
	result.Purged = true
	return result, nil
}

func purgeKeychainCredentials() (int, error) {
	removed := 0
	for attempts := 0; attempts < 1000; attempts++ {
		output, err := deleteNextKeychainCredential()
		if err == nil {
			removed++
			continue
		}
		detail := strings.ToLower(string(output) + " " + err.Error())
		if strings.Contains(detail, "could not be found") || strings.Contains(detail, "item not found") {
			return removed, nil
		}
		return removed, fmt.Errorf("delete macOS Keychain Coconet credential: %s", strings.TrimSpace(string(output)))
	}
	return removed, errors.New("too many Coconet Keychain credentials")
}
