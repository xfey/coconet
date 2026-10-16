package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
)

type processingCoordinator interface {
	SharedProcessing() bool
	Sync(context.Context, string) error
	RegisterProcessing(context.Context, string, contracts.ProcessingSpec) (contracts.ProcessingTask, error)
	ProcessingTasks(context.Context, string, int64) (contracts.ProcessingPage, error)
	ProcessingTask(context.Context, string, string) (contracts.ProcessingTask, error)
	ActProcessing(context.Context, string, string, string, string, string) (contracts.ProcessingTask, error)
}
type privateProcessingKey struct{}

// Explicit history imports retain their device-local approved call budget.
// They cannot silently delegate extra calls to another account.
func PrivateProcessing(ctx context.Context) context.Context {
	return context.WithValue(ctx, privateProcessingKey{}, true)
}

func (e *Engine) coordinator(ctx context.Context) processingCoordinator {
	if ctx.Value(privateProcessingKey{}) == true {
		return nil
	}
	c, ok := e.Publisher.(processingCoordinator)
	if !ok || !c.SharedProcessing() {
		return nil
	}
	return c
}

// Before doing its own work, a device helps one earlier failed/expired task.
// No project-wide executor is selected. Live claims and paused tasks are skipped.
func (e *Engine) helpBefore(ctx context.Context, own graphstore.Task) (bool, error) {
	c := e.coordinator(ctx)
	if c == nil {
		return false, nil
	}
	if e.TaskContext != nil {
		next, allowed, err := e.TaskContext(ctx, own)
		if err != nil || !allowed {
			return false, err
		}
		if e.coordinator(next) == nil {
			return false, nil
		}
	}
	state, err := e.Store.State(own.ProjectID)
	if err != nil {
		return false, err
	}
	executor := ""
	if own.Source != nil {
		executor = own.Source.SessionID
	} else {
		executor = state.Nodes[own.NodeID].Source.SessionID
	}
	if own.ExecutorSession != "" {
		executor = own.ExecutorSession
	}
	// Pagination is bounded; each page is metadata only.
	localTasks, err := e.Store.Tasks(own.ProjectID)
	if err != nil {
		return false, err
	}
	local := map[string]graphstore.Task{}
	for _, job := range localTasks {
		local[job.ID] = job
	}
	after := int64(0)
	for pageNo := 0; pageNo < 41; pageNo++ {
		page, err := c.ProcessingTasks(ctx, own.ProjectID, after)
		if err != nil {
			return false, err
		}
		for _, shared := range page.Tasks {
			if job, exists := local[shared.Spec.Key]; exists {
				next, _ := time.Parse(time.RFC3339Nano, job.NextAt)
				if job.Status == "paused" || job.Status == "publish_pending" || next.After(e.now()) {
					continue
				}
			}
			if shared.Spec.Owner == e.Actor || shared.Status == "paused" || shared.Status == "completed" || shared.Status == "superseded" {
				continue
			}
			until, _ := time.Parse(time.RFC3339Nano, shared.ClaimedUntil)
			if shared.Status == "running" && until.After(e.now()) {
				continue
			}
			created, _ := time.Parse(time.RFC3339Nano, shared.CreatedAt)
			if shared.Attempts == 0 && e.now().Sub(created) < time.Minute {
				continue
			}
			if shared.Claimant == e.Actor {
				retry, _ := time.Parse(time.RFC3339Nano, shared.RetryAfter)
				if retry.After(e.now()) {
					continue
				}
			}
			byThisDevice := 0
			for _, actor := range shared.AttemptedBy {
				if actor == e.Actor {
					byThisDevice++
				}
			}
			if byThisDevice >= 2 {
				continue
			}
			if shared.Attempts >= 3 {
				continue
			}
			// Do not claim a source that this replica cannot yet validate.
			session, exists := state.Sessions[shared.Spec.SessionID]
			if !exists || session.Owner != shared.Spec.Owner {
				continue
			}
			if shared.Spec.Kind == "stage" && session.ProcessedThrough >= shared.Spec.Version {
				continue
			}
			if shared.Spec.Kind == "relations" {
				n, ok := state.Nodes[shared.Spec.NodeID]
				if !ok || n.Relations == "ready" || n.Revision != shared.Spec.NodeRevision {
					continue
				}
			}
			source := workgraph.Source{SessionID: shared.Spec.SessionID, Version: shared.Spec.Version}
			job := graphstore.Task{ID: shared.Spec.Key, ProjectID: own.ProjectID, Kind: shared.Spec.Kind, Source: &source, NodeID: shared.Spec.NodeID, NodeRevision: shared.Spec.NodeRevision, Status: "queued", SharedID: shared.ID, ExecutorSession: executor}
			if err := e.Store.EnsureTask(job); err != nil {
				return false, err
			}
			return true, e.Process(ctx, job)
		}
		if page.Next == 0 {
			return false, nil
		}
		after = page.Next
	}
	return false, nil
}

