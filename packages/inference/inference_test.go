package inference

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProviderProcessFixture(t *testing.T) {
	mode := os.Getenv("COCO_INFERENCE_FIXTURE")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "claude_help") {
		if len(os.Args) > 2 && os.Args[len(os.Args)-1] == "--help" {
			if mode == "claude_help_wrapper" {
				fmt.Print("tclaude wrapper for Claude Code.\nTo show upstream help, run: `tclaude -- -h`\n")
				os.Exit(0)
			}
		} else if mode != "claude_help_wrapper" || !slices.Equal(os.Args[len(os.Args)-2:], []string{"--", "-h"}) {
			os.Exit(13) // The preflight may only request help, never start a turn.
		}
		if mode == "claude_help_old" {
			fmt.Print("--tools")
		} else {
			fmt.Print("--json-schema --tools --strict-mcp-config --setting-sources --no-session-persistence --disable-slash-commands --permission-mode")
		}
		os.Exit(0)
	}
	if index := slices.Index(os.Args, "generate-json-schema"); index >= 0 {
		out := os.Args[slices.Index(os.Args, "--out")+1]
		os.MkdirAll(filepath.Join(out, "v2"), 0700)
		params := `{"properties":{"environments":{},"ephemeral":{},"baseInstructions":{},"allowProviderModelFallback":{}}}`
		if mode == "codex_unsupported" {
			params = `{"properties":{}}`
		}
		os.WriteFile(filepath.Join(out, "v2", "ThreadStartParams.json"), []byte(params), 0600)
		os.WriteFile(filepath.Join(out, "v2", "ThreadStartResponse.json"), []byte(`{"properties":{"instructionSources":{}}}`), 0600)
		os.WriteFile(filepath.Join(out, "v2", "ListMcpServerStatusParams.json"), []byte(`{"properties":{"threadId":{}}}`), 0600)
		os.Exit(0)
	}
	if mode == "hang" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if strings.HasPrefix(mode, "claude") {
		for _, want := range []string{"--no-session-persistence", "--strict-mcp-config", "--disable-slash-commands"} {
			if !slices.Contains(os.Args, want) {
				os.Exit(7)
			}
		}
		if os.Getenv("MAX_THINKING_TOKENS") != "0" || os.Getenv("CLAUDE_CODE_DISABLE_THINKING") != "1" || os.Getenv("CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING") != "" {
			os.Exit(11)
		}
		settings := slices.Index(os.Args, "--settings")
		if settings < 0 || !strings.Contains(os.Args[settings+1], `"alwaysThinkingEnabled":false`) {
			os.Exit(12)
		}
		if mode == "claude_default" {
			if slices.Contains(os.Args, "--model") {
				os.Exit(14)
			}
			idx := slices.Index(os.Args, "--setting-sources")
			if idx < 0 || os.Args[idx+1] != "user" {
				os.Exit(15)
			}
			fmt.Print(`{"type":"result","subtype":"success","structured_output":{"answer":"ok"},"modelUsage":{"profile-default":{}},"usage":{"input_tokens":9}}`)
			os.Exit(0)
		}
		if mode == "claude_prelude" {
			fmt.Println("Login complete.\nStarting inference.")
		}
		if mode == "claude_error" {
			fmt.Print(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"enabled is not supported for this model thinking secret-token","usage":{"input_tokens":0}}`)
			os.Exit(1)
		}
		if mode == "claude_auth" || mode == "claude_rate_limit" {
			message := "HTTP 401 authentication rejected secret-token"
			if mode == "claude_rate_limit" {
				message = "HTTP 429 rate limit secret-token"
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": message})
			os.Exit(1)
		}
		if mode == "claude_bad" {
			fmt.Print("broken JSON")
			os.Exit(0)
		}
		fmt.Print(`{"type":"result","subtype":"success","is_error":false,"structured_output":{"answer":"ok"},"usage":{"input_tokens":9}}`)
		os.Exit(0)
	}
	switch mode {
	case "codex_startup_hang":
		time.Sleep(time.Minute)
	case "codex_startup_banner":
		fmt.Println("Update available: secret-token")
	case "codex_startup_auth":
		fmt.Println("Open https://example.invalid/login?token=secret-token to sign in.")
	case "codex_startup_noise":
		fmt.Println("unexpected wrapper failure secret-token")
	case "codex_startup_json":
		fmt.Println(`{"id":1,broken`)
	case "codex_startup_empty_frame":
		fmt.Println(`{}`)
	case "codex_startup_blank":
		fmt.Println()
	}
	scanner := bufio.NewScanner(os.Stdin)
	send := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	note := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	for scanner.Scan() {
		var req struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(8)
		}
		if path := os.Getenv("COCO_RPC_METHODS"); path != "" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(18)
			}
			fmt.Fprintln(file, req.Method)
			file.Close()
		}
		switch req.Method {
		case "config/read":
			send(map[string]any{"id": req.ID, "result": map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"personal.server": map[string]any{"token": "private"}}, "plugins": map[string]any{"plugin@source": map[string]any{}}}}})
		case "mcpServerStatus/list":
			if req.Params["threadId"] != "thread1" {
				os.Exit(16)
			}
			data := []any{}
			if mode == "codex_mcp" {
				data = append(data, map[string]any{"name": "unexpected"})
			}
			send(map[string]any{"id": req.ID, "result": map[string]any{"data": data, "nextCursor": nil}})
		case "initialize":
			send(map[string]any{"id": req.ID, "result": map[string]any{"userAgent": "fixture"}})
		case "thread/start":
			config := req.Params["config"].(map[string]any)
			if config["mcp_servers"].(map[string]any)["personal.server"].(map[string]any)["enabled"] != false || config["plugins"].(map[string]any)["plugin@source"].(map[string]any)["enabled"] != false {
				os.Exit(17)
			}
			if req.Params["ephemeral"] != true || len(req.Params["environments"].([]any)) != 0 {
				os.Exit(9)
			}
			model := "fixture"
			if mode == "codex_default" {
				if _, present := req.Params["model"]; present {
					os.Exit(14)
				}
				model = "profile-default"
			}
			send(map[string]any{"id": req.ID, "result": map[string]any{"thread": map[string]any{"id": "thread1"}, "model": model, "instructionSources": []string{}}})
		case "turn/start":
			if mode == "codex_late_noise" {
				fmt.Println("unexpected stdout after handshake: secret-token")
			}
			if req.Params["outputSchema"] == nil {
				os.Exit(10)
			}
			if mode == "codex_exit" {
				os.Exit(0)
			}
			if mode == "codex_tool" {
				note("item/started", map[string]any{"threadId": "thread1", "turnId": "turn1", "item": map[string]any{"type": "commandExecution"}})
				continue
			}
			if mode == "codex_permission" {
				send(map[string]any{"id": 91, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
				continue
			}
			// Deliberately deliver final events before the request receipt.
			note("item/completed", map[string]any{"threadId": "thread1", "turnId": "turn1", "item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": `{"answer":"ok"}`}})
			note("thread/tokenUsage/updated", map[string]any{"threadId": "thread1", "turnId": "turn1", "tokenUsage": map[string]any{"total": 9}})
			status := "completed"
			if mode == "codex_failed" {
				status = "failed"
			}
			note("turn/completed", map[string]any{"threadId": "thread1", "turn": map[string]any{"id": "turn1", "status": status}})
			send(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": "turn1"}}})
		}
	}
	os.Exit(0)
}

func fixtureRunner(t *testing.T, provider, mode string) Local {
	t.Helper()
	root := t.TempDir()
	return Local{Config: Config{Provider: provider, Command: os.Args[0], Args: []string{"-test.run=^TestProviderProcessFixture$", "--"}, Model: "fixture", ConfigRoot: filepath.Join(root, "profile"), WorkRoot: filepath.Join(root, "work"), Env: []string{"COCO_INFERENCE_FIXTURE=" + mode}, Timeout: 3 * time.Second}}
}
func fixtureRequest() Request {
	return Request{System: "Return the answer.", Input: json.RawMessage(`{"text":"hello"}`), Schema: json.RawMessage(`{"type":"object"}`)}
}

func TestCodexWaitsForCompletionAndHandlesEarlyEvents(t *testing.T) {
	r := fixtureRunner(t, "codex", "codex_ok")
	response, err := r.Infer(context.Background(), fixtureRequest())
	if err != nil || response.Text != `{"answer":"ok"}` || len(response.Usage) == 0 || !strings.Contains(response.Visible, "completed") {
		t.Fatalf("response=%+v error=%v", response, err)
	}
}

func TestNaturalCompletionStillHonorsCallerCancellation(t *testing.T) {
	r := fixtureRunner(t, "codex", "codex_ok")
	r.Config.Timeout = 0
	if response, err := r.Infer(context.Background(), fixtureRequest()); err != nil || response.Text != `{"answer":"ok"}` {
		t.Fatalf("unlimited inference failed: %+v %v", response, err)
	}
	r = fixtureRunner(t, "claude", "hang")
	r.Config.Timeout = 0
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.Infer(ctx, fixtureRequest()); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("caller cancellation ignored", err)
	}
}

func TestCodexStartupFailuresRemainBoundedAndPrivate(t *testing.T) {
	for _, test := range []struct{ mode, want string }{
		{"codex_startup_auth", "invalid protocol JSON"},
		{"codex_startup_noise", "invalid protocol JSON"},
		{"codex_startup_json", "invalid protocol JSON"},
		{"codex_startup_empty_frame", "invalid protocol frame"},
		{"codex_startup_banner", "invalid protocol JSON"},
		{"codex_startup_blank", "invalid protocol JSON"},
		{"codex_late_noise", "invalid protocol JSON"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			r := fixtureRunner(t, "codex", test.mode)
			response, err := r.Infer(context.Background(), fixtureRequest())
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "secret-token") || response.Text != "" {
				t.Fatalf("startup failure mishandled: %+v %v", response, err)
			}
		})
	}
	r := fixtureRunner(t, "codex", "codex_startup_hang")
	r.Config.Timeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := r.Infer(context.Background(), fixtureRequest()); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("startup escaped deadline: %v", err)
	}
}

func TestCodexCheckUsesHandshakeWithoutModelRequest(t *testing.T) {
	r := fixtureRunner(t, "codex", "codex_ok")
	methods := filepath.Join(t.TempDir(), "methods")
	r.Config.Env = append(r.Config.Env, "COCO_RPC_METHODS="+methods)
	if err := r.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(methods)
	if err != nil || !strings.Contains(string(data), "initialize\n") || strings.Contains(string(data), "thread/start") || strings.Contains(string(data), "turn/start") {
		t.Fatalf("capability check did not stay in handshake: %s %v", data, err)
	}
	if err := fixtureRunner(t, "codex", "codex_startup_auth").Check(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid protocol JSON") {
		t.Fatalf("check accepted a broken startup: %v", err)
	}
}

func TestCodexFailuresNeverBecomeSuccessfulResults(t *testing.T) {
	for _, mode := range []string{"codex_failed", "codex_exit", "codex_tool", "codex_permission", "codex_unsupported", "codex_mcp"} {
		t.Run(mode, func(t *testing.T) {
			r := fixtureRunner(t, "codex", mode)
			_, err := r.Infer(context.Background(), fixtureRequest())
			if err == nil {
				t.Fatal("invalid provider run succeeded")
			}
		})
	}
}

func TestClaudeStructuredResultAndMalformedOutput(t *testing.T) {
	r := fixtureRunner(t, "claude", "claude_ok")
	response, err := r.Infer(context.Background(), fixtureRequest())
	if err != nil || response.Text != `{"answer":"ok"}` || len(response.Usage) == 0 {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	r = fixtureRunner(t, "claude", "claude_bad")
	response, err = r.Infer(context.Background(), fixtureRequest())
	if err == nil || response.Visible != "broken JSON" {
		t.Fatalf("malformed output lost: %+v %v", response, err)
	}
}

func TestClaudeWrapperPreludeAndFailureReceipt(t *testing.T) {
	r := fixtureRunner(t, "claude", "claude_prelude")
	response, err := r.Infer(context.Background(), fixtureRequest())
	if err != nil || response.Text != `{"answer":"ok"}` || !strings.HasPrefix(response.Visible, "Login complete.") {
		t.Fatalf("wrapper receipt lost: %+v %v", response, err)
	}
	r = fixtureRunner(t, "claude", "claude_error")
	response, err = r.Infer(context.Background(), fixtureRequest())
	if err == nil || !strings.Contains(err.Error(), "thinking_configuration_rejected") || strings.Contains(err.Error(), "secret-token") || len(response.Usage) == 0 || response.Text != "" {
		t.Fatalf("failed receipt mishandled: %+v %v", response, err)
	}
}

func TestClaudeRejectsAmbiguousOrTrailingEnvelopes(t *testing.T) {
	receipt := `{"type":"result","subtype":"success","result":"ok"}`
	for _, input := range []string{receipt + "\n" + receipt, receipt + "\ntrailing text", `{"type":"status"}` + "\n" + receipt, "{broken\n" + receipt, `[{"type":"result"}]`} {
		if _, err := decodeClaudeResult([]byte(input)); err == nil {
			t.Fatalf("accepted ambiguous receipt: %s", input)
		}
	}
}

func TestClaudeFailuresExplainRecoveryWithoutLeakingProviderText(t *testing.T) {
	for _, test := range []struct{ mode, want string }{
		{"claude_auth", "same Claude installation and profile"},
		{"claude_rate_limit", "next retry or a paused task"},
	} {
		r := fixtureRunner(t, "claude", test.mode)
		response, err := r.Infer(context.Background(), fixtureRequest())
		if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "secret-token") || response.Text != "" {
			t.Fatalf("failure lost safe recovery guidance: %v", err)
		}
		if !strings.Contains(response.Visible, "secret-token") {
			t.Fatal("private diagnostic receipt lost provider evidence")
		}
	}
}

func TestInferenceDeadlineTerminatesProcess(t *testing.T) {
	r := fixtureRunner(t, "claude", "hang")
	r.Config.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := r.Infer(context.Background(), fixtureRequest())
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("deadline not enforced: %v", err)
	}
}

func TestInferenceEnvironmentDoesNotInheritCaptureSettings(t *testing.T) {
	t.Setenv("COCONET_HOME", "must-not-inherit")
	t.Setenv("MAX_THINKING_TOKENS", "10000")
	t.Setenv("CLAUDE_CODE_DISABLE_THINKING", "0")
	t.Setenv("CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING", "1")
	r := fixtureRunner(t, "claude", "claude_ok")
	cmd := r.command(t.TempDir(), "--version")
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "COCONET_") && entry != "COCONET_INTERNAL_INFERENCE=1" {
			t.Fatalf("capture environment leaked: %s", entry)
		}
		if entry == "MAX_THINKING_TOKENS=10000" || entry == "CLAUDE_CODE_DISABLE_THINKING=0" || strings.HasPrefix(entry, "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=") {
			t.Fatalf("inherited incompatible thinking configuration: %s", entry)
		}
	}
}

func TestCapabilityCheckDoesNotStartModelTurn(t *testing.T) {
	if err := fixtureRunner(t, "codex", "codex_ok").Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixtureRunner(t, "codex", "codex_unsupported").Check(context.Background()); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
	for _, mode := range []string{"claude_help_native", "claude_help_wrapper", "claude_help_old"} {
		runner := fixtureRunner(t, "claude", mode)
		if mode == "claude_help_wrapper" {
			command := filepath.Join(t.TempDir(), "tclaude")
			if err := os.Symlink(runner.Config.Command, command); err != nil {
				t.Fatal(err)
			}
			runner.Config.Command = command
		}
		err := runner.Check(context.Background())
		if (err != nil) != (mode == "claude_help_old") {
			t.Fatal(mode, err)
		}
	}
}

func TestProvidersInheritAndReportDefaultModel(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			runner := fixtureRunner(t, provider, provider+"_default")
			runner.Config.Model = ""
			response, err := runner.Infer(context.Background(), fixtureRequest())
			if err != nil || response.Model != "profile-default" || response.Text != `{"answer":"ok"}` {
				t.Fatal(response, err)
			}
			entries, err := os.ReadDir(runner.Config.WorkRoot)
			if err != nil || len(entries) != 0 {
				t.Fatal("temporary inference workspace retained", entries, err)
			}
		})
	}
}

func TestCodexIsolationDoesNotCopySecretsOrMisparseNames(t *testing.T) {
	cfg, err := codexThreadIsolation([]byte(`{"config":{"mcp_servers":{"a.b":{"env":{"SECRET":"private-value"}}},"plugins":{"plugin@source":{}}}}`))
	data, _ := json.Marshal(cfg)
	if err != nil || cfg["mcp_servers"].(map[string]any)["a.b"].(map[string]any)["enabled"] != false || strings.Contains(string(data), "private-value") {
		t.Fatal(cfg, err)
	}
	for _, invalid := range []string{`{}`, `{"config":{"mcp_servers":[]}}`, `{"config":{"plugins":"bad"}}`} {
		if _, err := codexThreadIsolation([]byte(invalid)); err == nil {
			t.Fatal("accepted invalid configuration", invalid)
		}
	}
}
