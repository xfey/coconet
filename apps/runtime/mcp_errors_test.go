package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

type mcpFailureView struct {
	Status           string             `json:"status"`
	Code             string             `json:"code"`
	NextStep         string             `json:"next_step"`
	ConnectionStatus string             `json:"connection_status"`
	Issues           []mcpArgumentIssue `json:"issues"`
}

func TestMCPArgumentFailuresIdentifyFieldsAndGuideRecovery(t *testing.T) {
	root := t.TempDir()
	managedMCPConfig(t, root, nil)
	chdirMCPTest(t, t.TempDir())
	tests := []struct{ tool, arguments, field, detail string }{
		{"search_work", `{"query":"CPT 数据生成","limit":101}`, "limit", "1 to 100"},
		{"search_work", `{"query":"ok","limit":0}`, "limit", "1 to 100"},
		{"search_work", `{"query":"ok","limit":-1}`, "limit", "1 to 100"},
		{"search_work", `{"query":"ok","limit":1.5}`, "limit", "integer"},
		{"search_work", `{"query":"ok","limit":"20"}`, "limit", "integer"},
		{"search_work", `{"query":"ok","limit":null}`, "limit", "integer"},
		{"search_work", `{"query":"   "}`, "query", "whitespace"},
		{"search_work", `{}`, "query", "required"},
		{"search_work", `{"query":42}`, "query", "string"},
		{"search_work", fmt.Sprintf(`{"query":%q}`, strings.Repeat("中", 4001)), "query", "4000"},
		{"search_work", fmt.Sprintf(`{"query":%q}`, strings.Repeat("term ", 65)), "query", "64"},
		{"search_work", fmt.Sprintf(`{"query":"ok","cursor":%q}`, strings.Repeat("a", 1025)), "cursor", "1024"},
		{"search_work", `{"query":"ok","project_id":"elsewhere"}`, "project_id", "not supported"},
		{"read_project_work_graph", `{"limit":101}`, "limit", "1 to 100"},
		{"list_library_entries", `{"limit":0}`, "limit", "1 to 100"},
		{"pull_session", `{"source":null}`, "source", "object"},
		{"pull_session", `{"source":{"session_id":"session"}}`, "source.version", "required"},
		{"pull_session", `{"source":{"session_id":" ","version":1}}`, "source.session_id", "whitespace"},
		{"pull_session", `{"source":{"session_id":"session","version":0}}`, "source.version", "positive integer"},
		{"pull_session", `{"source":{"session_id":"session","version":1,"other":true}}`, "source.other", "not supported"},
		{"add_session_to_library", `{"request_id":"short","work_node_id":"node","source":{"session_id":"s","version":1}}`, "request_id", "8–200"},
		{"add_session_to_library", fmt.Sprintf(`{"request_id":"request-1","work_node_id":"node","source":{"session_id":"s","version":1},"note":%q}`, strings.Repeat("中", 1001)), "note", "1000"},
		{"remove_session_from_library", `{"request_id":"request-1","work_node_id":"node","source":{"session_id":"s","version":1},"note":""}`, "note", "not supported"},
		{"fork_session", `{"request_id":"request-1","work_node_id":" ","source":{"session_id":"s","version":1}}`, "work_node_id", "whitespace"},
		{"coconet_health", `{"verbose":true}`, "verbose", "not supported"},
		{"coconet_health", `null`, "arguments", "object"},
		{"coconet_health", `[]`, "arguments", "object"},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprintf("%s/%d", tc.tool, i), func(t *testing.T) {
			response := decodeMCPToolCall[mcpFailureView](t, localMCPCall(t, tc.tool, json.RawMessage(tc.arguments)))
			if !response.IsError || response.Structured.Status != "invalid_request" || response.Structured.Code != "invalid_arguments" || response.Structured.NextStep == "" {
				t.Fatalf("missing actionable tool failure: %+v", response)
			}
			found := false
			for _, issue := range response.Structured.Issues {
				if issue.Field == tc.field && strings.Contains(issue.Message, tc.detail) {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %s / %s, got %+v", tc.field, tc.detail, response.Structured.Issues)
			}
			if response.Structured.ConnectionStatus != "not_connected" || !strings.Contains(response.Message, "coconet connect") {
				t.Fatal("argument error hid disconnected workspace", response)
			}
		})
	}
	for _, path := range []string{"control.json", "launch.json", "graph.db", "daemon.log"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatal("invalid/unconnected call started Runtime or wrote state", path)
		}
	}
}

