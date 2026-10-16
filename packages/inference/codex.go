package inference

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xfey/coconet/packages/subprocess"
)

type rpcFrame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type rpcProcess struct {
	ctx    context.Context
	input  *os.File
	frames chan []byte
	done   chan error
	cancel context.CancelFunc
	reader *os.File
}

func startRPC(ctx context.Context, cmd *exec.Cmd) (*rpcProcess, error) {
	ctx, cancel := context.WithCancel(ctx)
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		inRead.Close()
		inWrite.Close()
		cancel()
		return nil, err
	}
	p := &rpcProcess{ctx: ctx, input: inWrite, reader: outRead, frames: make(chan []byte, 32), done: make(chan error, 1), cancel: cancel}
	cmd.Stdin, cmd.Stdout = inRead, outWrite
	cmd.Stderr = &boundedBuffer{limit: 32 << 10}
	go func() {
		defer close(p.frames)
		scanner := bufio.NewScanner(outRead)
		scanner.Buffer(make([]byte, 4096), MaxResponse)
		for scanner.Scan() {
			line := append([]byte{}, scanner.Bytes()...)
			select {
			case p.frames <- line:
			case <-ctx.Done():
				return
			}
		}
		if scanner.Err() != nil {
			cancel()
		}
	}()
	go func() {
		err := subprocess.Run(ctx, cmd)
		inRead.Close()
		outWrite.Close()
		p.done <- err
	}()
	return p, nil
}

func (p *rpcProcess) close()               { p.input.Close(); p.cancel(); <-p.done; p.reader.Close() }
func (p *rpcProcess) send(value any) error { return json.NewEncoder(p.input).Encode(value) }
func (p *rpcProcess) next() (rpcFrame, error) {
	for {
		select {
		case <-p.ctx.Done():
			return rpcFrame{}, p.ctx.Err()
		case line, ok := <-p.frames:
			if !ok {
				return rpcFrame{}, errors.New("app-server exited before completion")
			}
			f, err := decodeRPCFrame(line)
			if err != nil {
				return f, err
			}
			if f.Method != "" && len(f.ID) > 0 {
				// Never approve external tools, login, user prompts or permissions.
				_ = p.send(map[string]any{"id": f.ID, "error": map[string]any{"code": -32601, "message": "requests are disabled for inference"}})
				return f, errors.New("app-server requested a forbidden capability")
			}
			return f, nil
		}
	}
}

func (p *rpcProcess) call(id int, method string, params any) (json.RawMessage, error) {
	if err := p.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		frame, err := p.next()
		if err != nil {
			return nil, err
		}
		if string(frame.ID) != fmt.Sprint(id) {
			continue
		}
		if len(frame.Error) > 0 {
			return nil, fmt.Errorf("app-server rejected %s", method)
		}
		return frame.Result, nil
	}
}