func (e *Engine) processShared(ctx context.Context, job graphstore.Task, c processingCoordinator) error {
	if err := c.Sync(ctx, job.ProjectID); err != nil {
		return err
	}
	currentState, readErr := e.Store.State(job.ProjectID)
	if readErr != nil {
		return readErr
	}
	if obsoleteTask(currentState, job) {
		return e.finishObsolete(job)
	}
	var shared contracts.ProcessingTask
	var err error
	if job.SharedID != "" {
		shared, err = c.ProcessingTask(ctx, job.ProjectID, job.SharedID)
		if errors.Is(err, contracts.ErrProcessingStale) {
			job.SharedID = ""
			job.Status = "queued"
			return e.Store.PutTask(job)
		}
		if err != nil {
			return err
		}
		if shared.Status == "completed" || shared.Status == "superseded" {
			job.SharedID = ""
			job.Status = "queued"
			return e.Store.PutTask(job)
		}
	} else {
		// Follow-on tasks may need more dialogue than the previous capsule.
		// Fetch only normalized content, never resume the original Agent.
		current, readErr := e.Store.State(job.ProjectID)
		if readErr != nil {
			return readErr
		}
		source := current.Nodes[job.NodeID].Source
		if job.Source != nil {
			source = *job.Source
		}
		if current.Sessions[source.SessionID].Owner != e.Actor {
			if remote, ok := e.Publisher.(interface {
				Pull(context.Context, string, workgraph.Source, bool) error
			}); ok {
				if err := remote.Pull(ctx, job.ProjectID, source, false); err != nil {
					job.Status = "retry"
					job.NextAt = e.now().Add(time.Minute).Format(time.RFC3339Nano)
					job.Error = "shared dialogue is not yet available"
					if saveErr := e.Store.PutTask(job); saveErr != nil {
						return saveErr
					}
					return err
				}
			}
		}
		e.commitMu.Lock()
		state, readErr := e.Store.State(job.ProjectID)
		var input prepared
		if readErr == nil {
			if job.Kind == "stage" && job.Source != nil {
				if state.Sessions[job.Source.SessionID].ProcessedThrough >= job.Source.Version {
					e.commitMu.Unlock()
					return e.Store.DeleteTask(job.ProjectID, job.ID)
				}
				input, readErr = prepareStage(e.Store, state, *job.Source, job.Expanded)
			} else if job.Kind == "relations" {
				input, readErr = prepareRelations(e.Store, state, job.NodeID, job.Expanded)
			} else {
				readErr = errors.New("invalid shared task kind")
			}
		}
		var spec contracts.ProcessingSpec
		if readErr == nil {
			spec, readErr = fixedProcessingInput(state, job, input)
		}
		e.commitMu.Unlock()
		if readErr != nil {
			return readErr
		}
		shared, err = c.RegisterProcessing(ctx, job.ProjectID, spec)
		if err != nil {
			return err
		}
		job.SharedID = shared.ID
		if err := e.Store.PutTask(job); err != nil {
			return err
		}
	}
	value, err := readProcessingInput(shared)
	if err != nil {
		job.Status = "paused"
		job.Error = clip(err.Error(), 4000)
		if saveErr := e.Store.PutTask(job); saveErr != nil {
			return saveErr
		}
		return err
	}
	// Claim only after validating the bytes; a missing capsule never spends a
	// model attempt. The server lease clock is authoritative for publication.
	shared, err = c.ActProcessing(ctx, job.ProjectID, shared.ID, "claim", "", "")
	if errors.Is(err, contracts.ErrProcessingBusy) {
		job.NextAt = e.now().Add(5 * time.Second).Format(time.RFC3339Nano)
		job.Status = "retry"
		return e.Store.PutTask(job)
	}
	if errors.Is(err, contracts.ErrProcessingPaused) || errors.Is(err, contracts.ErrProcessingYielded) {
		job.Status = "paused"
		job.Error = err.Error()
		return e.Store.PutTask(job)
	}
	if err != nil {
		return err
	}
	input := value.Prepared
	job.Expanded = value.Expanded
	job.Attempts = shared.Attempts
	state, err := e.Store.State(job.ProjectID)
	if err != nil {
		return err
	}
	if !processingBaselineCurrent(state, shared, value) {
		_, err = c.ActProcessing(ctx, job.ProjectID, shared.ID, "supersede", shared.Lease, "processing baseline changed")
		if err != nil {
			return err
		}
		job.SharedID = ""
		job.Status = "queued"
		return e.Store.PutTask(job)
	}
	callCtx, cancel := context.WithCancel(ctx)
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-callCtx.Done():
				return
			case <-ticker.C:
				attempt, stop := context.WithTimeout(callCtx, 10*time.Second)
				_, err := c.ActProcessing(attempt, job.ProjectID, shared.ID, "renew", shared.Lease, "")
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-renewDone }()
	noText := job.Kind == "stage" && input.Audit.From == input.Audit.Through
	runner := e.Runner
	provider, model := e.Provider, e.Model
	var selectionErr error
	if !noText && e.ResolveRunner != nil {
		session := state.Sessions[input.Source.SessionID]
		if session.Owner != e.Actor {
			session = state.Sessions[job.ExecutorSession]
			if session.Owner != e.Actor {
				selectionErr = errors.New("no local Agent is available for this shared task")
			}
		}
		if selectionErr == nil {
			provider, model = session.Agent, ""
			runner, selectionErr = e.ResolveRunner(job.ProjectID, session)
		}
	}
	if described, ok := runner.(interface{ Configuration() any }); ok {
		input.Execution = described.Configuration()
	}
	archive, _ := json.Marshal(input)
	runID := newID("run_")
	template := "local-stage/5"
	if job.Kind == "relations" {
		template = "local-relations/7"
	}
	if noText {
		provider, model, template = "", "", "no-new-visible-text/1"
	}
	run := graphstore.Run{ID: runID, TaskID: job.ID, ProjectID: job.ProjectID, Kind: job.Kind, Status: "running", StartedAt: e.stamp(), Provider: provider, Model: model, Template: template, Input: archive, Base: state.Revision, Source: &input.Source, NodeID: job.NodeID, SharedTaskID: shared.ID, InputSHA256: shared.Spec.SHA256, Executor: e.Actor, SourceOwner: shared.Spec.Owner}
	for _, candidate := range input.Audit.Candidates {
		run.Protected = append(run.Protected, candidate.Source)
	}
	if err := e.Store.Begin(run); err != nil {
		_, _ = c.ActProcessing(ctx, job.ProjectID, shared.ID, "defer", shared.Lease, "")
		return err
	}
	job.RunID = runID
	job.Status = "running"
	job.ContextBasis = ""
	job.AfterPublish = ""
	job.NextAt = ""
	if err := e.Store.PutTask(job); err != nil {
		return err
	}
	var response inference.Response
	var modelErr error
	if noText {
		response.Text = `{"action":"none","stage":null,"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"No new visible dialogue; retain existing stage and commitments.","evidence":[]}`
		response.Visible = response.Text
	} else if selectionErr != nil {
		modelErr = selectionErr
	} else if runner == nil {
		modelErr = errors.New("no inference runner available")
	} else {
		response, modelErr = runner.Infer(callCtx, input.Request)
	}
	output, _ := json.Marshal(response)
	if err := e.Store.SaveModelResponse(runID, string(output), response.Usage, response.Model); err != nil {
		return err
	}
	// A native withdrawal may have arrived while a remote model was running.
	// Synchronize before interpreting either its success or its failure.
	if err := c.Sync(ctx, job.ProjectID); err != nil {
		return err
	}
	completedState, err := e.Store.State(job.ProjectID)
	if err != nil {
		return err
	}
	if obsoleteTask(completedState, job) {
		return e.finishObsolete(job)
	}
	fail := func(cause error) error {
		operation, status, reason := "fail", "retry", "inference attempt failed"
		var deferred *inference.Deferred
		if errors.As(cause, &deferred) {
			operation, status, reason = "defer", "budget_wait", ""
			job.NextAt = deferred.Until.UTC().Format(time.RFC3339Nano)
		}
		attempt, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		t, releaseErr := c.ActProcessing(attempt, job.ProjectID, shared.ID, operation, shared.Lease, reason)
		if releaseErr == nil {
			job.Attempts = t.Attempts
			if t.Status == "paused" {
				status = "paused"
			}
		}
		if err := e.Store.Finish(runID, "failed", clip(cause.Error(), 4000), e.stamp()); err != nil {
			return err
		}
		job.Status = status
		job.Error = clip(cause.Error(), 4000)
		if job.NextAt == "" {
			job.NextAt = e.now().Add(time.Minute).Format(time.RFC3339Nano)
		}
		if err := e.Store.PutTask(job); err != nil {
			return err
		}
		if deferred != nil {
			return releaseErr
		}
		return errors.Join(cause, releaseErr)
	}
	if modelErr != nil {
		return fail(modelErr)
	}
	auth := shared.Authorization()
	commit := workgraph.Commit{Schema: 1, ID: newID("commit_"), ProjectID: job.ProjectID, Actor: e.Actor, Base: state.Revision, RunID: runID, At: e.stamp(), Processing: &auth}
	needsContext := false
	if job.Kind == "stage" {
		commit.Stage, needsContext, err = parseStage(response.Text, input)
	} else {
		commit.Relations, needsContext, err = parseRelations(response.Text, input, job.NodeID)
	}
	if err != nil {
		return fail(err)
	}
	if needsContext && job.Kind == "stage" {
		_, err = c.ActProcessing(ctx, job.ProjectID, shared.ID, "supersede", shared.Lease, "additional evidence required")
		if err != nil {
			return err
		}
		if err := e.Store.Finish(runID, "needs_context", "additional evidence required", e.stamp()); err != nil {
			return err
		}
		job.Status = "queued"
		if job.Expanded {
			job.Status = "paused"
		}
		job.Expanded = true
		job.SharedID = ""
		job.RunID = ""
		job.Error = "additional evidence required"
		return e.Store.PutTask(job)
	}
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	latest, err := e.Store.State(job.ProjectID)
	if err != nil {
		return err
	}
	if !processingBaselineCurrent(latest, shared, value) {
		_, err = c.ActProcessing(ctx, job.ProjectID, shared.ID, "supersede", shared.Lease, "processing baseline changed")
		if err != nil && !errors.Is(err, contracts.ErrProcessingStale) {
			return err
		}
		if err := e.Store.Finish(runID, "superseded", "processing baseline changed", e.stamp()); err != nil {
			return err
		}
		if obsoleteTask(latest, job) {
			return e.Store.DeleteTask(job.ProjectID, job.ID)
		}
		job.SharedID = ""
		job.RunID = ""
		job.Status = "queued"
		return e.Store.PutTask(job)
	}
	commit.Base = latest.Revision
	if needsContext {
		job.AfterPublish = "queued"
		if job.Expanded {
			job.AfterPublish = "paused"
		}
		job.Expanded = true
		job.ContextBasis = relationContextBasis(latest, job.NodeID)
	}
	if _, _, err := workgraph.Apply(latest, commit); err != nil {
		return fail(fmt.Errorf("invalid shared result: %w", err))
	}
	if err := e.Store.Finish(runID, "ready", "", e.stamp()); err != nil {
		return err
	}
	if err := e.Store.SavePending(commit); err != nil {
		return err
	}
	job.Status = "publish_pending"
	if err := e.Store.PutTask(job); err != nil {
		return err
	}
	if err := e.publish(ctx, commit, job); err != nil {
		return err
	}
	return e.queueAssistedFollowups(job, input.Source.SessionID)
}

