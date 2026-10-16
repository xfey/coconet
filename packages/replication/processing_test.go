package replication

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xfey/coconet/packages/graphstore"
	"reflect"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

type processingWire struct{ *localTransport }

func (t processingWire) RegisterProcessing(_ context.Context, p string, input contracts.ProcessingRegister) (contracts.ProcessingTask, error) {
	return t.s.RegisterProcessing(p, t.account, input)
}
func (t processingWire) ProcessingTasks(_ context.Context, p string, after int64) (contracts.ProcessingPage, error) {
	return t.s.ProcessingTasks(p, after)
}
func (t processingWire) ProcessingTask(_ context.Context, p, id string) (contracts.ProcessingTask, error) {
	return t.s.ProcessingTask(p, id)
}
func (t processingWire) ActProcessing(_ context.Context, p, id string, input contracts.ProcessingAction) (contracts.ProcessingTask, error) {
	return t.s.ActProcessing(p, t.account, id, input)
}

type processingModel func(inference.Request) (inference.Response, error)

func (f processingModel) Infer(_ context.Context, r inference.Request) (inference.Response, error) {
	return f(r)
}

const acceptedStage = `{"action":"start","stage":{"objective":"完善重试反馈","summary":"已明确停止条件","approach":"失败有限重试"},"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"accepted delivery","evidence":["e1"]}`
const acceptedRelations = `{"status":"complete","parents":[],"reason":"self-contained work"}`

