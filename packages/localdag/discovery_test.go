package localdag

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/workgraph"
)

func TestDiscoveryProjectsOnlyAcceptedWorkAndReportsCoverage(t *testing.T) {
	state := workgraph.New("project")
	state.Revision = 19
	state.Sessions["s"] = workgraph.Session{ID: "s", Owner: "device", Agent: "codex", NativeID: "private-native-id", Latest: 3, ProcessedThrough: 2, Items: []workgraph.Item{{ID: "i", Text: "验证中断恢复"}}}
	state.Snapshots["s/3"] = workgraph.Snapshot{Source: workgraph.Source{SessionID: "s", Version: 3}, Conversation: "private-content-root"}
	state.Nodes["b"] = workgraph.Node{ID: "b", Text: workgraph.Text{Objective: "完成停止提示"}, Source: workgraph.Source{SessionID: "s", Version: 2}, Relations: "needs_context", Parents: []workgraph.Edge{{Parent: "a", Reason: "private-processing-evidence"}}}
	state.Nodes["a"] = workgraph.Node{ID: "a", Source: workgraph.Source{SessionID: "s", Version: 1}, Relations: "ready"}
	state.Library["a"] = "稳定重试方案"
	view := projectGraph(state)
	if view.Revision != 19 || view.Coverage.StagePendingSessions != 1 || view.Coverage.RelationsPending != 1 || view.Nodes[0].ID != "a" || view.Nodes[1].Source.Version != 2 || view.SessionWork[0].Items[0].Text != "验证中断恢复" {
		t.Fatalf("incorrect discovery projection: %+v", view)
	}
	if len(view.Nodes[0].Children) != 1 || view.Nodes[0].Children[0] != "b" || view.Nodes[1].Parents[0] != "a" || view.Nodes[0].Library.Note != "稳定重试方案" {
		t.Fatal("relationships or collection note lost")
	}
	data, _ := json.Marshal(view)
	if strings.Contains(string(data), "private-") {
		t.Fatal("discovery exposed content or native metadata")
	}
	if workgraph.Digest(view) != workgraph.Digest(projectGraph(state)) {
		t.Fatal("map iteration changes discovery order")
	}
}

func TestLocalSearchUsesOnlyStructuredTextAndReportsTruncation(t *testing.T) {
	e, _ := fixture(t)
	for _, id := range []string{"a", "b", "c"} {
		capture(t, e, id, 1, "private-conversation-only-marker")
		e.Runner = output(stage("start", &workgraph.Text{Objective: "重试提示", Summary: "Retry failures are bounded."}, "e1"))
		process(t, e, "stage", id)
	}
	result, err := e.Search("project", WorkSearch{Query: "RETRY 重试", Limit: 2})
	if err != nil || len(result.Nodes) != 2 || !result.Coverage.Truncated || result.Coverage.Nodes != 3 || result.Coverage.RelationsPending != 3 {
		t.Fatalf("unexpected search coverage: %+v %v", result, err)
	}
	if len(result.Nodes[0].Reasons) != 2 || result.Nodes[0].Node.ID >= result.Nodes[1].Node.ID {
		t.Fatal("lexical matches or stable order lost")
	}
	result, err = e.Search("project", WorkSearch{Query: "private-conversation-only-marker"})
	if err != nil || len(result.Nodes) != 0 || result.Coverage.Scope != "node_text_library_notes_and_indexed_conversations" {
		t.Fatal("search read conversation bodies", err)
	}
	for _, q := range []WorkSearch{{}, {Query: "  "}, {Query: "retry", Limit: -1}, {Query: "retry", Limit: 101}, {Query: strings.Repeat("中", 4001)}} {
		if _, err := e.Search("project", q); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
}