func TestMCPValidInputSeparatesDisconnectedWorkspaceFromParameterErrors(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	managedMCPConfig(t, t.TempDir(), nil)
	chdirMCPTest(t, t.TempDir())
	for name, args := range map[string]any{
		"coconet_health":              map[string]any{},
		"search_work":                 map[string]any{"query": "CPT 数据生成", "limit": 100},
		"read_project_work_graph":     map[string]any{"limit": 100},
		"list_library_entries":        map[string]any{},
		"pull_session":                map[string]any{"source": map[string]any{"session_id": "session", "version": 1}},
		"add_session_to_library":      map[string]any{"request_id": "request-1", "work_node_id": "node", "source": map[string]any{"session_id": "session", "version": 1}, "note": strings.Repeat("中", 1000)},
		"remove_session_from_library": map[string]any{"request_id": "request-1", "work_node_id": "node", "source": map[string]any{"session_id": "session", "version": 1}},
		"fork_session":                map[string]any{"request_id": "request-1", "work_node_id": "node", "source": map[string]any{"session_id": "session", "version": 1}},
	} {
		result := decodeMCPToolCall[mcpFailureView](t, localMCPCall(t, name, args))
		if !result.IsError || result.Structured.Status != "not_connected" || result.Structured.Code != "workspace_not_connected" || !strings.Contains(result.Message, "coconet connect") {
			t.Fatalf("%s: connection problem confused with input or inference: %+v", name, result)
		}
	}
}

func TestMCPAdvertisedFieldsExplainConstraints(t *testing.T) {
	var inspect func(map[string]any, string)
	inspect = func(shape map[string]any, path string) {
		properties, _ := shape["properties"].(map[string]any)
		for name, raw := range properties {
			field := raw.(map[string]any)
			if text, _ := field["description"].(string); strings.TrimSpace(text) == "" {
				t.Errorf("%s.%s has no model-visible parameter guidance", path, name)
			}
			inspect(field, path+"."+name)
		}
	}
	for _, raw := range localMCPTools(contracts.AgentCodex) {
		tool := raw.(map[string]any)
		inspect(tool["inputSchema"].(map[string]any), tool["name"].(string))
	}
}

func TestMCPRuntimeFailuresRemainActionableAndDoNotBecomeEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		body, expected string
	}{
		{"auth", 401, "", "local daemon authorization failed"},
		{"server", 503, "", "HTTP 503"},
		{"invalid-json", 200, "broken", "invalid response"},
		{"null-json", 200, "null", "JSON object"},
		{"operation", 400, "cursor is invalid; restart without a cursor", "restart without a cursor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, workspace := t.TempDir(), t.TempDir()
			managedMCPConfig(t, root, []localDAGWorkspace{{Path: workspace, ProjectID: "project"}})
			chdirMCPTest(t, workspace)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/status" {
					if tc.name == "auth" || tc.name == "server" {
						w.WriteHeader(tc.status)
					}
					fmt.Fprint(w, `{}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			if err := atomicWriteJSON(filepath.Join(root, "control.json"), localDAGEndpoint{Version: version, URL: server.URL, Token: strings.Repeat("a", 64)}, 0600); err != nil {
				t.Fatal(err)
			}
			result := decodeMCPToolCall[mcpFailureView](t, localMCPCall(t, "search_work", map[string]any{"query": "CPT 数据生成"}))
			if !result.IsError || !strings.Contains(result.Message, tc.expected) || !strings.Contains(result.Structured.NextStep, "coconet diagnostics list") {
				t.Fatalf("misleading failure: %+v", result)
			}
			if tc.name == "server" && strings.Contains(result.Message, "authorization") {
				t.Fatal("server failure mislabeled as login failure")
			}
		})
	}
}

func TestMCPMalformedCallsKeepProtocolErrorAndRecovery(t *testing.T) {
	for _, params := range []string{`null`, `[]`, `{}`, `{"name":42}`, `{"name":"no_such_tool","arguments":{}}`} {
		result := handleMCPRequest(context.Background(), rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(params)})
		if result.Error == nil || result.Error.Code != -32602 || !strings.Contains(result.Error.Message, "tools/list") {
			t.Fatalf("malformed call lacked protocol guidance: %+v", result)
		}
	}
}