func TestSharedTaskHandoffUsesIdenticalInputBeforeOwnWorkAndKeepsAuthor(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, a, wa := replica(t, server, "alice", "a")
	right, b, wb := replica(t, server, "bob", "b")
	left.Remote = processingWire{wa}
	right.Remote = processingWire{wb}
	aSource, err := a.Capture(ctx, source("a-session", "先把失败反馈讲清楚，连续失败就先停下来。"))
	if err != nil {
		t.Fatal(err)
	}
	if err := left.SyncContents(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	var original inference.Request
	a.Runner = processingModel(func(r inference.Request) (inference.Response, error) {
		original = r
		return inference.Response{}, errors.New("local provider unavailable")
	})
	if err := a.Drain(ctx, "project", 1); err == nil {
		t.Fatal("failure was hidden")
	}
	bSource, err := b.Capture(ctx, source("b-session", "理解，接下来补一下诊断说明。"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	b.Runner = processingModel(func(r inference.Request) (inference.Response, error) {
		calls++
		if calls == 1 && !reflect.DeepEqual(r, original) {
			t.Fatal("handoff changed the fixed model input")
		}
		return inference.Response{Text: acceptedStage, Model: "fixture"}, nil
	})
	if err := b.Drain(ctx, "project", 1); err != nil {
		t.Fatal(err)
	}
	state, _ := right.Store.State("project")
	if calls != 1 || state.Sessions[aSource.SessionID].ProcessedThrough != 1 || state.Sessions[bSource.SessionID].ProcessedThrough != 0 {
		t.Fatal("own work ran ahead of handoff", state.Sessions)
	}
	node := state.Nodes[state.Sessions[aSource.SessionID].Tail]
	if state.Sessions[node.Source.SessionID].Owner != "alice.a" {
		t.Fatal("helper became author")
	}
	runs, _ := right.Store.Runs("project")
	found := false
	for _, r := range runs {
		if r.SharedTaskID != "" {
			found = r.SourceOwner == "alice.a" && r.Executor == "bob.b" && r.InputSHA256 != ""
		}
	}
	if !found {
		t.Fatal("missing execution provenance")
	}
	if err := b.Drain(ctx, "project", 1); err != nil {
		t.Fatal(err)
	}
	state, _ = right.Store.State("project")
	if state.Sessions[bSource.SessionID].ProcessedThrough != 1 {
		t.Fatal("helper did not resume own work")
	}
	b.Runner = processingModel(func(inference.Request) (inference.Response, error) {
		return inference.Response{Text: acceptedRelations}, nil
	})
	if err := b.Drain(ctx, "project", 4); err != nil {
		t.Fatal(err)
	}
	state, _ = right.Store.State("project")
	for _, n := range state.Nodes {
		if n.Relations != "ready" {
			t.Fatal("assisted followup abandoned", n)
		}
	}
	if err := left.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	a.Runner = processingModel(func(inference.Request) (inference.Response, error) {
		t.Fatal("recovered owner repeated accepted work")
		return inference.Response{}, nil
	})
	if err := a.Drain(ctx, "project", 4); err != nil {
		t.Fatal(err)
	}
	end, _ := left.Store.State("project")
	if workgraph.Digest(state) != workgraph.Digest(end) {
		t.Fatal("replicas diverged")
	}
}

func TestSharedProcessingLostAppendReplyReusesResultWithoutSecondModelCall(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, e, wire := replica(t, server, "alice", "a")
	client.Remote = processingWire{wire}
	if _, err := e.Capture(ctx, source("one", "完善停止条件。")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.Runner = processingModel(func(inference.Request) (inference.Response, error) {
		calls++
		wire.loseReceipt = true
		return inference.Response{Text: acceptedStage}, nil
	})
	if err := e.Drain(ctx, "project", 1); err == nil {
		t.Fatal("lost receipt not reported")
	}
	if err := client.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := e.RecoverLocal(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := client.Store.State("project")
	if calls != 1 || len(state.Nodes) != 1 {
		t.Fatal("lost acknowledgement repeated inference")
	}
}

func TestDelegatedAuthorizationCannotBeForgedInOpaquePayload(t *testing.T) {
	c := workgraph.Commit{Schema: 1, ID: "c", ProjectID: "project", Actor: "bob.b", RunID: "r", Processing: &contracts.ProcessingAuthorization{TaskID: "forged"}}
	// Matching payload hashes are insufficient: the accepted transport header
	// must carry the same server-validated task authorization.
	r := contracts.RelayReceipt{SchemaVersion: 1, ProjectID: "project", Revision: 1, Writer: "bob.b", Commit: contracts.RelayCommit{SchemaVersion: 1, ID: "c", Base: 0, Codec: "coconet-json-v3", Objects: []string{}}}
	r.Commit.Payload, _ = json.Marshal(c)
	r.Commit.SHA256 = graphstore.ObjectDigest(r.Commit.Payload)
	if _, err := decodeReceipt("project", r); err == nil {
		t.Fatal("unverified delegation accepted")
	}
}

func TestFailedHandoffDoesNotBlockHelpersOwnRequest(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, a, wa := replica(t, server, "alice", "a")
	right, b, wb := replica(t, server, "bob", "b")
	left.Remote = processingWire{wa}
	right.Remote = processingWire{wb}
	if _, err := a.Capture(ctx, source("old", "先完善停止条件。")); err != nil {
		t.Fatal(err)
	}
	failing := processingModel(func(inference.Request) (inference.Response, error) {
		return inference.Response{}, errors.New("provider unavailable")
	})
	a.Runner = failing
	if err := a.Drain(ctx, "project", 1); err == nil {
		t.Fatal("missing failure")
	}
	second, err := b.Capture(ctx, source("new", "接下来补一下说明。"))
	if err != nil {
		t.Fatal(err)
	}
	b.Runner = failing
	if err := b.Drain(ctx, "project", 1); err == nil {
		t.Fatal("handoff failure missing")
	}
	b.Runner = processingModel(func(inference.Request) (inference.Response, error) {
		return inference.Response{Text: acceptedStage}, nil
	})
	if err := b.Drain(ctx, "project", 1); err != nil {
		t.Fatal(err)
	}
	state, _ := right.Store.State("project")
	if state.Sessions[second.SessionID].ProcessedThrough != 1 {
		t.Fatal("bad earlier request blocked own work")
	}
}

func TestDelegatedResultBootstrapsFromCheckpointWithoutTaskBody(t *testing.T) {
	ctx := context.Background()
	server, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	left, a, wa := replica(t, server, "alice", "a")
	right, b, wb := replica(t, server, "bob", "b")
	left.Remote, right.Remote = processingWire{wa}, processingWire{wb}
	if _, err := a.Capture(ctx, source("source", "先说明失败后如何排查。")); err != nil {
		t.Fatal(err)
	}
	a.Runner = processingModel(func(inference.Request) (inference.Response, error) {
		return inference.Response{}, errors.New("provider unavailable")
	})
	if err := a.Drain(ctx, "project", 1); err == nil {
		t.Fatal("missing failure")
	}
	if _, err := b.Capture(ctx, source("helper", "看看其他同事在做什么。")); err != nil {
		t.Fatal(err)
	}
	b.Runner = processingModel(func(inference.Request) (inference.Response, error) {
		return inference.Response{Text: acceptedStage}, nil
	})
	if err := b.Drain(ctx, "project", 1); err != nil {
		t.Fatal(err)
	}
	right.Remote = &checkpointTransport{localTransport: wb}
	if err := right.PublishCheckpoint(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	fresh, _, wf := replica(t, server, "carol", "c")
	fresh.Remote = &checkpointTransport{localTransport: wf}
	if err := fresh.Sync(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	want, _ := right.Store.State("project")
	got, _ := fresh.Store.State("project")
	if workgraph.Digest(want) != workgraph.Digest(got) || len(got.Nodes) != 1 {
		t.Fatal("delegated checkpoint diverged")
	}
	for _, n := range got.Nodes {
		if got.Sessions[n.Source.SessionID].Owner != "alice.a" {
			t.Fatal("checkpoint changed source author")
		}
	}
}

func TestOwnerRewindFencesHelperLateSuccessAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			ctx := context.Background()
			server, err := relay.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			left, a, wa := replica(t, server, "alice", "a")
			right, b, wb := replica(t, server, "bob", "b")
			left.Remote = processingWire{wa}
			right.Remote = processingWire{wb}
			old, err := a.Capture(ctx, source("a-session", "ABC"))
			if err != nil {
				t.Fatal(err)
			}
			if err := left.SyncContents(ctx, "project"); err != nil {
				t.Fatal(err)
			}
			a.Runner = processingModel(func(inference.Request) (inference.Response, error) {
				return inference.Response{}, errors.New("owner temporarily unavailable")
			})
			if err := a.Drain(ctx, "project", 1); err == nil {
				t.Fatal("missing failure")
			}
			if _, err := b.Capture(ctx, source("b-session", "E")); err != nil {
				t.Fatal(err)
			}
			b.Runner = processingModel(func(inference.Request) (inference.Response, error) {
				replacement := source("a-session", "ABD")
				replacement.Authoritative = true
				if _, err := a.Capture(ctx, replacement); err != nil {
					t.Fatal(err)
				}
				if failed {
					return inference.Response{}, errors.New("late provider failure")
				}
				return inference.Response{Text: acceptedStage}, nil
			})
			if err := b.Drain(ctx, "project", 1); err != nil {
				t.Fatal("withdrawn helper result was retried", err)
			}
			state, err := right.Store.State("project")
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Nodes) != 0 || !state.Snapshots[old.Key()].Superseded {
				t.Fatal("helper resurrected old work", state)
			}
			page, err := server.ProcessingTasks("project", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Tasks) != 0 {
				t.Fatal("withdrawn task remains eligible", page)
			}
			if err := right.Pull(ctx, "project", old, false); !errors.Is(err, workgraph.ErrSource) {
				t.Fatal("peer pulled withdrawn version", err)
			}
			if err := left.PublishCheckpoint(ctx, "project"); err != nil {
				t.Fatal(err)
			}
			fresh, _, _ := replica(t, server, "carol", "c")
			if err := fresh.Sync(ctx, "project"); err != nil {
				t.Fatal("new replica cannot bootstrap withdrawal", err)
			}
			freshState, _ := fresh.Store.State("project")
			if workgraph.Digest(freshState) != workgraph.Digest(state) {
				t.Fatal("replicas disagree")
			}
		})
	}
}
