package main

import (
	"encoding/json"
	"errors"
	"strings"
)

// Health is an explicit diagnostic tool, but its model-facing view still only
// describes the selected project. CLI status retains the full operator record.
// An allowlist keeps paths, raw errors and unrelated projects out of both MCP
// structuredContent and its text-only-host mirror.
func mcpProjectHealth(raw json.RawMessage) (map[string]any, error) {
	var status struct {
		Status     string                `json:"status"`
		Version    string                `json:"runtime_version"`
		Mode       string                `json:"sync_mode"`
		Paused     bool                  `json:"paused"`
		Configured bool                  `json:"inference_configured"`
		Errors     map[string]string     `json:"errors"`
		Project    *localOperationStatus `json:"project"`
	}
	if err := json.Unmarshal(raw, &status); err != nil || status.Status == "" || status.Project == nil {
		return nil, errors.New("local Runtime returned incomplete project health; retry or inspect coconet projection status")
	}
	p := status.Project
	processing := "enabled"
	if !status.Configured {
		processing = "not_configured"
	} else if status.Paused {
		processing = "paused"
	}
	// Group background issues rather than describing every retry as a new fault.
	issues := map[string]int{}
	for _, issue := range p.Issues {
		issues[issue.Kind+"/"+issue.State]++
	}
	components := map[string]string{}
	for _, kind := range []string{"configuration", "replication", "content", "indexing", "processing", "publication"} {
		if status.Errors[kind] != "" || status.Errors[kind+"/"+p.ProjectID] != "" {
			components[kind] = "error"
		}
	}
	project := map[string]any{
		"project_id": p.ProjectID, "accepted_revision": p.Revision, "last_confirmed_checkpoint_revision": p.Published,
		"replicas":     p.Replicas,
		"search_index": map[string]any{"revision": p.SearchIndex.Revision, "expected": p.SearchIndex.Expected, "indexed": p.SearchIndex.Indexed, "complete": p.SearchIndex.Complete},
		"background":   map[string]any{"capture": map[string]int{"queued": max(0, p.CapturePending-p.CapturePaused), "paused": p.CapturePaused}, "tasks": p.Tasks, "pending_commits": p.PendingCommits, "issue_count": p.IssueCount, "issue_groups": issues, "issue_groups_complete": len(p.Issues) == p.IssueCount},
	}
	if len(components) > 0 {
		project["component_errors"] = components
	}
	if p.IssueCount > 0 || len(components) > 0 {
		project["diagnostics_command"] = "coconet projection status --json"
	}
	return map[string]any{"status": status.Status, "runtime_version": status.Version, "sync_mode": status.Mode, "processing": processing, "project": project}, nil
}

// A provider stack is useful in diagnostics, not as an instruction to the
// working Agent. Preserve concise controlled errors and their recovery advice.
func mcpRuntimeErrorText(err error) string {
	text := err.Error()
	if len(text) > 600 {
		return "The requested Coconet operation failed. Detailed error information is available in local diagnostics."
	}
	// Local recovery explanations use a single explicit Next step line. Other
	// multiline output may be a subprocess stack or wrapper log; do not relay it.
	for _, line := range strings.Split(text, "\n")[1:] {
		if line != "" && !strings.HasPrefix(line, "Next step: ") {
			return "The requested Coconet operation failed. Detailed error information is available in local diagnostics."
		}
	}
	return text
}