// Finish the already-synced backlog of an assisted Session. This is a temporary
// local continuation, not permanent ownership of future captures.
func (e *Engine) queueAssistedFollowups(job graphstore.Task, sessionID string) error {
	state, err := e.Store.State(job.ProjectID)
	if err != nil {
		return err
	}
	session := state.Sessions[sessionID]
	if session.Owner == e.Actor {
		return nil
	}
	if session.ProcessedThrough < session.Latest {
		source := workgraph.Source{SessionID: sessionID, Version: session.ProcessedThrough + 1}
		if err := e.Store.EnsureTask(graphstore.Task{ID: "stage/" + source.Key(), ProjectID: job.ProjectID, Kind: "stage", Source: &source, Status: "queued", ExecutorSession: job.ExecutorSession}); err != nil {
			return err
		}
	}
	for _, node := range state.Nodes {
		if node.Source.SessionID != sessionID || node.Relations != "pending" {
			continue
		}
		if err := e.Store.EnsureTask(graphstore.Task{ID: fmt.Sprintf("relations/%s/%d", node.ID, node.Revision), ProjectID: job.ProjectID, Kind: "relations", NodeID: node.ID, NodeRevision: node.Revision, Status: "queued", ExecutorSession: job.ExecutorSession}); err != nil {
			return err
		}
	}
	return nil
}

