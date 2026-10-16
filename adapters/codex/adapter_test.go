package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

func TestHealthDoesNotLaunchCommand(t *testing.T) {
	adapter := New("codex-fixture")
	adapter.Locate = func(command string) (string, error) {
		if command != "codex-fixture" {
			t.Fatalf("unexpected command %q", command)
		}
		return "/fixture/codex-fixture", nil
	}
	if health := adapter.Health(context.Background()); health.Status != contracts.StatusOK {
		t.Fatalf("unexpected status %q", health.Status)
	}
}

func TestObserveRetainsToolProvenanceAndExecutionResult(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "fixture.jsonl")
	artifactBytes := writeTestRollout(t, artifact, "inside", "legacy", nil, "native evidence")
	fake := &fakeClient{responses: map[string]any{
		"thread/read": map[string]any{"thread": map[string]any{
			"id": "inside", "cwd": root, "path": artifact,
			"turns": []map[string]any{{"id": "turn-1", "items": []map[string]any{
				{"type": "userMessage", "text": "检查恢复流程"},
				{"type": "mcpToolCall", "tool": "read_session", "server": "coconet", "arguments": map[string]any{"server_session_id": "sess_sourcefixture01", "version": 2}, "status": "completed", "result": map[string]any{"content": []any{map[string]any{"text": "来源结果"}}}},
				{"type": "commandExecution", "command": "verify recovery", "status": "completed", "exitCode": 1, "aggregatedOutput": "验证失败"},
				{"type": "agentMessage", "text": "尚未完成恢复验证"},
			}}},
		}},
	}}
	adapter := New("codex-fixture")
	adapter.ConfigRoot = t.TempDir()
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	observation, checkpoint, err := adapter.Observe(context.Background(), root, "inside", adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Normalized.Turns) != 4 || observation.RawArtifact.Size != int64(len(artifactBytes)) || observation.Session.AdapterVersion != "6" {
		t.Fatal("normalization lost item boundaries or changed the opaque artifact")
	}
	roles := []string{"user", "tool", "tool", "assistant"}
	var texts []string
	for index, turn := range observation.Normalized.Turns {
		if turn.Role != roles[index] || turn.Ordinal != index {
			t.Fatal("native item role or source order was lost")
		}
		texts = append(texts, turn.Text)
	}
	if texts[0] != "检查恢复流程" || texts[3] != "尚未完成恢复验证" || observation.Normalized.Turns[1].ToolName != "read_session" {
		t.Fatal("tool evidence contaminated a message or lost its tool identity")
	}
	text := strings.Join(texts, "\n")
	for _, evidence := range []string{"检查恢复流程", "read_session", "sess_sourcefixture01", `"version":2`, "来源结果", `"exitCode":1`, "verify recovery", "验证失败", "尚未完成恢复验证"} {
		if !strings.Contains(text, evidence) {
			t.Fatalf("normalized evidence lost %q", evidence)
		}
	}
	duplicate, _, err := adapter.Observe(context.Background(), root, "inside", checkpoint)
	if err != nil || len(duplicate.Normalized.Turns) != 0 || duplicate.VisibleText == nil || len(duplicate.VisibleText.Turns) != 2 {
		t.Fatal("unchanged structured evidence was not deduplicated", err)
	}
	legacyText := "检查恢复流程\n尚未完成恢复验证"
	_, legacy := adapters.ApplyCheckpoint([]contracts.NormalizedTurn{{TurnID: "turn-1", Ordinal: 0, Role: "user", Kind: "message", Text: legacyText, ContentHash: adapters.Digest("user\x00message\x00" + legacyText)}}, adapters.ObservationCheckpoint{})
	upgraded, _, err := adapter.Observe(context.Background(), root, "inside", legacy)
	if err != nil || !upgraded.Normalized.Reset || upgraded.Normalized.SourceEpoch != 2 || len(upgraded.Normalized.Turns) != 4 || upgraded.Normalized.Turns[0].TurnID != "turn-1/item/0" {
		t.Fatal("evidence enrichment did not use the existing cumulative revision path", err)
	}
}

