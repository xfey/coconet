package localdag

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestProviderWaitsDoNotSpendResultRetries(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "完善失败时的提示。")
	now := e.now()
	e.Now = func() time.Time { return now }
	for _, kind := range []string{"authentication_required", "configuration_required", "context_capacity_exceeded", "transient_network", "quota_exhausted"} {
		e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
			return inference.Response{}, &inference.Waiting{Kind: kind, Cause: errors.New("fixture unavailable")}
		})
		if err := e.Drain(context.Background(), "project", 1); err == nil {
			t.Fatal("wait hidden")
		}
		jobs, _ := store.Tasks("project")
		if len(jobs) != 1 || jobs[0].Attempts != 0 || jobs[0].Status != "retry" {
			t.Fatal(jobs)
		}
		now = now.Add(time.Hour)
	}
	e.Runner = output(`{"invalid":"result"}`)
	for i := 1; i <= 3; i++ {
		if err := e.Drain(context.Background(), "project", 1); err == nil {
			t.Fatal("invalid result accepted")
		}
		jobs, _ := store.Tasks("project")
		if jobs[0].Attempts != i || (jobs[0].Status == "paused") != (i == 3) {
			t.Fatal(jobs)
		}
		now = now.Add(time.Hour)
	}
}

func TestLongPublicationOutageRetainsCompletedResult(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "补充断网恢复。")
	now := e.now()
	e.Now = func() time.Time { return now }
	calls := 0
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		calls++
		return inference.Response{Text: stage("start", &workgraph.Text{Objective: "补充断网恢复"}, "e1")}, nil
	})
	e.Publisher = publisherFunc(func(context.Context, workgraph.Commit) error { return errors.New("network unavailable") })
	if err := e.Process(context.Background(), task(t, e, "stage", "session")); err == nil {
		t.Fatal("outage hidden")
	}
	for i := 0; i < 12; i++ {
		now = now.Add(time.Hour)
		if err := e.RecoverLocal(context.Background(), "project"); err != nil {
			t.Fatal(err)
		}
		if err := e.Flush(context.Background(), "project"); err == nil {
			t.Fatal("outage hidden")
		}
		jobs, _ := store.Tasks("project")
		if len(jobs) != 1 || jobs[0].Status != "publish_pending" {
			t.Fatal("automatic publication stopped", jobs)
		}
	}
	e.Publisher = nil
	now = now.Add(time.Hour)
	if err := e.Flush(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if calls != 1 || len(state.Nodes) != 1 {
		t.Fatal("successful model was repeated or lost", calls, state)
	}
}
