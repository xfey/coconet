package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"github.com/xfey/coconet/packages/contracts"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var errMCPNotConnected = errors.New("this folder is not connected to a Coconet project")

const mcpConnectHelp = "Run coconet connect in the intended work folder, or open the Agent in an already connected folder, then retry. Joining a project in the browser alone does not connect a local folder."
const mcpDiagnosticsHelp = "Run coconet status --verbose and coconet diagnostics list in this folder to inspect the cause before retrying."

type mcpArgumentIssue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type mcpInputSchema struct {
	compiled *jsonschema.Schema
	shape    map[string]any
}

// Validate against exactly what tools/list advertises, instead of maintaining a
// second set of bounds in the dispatcher. Compile once; no remote schema loads.
var mcpInputSchemas = sync.OnceValues(func() (map[string]mcpInputSchema, error) {
	result := map[string]mcpInputSchema{}
	for _, raw := range localMCPTools(contracts.AgentCodex) {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		data, err := json.Marshal(tool["inputSchema"])
		if err != nil {
			return nil, err
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		compiler := jsonschema.NewCompiler()
		location := "urn:coconet:mcp:" + name
		if err := compiler.AddResource(location, value); err != nil {
			return nil, err
		}
		compiled, err := compiler.Compile(location)
		if err != nil {
			return nil, err
		}
		result[name] = mcpInputSchema{compiled, tool["inputSchema"].(map[string]any)}
	}
	return result, nil
})

func (schema mcpInputSchema) validate(raw json.RawMessage) []mcpArgumentIssue {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []mcpArgumentIssue{{"arguments", "must contain one valid JSON object"}}
	}
	var issues []mcpArgumentIssue
	if err := schema.compiled.Validate(value); err != nil {
		var validation *jsonschema.ValidationError
		if !errors.As(err, &validation) {
			return []mcpArgumentIssue{{"arguments", "does not match inputSchema; inspect tools/list"}}
		}
		var visit func(*jsonschema.ValidationError)
		visit = func(v *jsonschema.ValidationError) {
			if len(v.Causes) > 0 {
				for _, cause := range v.Causes {
					visit(cause)
				}
				return
			}
			field := strings.Join(v.InstanceLocation, ".")
			add := func(name, detail string) {
				if name == "" {
					name = "arguments"
				}
				issues = append(issues, mcpArgumentIssue{name, detail})
			}
			child := func(name string) string {
				if field == "" {
					return name
				}
				return field + "." + name
			}
			switch reason := v.ErrorKind.(type) {
			case *kind.Required:
				for _, name := range reason.Missing {
					add(child(name), "is required")
				}
			case *kind.AdditionalProperties:
				for _, name := range reason.Properties {
					add(child(name), "is not supported; remove this field and use the parameters in tools/list")
				}
			default:
				detail := v.ErrorKind.LocalizedString(message.NewPrinter(language.English))
				shape := schema.shape
				for _, name := range v.InstanceLocation {
					properties, _ := shape["properties"].(map[string]any)
					shape, _ = properties[name].(map[string]any)
				}
				if description, ok := shape["description"].(string); ok {
					detail += ". " + description
				}
				add(field, detail)
			}
		}
		visit(validation)
	}
	// JSON Schema's minLength permits whitespace; identifiers and search text
	// must carry content. Keep free-form notes and opaque cursors untouched.
	var checkText func(map[string]any, string)
	checkText = func(object map[string]any, prefix string) {
		for key, raw := range object {
			if nested, ok := raw.(map[string]any); ok {
				checkText(nested, prefix+key+".")
			}
			text, ok := raw.(string)
			if !ok {
				continue
			}
			switch key {
			case "query", "request_id", "work_node_id", "session_id":
				if text != "" && strings.TrimSpace(text) == "" {
					issues = append(issues, mcpArgumentIssue{prefix + key, "must not contain only whitespace"})
				}
			}
			if key == "query" && len(strings.Fields(text)) > 64 {
				issues = append(issues, mcpArgumentIssue{prefix + key, "must contain at most 64 whitespace-separated terms; shorten the query or split it into separate searches"})
			}
		}
	}
	if object, ok := value.(map[string]any); ok {
		checkText(object, "")
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Field == issues[j].Field {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Field < issues[j].Field
	})
	return issues
}

func mcpFailure(response rpcResponse, status contracts.Status, code, detail, nextStep string) rpcResponse {
	response.Result = mcpToolResult(detail+"\nNext step: "+nextStep, map[string]any{
		"status": status, "code": code, "error": detail, "next_step": nextStep,
	}, true)
	return response
}

func mcpInvalidArguments(response rpcResponse, tool string, issues []mcpArgumentIssue, resolveErr error) rpcResponse {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+": "+issue.Message)
	}
	nextStep := "Correct the listed fields and retry. Inspect this tool's inputSchema in tools/list for allowed parameters."
	structured := map[string]any{
		"status": contracts.StatusInvalidRequest, "code": "invalid_arguments", "tool": tool, "issues": issues,
	}
	if errors.Is(resolveErr, errMCPNotConnected) {
		structured["connection_status"] = contracts.StatusNotConnected
		nextStep += " " + mcpConnectHelp
	}
	detail := fmt.Sprintf("Invalid %s arguments: %s", tool, strings.Join(parts, "; "))
	structured["error"], structured["next_step"] = detail, nextStep
	response.Result = mcpToolResult(detail+"\nNext step: "+nextStep, structured, true)
	return response
}
