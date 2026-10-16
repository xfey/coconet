package main

import (
	"os"
	"path/filepath"

	"github.com/xfey/coconet/adapters/claude"
	"github.com/xfey/coconet/adapters/codex"
	"github.com/xfey/coconet/adapters/cursor"
	"github.com/xfey/coconet/packages/contracts"
)

func libraryConfigRoot(agent contracts.AgentType) string {
	if agent == contracts.AgentCodex {
		return codex.New("").ConfigRoot
	}
	if agent == contracts.AgentCursor {
		return cursor.New("").ConfigRoot
	}
	return claude.New("").ConfigRoot
}

// The trusted Plugin wrapper supplies this location. Invalid or missing files
// must not prevent the other read-only startup context from being delivered.
func installedTeamContextSkillPath(path string) string {
	if !filepath.IsAbs(path) || filepath.Base(path) != "SKILL.md" || filepath.Base(filepath.Dir(path)) != "coconet-team-context" {
		return ""
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return canonical
}
