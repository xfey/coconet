package localdag

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
)

func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}

// Publisher must confirm a commit in the shared order. On CAS conflict it
// refreshes Store before returning ErrConflict. Nil is isolated local mode.
type Publisher interface {
	Publish(context.Context, workgraph.Commit) error
}
type Engine struct {
	commitMu sync.Mutex
	Store    *graphstore.Store
	Runner   inference.Runner
	// ResolveRunner selects an owner-local Agent using private routing metadata.
	// It never adds routing fields to model input.
	ResolveRunner func(string, workgraph.Session) (inference.Runner, error)
	Publisher     Publisher
	Actor         string
	Provider      string
	Model         string
	Now           func() time.Time
	// TaskContext applies device-local scheduling policy before input creation.
	// A false result leaves the durable task intact without spending an attempt.
	TaskContext func(context.Context, graphstore.Task) (context.Context, bool, error)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func (e *Engine) stamp() string { return e.now().Format(time.RFC3339Nano) }

// Reconcile reconstructs missing work after a crash between graph publication
// and task scheduling. It never resets paused tasks or runs another writer's
// inference. One stage per Session is eligible, relations have separate keys.
func (e *Engine) Reconcile(project string) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	return e.reconcile(project)
}

func (e *Engine) reconcile(project string) error {
	state, err := e.Store.State(project)
	if err != nil {
		return err
	}
	tasks, err := e.Store.Tasks(project)
	if err != nil {
		return err
	}
	pending, err := e.Store.Pending(project)
	if err != nil {
		return err
	}
	pendingRuns := map[string]bool{}
	for _, commit := range pending {
		pendingRuns[commit.RunID] = true
	}
	existing := map[string]graphstore.Task{}
	for _, task := range tasks {
		if (task.Kind == "retire" || task.Kind == "library") && !pendingRuns[task.RunID] {
			if err := e.Store.DeleteTask(project, task.ID); err != nil {
				return err
			}
			continue
		}
		obsolete := obsoleteTask(state, task)
		if obsolete && task.Status != "running" && task.Status != "publish_pending" {
			if err := e.Store.DeleteTask(project, task.ID); err != nil {
				return err
			}
			continue
		}
		if task.Kind == "relations" && task.Status == "paused" && task.ContextBasis != "" && task.ContextBasis != relationContextBasis(state, task.NodeID) {
			task.Status, task.NextAt, task.Error = "queued", "", "new relation candidates available"
			task.Attempts = 0
			if err := e.Store.PutTask(task); err != nil {
				return err
			}
		}
		existing[task.ID] = task
	}
	add := func(task graphstore.Task) error {
		if _, ok := existing[task.ID]; ok {
			return nil
		}
		return e.Store.EnsureTask(task)
	}
	for _, session := range state.Sessions {
		if session.Owner != e.Actor {
			continue
		}
		if session.ProcessedThrough < session.Latest {
			source := workgraph.Source{SessionID: session.ID, Version: session.ProcessedThrough + 1}
			if err := add(graphstore.Task{ID: "stage/" + source.Key(), ProjectID: project, Kind: "stage", Source: &source, Status: "queued"}); err != nil {
				return err
			}
		}
	}
	for _, node := range state.Nodes {
		if state.Sessions[node.Source.SessionID].Owner != e.Actor || node.State == "withdrawn" || node.Relations == "ready" {
			continue
		}
		// Revision in the key permits a fresh job when new stage evidence arrives,
		// while the node's independent cursor preserves earlier pending evidence.
		id := fmt.Sprintf("relations/%s/%d", node.ID, node.Revision)
		if err := add(graphstore.Task{ID: id, ProjectID: project, Kind: "relations", NodeID: node.ID, NodeRevision: node.Revision, Status: "queued"}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) Process(ctx context.Context, task graphstore.Task) error {
	if e.Store == nil || (e.Runner == nil && e.ResolveRunner == nil) || e.Actor == "" {
		return errors.New("incomplete local DAG engine")
	}
	if task.Status == "paused" || task.Status == "publish_pending" {
		return nil
	}
	if e.TaskContext != nil {
		next, allowed, err := e.TaskContext(ctx, task)
		if err != nil || !allowed {
			return err
		}
		ctx = next
	}
	if task.NextAt != "" {
		next, err := time.Parse(time.RFC3339Nano, task.NextAt)
		if err != nil {
			return err
		}
		if e.now().Before(next) {
			return nil
		}
	}
	if coordinator := e.coordinator(ctx); coordinator != nil {
		return e.processShared(ctx, task, coordinator)
	}
	// Fix inputs and their temporary roots together with respect to capture
	// and retirement. Model execution itself never holds the commit lock.
	releaseContent := e.Store.PinContent()
	defer releaseContent()
	e.commitMu.Lock()
	locked := true
	defer func() {
		if locked {
			e.commitMu.Unlock()
		}
	}()
	state, err := e.Store.State(task.ProjectID)
	if err != nil {
		return err
	}
	if obsoleteTask(state, task) {
		return e.Store.DeleteTask(task.ProjectID, task.ID)
	}
	var input prepared
	switch task.Kind {
	case "stage":
		if task.Source == nil {
			return errors.New("stage task has no source")
		}
		session, ok := state.Sessions[task.Source.SessionID]
		if !ok || session.Owner != e.Actor {
			return workgraph.ErrPermission
		}
		if session.ProcessedThrough >= task.Source.Version {
			return e.Store.DeleteTask(task.ProjectID, task.ID)
		}
		input, err = prepareStage(e.Store, state, *task.Source, task.Expanded)
	case "relations":
		node, ok := state.Nodes[task.NodeID]
		if !ok || state.Sessions[node.Source.SessionID].Owner != e.Actor {
			return workgraph.ErrPermission
		}
		if node.Revision != task.NodeRevision || node.Relations == "ready" {
			return e.Store.DeleteTask(task.ProjectID, task.ID)
		}
		input, err = prepareRelations(e.Store, state, task.NodeID, task.Expanded)
	default:
		return errors.New("unknown DAG task kind")
	}
	if err != nil {
		return err
	}
	// The local and shared paths use the same complete request and capsule
	// limits. Reject known-incomplete input before spending a model attempt.
	if _, err := fixedProcessingInput(state, task, input); err != nil {
		return e.waitInput(task, state, err)
	}
	noNewText := task.Kind == "stage" && input.Audit.From == input.Audit.Through
	runner := e.Runner
	provider, model := e.Provider, e.Model
	var selectionErr error
	if !noNewText && e.ResolveRunner != nil {
		session := state.Sessions[input.Source.SessionID]
		provider, model = session.Agent, ""
		runner, selectionErr = e.ResolveRunner(task.ProjectID, session)
		if selectionErr == nil && runner == nil {
			selectionErr = errors.New("source Agent is unavailable")
		}
	}
	if described, ok := runner.(interface{ Configuration() any }); ok {
		input.Execution = described.Configuration()
	}
	if noNewText {
		input.Execution = map[string]string{"rule": "no-new-visible-text/1"}
	}
	archive, err := json.Marshal(input)
	if err != nil {
		return err
	}
	runID := newID("run_")
	template := "local-stage/5"
	if task.Kind == "relations" {
		template = "local-relations/7"
	}
	run := graphstore.Run{ID: runID, TaskID: task.ID, ProjectID: task.ProjectID, Kind: task.Kind, Status: "running", StartedAt: e.stamp(), Provider: provider, Model: model, Template: template, Input: archive, Base: state.Revision, Source: &input.Source, NodeID: task.NodeID}
	for _, candidate := range input.Audit.Candidates {
		run.Protected = append(run.Protected, candidate.Source)
	}
	if noNewText {
		run.Provider, run.Model, run.Template = "", "", "no-new-visible-text/1"
	}
	if err := e.Store.Begin(run); err != nil {
		return err
	}
	if !noNewText {
		task.Attempts++
	}
	task.RunID = runID
	task.Status = "running"
	task.ContextBasis, task.AfterPublish = "", ""
	if err := e.Store.PutTask(task); err != nil {
		return err
	}
	e.commitMu.Unlock()
	locked = false
	releaseContent()
	var response inference.Response
	var modelErr error
	if noNewText {
		response.Text = `{"action":"none","stage":null,"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"No new visible dialogue; retain existing stage and commitments.","evidence":[]}`
		response.Visible = response.Text
	} else if selectionErr != nil {
		modelErr = inference.ConfigurationError(selectionErr)
	} else {
		response, modelErr = runner.Infer(ctx, input.Request)
	}
	output, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if err := e.Store.SaveModelResponse(runID, string(output), response.Usage, response.Model); err != nil {
		return err
	}
	if modelErr != nil {
		var deferred *inference.Deferred
		if errors.As(modelErr, &deferred) {
			if err := e.Store.Finish(runID, "deferred", deferred.Error(), e.stamp()); err != nil {
				return err
			}
			task.Attempts--
			task.Status, task.Error = "budget_wait", deferred.Error()
			task.NextAt = deferred.Until.UTC().Format(time.RFC3339Nano)
			return e.Store.PutTask(task)
		}
		return e.fail(task, "failed", modelErr)
	}
	commit := workgraph.Commit{Schema: workgraph.SchemaVersion, ID: newID("commit_"), ProjectID: task.ProjectID, Actor: e.Actor, Base: state.Revision, RunID: runID, At: e.stamp()}
	needsContext := false
	if task.Kind == "stage" {
		commit.Stage, needsContext, err = parseStage(response.Text, input)
	} else {
		commit.Relations, needsContext, err = parseRelations(response.Text, input, task.NodeID)
	}
	if err != nil {
		return e.fail(task, "failed", err)
	}
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	latestState, readErr := e.Store.State(task.ProjectID)
	if readErr != nil {
		return readErr
	}
	if obsoleteTask(latestState, task) {
		return e.finishObsolete(task)
	}
	// Snapshot uploads or another Session can advance the project revision
	// without changing this Session's semantic baseline. Reuse only that stage
	// result; relation candidates always remain tied to their original graph.
	if task.Kind == "stage" {
		latest, readErr := e.Store.State(task.ProjectID)
		if readErr != nil {
			return readErr
		}
		session := latest.Sessions[input.Source.SessionID]
		if latest.Revision != state.Revision && session.Revision == input.SessionRevision && session.ProcessedThrough+1 == input.Source.Version {
			state = latest
			commit.Base = latest.Revision
		}
	}
	if needsContext && task.Kind == "stage" {
		if err := e.Store.Finish(runID, "needs_context", "additional evidence required", e.stamp()); err != nil {
			return err
		}
		task.Status = e.contextRetry(state, task, input)
		task.Attempts = 0
		task.NextAt = ""
		task.Error = "additional evidence required"
		task.Expanded = true
		return e.Store.PutTask(task)
	}
	if needsContext {
		task.AfterPublish = e.contextRetry(state, task, input)
		task.Attempts = 0
		task.Expanded = true
		task.ContextBasis = relationContextBasis(state, task.NodeID)
	}
	if _, _, err := workgraph.Apply(state, commit); err != nil {
		return e.fail(task, "failed", err)
	}
	if err := e.Store.Finish(runID, "ready", "", e.stamp()); err != nil {
		return err
	}
	if err := e.Store.SavePending(commit); err != nil {
		return err
	}
	task.Status = "publish_pending"
	if err := e.Store.PutTask(task); err != nil {
		return err
	}
	return e.publish(ctx, commit, task)
}

func (e *Engine) fail(task graphstore.Task, status string, cause error) error {
	if state, err := e.Store.State(task.ProjectID); err == nil && obsoleteTask(state, task) {
		return e.finishObsolete(task)
	}
	message := cause.Error()
	if len(message) > 4000 {
		message = clip(message, 4000)
	}
	if err := e.Store.Finish(task.RunID, status, message, e.stamp()); err != nil {
		return err
	}
	task.Error = message
	task.Status = "retry"
	if task.Attempts >= 3 {
		task.Status = "paused"
	}
	task.NextAt = e.now().Add(time.Duration(1<<min(task.Attempts, 6)) * time.Second).Format(time.RFC3339Nano)
	if delay, waiting := inference.RetryDelay(cause); waiting {
		task.Attempts = max(0, task.Attempts-1)
		task.Status = "retry"
		task.NextAt = e.now().Add(delay).Format(time.RFC3339Nano)
	}
	if err := e.Store.PutTask(task); err != nil {
		return err
	}
	return cause
}

func (e *Engine) publish(ctx context.Context, commit workgraph.Commit, task graphstore.Task) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = nil
		if e.Publisher != nil {
			err = e.Publisher.Publish(ctx, commit)
		}
		if errors.Is(err, contracts.ErrProcessingStale) {
			var next workgraph.Commit
			next, err = e.reclaimPublication(ctx, commit)
			if err == nil {
				commit = next
				// A refresh is not acceptance. The new authorization must pass
				// the ordinary server append transaction, even on the last pass.
				err = e.Publisher.Publish(ctx, commit)
			}
		}
		if err == nil {
			_, err = e.Store.Apply(commit, true)
		}
		if !errors.Is(err, workgraph.ErrConflict) {
			break
		}
		latest, readErr := e.Store.State(commit.ProjectID)
		if readErr != nil {
			return readErr
		}
		reusable := commit.Snapshot != nil
		if d := commit.Stage; d != nil {
			session := latest.Sessions[d.Source.SessionID]
			reusable = session.Revision == d.SessionRevision && session.ProcessedThrough+1 == d.Source.Version
		}
		if d := commit.Library; d != nil {
			_, selectionErr := workgraph.SelectNode(latest, d.NodeID, d.Source)
			reusable = d.Source != nil && selectionErr == nil
		}
		if !reusable || attempt == 2 {
			break
		}
		next := commit
		next.ID = newID("commit_")
		next.Base = latest.Revision
		if _, _, validateErr := workgraph.Apply(latest, next); validateErr != nil {
			break
		}
		if rebaseErr := e.Store.RebasePending(commit, next); rebaseErr != nil {
			return rebaseErr
		}
		commit = next
	}
	if errors.Is(err, workgraph.ErrConflict) && commit.Snapshot == nil {
		if finishErr := e.Store.Finish(commit.RunID, "superseded", "graph baseline changed before publication", e.stamp()); finishErr != nil {
			return finishErr
		}
		if removeErr := e.Store.RemovePending(commit.ProjectID, commit.ID); removeErr != nil {
			return removeErr
		}
		if commit.Library != nil {
			if err := e.Store.DeleteTask(task.ProjectID, task.ID); err != nil {
				return err
			}
			return ErrSelectionChanged
		}
		if commit.Retire != nil {
			// Maintenance will recompute against the next accepted graph, never
			// enqueue a semantic model task for stale deletion candidates.
			return e.Store.DeleteTask(task.ProjectID, task.ID)
		}
		current, readErr := e.Store.State(task.ProjectID)
		if readErr != nil {
			return readErr
		}
		if obsoleteTask(current, task) {
			return e.Store.DeleteTask(task.ProjectID, task.ID)
		}
		task.Status = "queued"
		task.SharedID = ""
		task.NextAt = ""
		task.Error = "graph baseline changed before publication"
		if saveErr := e.Store.PutTask(task); saveErr != nil {
			return saveErr
		}
		return err
	}
	if err != nil {
		if accepted, readErr := e.Store.HasCommit(commit); readErr != nil {
			return errors.Join(err, readErr)
		} else if accepted {
			return nil // Applying the receipt already finalized the matching task.
		}
		task.PublishAttempts++
		task.Status = "publish_pending"
		task.Error = err.Error()
		task.NextAt = e.now().Add(time.Duration(1<<min(task.PublishAttempts, 8)) * time.Second).Format(time.RFC3339Nano)
		// Connectivity failures retain the already-generated result indefinitely
		// at a capped backoff. Permanent semantic/protocol refusals stay visible.
		var refusal interface{ Permanent() bool }
		if errors.Is(err, workgraph.ErrPermission) || errors.As(err, &refusal) && refusal.Permanent() {
			task.Status = "paused"
		}
		if saveErr := e.Store.PutTask(task); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		return err
	}
	return nil // Task completion / bounded continuation is atomic with Apply.
}

