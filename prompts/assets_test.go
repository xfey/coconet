package prompts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionPromptAssetsAreComplete(t *testing.T) {
	for name, content := range map[string]string{
		"team context":       CoconetTeamContextSkill(),
		"Codex default":      CodexDefaultPrompt(),
		"Skill location":     SkillLocationContext(),
		"discovery evidence": DiscoveryEvidence(),
		"local stage":        LocalStageSystem(),
		"local relations":    LocalRelationsSystem(),
		"local Session":      LocalSessionContext(),
	} {
		if strings.TrimSpace(content) == "" {
			t.Fatalf("%s prompt asset is empty", name)
		}
	}
	for _, name := range []string{
		"coconet_health",
		"read_project_work_graph",
		"list_library_entries",
		"search_work",
		"pull_session",
		"fork_session",
	} {
		if strings.TrimSpace(MCPTool("local_"+name).Description) == "" {
			t.Fatalf("MCP tool %s has no description", name)
		}
	}

}

func TestGeneratedPluginTextMatchesCanonicalAssets(t *testing.T) {
	root := promptRepositoryRoot(t)
	wantSkill := CoconetTeamContextSkill()
	for _, relative := range []string{
		"plugins/codex/coconet/skills/coconet-team-context/SKILL.md",
		"plugins/claude/coconet/skills/coconet-team-context/SKILL.md",
		"plugins/cursor/coconet/skills/coconet-team-context/SKILL.md",
	} {
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != wantSkill {
			t.Fatalf("%s is stale; run go generate ./prompts", relative)
		}
	}

	manifestContent, err := os.ReadFile(filepath.Join(root, "plugins/codex/coconet/.codex-plugin/plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Interface struct {
			DefaultPrompt []string `json:"defaultPrompt"`
		} `json:"interface"`
	}
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Interface.DefaultPrompt) != 1 || manifest.Interface.DefaultPrompt[0] != CodexDefaultPrompt() {
		t.Fatal("Codex defaultPrompt is stale; run go generate ./prompts")
	}
}

func promptRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve prompt package path")
	}
	return filepath.Dir(filepath.Dir(current))
}