func TestObserveItemAppendAndRevisionKeepCheckpointBoundaries(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "fixture.jsonl")
	writeTestRollout(t, artifact, "inside", "legacy", nil, "native evidence")
	items := []map[string]any{
		{"type": "userMessage", "text": "调查失败原因"},
		{"type": "reasoning", "text": "internal reasoning"},
		{"type": "commandExecution", "command": "check", "status": "inProgress", "aggregatedOutput": strings.Repeat("diagnostic output\n", 1000)},
	}
	response := func(items []map[string]any) any {
		return map[string]any{"thread": map[string]any{
			"id": "inside", "cwd": root, "path": artifact,
			"turns": []map[string]any{{"id": "native-turn", "items": items}},
		}}
	}
	fake := &fakeClient{responses: map[string]any{"thread/read": response(items)}}
	adapter := New("codex-fixture")
	adapter.ConfigRoot = t.TempDir()
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	first, checkpoint, err := adapter.Observe(context.Background(), root, "inside", adapters.ObservationCheckpoint{})
	if err != nil || len(first.Normalized.Turns) != 3 {
		t.Fatal("initial structured observation failed", err)
	}
	if first.Normalized.Turns[0].Text != "调查失败原因" || first.Normalized.Turns[1].Kind != "event" || first.Normalized.Turns[2].Role != "tool" {
		t.Fatal("large tool output or internal event entered user evidence")
	}
	items = append(items, map[string]any{"type": "agentMessage", "text": "原因尚未确认"})
	fake.responses["thread/read"] = response(items)
	appended, checkpoint, err := adapter.Observe(context.Background(), root, "inside", checkpoint)
	if err != nil || appended.Normalized.Reset || len(appended.Normalized.Turns) != 1 || appended.Normalized.Turns[0].Ordinal != 3 || appended.Normalized.Turns[0].Role != "assistant" {
		t.Fatal("appending an assistant result rewrote earlier item identities", err)
	}
	items[2]["status"], items[2]["exitCode"] = "completed", 1
	fake.responses["thread/read"] = response(items)
	revised, _, err := adapter.Observe(context.Background(), root, "inside", checkpoint)
	if err != nil || !revised.Normalized.Reset || revised.Normalized.SourceEpoch != 2 || len(revised.Normalized.Turns) != 4 {
		t.Fatal("revised tool result did not advance the cumulative source epoch", err)
	}
	if revised.Normalized.Turns[0].TurnID != first.Normalized.Turns[0].TurnID || revised.Normalized.Turns[2].TurnID != first.Normalized.Turns[2].TurnID {
		t.Fatal("tool completion changed stable item identities")
	}
}

func TestForkImportsOpaqueArtifactCreatesOneNativeChildAndReplaysMarker(t *testing.T) {
	root := t.TempDir()
	configRoot := t.TempDir()
	attemptRoot := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "source.jsonl")
	writeTestRollout(t, artifact, "01980000-0000-7000-8000-000000000001", "legacy", nil, "native source")
	hash, _, err := adapters.HashFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	client := &forkFakeClient{sourceID: "01980000-0000-7000-8000-000000000001", childID: "01980000-0000-7000-8000-000000000002"}
	adapter := New("codex-fixture")
	adapter.ConfigRoot = configRoot
	starts := 0
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) {
		starts++
		return client, nil
	}
	request := adapters.ForkRequest{
		AttemptDirectory: attemptRoot, SourceArtifactPath: artifact, SourceArtifactSHA256: hash,
		SourceNativeSessionID: client.sourceID, CWD: root,
	}
	first, err := adapter.Fork(context.Background(), request)
	if err != nil || first.NativeSessionID != client.childID || client.forkCalls != 1 {
		t.Fatalf("unexpected first Fork %#v calls=%d err=%v", first, client.forkCalls, err)
	}
	if _, err := os.Stat(client.importedPath); !errors.Is(err, os.ErrNotExist) || starts != 2 {
		t.Fatalf("Fork was not verified in a fresh process after source cleanup: starts=%d err=%v", starts, err)
	}
	second, err := adapter.Fork(context.Background(), request)
	if err != nil || second.NativeSessionID != client.childID || client.forkCalls != 1 {
		t.Fatalf("marker replay created another Fork %#v calls=%d err=%v", second, client.forkCalls, err)
	}
	command := adapter.ResumeCommand(second.NativeSessionID)
	if command.Program != "codex-fixture" || len(command.Args) != 2 || command.Args[0] != "resume" {
		t.Fatalf("unexpected Resume command %#v", command)
	}
}

func TestHealthReportsMissingCommand(t *testing.T) {
	adapter := New("codex-fixture")
	adapter.Locate = func(string) (string, error) { return "", errors.New("missing") }
	if health := adapter.Health(context.Background()); health.Status != contracts.StatusUnavailable {
		t.Fatalf("unexpected status %q", health.Status)
	}
}