// Withdrawal is an owner action; it can release a stale capsule even while a
// helper holds the old lease. Relay validates ownership, not model semantics.
func (e *Engine) supersedeObsoleteShared(ctx context.Context, project string) error {
	c := e.coordinator(ctx)
	if c == nil {
		return nil
	}
	state, err := e.Store.State(project)
	if err != nil {
		return err
	}
	var after int64
	for pages := 0; pages < 41; pages++ {
		page, err := c.ProcessingTasks(ctx, project, after)
		if err != nil {
			return err
		}
		for _, shared := range page.Tasks {
			if shared.Spec.Owner != e.Actor || shared.Status == "completed" || shared.Status == "superseded" {
				continue
			}
			source := workgraph.Source{SessionID: shared.Spec.SessionID, Version: shared.Spec.Version}
			task := graphstore.Task{Kind: shared.Spec.Kind, Source: &source, NodeID: shared.Spec.NodeID, NodeRevision: shared.Spec.NodeRevision}
			if !obsoleteTask(state, task) {
				continue
			}
			if _, err := c.ActProcessing(ctx, project, shared.ID, "supersede", "", "source branch or work boundary changed"); err != nil && !errors.Is(err, contracts.ErrProcessingStale) {
				return err
			}
		}
		if page.Next == 0 {
			return nil
		}
		after = page.Next
	}
	return nil
}