// Retry resumes the correct durable phase, including publication-only capture
// jobs. It never invokes inference for a result already waiting in the outbox.
func (e *Engine) Retry(project, id string) (graphstore.Task, error) {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	tasks, err := e.Store.Tasks(project)
	if err != nil {
		return graphstore.Task{}, err
	}
	pending, err := e.Store.Pending(project)
	if err != nil {
		return graphstore.Task{}, err
	}
	for _, task := range tasks {
		if task.ID != id || task.Status != "paused" {
			continue
		}
		task.Status = "queued"
		for _, commit := range pending {
			if commit.RunID == task.RunID {
				task.Status = "publish_pending"
				break
			}
		}
		if task.Kind == "capture" && task.Status != "publish_pending" {
			return graphstore.Task{}, errors.New("capture has no durable proposal; recapture the fixed observation")
		}
		if task.SharedID != "" {
			if c := e.coordinator(context.Background()); c != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				shared, err := c.ProcessingTask(ctx, project, task.SharedID)
				if errors.Is(err, contracts.ErrProcessingStale) {
					task.SharedID = ""
					err = nil
				}
				if err == nil && (shared.Attempts > 0 && shared.Status != "completed" && shared.Status != "superseded") {
					_, err = c.ActProcessing(ctx, project, task.SharedID, "retry", "", "")
				}
				cancel()
				if err != nil {
					return graphstore.Task{}, err
				}
			}
		}
		task.Attempts, task.PublishAttempts = 0, 0
		task.NextAt, task.Error = "", ""
		return task, e.Store.PutTask(task)
	}
	return graphstore.Task{}, errors.New("paused task not found")
}

