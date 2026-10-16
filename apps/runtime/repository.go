package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type repository struct {
	root  string
	isGit bool
}

func discoverGitRepository(cwd string) (repository, error) {
	canonicalCWD, err := canonicalPath(cwd)
	if err != nil {
		return repository{}, err
	}
	command := exec.Command("git", "-C", canonicalCWD, "rev-parse", "--show-toplevel")
	output, err := command.CombinedOutput()
	if err != nil {
		return repository{}, fmt.Errorf("current directory is not inside a Git repository: %s", strings.TrimSpace(string(output)))
	}
	root, err := canonicalPath(strings.TrimSpace(string(output)))
	if err != nil {
		return repository{}, err
	}
	return repository{root: root, isGit: true}, nil
}

func discoverCollaborationRoot(cwd string) (repository, error) {
	if gitRepository, err := discoverGitRepository(cwd); err == nil {
		return gitRepository, nil
	}
	root, err := canonicalPath(cwd)
	if err != nil {
		return repository{}, err
	}
	return repository{root: root, isGit: false}, nil
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	return filepath.Clean(resolved), nil
}
