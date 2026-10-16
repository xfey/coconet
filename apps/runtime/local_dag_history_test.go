package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
)

type historyModelFunc func(context.Context, inference.Request) (inference.Response, error)

func (f historyModelFunc) Infer(ctx context.Context, r inference.Request) (inference.Response, error) {
	return f(ctx, r)
}

func historyFixture(t *testing.T, names ...string) (string, *localdag.Engine, localHistoryPlan) {
	t.Helper()
	root, workspace, profile := t.TempDir(), t.TempDir(), t.TempDir()
	workspace, _ = canonicalPath(workspace)
	profile, _ = canonicalPath(profile)
	config := localDAGServiceConfig{Schema: 1, Actor: "device", Provider: "codex", Command: "isolated-fixture", Profile: filepath.Join(root, "inference"), Model: "fixture", MaxCalls: 20, TimeoutSeconds: 120, Workspaces: []localDAGWorkspace{{Path: workspace, ProjectID: "project"}}}
	if err := atomicWriteJSON(filepath.Join(root, "runtime.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	engine := &localdag.Engine{Store: store, Actor: config.Actor, Provider: config.Provider, Model: config.Model}
	t.Cleanup(func() { engine.Store.Close() })
	engine.TaskContext = localHistoryTaskContext(root, engine)
	candidates := []contracts.DiscoveredSession{}
	for _, name := range names {
		candidates = append(candidates, contracts.DiscoveredSession{SchemaVersion: 1, AgentType: contracts.AgentCodex, NativeSessionID: name, CWD: workspace, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	value, err := localHistoryAction(context.Background(), root, engine, "project", "history-preview", localHistoryRequest{Agent: contracts.AgentCodex, Root: workspace, ConfigRoot: profile}, previewAdapter{agent: contracts.AgentCodex, sessions: candidates})
	if err != nil {
		t.Fatal(err)
	}
	return root, engine, value.(localHistoryPlan)
}

func historyAction(t *testing.T, root string, engine *localdag.Engine, action string, request localHistoryRequest) localHistoryPlan {
	t.Helper()
	result, err := localHistoryAction(context.Background(), root, engine, "project", "history-"+action, request, nil)
	if err != nil {
		t.Fatal(action, err)
	}
	return result.(localHistoryPlan)
}

func historyPrepared(t *testing.T, root string, engine *localdag.Engine, plan localHistoryPlan, index int) localDAGInboxEvent {
	t.Helper()
	item := plan.Items[index]
	text := "先看看错误提示，把需要用户重试的情况说清楚。"
	turn := contracts.NormalizedTurn{TurnID: "t", Ordinal: 0, Role: "user", Kind: "message", Text: text, ContentHash: adapters.Digest(text)}
	normalized, _ := adapters.ApplyCheckpoint([]contracts.NormalizedTurn{turn}, adapters.ObservationCheckpoint{})
	artifact, err := engine.Store.SaveArtifactBytes([]byte("fixed historical source; no future turns"))
	if err != nil {
		t.Fatal(err)
	}
	observation := localdag.Observation{ProjectID: plan.ProjectID, Agent: string(plan.Agent), NativeID: item.Session.NativeSessionID, Normalized: normalized, ArtifactManifest: artifact, Visible: &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: len(text)}}}}}}
	data, _ := json.Marshal(observation)
	path, event, err := historyItemEvent(root, plan, item)
	if err != nil {
		t.Fatal(err)
	}
	event.Prepared, err = engine.Store.PutObject(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteJSON(path, event, 0600); err != nil {
		t.Fatal(err)
	}
	return event
}

func historyTick(t *testing.T, root string, engine *localdag.Engine) {
	t.Helper()
	if err := tickLocalHistory(context.Background(), root, engine); err != nil {
		t.Fatal(err)
	}
}

const historyNoWork = `{"action":"none","stage":null,"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"inspection only","evidence":[]}`
const historyStage = `{"action":"start","stage":{"objective":"完善错误提示","summary":"说明了重试条件","approach":"先分类再提示"},"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"明确交付","evidence":["e1"]}`

func TestHistoryPlanLimitMatchesDurableEncoding(t *testing.T) {
	root, _, plan := historyFixture(t, "boundary")
	path, _ := historyPlanPath(root, plan.ID)
	before, _ := os.ReadFile(path)
	encoded, _ := json.MarshalIndent(plan, "", "  ")
	plan.Candidates[0].CWD += strings.Repeat("x", (512<<10)-len(encoded))
	compact, _ := json.Marshal(plan)
	if len(compact) >= 512<<10 {
		t.Fatal("fixture must fit compact encoding")
	}
	if err := saveHistoryPlan(root, plan); err == nil {
		t.Fatal("saved metadata exceeding reader's on-disk size bound")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("oversized plan replaced the previous readable plan")
	}
}

func TestHistorySelectionBatchBudgetAndCompletion(t *testing.T) {
	root, engine, plan := historyFixture(t, "first", "second", "unselected")
	state, _ := engine.Store.State("project")
	if len(state.Sessions) != 0 || plan.Status != "preview" || len(plan.Candidates) != 3 {
		t.Fatal("preview read or captured history")
	}
	plan = historyAction(t, root, engine, "start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"first", "second"}, MaxCalls: 1, BatchSize: 1})
	if _, err := localHistoryAction(context.Background(), root, engine, "project", "history-start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"unselected"}, MaxCalls: 10, BatchSize: 1}, nil); err == nil {
		t.Fatal("start replaced an existing selection")
	}
	calls := 0
	engine.Runner = &limitedInference{Store: engine.Store, Limit: 20, Runner: historyModelFunc(func(context.Context, inference.Request) (inference.Response, error) {
		calls++
		return inference.Response{Text: historyNoWork}, nil
	})}
	historyPrepared(t, root, engine, plan, 0)
	historyPrepared(t, root, engine, plan, 1)
	historyTick(t, root, engine)
	if err := engine.Drain(context.Background(), "project", 10); err != nil {
		t.Fatal(err)
	}
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	if plan.Status != "batch_wait" || plan.UsedCalls != 1 || plan.Items[0].Status != "complete" || plan.Items[1].Status != "selected" {
		t.Fatalf("batch did not stop after first item: %+v", plan)
	}
	if _, err := localHistoryAction(context.Background(), root, engine, "project", "history-resume", localHistoryRequest{PlanID: plan.ID}, nil); err == nil {
		t.Fatal("resume reset spent budget")
	}
	plan = historyAction(t, root, engine, "resume", localHistoryRequest{PlanID: plan.ID, MaxCalls: 2})
	historyTick(t, root, engine)
	if err := engine.Drain(context.Background(), "project", 10); err != nil {
		t.Fatal(err)
	}
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	state, _ = engine.Store.State("project")
	if plan.Status != "complete" || calls != 2 || plan.UsedCalls != 2 || len(state.Sessions) != 2 {
		t.Fatalf("bounded import did not complete: %+v calls=%d", plan, calls)
	}
	if _, err := localHistoryAction(context.Background(), root, engine, "project", "history-forget", localHistoryRequest{PlanID: plan.ID}, nil); err != nil {
		t.Fatal(err)
	}
	plans, err := readHistoryPlans(root)
	if err != nil || len(plans) != 0 {
		t.Fatal("completed plan metadata not removed", err)
	}
}

func TestHistoryPauseRestartAndRelationsShareOnePersistentBudget(t *testing.T) {
	root, engine, plan := historyFixture(t, "first")
	plan = historyAction(t, root, engine, "start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"first"}, MaxCalls: 1, BatchSize: 1})
	event := historyPrepared(t, root, engine, plan, 0)
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "pause", localHistoryRequest{PlanID: plan.ID})
	calls := 0
	model := historyModelFunc(func(context.Context, inference.Request) (inference.Response, error) {
		calls++
		if calls == 1 {
			return inference.Response{Text: historyStage}, nil
		}
		return inference.Response{Text: `{"status":"complete","parents":[],"reason":"independent work"}`}, nil
	})
	engine.Runner = &limitedInference{Store: engine.Store, Limit: 20, Runner: model}
	if err := engine.Drain(context.Background(), "project", 5); err != nil || calls != 0 {
		t.Fatal("paused history invoked model", err, calls)
	}
	if err := enqueueLocalDAGObservation(root, event.Request); err != nil {
		t.Fatal(err)
	}
	if err := drainLocalDAGInbox(context.Background(), root, engine); err != nil {
		t.Fatal(err)
	}
	queue, _ := localDAGInbox(root)
	if len(queue) != 1 {
		t.Fatal("new native history bypassed a paused import")
	}
	historyAction(t, root, engine, "resume", localHistoryRequest{PlanID: plan.ID})
	if err := engine.Drain(context.Background(), "project", 5); err != nil || calls != 1 {
		t.Fatal("stage and relations exceeded shared import budget", err, calls)
	}
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	if plan.Status != "budget_wait" {
		t.Fatal(plan.Status)
	}
	if err := engine.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	engine.Store = store
	engine.Runner = &limitedInference{Store: store, Limit: 20, Runner: model}
	if err := engine.RecoverLocal(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Drain(context.Background(), "project", 5); err != nil || calls != 1 {
		t.Fatal("restart replenished import budget", err)
	}
	historyAction(t, root, engine, "resume", localHistoryRequest{PlanID: plan.ID, MaxCalls: 2})
	if err := engine.Drain(context.Background(), "project", 1); err != nil {
		t.Fatal(err)
	}
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	if plan.Status != "complete" || calls != 2 {
		t.Fatal("relations not completed under extended total cap", plan.Status, calls)
	}
	blocked, err := localHistoryBlocksCapture(context.Background(), root, engine, event.Request)
	if err != nil || blocked {
		t.Fatal("completed import still blocks subsequent native updates", err)
	}
	replayed, err := store.Replay("project")
	if err != nil || len(replayed.Nodes) != 1 {
		t.Fatal("import cannot replay", err)
	}
}

func TestHistoryFixedInputSurvivesFailureAndCacheCollection(t *testing.T) {
	root, engine, plan := historyFixture(t, "first")
	plan = historyAction(t, root, engine, "start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"first"}, MaxCalls: 4, BatchSize: 1})
	event := historyPrepared(t, root, engine, plan, 0)
	// Poison the caller's native profile path. Only the already fixed observation
	// can succeed; import retries must not silently reread a longer live session.
	if err := os.Remove(plan.ConfigRoot); err != nil {
		t.Fatal(err)
	}
	roots, err := localHistoryCacheRoots(context.Background(), root, engine.Store)
	if err != nil || len(roots.Objects) != 1 || roots.Objects[0] != event.Prepared || len(roots.Artifacts) != 1 {
		t.Fatal("prepared capture not rooted", err)
	}
	if _, err := engine.Store.PruneCache(context.Background(), time.Now().Add(10*24*time.Hour), engine.Actor, roots); err != nil {
		t.Fatal(err)
	}
	historyTick(t, root, engine)
	calls := 0
	engine.Now = func() time.Time { return time.Now().Add(time.Hour) }
	engine.Runner = &limitedInference{Store: engine.Store, Limit: 20, Runner: historyModelFunc(func(context.Context, inference.Request) (inference.Response, error) {
		calls++
		return inference.Response{Visible: "failed provider response"}, errors.New("synthetic provider failure")
	})}
	for i := 0; i < 3; i++ {
		engine.Now = func() time.Time { return time.Now().Add(time.Duration(i+1) * time.Hour) }
		if err := engine.Drain(context.Background(), "project", 1); err == nil {
			t.Fatal("model failure disappeared")
		}
	}
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	if plan.Status != "failed" || plan.UsedCalls != 3 || !strings.Contains(plan.Items[0].Error, "synthetic") {
		t.Fatal("terminal failure not actionable", plan.Status)
	}
	historyAction(t, root, engine, "retry", localHistoryRequest{PlanID: plan.ID})
	engine.Runner = &limitedInference{Store: engine.Store, Limit: 20, Runner: historyModelFunc(func(context.Context, inference.Request) (inference.Response, error) {
		calls++
		return inference.Response{Text: historyNoWork}, nil
	})}
	if err := engine.Drain(context.Background(), "project", 1); err != nil {
		t.Fatal(err)
	}
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	state, _ := engine.Store.State("project")
	if plan.Status != "complete" || calls != 4 || len(state.Snapshots) != 1 {
		t.Fatal("retry changed fixed version or reset cap", plan.Status)
	}
	if roots, err := localHistoryCacheRoots(context.Background(), root, engine.Store); err != nil || len(roots.Objects) != 0 {
		t.Fatal("completed private plan still pins prepared content", err)
	}
}

func TestHistoryConfigurationChangeAndPartialInventoryAreExplicit(t *testing.T) {
	root, engine, plan := historyFixture(t, "first")
	config, _ := loadLocalDAGService(root)
	value, err := localHistoryAction(context.Background(), root, engine, "project", "history-preview", localHistoryRequest{Agent: plan.Agent, Root: plan.Root, ConfigRoot: plan.ConfigRoot}, previewAdapter{agent: plan.Agent, sessions: plan.Candidates, err: adapters.ErrDiscoveryLimit})
	if err != nil || !strings.Contains(value.(localHistoryPlan).ScopeNote, "partial inventory") {
		t.Fatal("discovery cap hidden", err)
	}
	config.Model = "different-model"
	if err := atomicWriteJSON(filepath.Join(root, "runtime.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := localHistoryAction(context.Background(), root, engine, "project", "history-start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"first"}, MaxCalls: 2, BatchSize: 1}, nil); err == nil {
		t.Fatal("stale preview silently chose a different model")
	}
}

func TestHistoryInFlightPauseDoesNotSpendAnotherCallOrBlockUnrelatedWork(t *testing.T) {
	root, engine, plan := historyFixture(t, "first")
	plan = historyAction(t, root, engine, "start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"first"}, MaxCalls: 3, BatchSize: 1})
	historyPrepared(t, root, engine, plan, 0)
	historyTick(t, root, engine)
	model := &blockingLocalModel{Started: make(chan struct{}, 1), Release: make(chan struct{})}
	engine.Runner = &limitedInference{Store: engine.Store, Limit: 20, Runner: model}
	done := make(chan error, 1)
	go func() { done <- engine.Drain(context.Background(), "project", 1) }()
	select {
	case <-model.Started:
	case <-time.After(3 * time.Second):
		t.Fatal("import model did not start")
	}
	historyAction(t, root, engine, "pause", localHistoryRequest{PlanID: plan.ID})
	close(model.Release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	if plan.Status != "paused" || plan.UsedCalls != 1 {
		t.Fatal("in-flight receipt changed the pause or quota")
	}
	// A later ordinary update of this native identity stays queued, while a
	// different Session remains usable through the regular capture interface.
	if _, err := captureLocalDAG(context.Background(), root, engine, localdag.Capture{ProjectID: "project", Agent: "codex", NativeID: "first"}); err == nil {
		t.Fatal("direct capture bypassed import boundary")
	}
	if _, err := captureLocalDAG(context.Background(), root, engine, localdag.Capture{ProjectID: "project", Agent: "codex", NativeID: "independent"}); err != nil {
		t.Fatal("paused import blocked other work", err)
	}
	if err := engine.Drain(context.Background(), "project", 5); err != nil {
		t.Fatal(err)
	}
	historyAction(t, root, engine, "resume", localHistoryRequest{PlanID: plan.ID})
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	if plan.Status != "complete" || plan.UsedCalls != 1 {
		t.Fatal("accepted in-flight result was processed twice")
	}
}

func TestHistoryControlIPCRecordsPauseAndHidesLargeListDetails(t *testing.T) {
	root, engine, plan := historyFixture(t, "first")
	plan = historyAction(t, root, engine, "start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"first"}, MaxCalls: 1, BatchSize: 1})
	historyAction(t, root, engine, "pause", localHistoryRequest{PlanID: plan.ID})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveLocalDAG(ctx, root, engine, 1, io.Discard) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "control.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var out, diagnostics bytes.Buffer
	options := localDAGClientOptions{Project: "project", History: &localHistoryRequest{PlanID: plan.ID}}
	if handled, code := localDAGClient(ctx, root, "history-pause", options, &out, &diagnostics); !handled || code != 0 {
		t.Fatal("history IPC failed", diagnostics.String())
	}
	runs, err := engine.Store.Runs("project")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, run := range runs {
		if run.Kind == "history" && run.Status == "completed" && run.Output != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("history control lacks audit record")
	}
	out.Reset()
	if handled, code := localDAGClient(ctx, root, "history-list", options, &out, &diagnostics); !handled || code != 0 {
		t.Fatal(diagnostics.String())
	}
	var listed []localHistoryPlan
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil || len(listed) != 1 || len(listed[0].Items) != 0 || len(listed[0].Candidates) != 0 {
		t.Fatal("unbounded history listing", err)
	}
}

func TestHistorySelectionWithoutModelQuota(t *testing.T) {
	root, engine, plan := historyFixture(t, "selected")
	plan = historyAction(t, root, engine, "start", localHistoryRequest{PlanID: plan.ID, Sessions: []string{"selected"}, BatchSize: 1})
	historyPrepared(t, root, engine, plan, 0)
	historyTick(t, root, engine)
	engine.Runner = historyModelFunc(func(ctx context.Context, _ inference.Request) (inference.Response, error) {
		// No daily or per-plan quota in automatic mode. Reservations remain durable telemetry.
		for i := 0; i < 125; i++ {
			if err := reserveHistoryCall(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return inference.Response{Text: historyNoWork}, nil
	})
	if err := engine.Drain(context.Background(), "project", 1); err != nil {
		t.Fatal(err)
	}
	historyTick(t, root, engine)
	plan = historyAction(t, root, engine, "status", localHistoryRequest{PlanID: plan.ID})
	if plan.Status != "complete" || plan.UsedCalls != 125 || plan.MaxCalls != 0 {
		t.Fatal(plan)
	}
}
