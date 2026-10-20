package localdag

import (
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/workgraph"
)

func TestDiscoveryPaginationIsRevisionAndSelectionBound(t *testing.T) {
	e, _ := fixture(t)
	for _, id := range []string{"a", "b", "c"} {
		capture(t, e, id, 1, "完善重试说明")
		e.Runner = output(stage("start", &workgraph.Text{Objective: "重试说明"}, "e1"))
		process(t, e, "stage", id)
	}
	first, err := e.GraphPage("project", GraphQuery{Limit: 1}, false)
	if err != nil || len(first.Nodes) != 1 || len(first.SessionWork) != 1 || first.NextCursor == "" || !first.Coverage.Truncated {
		t.Fatal(first, err)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 4; pages++ {
		page, err := e.GraphPage("project", GraphQuery{Limit: 1, Cursor: cursor}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range page.Nodes {
			if seen[node.ID] {
				t.Fatal("duplicate node")
			}
			seen[node.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 3 || cursor != "" {
		t.Fatal("pagination omitted nodes")
	}
	if _, err := e.GraphPage("project", GraphQuery{Cursor: first.NextCursor}, true); err == nil {
		t.Fatal("graph cursor reused for Library")
	}
	search, err := e.Search("project", WorkSearch{Query: "重试", Limit: 1})
	if err != nil || search.NextCursor == "" {
		t.Fatal(search, err)
	}
	next, err := e.Search("project", WorkSearch{Query: "重试", Limit: 1, Cursor: search.NextCursor})
	if err != nil || next.Nodes[0].Node.ID == search.Nodes[0].Node.ID {
		t.Fatal(next, err)
	}
	if _, err := e.Search("project", WorkSearch{Query: "different", Cursor: search.NextCursor}); err == nil {
		t.Fatal("cursor reused for another query")
	}
	capture(t, e, "a", 2, "完善重试说明", "再把文字改短一点")
	if _, err := e.GraphPage("project", GraphQuery{Cursor: first.NextCursor}, false); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatal("mixed revisions", err)
	}
	if _, err := e.Search("project", WorkSearch{Query: "重试", Cursor: search.NextCursor}); err != nil {
		t.Fatal("unprocessed snapshot invalidated unchanged search results", err)
	}
}
