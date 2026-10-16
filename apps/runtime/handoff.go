package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"strings"
	"time"
)

const mcpHandoffTimeout = 45 * time.Second

type gitSnapshot struct {
	Head        string
	Branch      string
	Dirty       bool
	Fingerprint string
}

func captureGitSnapshot(ctx context.Context, cwd string) (gitSnapshot, error) {
	head, err := gitOutput(ctx, cwd, "rev-parse", "HEAD")
	if err != nil {
		return gitSnapshot{}, err
	}
	branch, _ := gitOutput(ctx, cwd, "symbolic-ref", "--short", "HEAD")
	status, err := gitOutput(ctx, cwd, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return gitSnapshot{}, err
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{head, branch, status}, "\x00")))
	return gitSnapshot{Head: head, Branch: branch, Dirty: status != "", Fingerprint: hex.EncodeToString(digest[:])}, nil
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
