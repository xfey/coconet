package localdag

import (
	"context"
	"errors"
	"testing"

	"github.com/xfey/coconet/packages/workgraph"
)

func TestUnresolvedRelationsPublishSharedStateAndWakeForLateCandidates(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "consumer", 1, "使用同事的鉴权方案完善重试。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善重试", Summary: "已采用同事的鉴权方案。"}, "e1"))
	process(t, e, "stage", "consumer")
	state, _ := store.State("project")
	id := state.Sessions["consumer"].Tail
	e.Runner = output(`{"status":"needs_context","parents":[],"reason":"The referenced authentication work is not in the candidate graph."}`)
	// A receipt arrives through replication before the append response is lost.
	e.Publisher = publisherFunc(func(_ context.Context, c workgraph.Commit) error {
		_, err := store.Apply(c, false)
		if err != nil {
			return err
		}
		return errors.New("lost response")
	})
	process(t, e, "relations", "")
	state, _ = store.State("project")
	if state.Nodes[id].Relations != "needs_context" || state.Nodes[id].RelationThrough != 0 {
		t.Fatal("missing context was not shared, or consumed evidence")
	}
	job := task(t, e, "relations", "")
	if !job.Expanded || job.Status != "queued" || job.RunID != "" {
		t.Fatal("publication lost bounded continuation", job)
	}
	if err := e.RecoverLocal(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	job = task(t, e, "relations", "")
	if !job.Expanded || job.Status != "queued" {
		t.Fatal("recovery reset continuation")
	}
	e.Publisher = nil
	process(t, e, "relations", "")
	job = task(t, e, "relations", "")
	if job.Status != "paused" || job.ContextBasis == "" {
		t.Fatal("repeated missing context did not pause", job)
	}
	// A capture alone supplies no candidate outcome and must not spend a call.
	capture(t, e, "producer", 1, "补齐鉴权规则。")
	if err := e.Reconcile("project"); err != nil {
		t.Fatal(err)
	}
	job = task(t, e, "relations", "")
	if job.Status != "paused" {
		t.Fatal("raw capture woke a semantic relation")
	}
	e.Runner = output(stage("start", &workgraph.Text{Objective: "补齐鉴权规则", Summary: "完成设备鉴权规则。"}, "e1"))
	process(t, e, "stage", "producer")
	if err := e.Reconcile("project"); err != nil {
		t.Fatal(err)
	}
	tasks, _ := store.Tasks("project")
	found := false
	for _, job := range tasks {
		if job.NodeID == id {
			found = job.Status == "queued" && job.Expanded && job.Attempts == 0
		}
	}
	if !found {
		t.Fatal("late accepted candidate did not wake paused work")
	}
}

func TestModelFailureIsNotWokenByUnrelatedCandidateChanges(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "consumer", 1, "完善文档。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善文档"}, "e1"))
	process(t, e, "stage", "consumer")
	job := task(t, e, "relations", "")
	job.Status, job.Error = "paused", "provider unavailable"
	if err := store.PutTask(job); err != nil {
		t.Fatal(err)
	}
	capture(t, e, "producer", 1, "完善鉴权。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善鉴权"}, "e1"))
	process(t, e, "stage", "producer")
	if err := e.Reconcile("project"); err != nil {
		t.Fatal(err)
	}
	tasks, _ := store.Tasks("project")
	for _, current := range tasks {
		if current.ID == job.ID && current.Status != "paused" {
			t.Fatal("provider failure retried without authorization")
		}
	}
}
