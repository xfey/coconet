package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
)

type modelFunc func(inference.Request) (inference.Response, error)

func (f modelFunc) Infer(_ context.Context, r inference.Request) (inference.Response, error) {
	return f(r)
}

type publisherFunc func(context.Context, workgraph.Commit) error

func (f publisherFunc) Publish(ctx context.Context, c workgraph.Commit) error { return f(ctx, c) }

func TestExhaustedBudgetWaitsWithoutConsumingModelRetryAttempts(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "先看看失败提示。")
	now := e.now()
	calls := 0
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		calls++
		return inference.Response{}, &inference.Deferred{Until: now.Add(time.Hour)}
	})
	if err := e.Drain(context.Background(), "project", 1); err != nil {
		t.Fatal(err)
	}
	tasks, _ := store.Tasks("project")
	if len(tasks) != 1 || tasks[0].Status != "budget_wait" || tasks[0].Attempts != 0 {
		t.Fatal("quota became a failed attempt", tasks)
	}
	if err := e.Drain(context.Background(), "project", 3); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("exhausted budget caused repeated attempts")
	}
	if err := e.RecoverLocal(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	tasks, _ = store.Tasks("project")
	if tasks[0].Status != "budget_wait" {
		t.Fatal("restart discarded budget wait")
	}
	e.Now = func() time.Time { return now.Add(time.Hour) }
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		calls++
		return inference.Response{Text: `{"action":"none","stage":null,"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"inspection only","evidence":[]}`}, nil
	})
	if err := e.Drain(context.Background(), "project", 1); err != nil {
		t.Fatal(err)
	}
	tasks, _ = store.Tasks("project")
	if calls != 2 || len(tasks) != 0 {
		t.Fatal("quota did not resume work", tasks)
	}
}

func TestLifecycleOnlySnapshotSkipsModelWithoutChangingNodeSource(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "完善重试文档。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善重试文档", Summary: "正在补充停止条件。"}, "e1"))
	process(t, e, "stage", "session")
	before, _ := store.State("project")
	id := before.Sessions["session"].Tail
	capture(t, e, "session", 2, "完善重试文档。")
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		t.Fatal("lifecycle-only snapshot invoked the model")
		return inference.Response{}, nil
	})
	process(t, e, "stage", "session")
	after, _ := store.State("project")
	if after.Sessions["session"].ProcessedThrough != 2 || after.Nodes[id].Source != before.Nodes[id].Source || workgraph.Digest(after.Nodes[id].Text) != workgraph.Digest(before.Nodes[id].Text) {
		t.Fatal("lifecycle-only update changed substantive work")
	}
	runs, _ := store.Runs("project")
	found := false
	for _, run := range runs {
		if run.Template == "no-new-visible-text/1" {
			found = run.Provider == "" && run.Model == "" && run.Status == "published"
		}
	}
	if !found {
		t.Fatal("deterministic skip lacks its own trace")
	}
}

