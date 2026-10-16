package contracts

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/v1/*.json testdata/*.json openapi/v1/openapi.json
var contractFiles embed.FS

func TestContractFixtures(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{
		"common.schema.json",
		"relay.schema.json",
		"error.schema.json",
		"health.schema.json",
		"deployment.schema.json",
		"session-envelope.schema.json",
		"session-observation.schema.json",
		"session-discovery.schema.json",
		"session-sync.schema.json",
		"session-pull.schema.json",
		"session-library.schema.json",
		"session-fork.schema.json",
		"session-context.schema.json",
		"work-graph.schema.json",
		"history-batch.schema.json",
		"mcp-health.schema.json",
		"account.schema.json",
		"auth.schema.json",
		"dashboard.schema.json",
		"project-activity.schema.json",
		"project.schema.json",
		"connection.schema.json",
		"binding.schema.json",
		"workspaces.schema.json",
	} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(
			"https://schemas.coddis.dev/v1/"+name,
			document,
		); err != nil {
			t.Fatal(err)
		}
	}

	cases := map[string]string{
		"relay-page.json":          "relay.schema.json#/$defs/page",
		"project-projection.json":  "history-batch.schema.json#/$defs/projection_response",
		"project-activity.json":    "project-activity.schema.json#/$defs/list_response",
		"library-list.json":        "session-library.schema.json#/$defs/list_response",
		"session-library.json":     "session-library.schema.json#/$defs/add_response",
		"dashboard-session.json":   "dashboard.schema.json#/$defs/session_response",
		"dashboard-projects.json":  "dashboard.schema.json#/$defs/projects_response",
		"account-key-reset.json":   "dashboard.schema.json#/$defs/reset_key_response",
		"error.unavailable.json":   "error.schema.json",
		"health.runtime.json":      "health.schema.json",
		"health.server.json":       "health.schema.json",
		"deployment.json":          "deployment.schema.json",
		"mcp-health.json":          "mcp-health.schema.json",
		"session-observation.json": "session-observation.schema.json",
		"session-discovery.json":   "session-discovery.schema.json",
		"session-sync-commit.json": "session-sync.schema.json",
		"session-index.json":       "session-context.schema.json#/$defs/index_response",
		"session-recent.json":      "session-context.schema.json#/$defs/recent_response",
		"session-search.json":      "session-context.schema.json#/$defs/search_response",
		"library-tag-search.json":  "session-context.schema.json#/$defs/search_response",
		"library-search.json":      "session-context.schema.json#/$defs/search_response",
		"session-read.json":        "session-context.schema.json#/$defs/read_response",
		"history-batch.json":       "history-batch.schema.json",
		"history-recovery.json":    "history-batch.schema.json",
		"work-graph.json":          "work-graph.schema.json#/$defs/read_response",
		"work-search.json":         "work-graph.schema.json#/$defs/search_response",
		"session-pull.json":        "session-pull.schema.json#/$defs/result",
		"session-fork.json":        "session-fork.schema.json#/$defs/result",
		"account.json":             "account.schema.json",
		"project-access.json":      "project.schema.json",
		"connection.json":          "connection.schema.json",
		"project-declaration.json": "binding.schema.json",
		"binding.local.json":       "binding.schema.json#/$defs/local_binding_state",
		"workspace-report.json":    "workspaces.schema.json#/$defs/report",
		"project-folders.json":     "workspaces.schema.json#/$defs/list_response",
	}
	for fixture, schemaName := range cases {
		t.Run(fixture, func(t *testing.T) {
			schema, err := compiler.Compile("https://schemas.coddis.dev/v1/" + schemaName)
			if err != nil {
				t.Fatal(err)
			}
			content, err := contractFiles.ReadFile("testdata/" + fixture)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(content, &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); err != nil {
				t.Fatal(err)
			}

			if fixture == "project-activity.json" {
				events := value.(map[string]any)["events"].([]any)
				checkpoint := events[0].(map[string]any)
				node := checkpoint["node"].(map[string]any)
				node["state"] = "current"
				if schema.Validate(value) == nil {
					t.Fatal("mutable current node accepted as historical checkpoint")
				}
				node["state"] = "sealed"
				checkpoint["kind"] = "completed"
				if schema.Validate(value) == nil {
					t.Fatal("invented completion event accepted")
				}
				checkpoint["kind"] = "checkpoint"
				delete(checkpoint, "node")
				if schema.Validate(value) == nil {
					t.Fatal("checkpoint without its fixed snapshot accepted")
				}
			}
			if fixture == "work-graph.json" {
				response := value.(map[string]any)
				for _, warning := range []string{"projection_processing", "projection_incomplete"} {
					response["warning"] = warning
					if err := schema.Validate(value); err != nil {
						t.Fatal("graph notification rejected", err)
					}
				}
				delete(response, "warning")
				node := response["nodes"].([]any)[0].(map[string]any)
				delete(node, "summary")
				if err := schema.Validate(value); err != nil {
					t.Fatal("objective-only node rejected", err)
				}
				node["focus"] = "obsolete"
				if schema.Validate(value) == nil {
					t.Fatal("obsolete node field accepted")
				}
				delete(node, "focus")
				node["pending"] = "obsolete"
				if schema.Validate(value) == nil {
					t.Fatal("node-owned items accepted")
				}
				delete(node, "pending")
				response["nodes"] = []any{}
				if err := schema.Validate(value); err != nil {
					t.Fatal("items-only response rejected", err)
				}
				delete(response, "session_work")
				if schema.Validate(value) == nil {
					t.Fatal("Session state field is missing")
				}
			}
		})
	}
}

func TestGoTypesDecodeRepresentativeFixtures(t *testing.T) {
	decodeFixture[ListLibraryEntriesResponse](t, "library-list.json")
	decodeFixture[AddSessionToLibraryResponse](t, "session-library.json")
	decodeFixture[DashboardSessionResponse](t, "dashboard-session.json")
	decodeFixture[DashboardProjectsResponse](t, "dashboard-projects.json")
	decodeFixture[ResetAccountKeyResponse](t, "account-key-reset.json")
	decodeFixture[HealthReport](t, "health.runtime.json")
	decodeFixture[DeploymentInfo](t, "deployment.json")
	decodeFixture[ErrorEnvelope](t, "error.unavailable.json")
	decodeFixture[ListProjectActivityResponse](t, "project-activity.json")
	decodeFixture[SessionObservation](t, "session-observation.json")
	decodeFixture[SessionDiscovery](t, "session-discovery.json")
	decodeFixture[CommitSyncResponse](t, "session-sync-commit.json")
	decodeFixture[IndexSessionResponse](t, "session-index.json")
	decodeFixture[ListRecentSessionsResponse](t, "session-recent.json")
	decodeFixture[SearchSessionsResponse](t, "session-search.json")
	decodeFixture[SearchSessionsResponse](t, "library-search.json")
	decodeFixture[ReadSessionResponse](t, "session-read.json")
	decodeFixture[ReadProjectWorkGraphResponse](t, "work-graph.json")
	decodeFixture[SearchWorkResponse](t, "work-search.json")
	decodeFixture[HistoryBatchResponse](t, "history-batch.json")
	decodeFixture[HistoryBatchResponse](t, "history-recovery.json")
	decodeFixture[PullSessionResult](t, "session-pull.json")
	decodeFixture[ForkSessionResult](t, "session-fork.json")
	decodeFixture[MCPHealthResult](t, "mcp-health.json")
	decodeFixture[Account](t, "account.json")
	decodeFixture[ProjectAccess](t, "project-access.json")
	decodeFixture[ConnectProjectRequest](t, "connection.json")
	decodeFixture[ProjectDeclaration](t, "project-declaration.json")
	decodeFixture[LocalBindingState](t, "binding.local.json")
}

func TestAccountAuthorizationContracts(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "account.schema.json", "auth.schema.json"} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, definition, content string
		valid                     bool
	}{
		{"GitHub configuration", "configuration", `{"schema_version":1,"provider":"github","label":"GitHub"}`, true},
		{"configuration cannot expose secret", "configuration", `{"schema_version":1,"provider":"github","label":"GitHub","client_secret":"hidden"}`, false},
		{"authorization preview", "decision_request", `{"authorization_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, true},
		{"decline", "decision_request", `{"authorization_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","approve":false}`, true},
		{"decision must be boolean", "decision_request", `{"authorization_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","approve":"false"}`, false},
		{"setup preview", "decision_response", `{"device_name":"Laptop","connect_project":true,"project_name":"Local folder"}`, true},
		{"setup decision", "decision_request", `{"authorization_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","approve":true,"project_id":"coco_fixture01"}`, true},
		{"pending cannot select a project", "device_response", `{"status":"pending","project_id":"coco_fixture01"}`, false},
		{"pending", "device_response", `{"status":"pending"}`, true},
		{"pending cannot issue credentials", "device_response", `{"status":"pending","access_token":"abcdefghijklmnopqrstuvwxyz123456"}`, false},
		{"success needs identity", "device_response", `{"status":"ok","access_token":"abcdefghijklmnopqrstuvwxyz123456"}`, false},
		{"success without email", "device_response", `{"status":"ok","access_token":"abcdefghijklmnopqrstuvwxyz123456","account":{"schema_version":1,"account_id":"acct_fixture01","display_name":"Developer","email":"","created_at":"2026-09-14T00:00:00Z"}}`, true},
		{"directory revision required", "directory", `{"cwd":"/work","path":"/work","remote":false,"can_browse":true}`, false},
		{"directory selection", "directory_update", `{"device_code":"fixture","code_verifier":"abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG","request_id":"folder","cwd":"/work","path":"/work"}`, true},
		{"cancel cannot change directory", "directory_update", `{"device_code":"fixture","code_verifier":"abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG","request_id":"folder","error":"cancelled","cwd":"/work","path":"/work"}`, false},
		{"pending chooser", "device_response", `{"status":"pending","directory_request_id":"folder"}`, true},
		{"short verifier", "poll_request", `{"device_code":"fixture","code_verifier":"short"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := compiler.Compile("https://schemas.coddis.dev/v1/auth.schema.json#/$defs/" + tc.definition)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal([]byte(tc.content), &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != tc.valid {
				t.Fatalf("unexpected contract validation: %v", err)
			}
		})
	}
}

func TestVisibleTextProjectionContract(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "session-envelope.schema.json", "session-observation.schema.json", "session-library.schema.json", "session-context.schema.json"} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("https://schemas.coddis.dev/v1/session-context.schema.json#/$defs/index_request")
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := contractFiles.ReadFile("testdata/session-observation.json")
	var observation SessionObservation
	if err := json.Unmarshal(fixture, &observation); err != nil {
		t.Fatal(err)
	}
	request := IndexSessionRequest{SchemaVersion: 1, RequestID: "request-visible-schema", ProjectID: "coco_visiblefixture", ServerSessionID: "sess_visiblefixture", Version: 1, Normalized: observation.Normalized, VisibleText: observation.VisibleText}
	encoded, _ := json.Marshal(request)
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"projection", func(map[string]any) {}, true},
		{"legacy", func(v map[string]any) { delete(v, "visible_text") }, true},
		{"empty", func(v map[string]any) { v["visible_text"].(map[string]any)["turns"] = []any{} }, true},
		{"null", func(v map[string]any) { v["visible_text"] = nil }, false},
		{"version", func(v map[string]any) { v["visible_text"].(map[string]any)["version"] = 2 }, false},
		{"unknown", func(v map[string]any) { v["visible_text"].(map[string]any)["text"] = "duplicate" }, false},
		{"missing cursor", func(v map[string]any) { delete(v["visible_text"].(map[string]any), "source_cursor") }, false},
		{"negative range", func(v map[string]any) {
			v["visible_text"].(map[string]any)["turns"].([]any)[0].(map[string]any)["ranges"].([]any)[0].(map[string]any)["start"] = -1
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatal(err)
			}
			tc.change(value)
			if err := schema.Validate(value); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestLibrarySubmissionResultStates(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "session-library.schema.json"} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("https://schemas.coddis.dev/v1/session-library.schema.json#/$defs/submission_result")
	if err != nil {
		t.Fatal(err)
	}
	entry := &LibraryEntry{WorkNodeID: "work_fixture01", EntryID: "libentry_fixture01", ProjectID: "coco_fixture01", Source: SessionReadReference{ServerSessionID: "sess_fixture01", Version: 1}, SubmittedBy: "acct_fixture01", CollectedAt: "2026-09-09T00:00:00Z"}
	cases := []struct {
		name      string
		state     string
		status    Status
		entry     *LibraryEntry
		retryable bool
		valid     bool
	}{
		{"collected", "collected", StatusOK, entry, false, true},
		{"pending", "pending", StatusUnavailable, nil, true, true},
		{"before capture", "not_captured", StatusUnsupported, nil, false, true},
		{"failed", "failed", StatusForbidden, nil, false, true},
		{"missing entry", "collected", StatusOK, nil, false, false},
		{"unfinished entry", "pending", StatusUnavailable, entry, true, false},
		{"unfinished success", "pending", StatusOK, nil, true, false},
		{"retryable success", "collected", StatusOK, entry, true, false},
		{"failed collection", "collected", StatusUnavailable, entry, false, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			content, err := json.Marshal(LibrarySubmissionResult{SchemaVersion: SchemaVersion, RequestID: "library-request-fixture", State: test.state, Status: test.status, Entry: test.entry, Retryable: test.retryable})
			if err != nil {
				t.Fatal(err)
			}
			var document any
			if err := json.Unmarshal(content, &document); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(document); (err == nil) != test.valid {
				t.Fatalf("valid=%v: %v", test.valid, err)
			}
		})
	}
}

func TestLibrarySearchContractBoundaries(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "session-library.schema.json", "session-context.schema.json"} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("https://schemas.coddis.dev/v1/session-context.schema.json#/$defs/search_response")
	if err != nil {
		t.Fatal(err)
	}
	content, err := contractFiles.ReadFile("testdata/library-search.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"note match", "missing entry", "fabricated Turn", "no match", "expanded scope", "missing scope"} {
		t.Run(name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(content, &document); err != nil {
				t.Fatal(err)
			}
			hit := document["results"].([]any)[0].(map[string]any)
			switch name {
			case "missing entry":
				delete(hit, "library_entry")
			case "fabricated Turn":
				hit["library_note_match"].(map[string]any)["turn_id"] = "invented"
			case "no match":
				delete(hit, "library_note_match")
			case "expanded scope":
				document["scope"] = "all"
			case "missing scope":
				delete(document, "scope")
			}
			if err := schema.Validate(document); (err == nil) != (name == "note match") {
				t.Fatalf("unexpected Library evidence validation: %v", err)
			}
		})
	}
}

func TestLibraryTagContractBoundaries(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "session-library.schema.json", "session-context.schema.json"} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("https://schemas.coddis.dev/v1/session-context.schema.json#/$defs/search_response")
	if err != nil {
		t.Fatal(err)
	}
	content, err := contractFiles.ReadFile("testdata/library-tag-search.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "missing tagging", "pending with tags", "empty match", "invented turn", "missing date", "duplicate tags", "too many tags", "oversize tag"} {
		t.Run(mode, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(content, &document); err != nil {
				t.Fatal(err)
			}
			hit := document["results"].([]any)[0].(map[string]any)
			entry := hit["library_entry"].(map[string]any)
			tagging := entry["tagging"].(map[string]any)
			switch mode {
			case "missing tagging":
				delete(entry, "tagging")
			case "pending with tags":
				tagging["state"] = "pending"
			case "empty match":
				hit["library_tag_match"].(map[string]any)["tags"] = []any{}
			case "invented turn":
				hit["library_tag_match"].(map[string]any)["turn_id"] = "not-a-turn"
			case "missing date":
				delete(tagging, "generated_at")
			case "duplicate tags":
				tagging["tags"] = []any{"cache", "cache"}
			case "too many tags":
				tagging["tags"] = []any{"a", "b", "c", "d", "e", "f", "g"}
			case "oversize tag":
				tagging["tags"] = []any{strings.Repeat("中", 49)}
			}
			if err := schema.Validate(document); (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
		})
	}
	requestSchema, err := compiler.Compile("https://schemas.coddis.dev/v1/session-context.schema.json#/$defs/search_request")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "all", "library"} {
		document := map[string]any{"schema_version": 1, "request_id": "request-tag-contract", "project_id": "coco_fixture01", "include_library_tags": true}
		if scope != "" {
			document["scope"] = scope
		}
		if err := requestSchema.Validate(document); (err == nil) != (scope == "library") {
			t.Fatal("tag query scope validation", scope, err)
		}
	}
}

func TestHistoryBatchRequestContracts(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "work-graph.schema.json", "history-batch.schema.json"} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	begin, err := compiler.Compile("https://schemas.coddis.dev/v1/history-batch.schema.json#/$defs/begin_request")
	if err != nil {
		t.Fatal(err)
	}
	resolve, err := compiler.Compile("https://schemas.coddis.dev/v1/history-batch.schema.json#/$defs/resolve_request")
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{"schema_version": 1, "request_id": "request-fixture", "project_id": "coco_fixture01", "batch_id": "batch-fixture", "members": []any{}, "discovery_issues": []any{}}
	if err := begin.Validate(valid); err != nil {
		t.Fatal(err)
	}
	delete(valid, "members")
	if err := begin.Validate(valid); err == nil {
		t.Fatal("absent manifest accepted as an empty batch")
	}
	valid["members"] = []any{map[string]any{"member_id": "member-fixture", "agent_type": "codex", "native_session_id": "native-fixture", "artifact_sha256": strings.Repeat("a", 64), "failure_code": "capture_failed"}}
	if err := begin.Validate(valid); err == nil {
		t.Fatal("failed capture accepted with an artifact")
	}
	input := map[string]any{"schema_version": 1, "request_id": "request-fixture", "project_id": "coco_fixture01", "batch_id": "batch-fixture", "member_id": "member-fixture", "source": map[string]any{"server_session_id": "sess_fixture01", "version": 1}}
	if err := resolve.Validate(input); err != nil {
		t.Fatal(err)
	}
	input["failure_code"] = "sync_failed"
	if err := resolve.Validate(input); err == nil {
		t.Fatal("source and terminal failure accepted together")
	}
	delete(input, "source")
	if err := resolve.Validate(input); err != nil {
		t.Fatal(err)
	}
	input["batch_id"] = "batch/path"
	if err := resolve.Validate(input); err == nil {
		t.Fatal("batch ID with path separators accepted")
	}
	outcome, err := compiler.Compile("https://schemas.coddis.dev/v1/history-batch.schema.json#/$defs/extraction_result")
	if err != nil {
		t.Fatal(err)
	}
	build, err := compiler.Compile("https://schemas.coddis.dev/v1/history-batch.schema.json#/$defs/graph_build")
	if err != nil {
		t.Fatal(err)
	}
	progress := map[string]any{"state": "published", "attempts": 0}
	if err := build.Validate(progress); err == nil {
		t.Fatal("published graph accepted without publication time")
	}
	progress["published_at"] = "2026-09-08T01:00:00Z"
	if err := build.Validate(progress); err != nil {
		t.Fatal(err)
	}
	progress["last_error"] = "bootstrap_graph_invalid"
	if err := build.Validate(progress); err == nil {
		t.Fatal("published graph accepted with an active error")
	}
	delete(progress, "published_at")
	progress["state"], progress["attempts"] = "retrying", 1
	if err := build.Validate(progress); err == nil {
		t.Fatal("retrying graph accepted without a retry deadline")
	}
	progress["next_attempt_at"] = "2026-09-08T01:00:01Z"
	if err := build.Validate(progress); err != nil {
		t.Fatal(err)
	}
	progress["state"] = "suspended"
	if err := build.Validate(progress); err == nil {
		t.Fatal("suspended work accepted with a retry deadline")
	}
	delete(progress, "next_attempt_at")
	if err := build.Validate(progress); err != nil {
		t.Fatal(err)
	}
	delete(progress, "last_error")
	if err := build.Validate(progress); err == nil {
		t.Fatal("suspended work accepted without a failure reason")
	}
	for _, status := range []string{"updated", "unchanged", "failed"} {
		value := map[string]any{"status": status, "completed_at": "2026-09-08T01:00:00Z"}
		if status == "failed" {
			if err := outcome.Validate(value); err == nil {
				t.Fatal("failed extraction accepted without a reason")
			}
			value["error_code"] = "work_extraction_request_failed"
		}
		if err := outcome.Validate(value); err != nil {
			t.Fatalf("valid %s outcome rejected: %v", status, err)
		}
		if status != "failed" {
			value["error_code"] = "work_extraction_request_failed"
			if err := outcome.Validate(value); err == nil {
				t.Fatal("successful extraction accepted with a failure")
			}
		}
	}
}

func TestSearchRequestDeprecatesSeparateSignalInputs(t *testing.T) {
	content, err := contractFiles.ReadFile("schemas/v1/session-context.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				Deprecated  bool   `json:"deprecated"`
				Description string `json:"description"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	properties := document.Definitions["search_request"].Properties
	for _, name := range []string{"query", "paths"} {
		if properties[name].Deprecated {
			t.Fatalf("current Search input %s is marked deprecated", name)
		}
	}
	for _, name := range []string{"symbols", "error_tokens"} {
		property := properties[name]
		if !property.Deprecated || !strings.Contains(property.Description, "query") {
			t.Fatalf("legacy Search input %s is not documented as query compatibility: %#v", name, property)
		}
	}
}

func TestObservationSchemaReservesSourceTurnIDForServerHistory(t *testing.T) {
	content, err := contractFiles.ReadFile("schemas/v1/session-observation.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Properties map[string]struct {
			Properties map[string]struct {
				Items struct {
					Properties map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	turnProperties := document.Properties["normalized"].Properties["turns"].Items.Properties
	if _, ok := turnProperties["source_turn_id"]; ok {
		t.Fatal("client observation schema exposes Server-owned source_turn_id")
	}
}

func TestArtifactTransferURLContract(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "session-sync.schema.json", "session-observation.schema.json", "session-envelope.schema.json", "session-context.schema.json", "session-library.schema.json", "work-graph.schema.json"} {
		content, err := contractFiles.ReadFile("schemas/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("https://schemas.coddis.dev/v1/session-sync.schema.json#/$defs/artifact_transfer_url")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"https://bucket.example.test/object?signature=fixture", "/v1/artifacts/objects/chunk_fixture"} {
		if err := schema.Validate(value); err != nil {
			t.Fatalf("valid artifact transfer URL %q was rejected: %v", value, err)
		}
	}
	for _, value := range []string{"//evil.example.test/object", "file:///var/lib/coconet/artifacts/object", "v1/artifacts/objects/chunk_fixture"} {
		if err := schema.Validate(value); err == nil {
			t.Fatalf("unsafe artifact transfer URL %q was accepted", value)
		}
	}
}

func TestOpenAPIRoutesUseVersionedContract(t *testing.T) {
	content, err := contractFiles.ReadFile("openapi/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"/healthz",
		"/v1/deployment",
		"/v1/projects/{project_id}/relay/commits",
		"/v1/projects/{project_id}/relay/checkpoint",
		"/v1/dashboard/projects/{project_id}/published-graph",
		"/v1/auth/config",
		"/v1/auth/start",
		"/v1/auth/callback",
		"/v1/auth/device",
		"/v1/auth/token",
		"/v1/auth/logout",
		"/v1/account",
		"/v1/account/key",
		"/v1/dashboard/session",
		"/v1/dashboard/projects",
		"/v1/dashboard/projects/{project_id}/graph",
		"/v1/dashboard/projects/{project_id}/projection",
		"/v1/dashboard/projects/{project_id}/sessions/{server_session_id}/read",
		"/v1/connections",
		"/v1/projects",
		"/v1/projects/{project_id}/access",
		"/v1/projects/{project_id}/library/entries",
		"/v1/projects/{project_id}/library/entries/list",
		"/v1/projects/{project_id}/membership",
		"/v1/projects/{project_id}/join-requests",
		"/v1/projects/{project_id}/manage",
		"/v1/projects/{project_id}/join-requests/{join_request_id}/decision",
		"/v1/projects/{project_id}/members/{account_id}/remove",
		"/v1/projects/{project_id}/leave",
		"/v1/projects/{project_id}/dissolve",
		"/v1/projects/{project_id}/sessions/prepare",
		"/v1/projects/{project_id}/sessions/commit",
		"/v1/projects/{project_id}/sessions/{server_session_id}/versions/{version}/index",
		"/v1/projects/{project_id}/sessions/search",
		"/v1/projects/{project_id}/sessions/{server_session_id}/read",
		"/v1/projects/{project_id}/work-graph",
		"/v1/projects/{project_id}/work/search",
		"/v1/projects/{project_id}/projection",
		"/v1/projects/{project_id}/projection/retry",
		"/v1/projects/{project_id}/history-batches",
		"/v1/projects/{project_id}/history-batches/{batch_id}/resolve",
		"/v1/projects/{project_id}/history-batches/{batch_id}/complete",
		"/v1/projects/{project_id}/sessions/pull",
		"/v1/projects/{project_id}/sessions/fork/prepare",
		"/v1/projects/{project_id}/sessions/fork/complete",
	} {
		if document.Paths[route] == nil {
			t.Fatalf("OpenAPI document is missing %s", route)
		}
	}
	if document.OpenAPI != "3.1.0" || document.Paths["/v1/accounts/bootstrap"] != nil || document.Paths["/v1/auth/device/code"] != nil || document.Paths["/v1/auth/device/token"] != nil || document.Paths["/v1/dev/auth/device/approve"] != nil {
		t.Fatal("OpenAPI version or removed authentication route boundary is invalid")
	}

	var operations struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Deprecated  bool   `json:"deprecated"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(content, &operations); err != nil {
		t.Fatal(err)
	}
	operationPaths := make(map[string]string)
	for route, methods := range operations.Paths {
		for method, operation := range methods {
			if operation.OperationID == "" {
				continue
			}
			if previous := operationPaths[operation.OperationID]; previous != "" {
				t.Fatalf("OpenAPI operationId %q is shared by %s and %s %s", operation.OperationID, previous, strings.ToUpper(method), route)
			}
			operationPaths[operation.OperationID] = strings.ToUpper(method) + " " + route
			legacy := route == "/v1/projects/{project_id}/join-requests" ||
				route == "/v1/projects/{project_id}/manage" ||
				strings.Contains(route, "/join-requests/{join_request_id}/decision") ||
				strings.Contains(route, "/members/{account_id}/remove") ||
				route == "/v1/projects/{project_id}/leave" ||
				route == "/v1/projects/{project_id}/dissolve" ||
				(strings.HasPrefix(route, "/v1/projects/") && (strings.Contains(route, "/sessions/search") || strings.Contains(route, "/sessions/recent") || strings.HasSuffix(route, "/read") || strings.HasSuffix(route, "/index"))) ||
				(strings.HasPrefix(route, "/v1/projects/{project_id}/") && (strings.Contains(route, "/sessions/") || strings.Contains(route, "/projection") || strings.Contains(route, "/history-batches") || strings.Contains(route, "/library/") || strings.HasSuffix(route, "/work-graph") || strings.HasSuffix(route, "/work/search"))) ||
				(strings.HasPrefix(route, "/v1/dashboard/projects/{project_id}/") && (strings.HasSuffix(route, "/graph") || strings.Contains(route, "/projection") || strings.Contains(route, "/sessions/") || strings.HasSuffix(route, "/activity") || strings.Contains(route, "/library")))
			if operation.Deprecated != legacy {
				t.Fatalf("OpenAPI deprecated=%t for %s %s, want %t", operation.Deprecated, strings.ToUpper(method), route, legacy)
			}
		}
	}
}

func decodeFixture[T any](t *testing.T, name string) T {
	t.Helper()
	content, err := contractFiles.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatal(fmt.Errorf("decode %s: %w", name, err))
	}
	return value
}