// Flush retries durable publication without re-running a model. The daemon
// calls this between tasks; Recover is reserved for exclusive startup recovery.
func (e *Engine) Flush(ctx context.Context, project string) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	pending, err := e.Store.Pending(project)
	if err != nil {
		return err
	}
	tasks, err := e.Store.Tasks(project)
	if err != nil {
		return err
	}
	byRun := map[string]graphstore.Task{}
	for _, task := range tasks {
		byRun[task.RunID] = task
	}
	var failures []error
	for _, commit := range pending {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		task, ok := byRun[commit.RunID]
		if !ok {
			return errors.New("pending commit has no task")
		}
		if task.Status == "paused" {
			continue
		}
		if task.NextAt != "" {
			next, err := time.Parse(time.RFC3339Nano, task.NextAt)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if e.now().Before(next) {
				continue
			}
		}
		if err := e.publish(ctx, commit, task); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Recover is called exclusively by the daemon owner after restart. Unknown
// model outcomes are archived as interrupted; publication retries keep IDs.
func (e *Engine) Recover(ctx context.Context, project string) error {
	return e.recover(ctx, project, true)
}

// RecoverLocal restores interrupted work without waiting for a network. The
// daemon must be available offline; its publication loop handles the outbox.
func (e *Engine) RecoverLocal(ctx context.Context, project string) error {
	return e.recover(ctx, project, false)
}

func (e *Engine) recover(ctx context.Context, project string, publish bool) error {
	runs, err := e.Store.Runs(project)
	if err != nil {
		return err
	}
	tasks, err := e.Store.Tasks(project)
	if err != nil {
		return err
	}
	byID := map[string]graphstore.Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	pending, err := e.Store.Pending(project)
	if err != nil {
		return err
	}
	pendingRuns := map[string]bool{}
	history, err := e.Store.History(project)
	if err != nil {
		return err
	}
	accepted := map[string]workgraph.Commit{}
	for _, entry := range history {
		accepted[entry.Commit.RunID] = entry.Commit
	}
	for _, commit := range pending {
		pendingRuns[commit.RunID] = true
		var task graphstore.Task
		for _, r := range runs {
			if r.ID == commit.RunID {
				task = byID[r.TaskID]
				break
			}
		}
		if task.ID == "" {
			return errors.New("pending publication is missing its durable task")
		}
		if publish {
			if err := e.publish(ctx, commit, task); err != nil {
				return err
			}
		}
	}
	for _, run := range runs {
		if pendingRuns[run.ID] {
			continue
		}
		if run.Status == "published" {
			if task, ok := byID[run.TaskID]; ok && task.RunID == run.ID {
				if err := e.Store.DeleteTask(project, task.ID); err != nil {
					return err
				}
			}
			continue
		}
		if run.Status != "running" && run.Status != "responded" && run.Status != "ready" {
			continue
		}
		if commit, ok := accepted[run.ID]; ok && run.Status == "ready" {
			if _, err := e.Store.Apply(commit, true); err != nil {
				return err
			}
			continue
		}
		status := "interrupted"
		if run.Status == "ready" {
			status = "superseded"
		}
		if err := e.Store.Finish(run.ID, status, "runtime interrupted before durable publication", e.stamp()); err != nil {
			return err
		}
		if task, ok := byID[run.TaskID]; ok {
			task.Status = "retry"
			task.Error = "interrupted prior attempt"
			task.NextAt = ""
			if err := e.Store.PutTask(task); err != nil {
				return err
			}
		}
	}
	return e.Reconcile(project)
}

func validateEvidence(refs []string, anchors map[string]bool, requireNew bool) error {
	if len(refs) > 32 {
		return errors.New("too many evidence anchors")
	}
	newEvidence := false
	for _, ref := range refs {
		new, ok := anchors[ref]
		if !ok {
			return errors.New("unknown evidence anchor")
		}
		newEvidence = newEvidence || new
	}
	if requireNew && !newEvidence {
		return errors.New("decision lacks new work evidence")
	}
	return nil
}

func parseStage(raw string, input prepared) (*workgraph.StageDecision, bool, error) {
	if !input.Complete {
		return nil, true, nil
	}
	var out struct {
		Action string          `json:"action"`
		Stage  *workgraph.Text `json:"stage"`
		Items  struct {
			Add      []string         `json:"add"`
			Revise   []workgraph.Item `json:"revise"`
			Complete []string         `json:"complete"`
			Withdraw []string         `json:"withdraw"`
		} `json:"items"`
		Reason   string   `json:"reason"`
		Evidence []string `json:"evidence"`
	}
	if err := decodeOutput(raw, stageSchema, &out); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(out.Reason) == "" || len(out.Reason) > 4000 {
		return nil, false, errors.New("decision reason must contain 1–4000 bytes")
	}
	out.Evidence = uniqueEvidence(out.Evidence)
	if err := validateEvidence(out.Evidence, input.Anchors, out.Action == "start" || out.Action == "continue"); err != nil {
		return nil, false, err
	}
	if out.Action == "needs_context" {
		if out.Stage != nil || len(out.Items.Add)+len(out.Items.Revise)+len(out.Items.Complete)+len(out.Items.Withdraw) > 0 {
			return nil, false, errors.New("needs_context cannot mutate work")
		}
		return nil, true, nil
	}
	d := &workgraph.StageDecision{Checkpoint: true, Source: input.Source, SessionRevision: input.SessionRevision, Action: out.Action, Text: out.Stage, Reason: out.Reason, Evidence: out.Evidence}
	if out.Action == "start" {
		d.NewNodeID = newID("node_")
	}
	for _, text := range out.Items.Add {
		d.Items.Add = append(d.Items.Add, workgraph.Item{ID: newID("item_"), Text: text})
	}
	for _, item := range out.Items.Revise {
		item.ID = input.Audit.Items[item.ID]
		if item.ID == "" {
			return nil, false, errors.New("unknown item reference")
		}
		d.Items.Revise = append(d.Items.Revise, item)
	}
	for _, ref := range out.Items.Complete {
		id := input.Audit.Items[ref]
		if id == "" {
			return nil, false, errors.New("unknown item reference")
		}
		d.Items.Complete = append(d.Items.Complete, id)
	}
	for _, ref := range out.Items.Withdraw {
		id := input.Audit.Items[ref]
		if id == "" {
			return nil, false, errors.New("unknown item reference")
		}
		d.Items.Withdraw = append(d.Items.Withdraw, id)
	}
	return d, false, nil
}

func parseRelations(raw string, input prepared, id string) (*workgraph.RelationDecision, bool, error) {
	var out struct {
		Status  string `json:"status"`
		Parents []struct {
			ID       string   `json:"id"`
			Reason   string   `json:"reason"`
			Evidence []string `json:"evidence"`
		} `json:"parents"`
		Reason string `json:"reason"`
	}
	if err := decodeOutput(raw, relationSchema, &out); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(out.Reason) == "" || len(out.Reason) > 4000 {
		return nil, false, errors.New("missing or oversized relation explanation")
	}
	if out.Status == "needs_context" && len(out.Parents) > 0 {
		return nil, false, errors.New("incomplete relations cannot propose edges")
	}
	d := &workgraph.RelationDecision{NodeID: id, NodeRevision: input.NodeRevision, Through: input.Audit.Through, Complete: true, Reason: out.Reason}
	if out.Status == "needs_context" || !input.Complete {
		d.Complete = false
		if !input.Complete {
			d.Reason = "Evidence or candidate coverage is incomplete."
		}
		return d, true, nil
	}
	for _, selected := range out.Parents {
		selected.Evidence = uniqueEvidence(selected.Evidence)
		parent, ok := input.Audit.Candidates[selected.ID]
		if !ok {
			return nil, false, errors.New("unknown parent reference")
		}
		if len(selected.Evidence) == 0 {
			return nil, false, errors.New("dependency lacks use evidence")
		}
		if err := validateEvidence(selected.Evidence, input.Anchors, true); err != nil {
			return nil, false, err
		}
		d.Parents = append(d.Parents, workgraph.Parent{NodeID: parent.ID, Revision: parent.Revision, Source: parent.Source, Reason: selected.Reason, Evidence: selected.Evidence})
	}
	return d, false, nil
}

func uniqueEvidence(refs []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		if !seen[ref] {
			result = append(result, ref)
			seen[ref] = true
		}
	}
	return result
}

func obsoleteTask(state workgraph.State, task graphstore.Task) bool {
	if task.Kind == "stage" && task.Source != nil {
		snapshot, exists := state.Snapshots[task.Source.Key()]
		return !exists || snapshot.Superseded || state.Sessions[task.Source.SessionID].ProcessedThrough >= task.Source.Version
	}
	if task.Kind == "relations" {
		node, exists := state.Nodes[task.NodeID]
		return !exists || node.State == "withdrawn" || node.Revision != task.NodeRevision || node.Relations == "ready"
	}
	return false
}
func (e *Engine) finishObsolete(task graphstore.Task) error {
	if task.RunID != "" {
		if err := e.Store.Finish(task.RunID, "superseded", "source branch or work boundary changed", e.stamp()); err != nil {
			return err
		}
	}
	return e.Store.DeleteTask(task.ProjectID, task.ID)
}
