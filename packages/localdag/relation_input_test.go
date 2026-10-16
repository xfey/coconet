package localdag

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/workgraph"
)

func TestRelationContextCrossesStageBoundaryWithoutBecomingNewEvidence(t *testing.T) {
	e, store := fixture(t)
	prior := []string{"先确认入口。", "入口已确认。", "补一个请求排查例子。", "例子支持响应标识。", "再补失败路径。", "失败路径已补。", "加上运行说明。", "排查例子的说明已完成。"}
	capture(t, e, "session", 1, prior...)
	e.Runner = output(stage("start", &workgraph.Text{Objective: "请求排查示例"}, "e3", "e8"))
	process(t, e, "stage", "session")
	before, _ := store.State("project")
	parent := before.Sessions["session"].Tail
	texts := append(append([]string{}, prior...), "把这个例子的说明压缩一下。", "仅压缩排查说明，代码未改。")
	capture(t, e, "session", 2, texts...)
	e.Runner = output(stage("start", &workgraph.Text{Objective: "精简示例说明"}, "e9", "e10"))
	process(t, e, "stage", "session")
	// A later capture must not leak into this fixed node's relation evidence.
	capture(t, e, "session", 3, append(texts, "之后再增加健康检查。")...)
	state, _ := store.State("project")
	id := state.Sessions["session"].Tail
	for _, expanded := range []bool{false, true} {
		input, err := prepareRelations(store, state, id, expanded)
		if err != nil {
			t.Fatal(err)
		}
		var body relationInput
		if err := json.Unmarshal(input.Request.Input, &body); err != nil {
			t.Fatal(err)
		}
		if input.Audit.From != 8 || input.Audit.Through != 10 || len(body.Evidence.Turns) != 8 || body.Evidence.Turns[0].ID != "e3" {
			t.Fatalf("lost bounded preceding-stage context (expanded=%t): %+v", expanded, body.Evidence)
		}
		for _, row := range body.Evidence.Turns {
			fresh := row.ID == "e9" || row.ID == "e10"
			if row.Context == fresh || input.Anchors[row.ID] != fresh || row.CurrentStage {
				t.Fatalf("earlier context became new adoption evidence: %+v", row)
			}
		}
		if strings.Contains(string(input.Request.Input), "之后再增加健康检查") {
			t.Fatal("unprocessed future capture entered the fixed input")
		}
		alias := ""
		for key, candidate := range input.Audit.Candidates {
			if candidate.ID == parent {
				alias = key
			}
		}
		if alias == "" {
			t.Fatal("preceding result unavailable as a candidate")
		}
		decision := func(refs ...string) string {
			data, _ := json.Marshal(map[string]any{"status": "complete", "parents": []any{map[string]any{"id": alias, "reason": "continues the example documentation", "evidence": refs}}, "reason": "same artifact"})
			return string(data)
		}
		if _, _, err := parseRelations(decision("e8"), input, id); err == nil {
			t.Fatal("earlier stage alone established a new dependency")
		}
		if _, _, err := parseRelations(decision("e8", "e9"), input, id); err != nil {
			t.Fatal("context plus current adoption was rejected", err)
		}
	}
	// After checking part of a stage, expansion recovers its earlier evidence
	// plus the same bounded preceding context, not the entire old conversation.
	node := state.Nodes[id]
	node.RelationThrough = 9
	state.Nodes[id] = node
	for _, expanded := range []bool{false, true} {
		input, err := prepareRelations(store, state, id, expanded)
		if err != nil {
			t.Fatal(err)
		}
		var body relationInput
		if err := json.Unmarshal(input.Request.Input, &body); err != nil {
			t.Fatal(err)
		}
		first := "e4"
		if expanded {
			first = "e3"
		}
		for _, row := range body.Evidence.Turns {
			if row.CurrentStage != (row.ID == "e9") {
				t.Fatalf("same-stage context misclassified: %+v", row)
			}
		}
		if body.Evidence.Turns[0].ID != first || input.Anchors["e9"] || !input.Anchors["e10"] {
			t.Fatal("expanded retry lost its independent relation boundary", body.Evidence)
		}
	}
}

func TestRelationCandidatesKeepTopologyOutOfModelInput(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "实现空白回退。", "已实现。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "空白回退"}, "e1"))
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	target := state.Sessions["session"].Tail
	base := state.Nodes[target]
	// Candidate eligibility still obeys topology; only the model projection
	// loses it. Audit retains the exact candidate sources and revisions.
	for _, id := range []string{"ancestor", "downstream", "descendant"} {
		n := base
		n.ID = id
		n.Text.Objective = id
		state.Nodes[id] = n
	}
	n := state.Nodes["downstream"]
	n.Parents = []workgraph.Edge{{Parent: "ancestor", Source: base.Source}}
	state.Nodes[n.ID] = n
	n = state.Nodes["descendant"]
	n.Parents = []workgraph.Edge{{Parent: target, Source: base.Source}}
	state.Nodes[n.ID] = n
	input, err := prepareRelations(store, state, target, false)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Candidates []map[string]json.RawMessage `json:"candidates"`
	}
	if err := json.Unmarshal(input.Request.Input, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Candidates) != 2 {
		t.Fatal("descendant filtering changed", body.Candidates)
	}
	for _, c := range body.Candidates {
		for key := range c {
			if key != "id" && key != "objective" && key != "summary" && key != "approach" {
				t.Fatal("non-semantic candidate metadata reached model", key)
			}
		}
	}
	for _, c := range input.Audit.Candidates {
		if c.Source != base.Source || c.Revision != base.Revision || c.ID == "descendant" {
			t.Fatal("fixed candidate validation metadata changed", c)
		}
	}
}