func TestOfflineFirstCaptureHasDurableBackoffAndPublicationOnlyRetry(t *testing.T) {
	e, store := fixture(t)
	calls := 0
	e.Publisher = publisherFunc(func(context.Context, workgraph.Commit) error { calls++; return errors.New("offline") })
	if _, err := e.Capture(context.Background(), Capture{ProjectID: "project", Agent: "fixture", NativeID: "session"}); err == nil {
		t.Fatal("offline capture claimed publication")
	}
	projects, _ := store.Projects()
	if len(projects) != 1 || projects[0] != "project" {
		t.Fatal("first unpublished project cannot be recovered")
	}
	if err := e.Flush(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("publication ignored backoff")
	}
	if _, err := e.Capture(context.Background(), Capture{ProjectID: "project", Agent: "fixture", NativeID: "session", Ended: true}); err == nil {
		t.Fatal("second capture overwrote the first pending version")
	}
	pending, _ := store.Pending("project")
	if len(pending) != 1 {
		t.Fatal("pending capture identity collision")
	}
	tasks, _ := store.Tasks("project")
	job := tasks[0]
	job.Status = "paused"
	if err := store.PutTask(job); err != nil {
		t.Fatal(err)
	}
	resumed, err := e.Retry("project", job.ID)
	if err != nil || resumed.Status != "publish_pending" {
		t.Fatal("capture retry queued inference", err)
	}
	e.Publisher = nil
	if err := e.Flush(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if state.Revision != 1 {
		t.Fatal("durable capture did not publish")
	}
	runs, _ := store.Runs("project")
	if len(runs) != 1 || runs[0].Status != "published" {
		t.Fatal("publication retry replaced capture evidence")
	}
}

func fixture(t *testing.T) (*Engine, *graphstore.Store) {
	t.Helper()
	store, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Engine{Store: store, Actor: "owner", Provider: "fixture", Model: "fixture", Now: func() time.Time { return time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) }}, store
}
func capture(t *testing.T, e *Engine, session string, version int, texts ...string) {
	t.Helper()
	captureAgent(t, e, "fixture", session, version, texts...)
}
func captureAgent(t *testing.T, e *Engine, agent, session string, version int, texts ...string) {
	t.Helper()
	entries := []conversation.Entry{}
	for i, text := range texts {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		entries = append(entries, conversation.Entry{Turn: contracts.NormalizedTurn{TurnID: fmt.Sprintf("native-secret-%d", i), Ordinal: i, Role: role, Kind: "message", Text: text}})
	}
	digest, err := SaveConversation(e.Store, entries)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := e.Store.State("project")
	c := workgraph.Commit{Schema: 1, ID: newID("capture_"), ProjectID: "project", Actor: e.Actor, Base: state.Revision, RunID: newID("capture_run_"), At: e.stamp(), Snapshot: &workgraph.Snapshot{Source: workgraph.Source{SessionID: session, Version: version}, Owner: e.Actor, Agent: agent, NativeID: "private-native-id", Conversation: digest, Digest: digest, Turns: len(entries), ObservedAt: e.stamp()}}
	if _, err := e.Store.Apply(c, false); err != nil {
		t.Fatal(err)
	}
}
func stage(action string, text *workgraph.Text, refs ...string) string {
	var body any
	if text != nil {
		body = map[string]string{"objective": text.Objective, "summary": text.Summary, "approach": text.Approach}
	}
	data, _ := json.Marshal(map[string]any{"action": action, "stage": body, "items": map[string]any{"add": []string{}, "revise": []any{}, "complete": []string{}, "withdraw": []string{}}, "reason": "accepted work", "evidence": refs})
	return string(data)
}
func output(raw string) modelFunc {
	return func(inference.Request) (inference.Response, error) {
		return inference.Response{Text: raw, Visible: raw}, nil
	}
}
func task(t *testing.T, e *Engine, kind, session string) graphstore.Task {
	t.Helper()
	if err := e.Reconcile("project"); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Store.Tasks("project")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Kind == kind && (session == "" || task.Source != nil && task.Source.SessionID == session) {
			return task
		}
	}
	t.Fatalf("missing %s task", kind)
	return graphstore.Task{}
}
func process(t *testing.T, e *Engine, kind, session string) {
	t.Helper()
	if err := e.Process(context.Background(), task(t, e, kind, session)); err != nil {
		t.Fatal(err)
	}
}

