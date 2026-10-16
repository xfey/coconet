package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

func TestHealthDoesNotLaunchCommand(t *testing.T) {
	adapter := New("tclaude")
	adapter.Locate = func(command string) (string, error) {
		if command != "tclaude" {
			t.Fatalf("unexpected command %q", command)
		}
		return "/fixture/tclaude", nil
	}
	if health := adapter.Health(context.Background()); health.Status != contracts.StatusOK {
		t.Fatalf("unexpected status %q", health.Status)
	}
}

func TestHelperStartupFailureHasActionableDiagnostic(t *testing.T) {
	root := t.TempDir()
	adapter := Adapter{ConfigRoot: root, NodeCommand: filepath.Join(root, "missing-node"), HelperPath: filepath.Join(root, "helper.mjs")}
	_, err := adapter.runHelper(context.Background(), helperRequest{Action: "observe", CWD: root})
	if err == nil || !strings.Contains(err.Error(), "Runtime bundle") || strings.Contains(err.Error(), root) {
		t.Fatalf("missing runtime diagnostic: %v", err)
	}
}

func TestSDKHelperRetainsToolInputsAndBothResultRepresentations(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to validate the SDK helper")
	}
	root := t.TempDir()
	configRoot := t.TempDir()
	artifactRoot := filepath.Join(configRoot, "projects", "fixture")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for _, id := range []string{"call", "string-result", "block-result", "host-error", "reported-error", "literal-marker", "mixed-call", "mixed-result", "meta", "sidechain", "unknown", "root-text"} {
		raw = append(raw, []byte(fmt.Sprintf("{\"uuid\":%q,\"parentUuid\":null}\n", id))...)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "source.jsonl"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	sdk := filepath.Join(t.TempDir(), "sdk.mjs")
	content := `export async function getSessionInfo() { return { sessionId: "source" }; }
export async function getSessionMessages() { return [
 { uuid: "call", type: "assistant", message: {content: [{type: "tool_use", id: "tool-1", name: "read_session", input: {server_session_id: "sess_sourcefixture01", version: 2}}]} },
 { uuid: "string-result", type: "user", message: {content: [{type: "tool_result", tool_use_id: "tool-1", is_error: true, content: "验证失败"}]} },
 { uuid: "block-result", type: "user", message: {content: [{type: "tool_result", tool_use_id: "tool-1", is_error: false, content: [{type: "text", text: "验证通过"}]}]} },
 { uuid: "host-error", type: "assistant", message: {model: "<synthetic>", content: [{type: "text", text: "API Error: host transport failed"}]} },
 { uuid: "reported-error", type: "assistant", message: {model: "fixture", content: [{type: "text", text: "API Error: the application request failed; its regression still needs a fix"}]} },
 { uuid: "literal-marker", type: "user", message: {content: [{type: "text", text: "Keep the literal <synthetic> marker in the test fixture"}]} },
 { uuid: "mixed-call", type: "assistant", message: {content: [{type:"text",text:"检查中文"},{type:"tool_use", id:"mix", name:"read", input:{path:"secret"}},{type:"text",text:"稍后说明"}]} },
 { uuid: "mixed-result", type: "user", message: {content: [{type:"tool_result",tool_use_id:"mix",content:[{type:"text",text:"hidden工具结果"}]},{type:"text",text:"用户追加"}]} },
 { uuid: "meta", type:"user", isMeta:true, message:{content:"injected"} },
 { uuid: "sidechain", type:"assistant", isSidechain:true, message:{content:"subagent"} },
 { uuid: "unknown", type:"assistant", message:{content:[{type:"unknown",text:"hidden"},{type:"thinking",text:"internal"},{type:"text",text:"可见代码 <tool_result>"}]} },
 { uuid: "root-text", type:"user", message:{text:"正文",content:"直接文本"} }
]; }
`
	if err := os.WriteFile(sdk, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs(filepath.Join("helper", "helper.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{Command: "tclaude", NodeCommand: node, HelperPath: helper, SDKEntry: sdk, ConfigRoot: configRoot}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observation, checkpoint, err := adapter.Observe(ctx, root, "source", adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Normalized.Turns) != 12 || observation.RawArtifact.Size != int64(len(raw)) {
		t.Fatal("SDK observation lost Turn or artifact identity")
	}
	for i, turn := range observation.Normalized.Turns[:3] {
		if turn.ToolName != "read_session" || !strings.Contains(turn.Text, "sess_sourcefixture01") || !strings.Contains(turn.Text, `"version":2`) {
			t.Fatalf("Turn %d lost its tool source", i)
		}
	}
	if !strings.Contains(observation.Normalized.Turns[1].Text, `"is_error":true`) || !strings.Contains(observation.Normalized.Turns[1].Text, "验证失败") || !strings.Contains(observation.Normalized.Turns[2].Text, "验证通过") {
		t.Fatal("SDK tool result status or content was lost")
	}
	if observation.Normalized.Turns[3].Kind != "event" || observation.Normalized.Turns[3].Text != "API Error: host transport failed" || observation.Normalized.Turns[4].Kind != "message" || observation.Normalized.Turns[5].Kind != "message" {
		t.Fatal("host metadata was not separated from actual work or literal user text")
	}
	if observation.VisibleText == nil {
		t.Fatal("missing visible projection")
	}
	visible := map[string]string{}
	for _, projected := range observation.VisibleText.Turns {
		for _, turn := range observation.Normalized.Turns {
			if turn.TurnID == projected.TurnID {
				visible[turn.TurnID] = adapters.VisibleText(turn.Text, projected.Ranges)
			}
		}
	}
	for id, want := range map[string]string{"mixed-call": "检查中文\n稍后说明", "mixed-result": "用户追加", "unknown": "可见代码 <tool_result>", "root-text": "正文\n直接文本", "meta": "", "sidechain": "", "host-error": "", "string-result": "", "block-result": ""} {
		if visible[id] != want {
			t.Fatalf("%s: got %q want %q", id, visible[id], want)
		}
	}
	duplicate, _, err := adapter.Observe(ctx, root, "source", checkpoint)
	if err != nil || len(duplicate.Normalized.Turns) != 0 || duplicate.VisibleText == nil || len(duplicate.VisibleText.Turns) != len(observation.VisibleText.Turns) {
		t.Fatal("unchanged SDK evidence was not deduplicated", err)
	}
}

func TestSDKActivityEvidenceRequiresStableCompleteMessageTimes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to validate the SDK helper")
	}
	helper, err := filepath.Abs(filepath.Join("helper", "helper.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, messages, want string
		mutate               bool
	}{
		{"bookkeeping-after-work", `[{type:"assistant",timestamp:"2026-09-10T11:24:07.473Z",uuid:"one",message:{content:"done"}}]`, "2026-09-10T11:24:07.473Z", false},
		{"later-work", `[{type:"user",timestamp:"2026-09-10T11:25:00.000Z",uuid:"one",message:{content:"continue"}},{type:"assistant",timestamp:"2026-09-10T11:24:07.473Z",uuid:"two",message:{content:"done"}}]`, "2026-09-10T11:25:00.000Z", false},
		{"missing-time", `[{type:"assistant",timestamp:"2026-09-10T11:24:07.473Z",uuid:"one",message:{content:"done"}},{type:"user",uuid:"two",message:{content:"continue"}}]`, "", false},
		{"invalid-time", `[{type:"user",timestamp:"invalid",uuid:"one",message:{content:"continue"}}]`, "", false},
		{"empty", `[]`, "", false},
		{"changed-during-read", `[{type:"assistant",timestamp:"2026-09-10T11:24:07.473Z",uuid:"one",message:{content:"done"}}]`, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			config := t.TempDir()
			artifact := filepath.Join(config, "projects", "fixture", "source.jsonl")
			if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
				t.Fatal(err)
			}
			raw := []byte("{\"type\":\"cost-state\"}\n{\"uuid\":\"one\"}\n{\"uuid\":\"two\"}\n")
			if err := os.WriteFile(artifact, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			sdk := filepath.Join(root, "sdk.mjs")
			content := fmt.Sprintf(`import {appendFile} from "node:fs/promises";
export async function getSessionInfo() {return {sessionId:"source"};}
export async function getSessionMessages() {if (%t) await appendFile(%q,JSON.stringify({type:"cost-state"})+"\n");return %s;}`, test.mutate, artifact, test.messages)
			if err := os.WriteFile(sdk, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			adapter := Adapter{Command: "tclaude", NodeCommand: node, HelperPath: helper, SDKEntry: sdk, ConfigRoot: config}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			observed, _, err := adapter.Observe(ctx, root, "source", adapters.ObservationCheckpoint{})
			if test.mutate {
				if err == nil || !strings.Contains(err.Error(), "changed during structured observation") {
					t.Fatalf("unstable capture accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if observed.Session.LastActivityAt != test.want {
				t.Fatalf("last activity = %q; want %q", observed.Session.LastActivityAt, test.want)
			}
			hash, size, err := adapters.HashFile(artifact)
			if err != nil || observed.RawArtifact.SHA256 != hash || observed.RawArtifact.Size != size {
				t.Fatal("activity evidence changed raw artifact identity", err)
			}
		})
	}
}

func TestForkUsesHelperOnceAndReplaysNativeMarker(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(artifact, []byte("opaque source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, _, err := adapters.HashFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	adapter := New("tclaude")
	forkCalls := 0
	childID := "01980000-0000-7000-8000-000000000002"
	adapter.run = func(_ context.Context, request helperRequest) ([]byte, error) {
		switch request.Action {
		case "fork":
			forkCalls++
			return json.Marshal(adapters.ForkResult{NativeSessionID: childID})
		case "verify_fork":
			return json.Marshal(adapters.ForkResult{NativeSessionID: request.NativeSessionID})
		default:
			return nil, errors.New("unexpected action")
		}
	}
	request := adapters.ForkRequest{
		AttemptDirectory: t.TempDir(), SourceArtifactPath: artifact, SourceArtifactSHA256: hash,
		SourceNativeSessionID: "01980000-0000-7000-8000-000000000001", CWD: root,
	}
	first, err := adapter.Fork(context.Background(), request)
	if err != nil || first.NativeSessionID != childID || forkCalls != 1 {
		t.Fatalf("unexpected first Fork %#v calls=%d err=%v", first, forkCalls, err)
	}
	second, err := adapter.Fork(context.Background(), request)
	if err != nil || second.NativeSessionID != childID || forkCalls != 1 {
		t.Fatalf("marker replay created another Fork %#v calls=%d err=%v", second, forkCalls, err)
	}
	command := adapter.ResumeCommand(second.NativeSessionID)
	if command.Program != "tclaude" || len(command.Args) != 2 || command.Args[0] != "--resume" {
		t.Fatalf("unexpected Resume command %#v", command)
	}
}

func TestHealthReportsMissingCommand(t *testing.T) {
	adapter := New("tclaude")
	adapter.Locate = func(string) (string, error) { return "", errors.New("missing") }
	if health := adapter.Health(context.Background()); health.Status != contracts.StatusUnavailable {
		t.Fatalf("unexpected status %q", health.Status)
	}
}

func TestDiscoverAndObserveValidateHelperResults(t *testing.T) {
	root := t.TempDir()
	adapter := New("tclaude")
	adapter.run = func(_ context.Context, request helperRequest) ([]byte, error) {
		switch request.Action {
		case "discover":
			return json.Marshal(contracts.SessionDiscovery{SchemaVersion: 1, Sessions: []contracts.DiscoveredSession{
				{SchemaVersion: 1, AgentType: contracts.AgentClaude, NativeSessionID: "inside", CWD: root, ActivityState: contracts.ActivityUnknown, UpdatedAt: "2026-08-24T08:00:00Z"},
				{SchemaVersion: 1, AgentType: contracts.AgentClaude, NativeSessionID: "outside", CWD: filepath.Dir(root), ActivityState: contracts.ActivityUnknown, UpdatedAt: "2026-08-24T09:00:00Z"},
			}})
		case "observe":
			return json.Marshal(contracts.SessionObservation{
				SchemaVersion: 1,
				Session:       contracts.SessionEnvelope{SchemaVersion: 1, AgentType: contracts.AgentClaude, NativeSessionID: "inside", CWD: root, ActivityState: contracts.ActivityUnknown, SourceObserved: "2026-08-24T08:00:00Z", ClientVersion: "2.1.154", AdapterName: "claude-agent-sdk", AdapterVersion: "1"},
				RawArtifact:   contracts.ArtifactObservation{Path: filepath.Join(root, "inside.jsonl"), MediaType: "application/x-ndjson", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 8},
				Normalized:    contracts.NormalizedObservation{Turns: []contracts.NormalizedTurn{{TurnID: "turn-1", Ordinal: 0, Role: "user", Kind: "message", Text: "fixture", ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}},
			})
		default:
			return nil, errors.New("unexpected action")
		}
	}
	sessions, err := adapter.Discover(context.Background(), root)
	if err != nil || len(sessions) != 1 || sessions[0].NativeSessionID != "inside" {
		t.Fatalf("unexpected discovery %#v: %v", sessions, err)
	}
	observation, checkpoint, err := adapter.Observe(context.Background(), root, "inside", adapters.ObservationCheckpoint{})
	if err != nil || observation.Normalized.ToCursor == "" || len(observation.Normalized.Turns) != 1 || checkpoint.Cursor == "" {
		t.Fatalf("unexpected observation %#v: %v", observation, err)
	}
}

func TestDiscoverReportsPartialInventory(t *testing.T) {
	root := t.TempDir()
	adapter := New("tclaude")
	adapter.run = func(context.Context, helperRequest) ([]byte, error) {
		return json.Marshal(contracts.SessionDiscovery{SchemaVersion: 1, Truncated: true, Sessions: []contracts.DiscoveredSession{{AgentType: contracts.AgentClaude, NativeSessionID: "inside", CWD: root}}})
	}
	sessions, err := adapter.Discover(context.Background(), root)
	if !errors.Is(err, adapters.ErrDiscoveryLimit) || len(sessions) != 1 {
		t.Fatal("partial inventory was reported as complete", err)
	}
}
