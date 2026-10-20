package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
	productprompts "github.com/xfey/coconet/prompts"
)

// Startup only explains the available local workflow and the trusted Skill
// location. It does not query a graph, start inference or read Session history.
func writeManagedSessionStartContext(agent contracts.AgentType, skillPath string, stdout io.Writer) {
	parts := []string{productprompts.LocalSessionContext()}
	if agent == contracts.AgentCodex {
		if path := installedTeamContextSkillPath(skillPath); path != "" {
			encoded, _ := json.Marshal(path)
			parts = append(parts, productprompts.SkillLocationContext()+"\ncoconet_team_context_skill_path="+string(encoded))
		}
	}
	if agent == contracts.AgentCursor {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"additional_context": strings.Join(parts, "\n")})
		return
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "SessionStart", "additionalContext": strings.Join(parts, "\n"),
	}})
}

type localMCPProject struct {
	root    string
	project string
}

// A configured workspace is an exclusive routing boundary, including failures.
// Never send its queries to the legacy semantic server after a local error.
func resolveLocalMCPProject() (localMCPProject, bool, error) {
	root, err := managedLocalDAGRoot()
	if err != nil {
		return localMCPProject{}, true, err
	}
	config, err := loadLocalDAGService(root)
	if errors.Is(err, os.ErrNotExist) && !localDAGIsConnectedRoot(root) {
		return localMCPProject{}, false, nil
	}
	if err != nil {
		return localMCPProject{}, true, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return localMCPProject{}, true, err
	}
	cwd, err = canonicalPath(cwd)
	if err != nil {
		return localMCPProject{}, true, err
	}
	selected := localDAGWorkspace{}
	for _, workspace := range config.Workspaces {
		if pathContains(workspace.Path, cwd) && len(workspace.Path) > len(selected.Path) {
			selected = workspace
		}
	}
	return localMCPProject{root: root, project: selected.ProjectID}, selected.ProjectID != "", nil
}

type boundedLocalResponse struct{ buffer bytes.Buffer }

func (w *boundedLocalResponse) Bytes() []byte { return w.buffer.Bytes() }

func (w *boundedLocalResponse) Write(p []byte) (int, error) {
	if w.buffer.Len()+len(p) > 16<<20 {
		return 0, errors.New("local response exceeds 16 MiB; narrow the selection")
	}
	return w.buffer.Write(p)
}

func (p localMCPProject) call(ctx context.Context, action string, options localDAGClientOptions) (json.RawMessage, error) {
	options.Project = p.project
	var nextLaunch time.Time
	for {
		var output boundedLocalResponse
		var diagnostics bytes.Buffer
		stale, err := localDAGOlderProcess(p.root)
		if err != nil {
			return nil, err
		}
		handled, code := false, 0
		if !stale {
			handled, code = localDAGClient(ctx, p.root, action, options, &output, &diagnostics)
		} else {
			// Authenticate the old endpoint before requesting a process handoff.
			// Rejection is an error, never a reason to launch or bypass the owner.
			handled, code = localDAGClient(ctx, p.root, "status", localDAGClientOptions{}, &output, &diagnostics)
			if handled && code == 0 {
				handled = false
			}
		}
		if handled {
			if code != 0 {
				return nil, fmt.Errorf("local Runtime: %s", strings.TrimSpace(diagnostics.String()))
			}
			var value json.RawMessage
			if err := decodeStrict(output.Bytes(), &value); err != nil {
				return nil, errors.New("local Runtime returned an invalid response")
			}
			if content := bytes.TrimSpace(value); len(content) == 0 || content[0] != '{' {
				return nil, errors.New("local Runtime returned an invalid response; expected a JSON object")
			}
			return value, nil
		}
		if !time.Now().Before(nextLaunch) {
			if err := startManagedLocalDAG(p.root); err != nil {
				return nil, err
			}
			nextLaunch = time.Now().Add(time.Second)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.New("local Runtime did not become available before the tool deadline; inspect daemon.log")
		case <-timer.C:
		}
	}
}

type localPullArguments struct {
	NodeID          string           `json:"work_node_id"`
	Source          workgraph.Source `json:"source"`
	IncludeArtifact bool             `json:"include_artifact,omitempty"`
}