func TestStagesPublishDespiteRelationFailureAndRetainEvidence(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "先把重试补上。", "已完成本地修改。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善重试", Summary: "已完成本地修改"}, "e1", "e2"))
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	id := state.Sessions["session"].Tail
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		return inference.Response{Visible: "provider timeout"}, errors.New("provider unavailable")
	})
	if err := e.Process(context.Background(), task(t, e, "relations", "")); err == nil {
		t.Fatal("provider failure lost")
	}
	capture(t, e, "session", 2, "先把重试补上。", "已完成本地修改。", "沿用他们刚刚的鉴权方案。", "已接入，测试通过。")
	e.Runner = output(stage("continue", nil, "e3", "e4"))
	process(t, e, "stage", "session")
	state, _ = store.State("project")
	node := state.Nodes[id]
	if node.Source.Version != 2 || node.RelationThrough != 0 || node.Relations != "pending" {
		t.Fatalf("relation failure blocked stage or lost cursor: %+v", node)
	}
	input, err := prepareRelations(store, state, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(input.Request.Input), "先把重试补上") || !strings.Contains(string(input.Request.Input), "鉴权方案") {
		t.Fatal("relation retry omitted earlier unchecked dialogue")
	}
	runs, _ := store.Runs("project")
	failed := false
	for _, run := range runs {
		if run.Status == "failed" {
			failed = strings.Contains(run.Output, "provider timeout")
		}
	}
	if !failed {
		t.Fatal("failed provider response not archived")
	}
	if _, err := store.Replay("project"); err != nil {
		t.Fatal(err)
	}
}

func TestLateDependencyUpdatesExistingNodeAndSealsParent(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "auth", 1, "把鉴权方案整理一下。", "方案完成。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "鉴权方案"}, "e1", "e2"))
	process(t, e, "stage", "auth")
	capture(t, e, "retry", 1, "先补重试。", "已完成。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "重试处理"}, "e1", "e2"))
	process(t, e, "stage", "retry")
	state, _ := store.State("project")
	target := state.Sessions["retry"].Tail
	parent := state.Sessions["auth"].Tail
	capture(t, e, "retry", 2, "先补重试。", "已完成。", "沿用刚刚的鉴权方案。", "已接入。")
	e.Runner = output(stage("continue", nil, "e3"))
	process(t, e, "stage", "retry")
	state, _ = store.State("project")
	input, err := prepareRelations(store, state, target, false)
	if err != nil {
		t.Fatal(err)
	}
	alias := ""
	for key, node := range input.Audit.Candidates {
		if node.ID == parent {
			alias = key
		}
	}
	data, _ := json.Marshal(map[string]any{"status": "complete", "parents": []any{map[string]any{"id": alias, "reason": "adopted authentication result", "evidence": []string{"e3", "e4"}}}, "reason": "explicit reuse"})
	// Real model output once appended quoted prose to the anchor. Do not
	// silently trim it: only exact references to this fixed input are valid.
	malformed := strings.Replace(string(data), `"e3"`, `"e3: adopted authentication"`, 1)
	if _, _, err := parseRelations(malformed, input, target); err == nil {
		t.Fatal("relation accepted an anchor with appended prose")
	}
	e.Runner = output(string(data))
	job := graphstore.Task{ID: "relations-test", ProjectID: "project", Kind: "relations", NodeID: target, NodeRevision: state.Nodes[target].Revision, Status: "queued"}
	if err := store.PutTask(job); err != nil {
		t.Fatal(err)
	}
	if err := e.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("project")
	if len(state.Nodes) != 2 || len(state.Nodes[target].Parents) != 1 || state.Nodes[parent].State != "sealed" {
		t.Fatalf("late dependency not applied: %+v", state.Nodes)
	}
	runs, err := store.Runs("project")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Kind == "stage" && run.Template != "local-stage/5" || run.Kind == "relations" && run.Template != "local-relations/7" {
			t.Fatalf("audit does not identify the independently versioned template: %s", run.Template)
		}
	}
}

func TestLostReceiptRecoveryReusesCommitWithoutAnotherModelCall(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "补上重试。")
	count := 0
	e.Runner = modelFunc(func(inference.Request) (inference.Response, error) {
		count++
		raw := stage("start", &workgraph.Text{Objective: "完善重试"}, "e1")
		return inference.Response{Text: raw, Visible: raw}, nil
	})
	e.Publisher = publisherFunc(func(_ context.Context, c workgraph.Commit) error {
		// Simulate successful relay acceptance, replica catch-up, then a lost
		// publication receipt. Matching peer acceptance acknowledges the local attempt.
		if _, err := store.Apply(c, false); err != nil {
			return err
		}
		return errors.New("receipt lost")
	})
	if err := e.Process(context.Background(), task(t, e, "stage", "session")); err != nil {
		t.Fatal("verified peer acceptance was not recognized", err)
	}
	// Peer acceptance is already durable; recovery must not supersede it.
	e.Publisher = nil
	if err := e.Recover(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if len(state.Nodes) != 1 || count != 1 {
		t.Fatalf("recovery duplicated inference: nodes=%d calls=%d", len(state.Nodes), count)
	}
	runs, _ := store.Runs("project")
	if len(runs) != 1 || runs[0].Status != "published" {
		t.Fatalf("accepted local attempt was not recovered: %+v", runs)
	}
}

