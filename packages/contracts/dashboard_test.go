package contracts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestDashboardAuthenticationContracts(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "account.schema.json", "dashboard.schema.json"} {
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
	schema, err := compiler.Compile("https://schemas.coddis.dev/v1/dashboard.schema.json#/$defs/reset_key_request")
	if err != nil {
		t.Fatal(err)
	}
	canonical := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, key := range []string{canonical, "short", canonical + "=", canonical[:42] + "B"} {
		err := schema.Validate(map[string]any{"schema_version": 1, "new_key": key})
		if (err == nil) != (key == canonical) {
			t.Fatal("key encoding contract differs from canonical base64url")
		}
	}
	content, err := contractFiles.ReadFile("openapi/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var api struct {
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(content, &api); err != nil {
		t.Fatal(err)
	}
	for path, methods := range api.Paths {
		if !strings.HasPrefix(path, "/v1/dashboard/") && path != "/v1/account/key" {
			continue
		}
		for method, operation := range methods {
			expected := "dashboardSession"
			if path == "/v1/account/key" {
				expected = "bearerAuth"
			}
			if path == "/v1/dashboard/session" && method != "get" {
				if operation.Security == nil || len(operation.Security) != 0 {
					t.Fatal("login/logout inherit bearer authentication")
				}
				continue
			}
			if len(operation.Security) != 1 || len(operation.Security[0]) != 1 {
				t.Fatal("browser and CLI authentication were combined")
			}
			if _, found := operation.Security[0][expected]; !found {
				t.Fatal("route has the wrong credential boundary")
			}
		}
	}
}

func TestDashboardLibraryAndDialogueContracts(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	files, err := contractFiles.ReadDir("schemas/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		content, _ := contractFiles.ReadFile("schemas/v1/" + file.Name())
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://schemas.coddis.dev/v1/"+file.Name(), document); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]any{
		"library_request":   map[string]any{"schema_version": 1, "request_id": "request-toggle", "source": map[string]any{"server_session_id": "sess_fixture1", "version": 1}, "collected": false, "entry_id": "libentry_fixture1"},
		"library_response":  DashboardLibraryResponse{SchemaVersion: 1, Status: StatusOK},
		"dialogue_request":  DashboardDialogueRequest{SchemaVersion: 1, RequestID: "request-dialogue", ProjectID: "coco_fixture1", ServerSessionID: "sess_fixture1", Version: 1},
		"dialogue_response": DashboardDialogueResponse{SchemaVersion: 1, Status: StatusOK, ServerSessionID: "sess_fixture1", Version: 1, AgentType: AgentClaude, Author: SessionAuthor{AccountID: "acct_fixture1", DisplayName: "Author"}, Messages: []DashboardMessage{{TurnID: "turn_fixture", Role: "assistant", Text: "中文😀", TextComplete: true}}},
	} {
		schema, err := compiler.Compile("https://schemas.coddis.dev/v1/dashboard.schema.json#/$defs/" + name)
		if err != nil {
			t.Fatal(err)
		}
		content, _ := json.Marshal(value)
		document, _ := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err := schema.Validate(document); err != nil {
			t.Fatal(name, err)
		}
		fields := document.(map[string]any)
		if name == "library_request" {
			delete(fields, "entry_id")
			if schema.Validate(fields) == nil {
				t.Fatal("removal without entry identity accepted")
			}
		} else if name == "dialogue_response" {
			fields["messages"].([]any)[0].(map[string]any)["role"] = "tool"
			if schema.Validate(fields) == nil {
				t.Fatal("tool message accepted in dialogue response")
			}
		}
	}
}
