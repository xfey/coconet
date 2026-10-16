package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
)

type textImportClient struct {
	cwd, profile, source string
	calls                int
	lose                 bool
	noLedger             bool
	reject               bool
	failure              bool
}

func (f *textImportClient) Close() error { return nil }
func (f *textImportClient) Request(method string, params, target any) error {
	var value any
	switch method {
	case "externalAgentConfig/import":
		f.calls++
		if f.reject {
			return &rpcError{Method: method, Code: -32601, Message: "unsupported import"}
		}
		items := params.(map[string]any)["migrationItems"].([]map[string]any)
		if len(items) != 1 || items[0]["itemType"] != "SESSIONS" {
			return errors.New("unexpected config migration")
		}
		session := items[0]["details"].(map[string]any)["sessions"].([]map[string]any)[0]
		f.source = session["path"].(string)
		hash, _, err := adapters.HashFile(f.source)
		if err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"records": []map[string]string{{"source_path": f.source, "content_sha256": hash, "imported_thread_id": "imported-child"}}})
		if !f.noLedger {
			if err := os.WriteFile(filepath.Join(f.profile, "external_agent_session_imports.json"), data, 0600); err != nil {
				return err
			}
		}
		value = map[string]string{"importId": "operation"}
	case "thread/read":
		value = map[string]any{"thread": map[string]string{"id": "imported-child", "cwd": f.cwd}}
	default:
		return errors.New("unexpected method " + method)
	}
	data, _ := json.Marshal(value)
	return json.Unmarshal(data, target)
}
func (f *textImportClient) WaitNotification(method, id string, target any) error {
	if method != "externalAgentConfig/import/completed" || id != "operation" {
		return errors.New("wrong notification")
	}
	if f.lose {
		return errors.New("lost import completion")
	}
	if f.failure {
		return json.Unmarshal([]byte(`{"itemTypeResults":[{"itemType":"SESSIONS","failures":[{"message":"source unavailable","failureStage":"read"}]}]}`), target)
	}
	data, _ := json.Marshal(map[string]any{"itemTypeResults": []any{map[string]any{"itemType": "SESSIONS", "successes": []any{map[string]string{"source": f.source, "target": "imported-child"}}}}})
	return json.Unmarshal(data, target)
}
func portableFixture(t *testing.T) adapters.TextImportRequest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compact.jsonl")
	if err := os.WriteFile(path, []byte("{\"turn_id\":\"u\",\"role\":\"user\",\"text\":\"保留原接口\"}\n{\"turn_id\":\"a\",\"role\":\"assistant\",\"text\":\"理解，先补测试。\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hash, _, _ := adapters.HashFile(path)
	return adapters.TextImportRequest{AttemptDirectory: t.TempDir(), CompactPath: path, CompactSHA256: hash, CWD: t.TempDir(), ObservedAt: "2026-10-15T01:02:03Z"}
}
func TestTextImportRecoversOfficialLedgerAfterLostCompletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	request := portableFixture(t)
	adapter := New("fixture")
	adapter.ConfigRoot = t.TempDir()
	fake := &textImportClient{cwd: request.CWD, profile: adapter.ConfigRoot, lose: true}
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	if _, err := adapter.ImportText(context.Background(), request); err == nil || !strings.Contains(err.Error(), "lost import") {
		t.Fatal(err)
	}
	if _, err := os.Stat(fake.source); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary source leaked")
	}
	result, err := adapter.ImportText(context.Background(), request)
	if err != nil || result.NativeSessionID != "imported-child" || fake.calls != 1 {
		t.Fatal(result, err, fake.calls)
	}
	again, err := adapter.ImportText(context.Background(), request)
	if err != nil || again != result || fake.calls != 1 {
		t.Fatal(again, err)
	}
}
func TestTextImportWaitsForCompletionAndStagesOnlySelectedSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	request := portableFixture(t)
	adapter := New("fixture")
	adapter.ConfigRoot = t.TempDir()
	fake := &textImportClient{cwd: request.CWD, profile: adapter.ConfigRoot}
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	result, err := adapter.ImportText(context.Background(), request)
	if err != nil || result.NativeSessionID != "imported-child" {
		t.Fatal(result, err)
	}
}

func TestTextImportUnknownOutcomeDoesNotCreateDuplicate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	request := portableFixture(t)
	adapter := New("fixture")
	adapter.ConfigRoot = t.TempDir()
	fake := &textImportClient{cwd: request.CWD, profile: adapter.ConfigRoot, lose: true, noLedger: true}
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	if _, err := adapter.ImportText(context.Background(), request); err == nil {
		t.Fatal("missing interrupted import failure")
	}
	if _, err := adapter.ImportText(context.Background(), request); err == nil || !strings.Contains(err.Error(), "no duplicate was created") {
		t.Fatal("unknown outcome was not protected", err)
	}
	if fake.calls != 1 {
		t.Fatal("repeated unknown import", fake.calls)
	}
}

func TestTextImportDefinitiveFailuresCanRetry(t *testing.T) {
	for _, rejection := range []bool{true, false} {
		t.Run(fmt.Sprint("rpc-rejection-", rejection), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			request := portableFixture(t)
			adapter := New("fixture")
			adapter.ConfigRoot = t.TempDir()
			fake := &textImportClient{cwd: request.CWD, profile: adapter.ConfigRoot, reject: rejection, failure: !rejection, noLedger: true}
			adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
			_, err := adapter.ImportText(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), "retry") && !strings.Contains(err.Error(), "Retry") {
				t.Fatal("missing retry guidance", err)
			}
			if !rejection && !strings.Contains(err.Error(), "read: source unavailable") {
				t.Fatal("missing native failure detail", err)
			}
			fake.reject, fake.failure, fake.noLedger = false, false, false
			result, err := adapter.ImportText(context.Background(), request)
			if err != nil || result.NativeSessionID != "imported-child" || fake.calls != 2 {
				t.Fatal(result, err, fake.calls)
			}
		})
	}
}