func TestDiscoverFiltersOutsideRootAndObserveUsesOpaqueArtifact(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "fixture.jsonl")
	artifactBytes := writeTestRollout(t, artifact, "inside", "legacy", nil, "native evidence")
	active, _ := json.Marshal(map[string]string{"type": "active"})
	fake := &fakeClient{responses: map[string]any{
		"thread/list": map[string]any{"data": []thread{
			{ID: "inside", CWD: root, CreatedAt: 1, UpdatedAt: 2, Status: active},
			{ID: "outside", CWD: filepath.Dir(root), CreatedAt: 1, UpdatedAt: 3},
		}},
		"thread/read": map[string]any{"thread": map[string]any{
			"id": "inside", "sessionId": "root", "cwd": root, "path": artifact, "cliVersion": "0.144.5", "status": map[string]string{"type": "active"},
			"turns": []map[string]any{{"id": "turn-1", "items": []map[string]any{{"type": "userMessage", "text": "fixture"}}}},
		}},
	}}
	adapter := New("codex-fixture")
	adapter.ConfigRoot = t.TempDir()
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	sessions, err := adapter.Discover(context.Background(), root)
	if err != nil || len(sessions) != 1 || sessions[0].NativeSessionID != "inside" {
		t.Fatalf("unexpected discovery %#v: %v", sessions, err)
	}
	observation, checkpoint, err := adapter.Observe(context.Background(), root, "inside", adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RawArtifact.Path != artifact || observation.RawArtifact.Size != int64(len(artifactBytes)) || len(observation.Normalized.Turns) != 1 || observation.Normalized.ToCursor == "" || checkpoint.Cursor == "" {
		t.Fatalf("unexpected observation %#v", observation)
	}
}

type fakeClient struct{ responses map[string]any }

func TestDiscoverRejectsRepeatedCursor(t *testing.T) {
	root := t.TempDir()
	adapter := New("codex-fixture")
	adapter.ConfigRoot = t.TempDir()
	fake := &fakeClient{responses: map[string]any{"thread/list": map[string]any{"data": []thread{}, "nextCursor": "same-cursor"}}}
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	if _, err := adapter.Discover(context.Background(), root); err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatal("discovery loop failed to detect repeated cursor", err)
	}
}