// startCodexRPC performs only the protocol handshake, never a model request.
// Check and inference share the same startup path and capability restrictions.
func (r Local) startCodexRPC(ctx context.Context, work string) (*rpcProcess, error) {
	// environments=[] is an experimental app-server capability, required here:
	// it removes environment access instead of relying on a read-only sandbox
	// (which by itself still permits filesystem reads). Unsupported builds fail.
	cmd := r.command(work, "app-server", "-c", "mcp_servers={}", "-c", "project_doc_max_bytes=0",
		"-c", "features.codex_hooks=false", "-c", "features.hooks=false", "-c", "features.apps=false",
		"-c", "features.shell_tool=false", "-c", "features.unified_exec=false", "-c", "features.multi_agent=false",
		"-c", "features.collab=false", "-c", "features.code_mode=false", "-c", "features.memory_tool=false",
		"-c", "features.skip_host_skill_discovery=true", "-c", `web_search="disabled"`)
	p, err := startRPC(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if _, err = p.call(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "coconet_inference", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		p.close()
		return nil, err
	}
	if err = p.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (r Local) codex(ctx context.Context, work string, request Request) (response Response, resultErr error) {
	response = Response{Model: r.Config.Model}
	// JSON-RPC may ignore unknown properties on older releases. Check the
	// installed experimental schema before relying on environments=[].
	if err := r.codexCapabilities(ctx, work); err != nil {
		return response, err
	}
	p, err := r.startCodexRPC(ctx, work)
	if err != nil {
		return response, err
	}
	defer p.close()
	// Empty TOML tables are merged with user settings, not replacements.
	// Read the effective configuration without writing it, then explicitly disable
	// each external capability in this ephemeral thread only. Do not archive
	// configuration values: they can contain credentials.
	effective, err := p.call(5, "config/read", map[string]any{"includeLayers": false, "cwd": work})
	if err != nil {
		return response, err
	}
	isolation, err := codexThreadIsolation(effective)
	if err != nil {
		return response, err
	}
	params := map[string]any{
		"allowProviderModelFallback": false, "cwd": work, "approvalPolicy": "never", "sandbox": "read-only",
		"ephemeral": true, "baseInstructions": request.System, "developerInstructions": "", "environments": []any{}, "dynamicTools": []any{},
		"serviceName": "coconet_inference", "config": isolation,
	}
	if r.Config.Model != "" {
		params["model"] = r.Config.Model
	}
	started, err := p.call(2, "thread/start", params)
	if err != nil {
		return response, err
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model        string   `json:"model"`
		Instructions []string `json:"instructionSources"`
	}
	if json.Unmarshal(started, &thread) != nil || thread.Thread.ID == "" {
		return response, errors.New("app-server returned no thread")
	}
	if len(thread.Instructions) > 0 {
		return response, errors.New("inference profile loaded unexpected instruction files")
	}
	if r.Config.Model != "" && thread.Model != "" && thread.Model != r.Config.Model {
		return response, errors.New("inference model silently changed")
	}
	response.Model = thread.Model
	mcp, err := p.call(4, "mcpServerStatus/list", map[string]any{"limit": 100, "threadId": thread.Thread.ID})
	if err != nil {
		return response, err
	}
	var servers struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor *string           `json:"nextCursor"`
	}
	if json.Unmarshal(mcp, &servers) != nil || servers.Data == nil || len(servers.Data) > 0 || servers.NextCursor != nil {
		return response, errors.New("inference profile has external MCP capabilities")
	}
	// Do not use call() here: completion/items may race the turn/start response.
	if err = p.send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{
		"threadId": thread.Thread.ID, "input": []any{map[string]any{"type": "text", "text": string(request.Input)}}, "outputSchema": request.Schema,
	}}); err != nil {
		return response, err
	}
	var turnID string
	var completed *struct {
		ID     string          `json:"id"`
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
	}
	var visible []json.RawMessage
	visibleSize := 0
	defer func() {
		if response.Visible == "" {
			response.Visible = joinVisible(visible)
		}
	}()
	for {
		frame, err := p.next()
		if err != nil {
			response.Visible = joinVisible(visible)
			return response, err
		}
		if string(frame.ID) == "3" {
			if len(frame.Error) > 0 {
				response.Visible = string(frame.Error)
				return response, errors.New("app-server rejected turn/start")
			}
			var started struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(frame.Result, &started) != nil || started.Turn.ID == "" {
				return response, errors.New("app-server returned no turn")
			}
			turnID = started.Turn.ID
		} else if frame.Method != "" {
			var event struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Item     struct {
					Type  string `json:"type"`
					Text  string `json:"text"`
					Phase string `json:"phase"`
				} `json:"item"`
				Turn struct {
					ID     string          `json:"id"`
					Status string          `json:"status"`
					Error  json.RawMessage `json:"error"`
				} `json:"turn"`
				Usage json.RawMessage `json:"tokenUsage"`
			}
			if json.Unmarshal(frame.Params, &event) != nil {
				return response, errors.New("invalid app-server event")
			}
			if event.ThreadID != thread.Thread.ID {
				continue
			}
			if turnID != "" && event.TurnID != "" && event.TurnID != turnID {
				return response, errors.New("unexpected inference turn")
			}
			// Preserve visible output even when a turn later fails. Keep deltas
			// because a killed provider may never emit an item/completed event.
			if frame.Method == "item/agentMessage/delta" || frame.Method == "item/completed" && event.Item.Type == "agentMessage" || frame.Method == "turn/completed" || frame.Method == "error" {
				visibleSize += len(frame.Params)
				if visibleSize > 1<<20 {
					return response, errors.New("provider response exceeds archive limit")
				}
				visible = append(visible, frame.Params)
			}
			switch frame.Method {
			case "item/started", "item/completed":
				switch event.Item.Type {
				case "userMessage", "agentMessage", "reasoning":
				default:
					return response, errors.New("inference attempted a forbidden tool or injected context")
				}
				if frame.Method == "item/completed" && event.Item.Type == "agentMessage" {
					if event.Item.Phase == "final_answer" || event.Item.Phase == "" {
						response.Text = event.Item.Text
					}
				}
			case "thread/tokenUsage/updated":
				response.Usage = event.Usage
			case "turn/completed":
				completed = &event.Turn
			}
		}
		if completed != nil && turnID != "" {
			response.Visible = joinVisible(visible)
			if completed.ID != turnID || completed.Status != "completed" || len(completed.Error) > 0 && string(completed.Error) != "null" {
				return response, errors.New("app-server turn failed or was interrupted")
			}
			if strings.TrimSpace(response.Text) == "" {
				return response, errors.New("app-server completed without a final answer")
			}
			return response, nil
		}
	}
}

