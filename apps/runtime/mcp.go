package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	productprompts "github.com/xfey/coconet/prompts"
)

const (
	mcpProtocolVersion = "2025-06-18"
	maxMCPMessage      = 1 << 20
	mcpToolTimeout     = 8 * time.Second
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func serveMCP(ctx context.Context, input io.Reader, output io.Writer, targetAgents ...contracts.AgentType) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxMCPMessage)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err := encoder.Encode(rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "parse error: send one valid JSON-RPC request object per line"},
			}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		response := handleMCPRequest(ctx, request, targetAgents...)
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP input: %w", err)
	}
	return nil
}

func handleMCPRequest(ctx context.Context, request rpcRequest, targetAgents ...contracts.AgentType) rpcResponse {
	targetAgent := contracts.AgentType("")
	if len(targetAgents) > 0 {
		targetAgent = targetAgents[0]
	}
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]bool{"listChanged": false},
			},
			"serverInfo": map[string]string{"name": "coconet", "version": version},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{
			"tools": mcpTools(targetAgent),
		}
	case "tools/call":
		var params mcpCallParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
			response.Error = &rpcError{Code: -32602, Message: "tools/call params must be an object with a nonempty string name and an arguments object; use tools/list to inspect available tools"}
			return response
		}
		project, managed, err := resolveLocalMCPProject()
		if err == nil && !managed {
			err = errMCPNotConnected
		}
		return handleLocalMCP(ctx, params, project, err, response, targetAgent)
	default:
		response.Error = &rpcError{Code: -32601, Message: "method not found; use initialize, ping, tools/list or tools/call"}
	}
	return response
}

func mcpTools(targetAgents ...contracts.AgentType) []any {
	targetAgent := contracts.AgentType("")
	if len(targetAgents) > 0 {
		targetAgent = targetAgents[0]
	}
	return localMCPTools(targetAgent)
}

func decodeMCPArguments(content json.RawMessage, target any) error {
	if len(content) == 0 {
		content = json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("arguments must contain one JSON object")
	}
	return nil
}

func discoveryMessage(message string) string {
	if message == "" {
		return productprompts.DiscoveryEvidence()
	}
	return message + "\n\n" + productprompts.DiscoveryEvidence()
}

func mcpToolResult(message string, structured any, isError bool) map[string]any {
	content := []any{map[string]string{"type": "text", "text": message}}
	// Some hosts forward only content to the model. Mirror the same sanitized
	// projection there so graph nodes, coverage and fixed read paths survive.
	if structured != nil {
		if data, err := json.Marshal(structured); err == nil {
			content = append(content, map[string]string{"type": "text", "text": string(data)})
		}
	}
	return map[string]any{
		"content":           content,
		"structuredContent": structured,
		"isError":           isError,
	}
}
