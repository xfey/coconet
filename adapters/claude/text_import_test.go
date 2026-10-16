package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/adapters"
)

// Actual SDK parsing/Fork is opt-in; it never starts Claude or a model call.
func TestSDKTextImportRoundTrip(t *testing.T) {
	sdk := os.Getenv("COCONET_TEST_CLAUDE_SDK")
	if sdk == "" {
		t.Skip("actual SDK probe not requested")
	}
	cases := map[string][]adapters.TextMessage{
		"basic":             {{ID: "1", Role: "user", Text: "接口保持不变。"}, {ID: "2", Role: "assistant", Text: "理解，先补测试。"}},
		"consecutive":       {{ID: "1", Role: "user", Text: "验证🥥"}, {ID: "2", Role: "assistant", Text: "  代码\n```go\na := 1\n```  "}, {ID: "3", Role: "assistant", Text: "检查通过"}},
		"leading-assistant": {{ID: "1", Role: "assistant", Text: "前文结论"}, {ID: "2", Role: "user", Text: "继续"}},
		"pending-user":      {{ID: "1", Role: "user", Text: "旧请求"}, {ID: "2", Role: "assistant", Text: "旧结果"}, {ID: "3", Role: "user", Text: "新请求"}},
		"rewound-branch":    {{ID: "A", Role: "user", Text: "A"}, {ID: "B", Role: "assistant", Text: "B"}, {ID: "D", Role: "user", Text: "D，C已撤回"}},
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
		cases["long"] = append(cases["long"], adapters.TextMessage{ID: fmt.Sprint(i), Role: role, Text: strings.Repeat("长文本：保留原接口。\n", 120)})
	}
	for name, messages := range cases {
		t.Run(name, func(t *testing.T) {
			cwd, _ := filepath.EvalSymlinks(t.TempDir())
			profile, _ := filepath.EvalSymlinks(t.TempDir())
			path := filepath.Join(t.TempDir(), "compact.jsonl")
			file, _ := os.Create(path)
			for _, message := range messages {
				if err := json.NewEncoder(file).Encode(message); err != nil {
					t.Fatal(err)
				}
			}
			file.Close()
			hash, _, _ := adapters.HashFile(path)
			request := adapters.TextImportRequest{AttemptDirectory: t.TempDir(), CompactPath: path, CompactSHA256: hash, CWD: cwd, ObservedAt: "2026-10-15T01:02:03Z"}
			adapter := New("tclaude")
			adapter.ConfigRoot = profile
			adapter.SDKEntry = sdk
			adapter.HelperPath, _ = filepath.Abs("helper/helper.mjs")
			result, err := adapter.ImportText(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			observation, _, err := adapter.Observe(context.Background(), cwd, result.NativeSessionID, adapters.ObservationCheckpoint{})
			if err != nil {
				t.Fatal(err)
			}
			if err := adapters.VerifyImportedText(context.Background(), request, observation); err != nil {
				t.Fatal(err)
			}
			retry, err := adapter.ImportText(context.Background(), request)
			if err != nil || retry != result {
				t.Fatal("retry changed identity", retry, err)
			}
			current, _, _ := adapters.HashFile(path)
			if current != hash {
				t.Fatal("source changed")
			}
		})
	}
}