func handleLocalMCP(ctx context.Context, params mcpCallParams, project localMCPProject, resolveErr error, response rpcResponse, targetAgent contracts.AgentType) rpcResponse {
	failure := func(status contracts.Status, err error) rpcResponse {
		nextStep := mcpDiagnosticsHelp
		if params.Name == "fork_session" || params.Name == "add_session_to_library" || params.Name == "remove_session_from_library" {
			nextStep += " If the outcome is uncertain, reuse the original request_id and all original arguments; do not create a new request just because a call timed out."
		}
		return mcpFailure(response, status, "local_runtime_error", mcpRuntimeErrorText(err), nextStep)
	}
	schemas, err := mcpInputSchemas()
	if err != nil {
		return mcpFailure(response, contracts.StatusInternalError, "tool_schema_error", "MCP input validation is unavailable.", "Update Coconet and restart the Agent; report the issue if it persists.")
	}
	schema, known := schemas[params.Name]
	if !known {
		response.Error = &rpcError{Code: -32602, Message: "unknown tool; call tools/list and use an advertised tool name"}
		return response
	}
	if issues := schema.validate(params.Arguments); len(issues) > 0 {
		return mcpInvalidArguments(response, params.Name, issues, resolveErr)
	}
	action := ""
	options := localDAGClientOptions{}
	timeout := mcpToolTimeout
	var invalid error
	switch params.Name {
	case "coconet_health":
		action = "status"
		invalid = decodeMCPArguments(params.Arguments, &struct{}{})
	case "read_project_work_graph":
		action = "graph"
		var args localdag.GraphQuery
		invalid = decodeMCPArguments(params.Arguments, &args)
		options.Limit, options.Cursor = args.Limit, args.Cursor
	case "list_library_entries":
		action = "library"
		var args localdag.GraphQuery
		invalid = decodeMCPArguments(params.Arguments, &args)
		options.Limit, options.Cursor = args.Limit, args.Cursor
	case "add_session_to_library", "remove_session_from_library":
		action = "collect"
		if params.Name == "remove_session_from_library" {
			action = "uncollect"
		}
		var args localdag.LibraryRequest
		invalid = decodeMCPArguments(params.Arguments, &args)
		options.Library = &args
	case "search_work":
		action = "search"
		var args localdag.WorkSearch
		invalid = decodeMCPArguments(params.Arguments, &args)
		options.Query, options.Limit, options.Cursor = args.Query, args.Limit, args.Cursor
	case "pull_session":
		action, timeout = "pull", mcpHandoffTimeout
		var args localPullArguments
		invalid = decodeMCPArguments(params.Arguments, &args)

		options.Node, options.Source, options.Native = args.NodeID, &args.Source, args.IncludeArtifact
	case "fork_session":
		if !contracts.ValidAgent(targetAgent) {
			return mcpFailure(response, contracts.StatusUnsupported, "agent_required", "Native Fork requires an identified Codex, Claude Code or Cursor CLI host.", "Run coconet agent list --verbose and follow its registration instructions, then reopen the Agent. Use pull_session to read text without a native Fork.")
		}
		action, timeout = "fork", localNativeForkTimeout
		var args localForkSelection
		invalid = decodeMCPArguments(params.Arguments, &args)
		if invalid == nil {
			cwd, err := os.Getwd()
			if err == nil {
				cwd, err = canonicalPath(cwd)
			}
			profile := ""
			if err == nil {
				profile, err = canonicalPath(libraryConfigRoot(targetAgent))
			}
			if err != nil {
				return failure(contracts.StatusUnavailable, fmt.Errorf("cannot resolve the Agent workspace: %w", err))
			}
			options.Fork = &localNativeForkRequest{localForkSelection: args, ProjectID: project.project, Agent: targetAgent, CWD: cwd, ConfigRoot: profile}
		}
	}
	if invalid != nil {
		field, detail := "arguments", "could not be decoded; use the field types in tools/list"
		var mismatch *json.UnmarshalTypeError
		if errors.As(invalid, &mismatch) {
			field, detail = mismatch.Field, "must be encoded as "+mismatch.Type.String()
		}
		return mcpInvalidArguments(response, params.Name, []mcpArgumentIssue{{field, detail}}, resolveErr)
	}
	if resolveErr != nil {
		if errors.Is(resolveErr, errMCPNotConnected) {
			return mcpFailure(response, contracts.StatusNotConnected, "workspace_not_connected", resolveErr.Error(), mcpConnectHelp)
		}
		return mcpFailure(response, contracts.StatusUnavailable, "local_configuration_error", fmt.Sprintf("local Runtime configuration: %v", resolveErr), mcpDiagnosticsHelp+" Do not delete local state or Agent history to repair configuration.")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	value, err := project.call(ctx, action, options)
	if err != nil {
		if action == "fork" && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded)) {
			return mcpFailure(response, contracts.StatusUnavailable, "fork_unconfirmed", "The local Fork did not confirm completion before this request ended. A Session may already have been created.", "Retry fork_session with the same request_id and all original arguments to recover that Session; do not create a new request ID.")
		}
		return failure(contracts.StatusUnavailable, err)
	}
	message := "Read from the verified local project replica."
	if action == "graph" || action == "search" || action == "library" {
		message = discoveryMessage(message)
	} else if action == "pull" {
		message = "Read the selected fixed source into verified local files. Use compact_path for local reading and search."
	} else if action == "collect" {
		message = "Collected the selected fixed source. The author can keep updating the original node."
	} else if action == "uncollect" {
		message = "Removed the selected entry from Session Library. The author’s current work is unchanged."
	} else if action == "fork" {
		message = "Created a separate native Session locally from the fixed source without changing workspace files. It is ready to resume; project synchronization runs in the background. Return the resume command; continue there only on the user's request."
	} else {
		message = "Current project status on this device. Background issues do not establish whether a selected source is available."
	}
	structured := map[string]any{"status": contracts.StatusOK, "message": message}
	if action == "status" {
		health, err := mcpProjectHealth(value)
		if err != nil {
			return failure(contracts.StatusUnavailable, err)
		}
		structured["runtime"] = health
	} else if err := json.Unmarshal(value, &structured); err != nil {
		return failure(contracts.StatusIntegrityError, errors.New("invalid local result"))
	}
	response.Result = mcpToolResult(message, structured, false)
	return response
}