func (client *fakeClient) Request(method string, _ any, target any) error {
	content, err := json.Marshal(client.responses[method])
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func (*fakeClient) Close() error { return nil }

type forkFakeClient struct {
	sourceID     string
	childID      string
	forkCalls    int
	importedPath string
}

func (client *forkFakeClient) Request(method string, params, target any) error {
	var response any
	switch method {
	case "thread/read":
		if _, err := os.Stat(client.importedPath); !errors.Is(err, os.ErrNotExist) {
			return errors.New("verification ran while the temporary source still existed")
		}
		threadID := params.(map[string]any)["threadId"].(string)
		response = map[string]any{"thread": map[string]any{"id": threadID, "forkedFromId": client.sourceID, "cwd": "/fixture", "turns": []any{}}}
	case "thread/fork":
		arguments := params.(map[string]any)
		config, _ := arguments["config"].(map[string]any)
		if config["features.hooks"] != false {
			return errors.New("internal Fork must not emit lifecycle Hooks before Coconet registers the child")
		}
		client.importedPath, _ = arguments["path"].(string)
		if arguments["cwd"] == "" || client.importedPath == "" {
			return errors.New("Fork must select the exact fixed source path and destination cwd")
		}
		segment, err := openRolloutSegment(client.importedPath, client.sourceID, 0)
		if err != nil {
			return err
		}
		_ = segment.file.Close()
		if segment.metadata.HistoryMode != "legacy" || segment.metadata.HistoryBase != nil {
			return errors.New("Fork import is not independent")
		}
		client.forkCalls++
		response = map[string]any{"thread": map[string]any{"id": client.childID, "forkedFromId": client.sourceID, "cwd": "/fixture", "turns": []any{}}}
	default:
		return errors.New("unexpected method")
	}
	content, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func (*forkFakeClient) Close() error { return nil }

func TestObserveEffectiveBranchRollbackAndStableArtifact(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "fixture.jsonl")
	writeTestRollout(t, artifact, "inside", "legacy", nil, "native evidence")
	response := func(labels ...string) any {
		turns := []map[string]any{}
		for _, label := range labels {
			turns = append(turns, map[string]any{"id": label, "items": []map[string]any{{"type": "userMessage", "text": label}}})
		}
		return map[string]any{"thread": map[string]any{"id": "inside", "cwd": root, "path": artifact, "turns": turns}}
	}
	fake := &fakeClient{responses: map[string]any{"thread/read": response("A", "B", "C")}}
	adapter := New("codex-fixture")
	adapter.ConfigRoot = root
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	_, checkpoint, err := adapter.Observe(context.Background(), root, "inside", adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	for _, labels := range [][]string{{"A", "B", "D"}, {}} {
		fake.responses["thread/read"] = response(labels...)
		observed, _, err := adapter.Observe(context.Background(), root, "inside", checkpoint)
		if err != nil || observed.ActiveHistory == nil || len(observed.ActiveHistory.IDs) != len(labels) {
			t.Fatal(observed, err)
		}
		for i, label := range labels {
			if observed.ActiveHistory.IDs[i] != label+"/item/0" {
				t.Fatal(observed.ActiveHistory)
			}
		}
	}
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) {
		return mutatingHistoryClient{fake, artifact}, nil
	}
	if _, _, err := adapter.Observe(context.Background(), root, "inside", adapters.ObservationCheckpoint{}); err == nil {
		t.Fatal("accepted history that changed during structured read")
	}
}

type mutatingHistoryClient struct {
	*fakeClient
	path string
}

func TestObserveRejectsRevertThatOnlyChangesTheActivePath(t *testing.T) {
	root := t.TempDir()
	oldPath, newPath := filepath.Join(root, "old.jsonl"), filepath.Join(root, "new.jsonl")
	writeTestRollout(t, oldPath, "inside", "legacy", nil, "old evidence")
	writeTestRollout(t, newPath, "inside", "legacy", nil, "new evidence")
	client := &switchingBranchClient{root: root, oldPath: oldPath, newPath: newPath}
	adapter := New("codex")
	adapter.ConfigRoot = root
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return client, nil }
	if _, _, err := adapter.Observe(context.Background(), root, "inside", adapters.ObservationCheckpoint{}); err == nil || !strings.Contains(err.Error(), "branch changed") {
		t.Fatal("accepted mixed branches while the old physical file stayed stable", err)
	}
}

type switchingBranchClient struct {
	root, oldPath, newPath string
	reads                  int
}

func (c *switchingBranchClient) Close() error { return nil }
func (c *switchingBranchClient) Request(method string, params, target any) error {
	c.reads++
	path := c.oldPath
	if c.reads == 3 {
		path = c.newPath
	}
	data, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "inside", "cwd": c.root, "path": path}})
	return json.Unmarshal(data, target)
}

func (c mutatingHistoryClient) Request(method string, params any, target any) error {
	if method == "thread/read" && params.(map[string]any)["includeTurns"] == true {
		f, err := os.OpenFile(c.path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString("\n")
		f.Close()
		if err != nil {
			return err
		}
	}
	return c.fakeClient.Request(method, params, target)
}

func TestOfficialImportMarkerIsLifecycleOnlyInsideImportTurn(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "fixture.jsonl")
	writeTestRollout(t, artifact, "imported", "legacy", nil, "native evidence")
	fake := &fakeClient{responses: map[string]any{"thread/read": map[string]any{"thread": map[string]any{
		"id": "imported", "cwd": root, "path": artifact, "turns": []map[string]any{
			{"id": "external-import-turn-1", "items": []map[string]any{{"type": "userMessage", "text": "user"}, {"type": "agentMessage", "text": "<EXTERNAL SESSION IMPORTED>"}, {"type": "agentMessage", "text": "<EXTERNAL SESSION IMPORTED>"}}},
			{"id": "ordinary-turn", "items": []map[string]any{{"type": "agentMessage", "text": "<EXTERNAL SESSION IMPORTED>"}}},
		},
	}}}}
	adapter := New("fixture")
	adapter.ConfigRoot = t.TempDir()
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	observed, _, err := adapter.Observe(context.Background(), root, "imported", adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.VisibleText.Turns) != 3 || observed.Normalized.Turns[2].Kind != "event" || observed.Normalized.Turns[1].Kind != "message" || observed.Normalized.Turns[3].Kind != "message" {
		t.Fatal("ordinary quoted marker was removed or lifecycle marker leaked", observed.Normalized)
	}
}