func TestInputExcludesPrivateMetadataAndRejectsUnprovidedReferences(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "private-session-id", 1, "做一轮检查。")
	state, _ := store.State("project")
	input, err := prepareStage(store, state, workgraph.Source{SessionID: "private-session-id", Version: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-session-id", "private-native-id", "native-secret", "session_id", "observed_at", "owner"} {
		if strings.Contains(string(input.Request.Input), secret) {
			t.Fatalf("model input leaked %s", secret)
		}
	}
	if _, _, err := parseStage(stage("start", &workgraph.Text{Objective: "检查"}, "invented-anchor"), input); err == nil {
		t.Fatal("unprovided anchor accepted")
	}
	if _, _, err := parseStage(`{"action":"none"}`, input); err == nil {
		t.Fatal("missing required response fields accepted")
	}
}

func TestNeedsContextDoesNotConsumeSnapshot(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "按第二项做吧。")
	e.Runner = output(stage("needs_context", nil, "e1"))
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	if state.Sessions["session"].ProcessedThrough != 0 || len(state.Nodes) != 0 {
		t.Fatal("unresolved evidence consumed")
	}
	tasks, _ := store.Tasks("project")
	if len(tasks) != 1 || !tasks[0].Expanded || tasks[0].Status != "queued" {
		t.Fatalf("no bounded context retry: %+v", tasks)
	}
	if err := e.Process(context.Background(), tasks[0]); err != nil {
		t.Fatal(err)
	}
	tasks, _ = store.Tasks("project")
	if tasks[0].Status != "paused" {
		t.Fatal("unbounded context retries")
	}
}

func TestIncompleteCandidateCoverageCannotConfirmIndependence(t *testing.T) {
	input := prepared{Complete: false}
	_, needs, err := parseRelations(`{"status":"complete","parents":[],"reason":"no match"}`, input, "node")
	if err != nil || !needs {
		t.Fatalf("incomplete graph accepted as independent: needs=%t err=%v", needs, err)
	}
}

func TestSnapshotArrivalReusesUnchangedStageBaseline(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "补重试。")
	e.Runner = modelFunc(func(request inference.Request) (inference.Response, error) {
		capture(t, e, "session", 2, "补重试。", "已完成。")
		raw := stage("start", &workgraph.Text{Objective: "完善重试"}, "e1")
		return inference.Response{Text: raw, Visible: raw}, nil
	})
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	session := state.Sessions["session"]
	if session.ProcessedThrough != 1 || session.Latest != 2 || len(state.Nodes) != 1 {
		t.Fatal("unchanged stage baseline was needlessly discarded")
	}
	runs, _ := store.Runs("project")
	if len(runs) != 1 || runs[0].Status != "published" {
		t.Fatal("reused stage result was not traced")
	}
}

