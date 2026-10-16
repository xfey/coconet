package codex

import (
	"encoding/json"
	"github.com/xfey/coconet/adapters"
	"testing"
)

func TestVisibleItemRangesRetainOnlyTopLevelDialogue(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"type":"userMessage","content":[{"type":"text","text":"用户中文"},{"type":"image","text":"hidden"},{"type":"tool_result","content":[{"type":"text","text":"nested"}]},{"type":"text","text":"例子 <tool>"}]}`, "用户中文\n例子 <tool>"},
		{`{"type":"agentMessage","phase":"commentary","text":"开始检查","output":"hidden","content":[{"type":"text","text":"结论"}],"fragments":[{"type":"text","text":"internal"}]}`, "开始检查\n结论"},
		{`{"type":"agentMessage","phase":"final_answer","text":"最终答复"}`, "最终答复"},
		{`{"type":"agentMessage","phase":"analysis","text":"internal"}`, ""},
		{`{"type":"reasoning","text":"internal"}`, ""},
		{`{"type":"commandExecution","output":"hidden"}`, ""},
		{`{"type":"plan","text":"公开计划"}`, "公开计划"},
	} {
		_, _, text, _ := normalizeItem(json.RawMessage(tc.raw))
		ranges := visibleItemRanges(json.RawMessage(tc.raw))
		if got := adapters.VisibleText(text, ranges); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
