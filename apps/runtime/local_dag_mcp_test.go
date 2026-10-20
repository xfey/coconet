package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestManagedSessionStartExplainsLocalToolsWithoutReadingOrStartingServices(t *testing.T) {
	root, workspace, profile := t.TempDir(), t.TempDir(), t.TempDir()
	managedMCPConfig(t, root, []localDAGWorkspace{{Path: workspace, ProjectID: "project"}})
	t.Setenv("CODEX_HOME", profile)
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	skill := filepath.Join(t.TempDir(), "skills", "coconet-team-context", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("private skill body"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"codex", "claude"} {
		data, _ := json.Marshal(hookEvent{HookEventName: "SessionStart", SessionID: "startup-native", CWD: workspace})
		var output, diagnostics bytes.Buffer
		if code := runHook([]string{"--agent", agent, "--skill-path", skill}, bytes.NewReader(data), &output, &diagnostics); code != 0 || diagnostics.Len() != 0 {
			t.Fatal("startup hook failed")
		}
		var result struct {
			System string `json:"systemMessage"`
			Hook   struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.System != "" || result.Hook.Event != "SessionStart" || !strings.Contains(result.Hook.Context, "local Runtime") || !strings.Contains(result.Hook.Context, "compact_path") || strings.Contains(result.Hook.Context, "current_session=") {
			t.Fatal("incorrect local workflow guidance", result)
		}
		if strings.Contains(result.Hook.Context, "coconet_team_context_skill_path=") != (agent == "codex") || strings.Contains(result.Hook.Context, "private skill body") || strings.Contains(result.Hook.Context, "startup-native") {
			t.Fatal("startup leaked history/identity or lost optional Skill path")
		}
	}
	for _, file := range []string{"graph.db", "daemon.log", "control.json", "launch.json", "inbox"} {
		if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
			t.Fatal("startup did unexpected work", file)
		}
	}
	t.Setenv("COCONET_INTERNAL_INFERENCE", "1")
	data, _ := json.Marshal(hookEvent{HookEventName: "SessionStart", SessionID: "internal-native", CWD: workspace})
	var output bytes.Buffer
	if code := runHook([]string{"--agent", "codex"}, bytes.NewReader(data), &output, &output); code != 0 || output.Len() != 0 {
		t.Fatal("internal inference received collaboration context")
	}
}

func managedMCPConfig(t *testing.T, root string, workspaces []localDAGWorkspace) {
	t.Helper()
	t.Setenv("COCONET_LOCAL_DAG_HOME", root)
	config := localDAGServiceConfig{Schema: 1, Actor: "reader", Provider: "codex", Command: "unused", Profile: filepath.Join(root, "inference"), Model: "unused", MaxCalls: 1, TimeoutSeconds: 120, Workspaces: workspaces}
	if err := atomicWriteJSON(filepath.Join(root, "runtime.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
}

func localMCPCall(t *testing.T, name string, args any) rpcResponse {
	t.Helper()
	data, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	return handleMCPRequest(context.Background(), rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: data}, contracts.AgentCodex)
}

func TestManagedMCPUsesLocalDaemonWithoutLegacyBindingOrTranscriptDiscovery(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	managedMCPConfig(t, root, []localDAGWorkspace{{Path: workspace, ProjectID: "project"}})
	chdirMCPTest(t, workspace)
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: "writer"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture := localdag.Capture{ProjectID: "project", NativeID: "private-native-id", Agent: "codex", Artifact: []byte("native original"),
		Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0, Role: "user", Kind: "message", Text: "conversation-only-marker"}}}}
	source, err := engine.Capture(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	at := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.Apply(workgraph.Commit{Schema: 1, ID: "stage-commit", ProjectID: "project", Actor: "writer", RunID: "fixture-stage", Base: state.Revision, At: at,
		Stage: &workgraph.StageDecision{Source: source, Action: "start", NewNodeID: "node", Text: &workgraph.Text{Objective: "完善重试提示", Summary: "Retry failures are bounded."}, Reason: "fixture"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated capture faults, including one in this project, must not block
	// accepted graph/search/read operations or leak raw diagnostics into MCP.
	for _, project := range []string{"project", "other"} {
		event := localDAGInboxEvent{Schema: 1, ID: "paused-" + project,
			Request:  localDAGObservationRequest{ProjectID: project, Agent: contracts.AgentClaude, NativeID: "unrelated-native"},
			Attempts: 8, Paused: true, Error: "PRIVATE_CAPTURE_STACK_" + project}
		if err := atomicWriteJSON(filepath.Join(root, "inbox", event.ID+".json"), event, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Replica reader owns no inference work. Nil Runner makes accidental calls
	// fail; no provider or network credentials are installed by this test.
	engine.Actor = "reader"
	done := make(chan error, 1)
	go func() { done <- serveLocalDAG(ctx, root, engine, 1, io.Discard) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "control.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	listed, _ := json.Marshal(mcpTools(contracts.AgentCodex))
	if strings.Contains(string(listed), "server_session_id") || !strings.Contains(string(listed), `"name":"fork_session"`) || !strings.Contains(string(listed), `"name":"pull_session"`) {
		t.Fatal("local tools advertised legacy identity or disconnected features", string(listed))
	}
	graph := decodeMCPToolCall[localdag.GraphView](t, localMCPCall(t, "read_project_work_graph", map[string]any{}))
	if graph.IsError || graph.Structured.ProjectID != "project" || len(graph.Structured.Nodes) != 1 || graph.Structured.Coverage.RelationsPending != 1 {
		t.Fatalf("graph did not come from local replica: %+v", graph)
	}
	serialized, _ := json.Marshal(graph.Structured)
	if strings.Contains(graph.Message, "server_session_id") || !strings.Contains(graph.Message, "fixed source") {
		t.Fatal("local discovery guidance contradicts the advertised source schema")
	}
	if strings.Contains(string(serialized), "private-native-id") || strings.Contains(string(serialized), "conversation-only-marker") {
		t.Fatal("discovery exposed native data")
	}
	search := decodeMCPToolCall[localdag.SearchView](t, localMCPCall(t, "search_work", map[string]any{"query": "RETRY"}))
	if search.IsError || len(search.Structured.Nodes) != 1 {
		t.Fatalf("search failed: %+v", search)
	}
	tooLarge := decodeMCPToolCall[mcpFailureView](t, localMCPCall(t, "search_work", map[string]any{"query": "RETRY", "limit": 101}))
	if !tooLarge.IsError || len(tooLarge.Structured.Issues) != 1 || tooLarge.Structured.Issues[0].Field != "limit" || tooLarge.Structured.ConnectionStatus != "" {
		t.Fatal("connected search failure misidentified cause", tooLarge)
	}
	corrected := decodeMCPToolCall[localdag.SearchView](t, localMCPCall(t, "search_work", map[string]any{"query": "RETRY", "limit": 100}))
	if corrected.IsError || len(corrected.Structured.Nodes) != 1 {
		t.Fatal("search did not recover after correcting limit", corrected)
	}
	badCursor := decodeMCPToolCall[mcpFailureView](t, localMCPCall(t, "search_work", map[string]any{"query": "RETRY", "cursor": "not-a-cursor"}))
	if !badCursor.IsError || !strings.Contains(badCursor.Message, "restart without a cursor") {
		t.Fatal("invalid cursor did not explain how to restart", badCursor)
	}
	empty := decodeMCPToolCall[localdag.SearchView](t, localMCPCall(t, "search_work", map[string]any{"query": "conversation-only-marker"}))
	if empty.IsError || len(empty.Structured.Nodes) != 0 {
		t.Fatal("search included conversation content")
	}
	if _, err := os.Stat(filepath.Join(root, "materialized")); !os.IsNotExist(err) {
		t.Fatal("discovery materialized a conversation")
	}
	args := localPullArguments{NodeID: "node", Source: source}
	pull := decodeMCPToolCall[localdag.NodeRead](t, localMCPCall(t, "pull_session", args))
	if pull.IsError || pull.Structured.Source != source || pull.Structured.NativePath != "" {
		t.Fatalf("compact pull failed: %+v", pull)
	}
	content, err := os.ReadFile(pull.Structured.CompactPath)
	if err != nil || !strings.Contains(string(content), "conversation-only-marker") {
		t.Fatal("compact result unavailable", err)
	}
	args.IncludeArtifact = true
	native := decodeMCPToolCall[localdag.NodeRead](t, localMCPCall(t, "pull_session", args))
	content, err = os.ReadFile(native.Structured.NativePath)
	if native.IsError || err != nil || string(content) != "native original" {
		t.Fatal("native result unavailable", err)
	}
	args.Source.Version++
	stale := decodeMCPToolCall[map[string]any](t, localMCPCall(t, "pull_session", args))
	if !stale.IsError || !strings.Contains(stale.Message, "refresh the project graph") || !strings.Contains(stale.Message, "Do not silently use another version") {
		t.Fatal("changed-source read was not explained")
	}
	health := decodeMCPToolCall[struct {
		Runtime struct {
			Status   string `json:"status"`
			Mode     string `json:"sync_mode"`
			Attempts int    `json:"attempts"`
			Project  struct {
				Capture struct {
					Capture struct {
						Paused int `json:"paused"`
					} `json:"capture"`
				} `json:"background"`
			} `json:"project"`
		} `json:"runtime"`
	}](t, localMCPCall(t, "coconet_health", map[string]any{}))
	if health.IsError || health.Structured.Runtime.Status != "running" || health.Structured.Runtime.Mode != "offline" || health.Structured.Runtime.Attempts != 0 || health.Structured.Runtime.Project.Capture.Capture.Paused != 1 {
		t.Fatalf("health conflates connectivity and processing: %+v", health)
	}
	healthJSON, _ := json.Marshal(health)
	if strings.Contains(string(healthJSON), "PRIVATE_CAPTURE_STACK") || strings.Contains(string(healthJSON), "unrelated-native") {
		t.Fatal("background diagnostics leaked", string(healthJSON))
	}
	// Ordinary commands share the same local route and fixed-source checks;
	// no legacy binding or remote API is available in this fixture.
	for _, command := range [][]string{
		{"health", "--json"}, {"projection", "status", "--json"},
		{"session", "pull", "--project", "project", "--node", "node", "--session", source.SessionID, "--version", "1"},
		{"session", "read", "--project", "project", "--node", "node", "--session", source.SessionID, "--version", "1"},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(ctx, command, nil, &out, &diagnostics); code != 0 {
			t.Fatal("ordinary local command failed", command[0], diagnostics.String())
		}
		if command[0] == "session" && command[1] == "read" {
			if !strings.Contains(out.String(), "conversation-only-marker") {
				t.Fatal("CLI did not return the fixed compact conversation")
			}
		} else if strings.Contains(out.String(), "conversation-only-marker") || strings.Contains(out.String(), "native original") {
			t.Fatal("status or path response leaked body")
		}
	}
	var staleOutput bytes.Buffer
	if code := runSessionCommand(ctx, []string{"pull", "--project", "project", "--node", "node", "--session", source.SessionID, "--version", "2"}, &staleOutput, &staleOutput); code == 0 || !strings.Contains(staleOutput.String(), "refresh the project graph") {
		t.Fatal("CLI silently followed a newer source", staleOutput.String())
	}
	own, err := engine.Capture(ctx, localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "own-paused"})
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := store.Tasks("project")
	retryID := ""
	for _, task := range tasks {
		if task.Source != nil && *task.Source == own && task.Kind == "stage" {
			task.Status, task.Error = "paused", "synthetic auth failure"
			if err := store.PutTask(task); err != nil {
				t.Fatal(err)
			}
			retryID = task.ID
		}
	}
	if retryID == "" {
		t.Fatal("own paused task not created")
	}
	var retryOutput bytes.Buffer
	if code := runProjectionCommand(ctx, []string{"retry", "--task", retryID, "--json"}, &retryOutput, &retryOutput); code != 0 || !strings.Contains(retryOutput.String(), `"status":"queued"`) {
		t.Fatal("ordinary retry not routed to durable local task", retryOutput.String())
	}
	for name, args := range map[string]any{
		"read_project_work_graph": map[string]any{"project_id": "other"},
		"search_work":             map[string]any{"paths": []string{"src"}},
		"pull_session":            map[string]any{"work_node_id": "node", "server_session_id": source.SessionID, "version": source.Version},
	} {
		if response := decodeMCPToolCall[map[string]any](t, localMCPCall(t, name, args)); !response.IsError || response.Structured["status"] != "invalid_request" {
			t.Fatal("invalid local arguments accepted", name)
		}
	}
}

func TestManagedMCPResolvesNestedWorkspacesAndFailsClosed(t *testing.T) {
	root, outer := t.TempDir(), t.TempDir()
	inner := filepath.Join(outer, "nested")
	if err := os.MkdirAll(filepath.Join(inner, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	managedMCPConfig(t, root, []localDAGWorkspace{{Path: outer, ProjectID: "outer"}, {Path: inner, ProjectID: "inner"}})
	chdirMCPTest(t, filepath.Join(inner, "src"))
	p, managed, err := resolveLocalMCPProject()
	if err != nil || !managed || p.project != "inner" {
		t.Fatal("nested workspace selection failed", err)
	}
	if response := decodeMCPToolCall[map[string]any](t, localMCPCall(t, "fork_session", map[string]any{})); !response.IsError || response.Structured["status"] != "invalid_request" {
		t.Fatal("invalid native Fork selection did not fail locally")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unauthorized", 401) }))
	defer server.Close()
	if err := atomicWriteJSON(filepath.Join(root, "control.json"), localDAGEndpoint{URL: server.URL, Token: strings.Repeat("a", 64)}, 0600); err != nil {
		t.Fatal(err)
	}
	rejected := decodeMCPToolCall[map[string]any](t, localMCPCall(t, "read_project_work_graph", map[string]any{}))
	if !rejected.IsError || !strings.Contains(rejected.Message, "local daemon authorization failed") {
		t.Fatal("IPC rejection fell back to old workflow")
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), []byte(`{"invalid":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	broken := decodeMCPToolCall[map[string]any](t, localMCPCall(t, "read_project_work_graph", map[string]any{}))
	if !broken.IsError || !strings.Contains(broken.Message, "local Runtime configuration") {
		t.Fatal("broken configuration fell back to old workflow")
	}
	for _, command := range [][]string{{"health"}, {"projection", "status"}, {"session", "pull", "--project", "inner", "--node", "node", "--session", "source", "--version", "1"}} {
		var output bytes.Buffer
		if code := run(context.Background(), command, nil, &output, &output); code == 0 || strings.Contains(output.String(), "legacy") {
			t.Fatal("ordinary command did not fail at local configuration", command, output.String())
		}
	}
}