func TestNewStageInputDistinguishesAbsentAndSealed(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "整理鉴权方案。")
	state, _ := store.State("project")
	input, err := prepareStage(store, state, workgraph.Source{SessionID: "session", Version: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	var initial stageInput
	if err := json.Unmarshal(input.Request.Input, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Previous.State != "absent" || initial.Previous.Stage != nil {
		t.Fatal("no previous node was represented as sealed")
	}
}

func TestCaptureRejectsRewrittenPrefixAndCopiesArtifact(t *testing.T) {
	e, store := fixture(t)
	c := Capture{ProjectID: "project", Agent: "fixture", NativeID: "native", Artifact: []byte("original snapshot"), Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "t1", Ordinal: 0, Role: "user", Kind: "message", Text: "检查错误。"}}}}
	source, err := e.Capture(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	digest := state.Snapshots[source.Key()].Artifact
	c.Artifact[0] = 'X'
	path, err := store.ArtifactFile(digest)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != "original snapshot" {
		t.Fatal("artifact was not an immutable copy")
	}
	c.Entries[0].Turn.Text = "rewritten"
	if _, err := e.Capture(context.Background(), c); err == nil {
		t.Fatal("rewritten prefix was treated as new delta")
	}
	state, _ = store.State("project")
	if state.Sessions[source.SessionID].Latest != 1 {
		t.Fatal("failed capture advanced version")
	}
}

type describedRunner struct {
	modelFunc
	config inference.Config
}

func (r describedRunner) Configuration() any { return r.config }

func TestPerSessionRunnerRoutesStagesRelationsAndRecordsActualModel(t *testing.T) {
	e, store := fixture(t)
	e.Runner = nil
	selected := []string{}
	e.ResolveRunner = func(project string, session workgraph.Session) (inference.Runner, error) {
		if project != "project" {
			t.Fatal(project)
		}
		selected = append(selected, session.Agent)
		return describedRunner{modelFunc: func(r inference.Request) (inference.Response, error) {
			if strings.Contains(string(r.Input), "private-profile") || strings.Contains(string(r.Input), "config_root") {
				t.Fatal("routing metadata entered prompt")
			}
			var schema map[string]any
			json.Unmarshal(r.Schema, &schema)
			properties := schema["properties"].(map[string]any)
			raw := stage("start", &workgraph.Text{Objective: "改进登录提示"}, "e1")
			if properties["action"] == nil {
				raw = `{"status":"complete","parents":[],"reason":"Independent work"}`
			}
			return inference.Response{Text: raw, Model: session.Agent + "-default"}, nil
		}, config: inference.Config{Provider: session.Agent, ConfigRoot: "/private-profile"}}, nil
	}
	for _, agent := range []string{"codex", "claude"} {
		captureAgent(t, e, agent, agent+"-session", 1, "先看看登录后的提示，简洁一点。")
		process(t, e, "stage", agent+"-session")
		process(t, e, "relations", "")
	}
	if strings.Join(selected, ",") != "codex,codex,claude,claude" {
		t.Fatal(selected)
	}
	runs, err := store.Runs("project")
	if err != nil || len(runs) != 4 {
		t.Fatal(runs, err)
	}
	for _, run := range runs {
		if run.Status != "published" || run.Model != run.Provider+"-default" || !strings.Contains(string(run.Input), "private-profile") {
			t.Fatal(run)
		}
	}
	// Lifecycle-only observations do not require an available Agent installation.
	e.ResolveRunner = func(string, workgraph.Session) (inference.Runner, error) {
		t.Fatal("no-text update resolved an Agent")
		return nil, nil
	}
	captureAgent(t, e, "claude", "claude-session", 2, "先看看登录后的提示，简洁一点。")
	process(t, e, "stage", "claude-session")
}

func TestMissingSourceRunnerIsAnArchivedRetryableFailure(t *testing.T) {
	e, store := fixture(t)
	captureAgent(t, e, "claude", "session", 1, "请整理一下登录界面。")
	e.ResolveRunner = func(string, workgraph.Session) (inference.Runner, error) {
		return nil, errors.New("source Agent unavailable")
	}
	err := e.Process(context.Background(), task(t, e, "stage", "session"))
	if err == nil {
		t.Fatal("missing Agent succeeded")
	}
	runs, _ := store.Runs("project")
	tasks, _ := store.Tasks("project")
	if len(runs) != 1 || runs[0].Status != "failed" || runs[0].Provider != "claude" || !strings.Contains(runs[0].Error, "source Agent unavailable") || len(tasks) != 1 || tasks[0].Attempts != 1 {
		t.Fatal(runs, tasks)
	}
}
