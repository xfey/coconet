package localdag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestCompleteChineseInputUsesUnifiedBudget(t *testing.T) {
	e, store := fixture(t)
	text := strings.Repeat("先梳理需求，再实现并验证。", 9000)
	capture(t, e, "session", 1, text)
	job := task(t, e, "stage", "session")
	state, _ := store.State("project")
	for _, expanded := range []bool{false, true} {
		input, err := prepareStage(store, state, *job.Source, expanded)
		if err != nil {
			t.Fatal(err)
		}
		var body stageInput
		if err := json.Unmarshal(input.Request.Input, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Evidence.Turns) != 1 || body.Evidence.Turns[0].Text != text || !input.Complete {
			t.Fatal("complete message was clipped")
		}
		spec, err := fixedProcessingInput(state, job, input)
		if err != nil || len(spec.Payload) > contracts.MaxProcessingInputBytes {
			t.Fatal("wide request does not fit portable capsule", err)
		}
	}
}

func TestOversizedStageStopsBeforeCallingModelAndKeepsSource(t *testing.T) {
	e, store := fixture(t)
	text := strings.Repeat("完整消息不能截断。", 24000)
	capture(t, e, "session", 1, text)
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		t.Fatal("oversized input reached model")
		return inference.Response{}, nil
	})
	job := task(t, e, "stage", "session")
	if err := e.Process(context.Background(), job); err == nil || !strings.Contains(err.Error(), "512 KiB") {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if state.Sessions["session"].ProcessedThrough != 0 || len(state.Nodes) != 0 {
		t.Fatal("incomplete task consumed source")
	}
	entries, err := readEntries(store, state.Snapshots[job.Source.Key()], 0, 1)
	if err != nil || entries[0].Turn.Text != text {
		t.Fatal("original text changed", err)
	}
	jobs, _ := store.Tasks("project")
	if len(jobs) != 1 || jobs[0].Status != "paused" || jobs[0].Attempts != 0 {
		t.Fatal(jobs)
	}
}

func TestRelationCandidatesAreCompleteOrRejectedBeforeCall(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "继续完成联调。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "联调"}, "e1"))
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	id := state.Sessions["session"].Tail
	job := task(t, e, "relations", "")
	for i := range 420 {
		node := state.Nodes[id]
		node.ID = fmt.Sprintf("candidate-%04d", i)
		node.Text.Summary = strings.Repeat("完整候选摘要", 70)
		state.Nodes[node.ID] = node
		if i != 149 && i != 419 {
			continue
		}
		input, err := prepareRelations(store, state, id, false)
		if err != nil || !input.Complete || len(input.Audit.Candidates) != i+1 {
			t.Fatal("candidate omitted", err)
		}
		spec, err := fixedProcessingInput(state, job, input)
		if i == 149 && (err != nil || len(spec.Payload) > contracts.MaxProcessingInputBytes) {
			t.Fatal("old candidate tier still applied", err)
		}
		if i == 419 && (err == nil || !strings.Contains(err.Error(), "512 KiB")) {
			t.Fatal("oversized candidates accepted", err)
		}
	}
}

func TestCapsuleLimitAndIncompleteInputAreCheckedIndependently(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "检查接口。")
	job := task(t, e, "stage", "session")
	state, _ := store.State("project")
	input, err := prepareStage(store, state, *job.Source, false)
	if err != nil {
		t.Fatal(err)
	}
	input.Complete = false
	if _, err := fixedProcessingInput(state, job, input); err == nil {
		t.Fatal("incomplete request registered")
	}
	if result, needs, err := parseStage(stage("none", nil), input); err != nil || !needs || result != nil {
		t.Fatal("incomplete stage accepted", result, err)
	}
	input.Complete = true
	input.Audit.Items = map[string]string{"oversized-audit": strings.Repeat("x", contracts.MaxProcessingInputBytes)}
	if _, err := fixedProcessingInput(state, job, input); err == nil || !strings.Contains(err.Error(), "task package") {
		t.Fatal("capsule capacity unchecked", err)
	}
}

func TestDuplicateEvidenceIsNormalizedWithoutInventingReferences(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "检查接口。")
	job := task(t, e, "stage", "session")
	state, _ := store.State("project")
	input, err := prepareStage(store, state, *job.Source, false)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := parseStage(stage("start", &workgraph.Text{Objective: "接口检查"}, "e1", "e1"), input)
	if err != nil || len(result.Evidence) != 1 {
		t.Fatal("duplicate rejected", err)
	}
	if _, _, err := parseStage(stage("start", &workgraph.Text{Objective: "接口检查"}, "e1", "unknown"), input); err == nil {
		t.Fatal("unknown anchor accepted")
	}
}
