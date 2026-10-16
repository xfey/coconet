// Package prompts exposes the production text sent to inference providers and
// coding agents. Edit the files in this directory instead of embedding product
// instructions in application code.
package prompts

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:generate node sync.mjs

//go:embed local-stage.system.txt
var localStageSystem string

//go:embed local-relations.system.txt
var localRelationsSystem string

func LocalStageSystem() string     { return normalized(localStageSystem) }
func LocalRelationsSystem() string { return normalized(localRelationsSystem) }

//go:embed local-session-context.txt
var localSessionContext string

func LocalSessionContext() string { return normalized(localSessionContext) }

//go:embed skill-location-context.txt
var skillLocationContext string

//go:embed coconet-team-context.SKILL.md
var coconetTeamContextSkill string

//go:embed codex-default-prompt.txt
var codexDefaultPrompt string

//go:embed mcp-tools.json
var mcpToolsJSON []byte

//go:embed discovery-evidence.txt
var discoveryEvidence string

type MCPToolCopy struct {
	Description string            `json:"description"`
	Arguments   map[string]string `json:"arguments,omitempty"`
}

var mcpTools = decodeMCPTools(mcpToolsJSON)

func SkillLocationContext() string { return normalized(skillLocationContext) }

func CoconetTeamContextSkill() string { return normalized(coconetTeamContextSkill) + "\n" }

func CodexDefaultPrompt() string { return normalized(codexDefaultPrompt) }

func DiscoveryEvidence() string { return normalized(discoveryEvidence) }

func MCPTool(name string) MCPToolCopy {
	copy, ok := mcpTools[name]
	if !ok {
		panic(fmt.Sprintf("missing MCP tool copy %q", name))
	}
	return copy
}

func (copy MCPToolCopy) Argument(name string) string {
	description := strings.TrimSpace(copy.Arguments[name])
	if description == "" {
		panic(fmt.Sprintf("missing MCP argument copy %q", name))
	}
	return description
}

func decodeMCPTools(content []byte) map[string]MCPToolCopy {
	var result map[string]MCPToolCopy
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		panic(fmt.Sprintf("decode embedded MCP tool copy: %v", err))
	}
	if len(result) == 0 {
		panic("embedded MCP tool copy is empty")
	}
	for name, copy := range result {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(copy.Description) == "" {
			panic(fmt.Sprintf("invalid MCP tool copy %q", name))
		}
		for argument, description := range copy.Arguments {
			if strings.TrimSpace(argument) == "" || strings.TrimSpace(description) == "" {
				panic(fmt.Sprintf("invalid MCP argument copy %q.%s", name, argument))
			}
		}
	}
	return result
}

func normalized(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}