func (r Local) codexCapabilities(ctx context.Context, work string) error {
	root := filepath.Join(work, "protocol")
	cmd := r.command(work, "app-server", "generate-json-schema", "--experimental", "--out", root)
	cmd.Stdout = &boundedBuffer{limit: 32 << 10}
	cmd.Stderr = &boundedBuffer{limit: 32 << 10}
	if err := subprocess.Run(ctx, cmd); err != nil {
		return fmt.Errorf("app-server capability check failed: %w", err)
	}
	for name, fields := range map[string][]string{"ThreadStartParams": {"environments", "ephemeral", "baseInstructions", "allowProviderModelFallback"}, "ThreadStartResponse": {"instructionSources"}, "ListMcpServerStatusParams": {"threadId"}} {
		data, err := os.ReadFile(filepath.Join(root, "v2", name+".json"))
		if err != nil {
			return errors.New("app-server inference schema is unavailable")
		}
		var shape struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(data, &shape) != nil {
			return errors.New("invalid app-server inference schema")
		}
		for _, field := range fields {
			if len(shape.Properties[field]) == 0 {
				return fmt.Errorf("app-server does not support required inference capability %s", field)
			}
		}
	}
	return nil
}

func joinVisible(events []json.RawMessage) string {
	data, _ := json.Marshal(events)
	return string(data)
}

func codexThreadIsolation(data []byte) (map[string]any, error) {
	var result struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if json.Unmarshal(data, &result) != nil || result.Config == nil {
		return nil, errors.New("app-server returned invalid effective configuration")
	}
	isolation := map[string]any{"project_doc_max_bytes": 0}
	for _, group := range []string{"mcp_servers", "plugins"} {
		raw, present := result.Config[group]
		if !present {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(raw, &entries) != nil {
			return nil, errors.New("app-server returned invalid capability configuration")
		}
		disabled := map[string]any{}
		for name := range entries {
			disabled[name] = map[string]any{"enabled": false}
		}
		// Nested values preserve literal dots/quotes in installation IDs.
		// Dotted override keys are not TOML-quoted keys in app-server.
		isolation[group] = disabled
	}
	return isolation, nil
}
