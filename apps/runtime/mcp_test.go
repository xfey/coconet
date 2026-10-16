package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	productprompts "github.com/xfey/coconet/prompts"
)

func TestMCPListsContextToolsAndRejectsProjectOverride(t *testing.T) {
	listed := handleMCPRequest(context.Background(), rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/list"})
	content, err := json.Marshal(listed.Result)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"coconet_health", "read_project_work_graph", "search_work", "list_library_entries", "pull_session"} {
		if !strings.Contains(string(content), `"name":"`+name+`"`) {
			t.Fatalf("tools/list omitted %s: %s", name, content)
		}
	}
	targeted := handleMCPRequest(context.Background(), rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("targeted"), Method: "tools/list"}, contracts.AgentCodex)
	targetedContent, err := json.Marshal(targeted.Result)
	if err != nil || !strings.Contains(string(targetedContent), `"name":"fork_session"`) {
		t.Fatalf("Agent-scoped tools/list omitted fork_session: %s %v", targetedContent, err)
	}
	if strings.Contains(string(content), `"name":"fork_session"`) {
		t.Fatalf("unscoped MCP unexpectedly advertised fork_session: %s", content)
	}
	if strings.Contains(string(targetedContent), `"name":"prepare_resume"`) {
		t.Fatalf("Agent-scoped MCP still advertised removed prepare_resume: %s", targetedContent)
	}
	override := handleMCPRequest(context.Background(), rpcRequest{
		JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"search_sessions","arguments":{"query":"context","project_id":"coco_untrusted01"}}`),
	})
	if override.Error == nil || override.Error.Code != -32602 {
		t.Fatalf("MCP accepted a caller-selected project_id: %#v", override)
	}
	for _, legacyField := range []string{"symbols", "error_tokens"} {
		legacy := handleMCPRequest(context.Background(), rpcRequest{
			JSONRPC: "2.0", ID: json.RawMessage("3"), Method: "tools/call",
			Params: json.RawMessage(`{"name":"search_sessions","arguments":{"query":"context","` + legacyField + `":["LegacySignal"]}}`),
		})
		if legacy.Error == nil || legacy.Error.Code != -32602 {
			t.Fatalf("MCP accepted removed %s input: %#v", legacyField, legacy)
		}
	}
}

func TestPullSessionToolDescriptionHasNoForkSemantics(t *testing.T) {
	found := false
	for _, raw := range mcpTools() {
		tool, ok := raw.(map[string]any)
		if !ok || tool["name"] != "pull_session" {
			continue
		}
		found = true
		description, _ := tool["description"].(string)
		lower := strings.ToLower(description)
		if description == "" || strings.Contains(lower, "fork") || strings.Contains(lower, "resume") {
			t.Fatalf("pull_session description crossed into Fork semantics: %q", description)
		}
	}
	if !found {
		t.Fatal("pull_session tool was not published")
	}
}

func TestMCPToolDescriptionsDescribeFunctionsAccurately(t *testing.T) {
	for _, raw := range mcpTools(contracts.AgentCodex) {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		if tool["description"] != productprompts.MCPTool("local_"+name).Description {
			t.Fatalf("tool %s did not publish its canonical description", name)
		}
	}
}

type decodedMCPToolCall[T any] struct {
	Message    string
	Structured T
	IsError    bool
}

func chdirMCPTest(t *testing.T, directory string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func decodeMCPToolCall[T any](t *testing.T, response rpcResponse) decodedMCPToolCall[T] {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("unexpected RPC error: %#v", response.Error)
	}
	content, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(content, &wire); err != nil {
		t.Fatal(err)
	}
	// Simulate a host that forwards text blocks and discards structuredContent.
	// Every tool exercised through this helper must preserve its actual data,
	// including empty results, failures, exact sources and downloaded paths.
	var textData any
	for _, block := range wire.Content {
		var value any
		if json.Unmarshal([]byte(block.Text), &value) == nil {
			textData = value
		}
	}
	var structuredData any
	if err := json.Unmarshal(wire.Structured, &structuredData); err != nil {
		t.Fatal(err)
	}
	textJSON, _ := json.Marshal(textData)
	structuredJSON, _ := json.Marshal(structuredData)
	if !bytes.Equal(textJSON, structuredJSON) {
		t.Fatal("a text-only MCP host lost tool data")
	}
	var structured T
	if err := json.Unmarshal(wire.Structured, &structured); err != nil {
		t.Fatal(err)
	}
	message := ""
	if len(wire.Content) > 0 {
		message = wire.Content[0].Text
	}
	return decodedMCPToolCall[T]{Message: message, Structured: structured, IsError: wire.IsError}
}

type mcpFixtureServer struct {
	server            *httptest.Server
	mutex             sync.Mutex
	projectID         string
	forbidden         bool
	empty             bool
	search            contracts.SearchSessionsRequest
	graph             contracts.ReadProjectWorkGraphRequest
	recent            contracts.ListRecentSessionsRequest
	read              contracts.ReadSessionRequest
	searchRequestPath string
	graphRequestPath  string
	recentRequestPath string
}

func (fixture *mcpFixtureServer) serveHTTP(response http.ResponseWriter, request *http.Request) {
	if serveFixtureDeployment(response, request) {
		return
	}
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	response.Header().Set("Content-Type", "application/json")
	if fixture.forbidden {
		response.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(response).Encode(contracts.ErrorEnvelope{
			SchemaVersion: 1, Status: contracts.StatusForbidden, RequestID: "request-mcp-forbidden",
			Code: "membership_required", Message: "Project membership is required", Retryable: false,
		})
		return
	}
	switch {
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/work-graph"):
		fixture.graph = contracts.ReadProjectWorkGraphRequest{}
		_ = json.NewDecoder(request.Body).Decode(&fixture.graph)
		fixture.graphRequestPath = request.URL.Path
		_ = json.NewEncoder(response).Encode(contracts.ReadProjectWorkGraphResponse{
			SchemaVersion: 1, Status: contracts.StatusOK, RequestID: fixture.graph.RequestID,
			SessionWork: []contracts.SessionWorkState{{Source: contracts.SessionReadReference{ServerSessionID: "sess_mcpfixture01", Version: 4}, Author: contracts.SessionAuthor{AccountID: "acct_author01", DisplayName: "Teammate"}, OpenItems: []contracts.WorkOpenItem{{ID: "item_mcpfixture01", Text: "Review examples", SourceVersion: 4}}}},
			Nodes: []contracts.ProjectWorkNode{{
				WorkNodeID: "work_mcpfixture01", Objective: "Implement Work Graph", Summary: "The compact graph reader is implemented.", Approach: "Project metadata avoids loading raw conversations.",
				State: contracts.WorkNodeCurrent, Author: contracts.SessionAuthor{AccountID: "acct_author01", DisplayName: "Teammate"},
				WorkTime: contracts.WorkTimeRange{Start: "2026-08-24T09:00:00Z", End: "2026-08-24T10:00:00Z"}, Parents: []string{},
				Source: contracts.SessionReadReference{ServerSessionID: "sess_mcpfixture01", Version: 3},
			}},
		})
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/sessions/recent"):
		fixture.recent = contracts.ListRecentSessionsRequest{}
		_ = json.NewDecoder(request.Body).Decode(&fixture.recent)
		fixture.recentRequestPath = request.URL.Path
		results := []contracts.RecentSessionResult{}
		if !fixture.empty {
			results = append(results, contracts.RecentSessionResult{
				ServerSessionID: "sess_mcpfixture01", AgentType: contracts.AgentClaude,
				Author:        contracts.SessionAuthor{AccountID: "acct_author01", DisplayName: "Teammate"},
				ActivityState: contracts.ActivityActive, LatestSyncedAt: "2026-08-24T10:00:00Z", LatestVersion: 4,
				LatestExcerpt: &contracts.RecentSessionExcerpt{TurnID: "turn_mcpfixture01", Ordinal: 1, Role: "assistant", Kind: "message", Text: "handler context"},
				RecentPaths:   []string{"apps/server/handler.go"},
				Lineage:       contracts.SessionLineage{RootServerSessionID: "sess_mcpfixture01", FamilyResultCount: 1},
				ReadRef:       contracts.SessionReadReference{ServerSessionID: "sess_mcpfixture01", Version: 3},
			})
		}
		_ = json.NewEncoder(response).Encode(contracts.ListRecentSessionsResponse{
			SchemaVersion: 1, Status: contracts.StatusOK, RequestID: fixture.recent.RequestID,
			Project: &contracts.ProjectReference{ProjectID: fixture.projectID, Name: "MCP Project", Status: contracts.ProjectActive}, Results: results,
		})
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/sessions/search"):
		fixture.search = contracts.SearchSessionsRequest{}
		_ = json.NewDecoder(request.Body).Decode(&fixture.search)
		fixture.searchRequestPath = request.URL.Path
		results := []contracts.SearchSessionResult{}
		if !fixture.empty {
			results = append(results, contracts.SearchSessionResult{
				ServerSessionID: "sess_mcpfixture01", AgentType: contracts.AgentClaude,
				Author:        contracts.SessionAuthor{AccountID: "acct_author01", DisplayName: "Teammate"},
				ActivityState: contracts.ActivityActive, LatestSyncedAt: "2026-08-24T10:00:00Z",
				HitVersion: 3, LatestVersion: 4,
				Lineage: contracts.SessionLineage{RootServerSessionID: "sess_mcpfixture01"},
				Matches: []contracts.SearchMatch{{TurnID: "turn_mcpfixture01", Ordinal: 1, Excerpt: "handler context", Reasons: []string{"text"}}},
				ReadRef: contracts.SessionReadReference{ServerSessionID: "sess_mcpfixture01", Version: 3},
			})
		}
		_ = json.NewEncoder(response).Encode(contracts.SearchSessionsResponse{
			SchemaVersion: 1, Status: contracts.StatusOK, RequestID: fixture.search.RequestID,
			Project:             &contracts.ProjectReference{ProjectID: fixture.projectID, Name: "MCP Project", Status: contracts.ProjectActive},
			QueryInterpretation: contracts.SearchQueryInterpretation{Query: fixture.search.Query, Paths: fixture.search.Paths}, Results: results,
		})
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/sessions/sess_mcpfixture01/read"):
		fixture.read = contracts.ReadSessionRequest{}
		_ = json.NewDecoder(request.Body).Decode(&fixture.read)
		_ = json.NewEncoder(response).Encode(contracts.ReadSessionResponse{
			SchemaVersion: 1, Status: contracts.StatusOK, RequestID: fixture.read.RequestID,
			ServerSessionID: "sess_mcpfixture01", AgentType: contracts.AgentClaude, ProjectStatus: contracts.ProjectActive,
			Version: fixture.read.Version, LatestVersion: 4, ActivityState: contracts.ActivityActive,
			Turns: []contracts.ReadSessionTurn{{TurnID: "turn_mcpfixture01", Ordinal: 1, Role: "assistant", Kind: "message", Text: "handler context", TextComplete: true, ContentHash: strings.Repeat("a", 64)}},
		})
	default:
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(contracts.ErrorEnvelope{SchemaVersion: 1, Status: contracts.StatusNotFound, RequestID: "request-not-found", Code: "not_found", Message: "not found"})
	}
}
