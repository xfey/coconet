package localdag

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestContentSearchFindsExactVersionsAndDeduplicatesPrefixes(t *testing.T) {
	e, store := fixture(t)
	ctx := context.Background()
	capture(t, e, "session", 1, "先看看其他同事在做什么，repair_queue 不要重复领取。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "确定补传流程"}, "e1"))
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	first := state.Nodes[state.Sessions["session"].Tail].Source
	capture(t, e, "session", 2, "先看看其他同事在做什么，repair_queue 不要重复领取。", "接下来补充磁盘配额，DISK_QUOTA_NEW 必须明确报错。")
	// The newer source has no node yet. Body matches must not be attached to the
	// earlier node just because they belong to the same Session.
	before, err := e.Search("project", WorkSearch{Query: "repair_queue"})
	if err != nil || before.Index.Complete || len(before.ContentMatches) != 0 {
		t.Fatal(before, err)
	}
	if coverage, err := store.IndexProject(ctx, "project", 16); err != nil || !coverage.Complete {
		t.Fatal(coverage, err)
	}
	for _, query := range []string{"REPAIR_QUEUE", "重复", "不要重复领取"} {
		result, err := e.Search("project", WorkSearch{Query: query})
		if err != nil || len(result.ContentMatches) != 1 || !result.Index.Complete {
			t.Fatal(query, result, err)
		}
		hit := result.ContentMatches[0]
		if hit.Source != first || len(hit.NodeIDs) != 1 || !strings.Contains(hit.Snippet, "repair_queue") {
			t.Fatal("prefix source mapping differs", hit)
		}
	}
	result, err := e.Search("project", WorkSearch{Query: "DISK_QUOTA_NEW"})
	if err != nil || len(result.ContentMatches) != 1 {
		t.Fatal(result, err)
	}
	hit := result.ContentMatches[0]
	if hit.Source.Version != 2 || len(hit.NodeIDs) != 0 || hit.Ordinal != 1 {
		t.Fatal("future text was attached to earlier node", hit)
	}
	// Pagination binds index generation as well as graph revision.
	page, err := e.Search("project", WorkSearch{Query: "repair_queue DISK_QUOTA_NEW", Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	next, err := e.Search("project", WorkSearch{Query: "repair_queue DISK_QUOTA_NEW", Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.ContentMatches) != 1 || next.ContentMatches[0].TurnID == page.ContentMatches[0].TurnID {
		t.Fatal(next, err)
	}
	beforeReset, _ := store.State("project")
	digest := workgraph.Digest(beforeReset)
	if err := store.ResetSearchIndex(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Search("project", WorkSearch{Query: "repair_queue DISK_QUOTA_NEW", Cursor: page.NextCursor}); err == nil {
		t.Fatal("stale index cursor accepted")
	}
	if _, err := store.IndexProject(ctx, "project", 16); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := e.Search("project", WorkSearch{Query: "repair_queue"})
	if err != nil || len(rebuilt.ContentMatches) != 1 {
		t.Fatal(rebuilt, err)
	}
	afterReset, _ := store.State("project")
	if digest != workgraph.Digest(afterReset) {
		t.Fatal("index rebuild changed DAG")
	}
	raw, err := store.ConversationFile(ctx, state.Snapshots[first.Key()].Conversation, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(raw)
	if err != nil || !strings.Contains(string(data), "repair_queue") {
		t.Fatal("index rebuild changed content", err)
	}
}

func TestContentSearchHonorsVisibleTextAndProjectBoundaries(t *testing.T) {
	e, store := fixture(t)
	ctx := context.Background()
	visible := "可见说明 searchable"
	input := Capture{ProjectID: "project", Agent: "fixture", NativeID: "private", Entries: []conversation.Entry{
		{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0, Role: "assistant", Kind: "message", Text: visible + " HIDDEN_SECRET"}, Visible: &visible},
		{Turn: contracts.NormalizedTurn{TurnID: "two", Ordinal: 1, Role: "tool", Kind: "tool_result", Text: "TOOL_SECRET"}},
	}}
	if _, err := e.Capture(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IndexProject(ctx, "project", 16); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"HIDDEN_SECRET", "TOOL_SECRET"} {
		result, err := e.Search("project", WorkSearch{Query: query})
		if err != nil || len(result.ContentMatches) != 0 {
			t.Fatal(query, result, err)
		}
	}
	visibleResult, err := e.Search("project", WorkSearch{Query: "可见"})
	if err != nil || len(visibleResult.ContentMatches) != 1 {
		t.Fatal(visibleResult, err)
	}
	foreign, err := e.Search("foreign", WorkSearch{Query: "searchable"})
	if err != nil || len(foreign.ContentMatches) != 0 {
		t.Fatal("cross-project index leak", foreign, err)
	}
}

func TestUnprocessedSearchSourceSupportsCompactReadOnly(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "这个边界先记录下来，还没有形成节点。")
	ctx := context.Background()
	if _, err := store.IndexProject(ctx, "project", 16); err != nil {
		t.Fatal(err)
	}
	result, err := e.Search("project", WorkSearch{Query: "边界"})
	if err != nil || len(result.ContentMatches) != 1 {
		t.Fatal(result, err)
	}
	source := result.ContentMatches[0].Source
	read, err := e.ReadSelectedNode(ctx, "project", "", source, false)
	if err != nil || read.NodeID != "" || read.CompactPath == "" || read.Source != source {
		t.Fatal(read, err)
	}
	if _, err := e.ReadSelectedNode(ctx, "project", "", source, true); err == nil {
		t.Fatal("native content opened without a node")
	}
	source.Version++
	if _, err := e.ReadSelectedNode(ctx, "project", "", source, false); err == nil {
		t.Fatal("unaccepted source read")
	}
}