// The local protocol uses a source object with Session identity; it does not
// expose or invent an old Authority server_session_id. Only connected tools are
// advertised during the staged product migration.
func localMCPTools(agent contracts.AgentType) []any {
	empty := map[string]any{"type": "object", "additionalProperties": false}
	// These descriptions reach the model alongside machine-readable bounds.
	cursor := map[string]any{"type": "string", "maxLength": 1024, "description": "Copy next_cursor from the previous response (at most 1024 characters). Omit it for the first page. Keep the same tool and query; restart without a cursor if the selected results change."}
	nodeID := map[string]any{"type": "string", "minLength": 1, "description": "Exact work node ID from read_project_work_graph or list_library_entries. Copy its source together with it; do not invent an ID."}
	requestID := map[string]any{"type": "string", "minLength": 8, "maxLength": 200, "description": "Stable unique ID, 8–200 characters. Reuse this ID and all original arguments after timeout or an uncertain result. A changed selection or new operation requires a new ID."}
	page := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50, "description": "Page size: integer from 1 to 100; omit for 50. Use next_cursor for more results."}, "cursor": cursor,
	}}
	source := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"session_id", "version"}, "description": "Exact retained source copied from graph, Library or search results. Do not substitute a newer version.", "properties": map[string]any{
		"session_id": map[string]any{"type": "string", "minLength": 1, "description": "Nonempty Coconet session_id from the selected source, not a native Agent session ID."},
		"version":    map[string]any{"type": "integer", "minimum": 1, "description": "Exact positive integer version from the selected source. Refresh the selection if it is no longer available."},
	}}
	tool := func(name string, schema map[string]any, readOnly bool) any {
		return map[string]any{"name": name, "description": productprompts.MCPTool("local_" + name).Description, "inputSchema": schema,
			"annotations": map[string]any{"readOnlyHint": readOnly}}
	}
	result := []any{
		tool("coconet_health", empty, true),
		tool("read_project_work_graph", page, true),
		tool("list_library_entries", page, true),
		tool("add_session_to_library", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"request_id", "work_node_id", "source"}, "properties": map[string]any{
			"request_id": requestID, "work_node_id": nodeID, "source": source, "note": map[string]any{"type": "string", "maxLength": 1000, "description": "Optional Library note, at most 1000 characters. Keep it unchanged when retrying the same request_id."},
		}}, false),
		tool("remove_session_from_library", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"request_id", "work_node_id", "source"}, "properties": map[string]any{
			"request_id": requestID, "work_node_id": nodeID, "source": source,
		}}, false),
		tool("fork_session", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"request_id", "source"}, "properties": map[string]any{
			"request_id": requestID, "work_node_id": nodeID, "source": source,
		}}, false),
		tool("search_work", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":  map[string]any{"type": "string", "minLength": 1, "maxLength": 4000, "description": "Nonblank search text: 1–4000 characters and at most 64 whitespace-separated literal terms. Matches any term; not a regex or SQL query."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 10, "description": "Page size: integer from 1 to 100; omit for 10. Use next_cursor for more results."},
			"cursor": cursor,
		}}, true),
		tool("pull_session", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"source"}, "properties": map[string]any{
			"work_node_id": nodeID, "source": source,
			"include_artifact": map[string]any{"type": "boolean", "default": false, "description": "Default false: read compact conversation text. True also retrieves the native artifact of that exact source, when available."},
		}}, false),
	}
	if !contracts.ValidAgent(agent) {
		filtered := []any{}
		for _, item := range result {
			if item.(map[string]any)["name"] != "fork_session" {
				filtered = append(filtered, item)
			}
		}
		return filtered
	}
	return result
}