// Explicit opt-in on Linux only; creates isolated profiles and starts App Server,
// never a model turn. The normal test suite never invokes an installed Agent.
func TestNativeTextImportRoundTrip(t *testing.T) {
	command := os.Getenv("COCONET_TEST_CODEX_COMMAND")
	if command == "" {
		t.Skip("native App Server probe not requested")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("native Codex tests are allowed only on AnyDev Linux")
	}
	cases := map[string][]adapters.TextMessage{
		"basic":          {{ID: "1", Role: "user", Text: "接口保持不变。"}, {ID: "2", Role: "assistant", Text: "理解，先补测试。"}},
		"consecutive":    {{ID: "1", Role: "user", Text: "验证🥥"}, {ID: "2", Role: "assistant", Text: "代码\n```go\na := 1\n```"}, {ID: "3", Role: "assistant", Text: "检查通过"}},
		"pending-user":   {{ID: "1", Role: "user", Text: "旧请求"}, {ID: "2", Role: "assistant", Text: "旧结果"}, {ID: "3", Role: "user", Text: "新请求"}},
		"rewound-branch": {{ID: "A", Role: "user", Text: "A"}, {ID: "B", Role: "assistant", Text: "B"}, {ID: "D", Role: "user", Text: "D，C已撤回"}},
	}
	cases["user-only"] = []adapters.TextMessage{{ID: "1", Role: "user", Text: "保留待处理请求"}}
	cases["consecutive-users"] = []adapters.TextMessage{{ID: "1", Role: "user", Text: "先检查"}, {ID: "2", Role: "user", Text: "补充：暂时不修改"}, {ID: "3", Role: "assistant", Text: "理解"}}
	cases["literal-import-marker"] = []adapters.TextMessage{{ID: "1", Role: "user", Text: "原样引用下面的文本"}, {ID: "2", Role: "assistant", Text: "<EXTERNAL SESSION IMPORTED>"}}
	cases["literal-tags-crlf"] = []adapters.TextMessage{{ID: "1", Role: "user", Text: "文本中的标签不是事件：\r\n<tool_result>保留</tool_result>\r\n\t缩进代码\r\n结束"}, {ID: "2", Role: "assistant", Text: "```json\n{\"type\":\"tool_use\",\"text\":\"literal\"}\n```"}}
	cases["large-message"] = []adapters.TextMessage{{ID: "1", Role: "user", Text: strings.Repeat("多行代码片段\n", 100000) + "结束"}, {ID: "2", Role: "assistant", Text: "大文本确认"}}
	for i := 0; i < 2000; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		cases["many-messages"] = append(cases["many-messages"], adapters.TextMessage{ID: fmt.Sprint(i), Role: role, Text: fmt.Sprintf("第 %d 条：%s", i, strings.Repeat("固定上下文。", 40))})
	}
	for i := 0; i < 72; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		cases["long"] = append(cases["long"], adapters.TextMessage{ID: fmt.Sprint(i), Role: role, Text: strings.Repeat("长文本：保留原接口。\n", 120) + "结束"})
	}
	for name, messages := range cases {
		t.Run(name, func(t *testing.T) {
			request := portableFixture(t)
			file, err := os.Create(request.CompactPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range messages {
				if err := json.NewEncoder(file).Encode(message); err != nil {
					file.Close()
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			request.CompactSHA256, _, err = adapters.HashFile(request.CompactPath)
			if err != nil {
				t.Fatal(err)
			}
			adapter := New(command)
			adapter.ConfigRoot = t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			result, err := adapter.ImportText(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			observation, _, err := adapter.Observe(ctx, request.CWD, result.NativeSessionID, adapters.ObservationCheckpoint{})
			if err != nil {
				t.Fatal(err)
			}
			if err := adapters.VerifyImportedText(ctx, request, observation); err != nil {
				t.Fatal(err)
			}
			again, err := adapter.ImportText(ctx, request)
			if err != nil || again != result {
				t.Fatal("retry changed child", again, err)
			}
			current, _, err := adapters.HashFile(request.CompactPath)
			if err != nil || current != request.CompactSHA256 {
				t.Fatal("source changed", err)
			}
		})
	}
}

func TestTextImportRejectsLossyOfficialInputsBeforeLaunchingCodex(t *testing.T) {
	for _, raw := range []string{
		"{\"turn_id\":\"1\",\"role\":\"assistant\",\"text\":\"前文\"}\n",
		"{\"turn_id\":\"1\",\"role\":\"user\",\"text\":\"  保留缩进\"}\n",
	} {
		request := portableFixture(t)
		os.WriteFile(request.CompactPath, []byte(raw), 0600)
		request.CompactSHA256, _, _ = adapters.HashFile(request.CompactPath)
		adapter := New("never-launch")
		adapter.start = func(context.Context, string, string, string) (appServerClient, error) {
			t.Fatal("lossy import started")
			return nil, nil
		}
		if _, err := adapter.ImportText(context.Background(), request); err == nil || !strings.Contains(err.Error(), "pull_session") {
			t.Fatal("missing recovery guidance", err)
		}
	}
}
