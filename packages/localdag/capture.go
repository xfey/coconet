package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

type Capture struct {
	Authoritative    bool                 `json:"authoritative,omitempty"`
	ProjectID        string               `json:"project_id"`
	Agent            string               `json:"agent"`
	NativeID         string               `json:"native_id"`
	Entries          []conversation.Entry `json:"entries"`
	Artifact         []byte               `json:"artifact,omitempty"`
	ArtifactManifest string               `json:"artifact_manifest,omitempty"`
	Ended            bool                 `json:"ended"`
	Inherited        bool                 `json:"inherited,omitempty"`
}

var ErrObservationSuperseded = errors.New("fixed native observation was superseded by a newer capture")

var ErrCapturePending = errors.New("previous fixed capture is awaiting publication; this observation will resume after synchronization")

// Capture makes immutable copies; it never reads or edits native Agent history.
// Callers provide a complete fixed observation, not a live file path.
func (e *Engine) Capture(ctx context.Context, c Capture) (workgraph.Source, error) {
	release := e.Store.PinContent()
	defer release()
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	return e.capture(ctx, c)
}

func (e *Engine) capture(ctx context.Context, c Capture) (workgraph.Source, error) {
	return e.captureMode(ctx, c, false)
}

func (e *Engine) captureMode(ctx context.Context, c Capture, deferred bool) (workgraph.Source, error) {
	if strings.TrimSpace(c.ProjectID) == "" || c.NativeID == "" || c.Agent == "" || e.Actor == "" {
		return workgraph.Source{}, errors.New("incomplete capture identity")
	}
	state, err := e.Store.State(c.ProjectID)
	if err != nil {
		return workgraph.Source{}, err
	}
	if c.Inherited {
		for _, entry := range c.Entries {
			if !entry.Inherited {
				return workgraph.Source{}, errors.New("native Fork initialization includes non-inherited work")
			}
		}
	}
	sessionID := ObservationSessionID(c.ProjectID, e.Actor, c.Agent, c.NativeID)
	pending, err := e.Store.Pending(c.ProjectID)
	if err != nil {
		return workgraph.Source{}, err
	}
	for _, proposal := range pending {
		if proposal.Snapshot != nil && proposal.Snapshot.SessionID == sessionID {
			if deferred && c.Inherited && proposal.Snapshot.Inherited {
				// Retry after an interrupted response must reuse the exact queued
				// baseline, never relabel a longer child history as inherited.
				entries, err := readEntries(e.Store, *proposal.Snapshot, 0, proposal.Snapshot.Turns)
				if err != nil {
					return workgraph.Source{}, err
				}
				if workgraph.Digest(entries) != workgraph.Digest(c.Entries) || proposal.Snapshot.Artifact != c.ArtifactManifest || proposal.Snapshot.Ended != c.Ended {
					return workgraph.Source{}, errors.New("queued native Fork initialization differs")
				}
				return proposal.Snapshot.Source, nil
			}
			return workgraph.Source{}, ErrCapturePending
		}
	}
	session := state.Sessions[sessionID]
	var rewrite *workgraph.ConversationRewrite
	if session.Latest > 0 {
		prior := state.Snapshots[(workgraph.Source{SessionID: sessionID, Version: session.Latest}).Key()]
		previous, err := readEntries(e.Store, prior, 0, prior.Turns)
		if err != nil {
			return workgraph.Source{}, err
		}
		common := 0
		for common < len(previous) && common < len(c.Entries) && workgraph.Digest(previous[common]) == workgraph.Digest(c.Entries[common]) {
			common++
		}
		if common < len(previous) {
			if !c.Authoritative {
				return workgraph.Source{}, errors.New("capture rewrote its normalized prefix without authoritative native history")
			}
			rewrite = &workgraph.ConversationRewrite{PreviousVersion: session.Latest, CommonTurns: common, RestoreEditableTails: true}
		}
	}
	conversationDigest, err := SaveConversation(e.Store, c.Entries)
	if err != nil {
		return workgraph.Source{}, err
	}
	artifactDigest := c.ArtifactManifest
	if artifactDigest != "" {
		if len(c.Artifact) > 0 {
			return workgraph.Source{}, errors.New("provide one artifact representation")
		}
		if err := e.Store.ValidateArtifact(artifactDigest); err != nil {
			return workgraph.Source{}, err
		}
	}
	if len(c.Artifact) > 0 {
		artifactDigest, err = e.Store.SaveArtifactBytes(c.Artifact)
		if err != nil {
			return workgraph.Source{}, err
		}
	}
	digest := workgraph.Digest(struct {
		Conversation, Artifact string
		Ended                  bool
	}{conversationDigest, artifactDigest, c.Ended})
	source := workgraph.Source{SessionID: sessionID, Version: session.Latest + 1}
	if session.Latest > 0 {
		previous := workgraph.Source{SessionID: sessionID, Version: session.Latest}
		if state.Snapshots[previous.Key()].Digest == digest {
			return previous, nil
		}
	}
	snapshot := workgraph.Snapshot{Source: source, Owner: e.Actor, Agent: c.Agent, NativeID: c.NativeID, Digest: digest, Conversation: conversationDigest, Artifact: artifactDigest, Turns: len(c.Entries), Ended: c.Ended, ObservedAt: e.stamp()}
	snapshot.Inherited = c.Inherited && source.Version == 1
	snapshot.Rewrite = rewrite
	for _, entry := range c.Entries {
		if !entry.Inherited {
			break
		}
		snapshot.InheritedTurns++
	}
	archive, err := json.Marshal(snapshot)
	if err != nil {
		return workgraph.Source{}, err
	}
	runID := newID("run_")
	taskID := "capture/" + source.Key()
	if err := e.Store.Begin(graphstore.Run{ID: runID, TaskID: taskID, ProjectID: c.ProjectID, Kind: "capture", Status: "running", StartedAt: e.stamp(), Template: "capture/1", Input: archive, Base: state.Revision, Source: &source}); err != nil {
		return workgraph.Source{}, err
	}
	if err := e.Store.SaveResponse(runID, `{"captured":true}`, nil); err != nil {
		return workgraph.Source{}, err
	}
	if err := e.Store.Finish(runID, "ready", "fixed observation copied", e.stamp()); err != nil {
		return workgraph.Source{}, err
	}
	commit := workgraph.Commit{Schema: 1, ID: newID("commit_"), ProjectID: c.ProjectID, Actor: e.Actor, Base: state.Revision, RunID: runID, At: e.stamp(), Snapshot: &snapshot}
	if _, _, err := workgraph.Apply(state, commit); err != nil {
		return workgraph.Source{}, err
	}
	job := graphstore.Task{ID: taskID, ProjectID: c.ProjectID, Kind: "capture", Source: &source, Status: "publish_pending", RunID: runID}
	if err := e.Store.PutTask(job); err != nil {
		return workgraph.Source{}, err
	}
	if err := e.Store.SavePending(commit); err != nil {
		return workgraph.Source{}, err
	}
	if deferred {
		return source, nil
	}
	if err := e.publish(ctx, commit, job); err != nil {
		return workgraph.Source{}, err
	}
	return source, errors.Join(e.reconcile(c.ProjectID), e.supersedeObsoleteShared(ctx, c.ProjectID))
}

// Drain makes bounded progress. Stage and relation failures are independent;
// callers receive every failure while later eligible tasks still run.
func (e *Engine) Drain(ctx context.Context, project string, maxAttempts int) error {
	if maxAttempts < 1 {
		return errors.New("attempt limit must be positive")
	}
	var failures []error
	if err := e.supersedeObsoleteShared(ctx, project); err != nil {
		failures = append(failures, err)
	}
	for count := 0; count < maxAttempts; count++ {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := e.Reconcile(project); err != nil {
			return errors.Join(append(failures, err)...)
		}
		tasks, err := e.Store.Tasks(project)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		var selected *graphstore.Task
		for _, task := range tasks {
			if task.Status != "queued" && task.Status != "retry" && task.Status != "budget_wait" {
				continue
			}
			if e.TaskContext != nil {
				_, allowed, err := e.TaskContext(ctx, task)
				if err != nil {
					return errors.Join(append(failures, err)...)
				}
				if !allowed {
					continue
				}
			}
			if task.NextAt != "" {
				next, err := time.Parse(time.RFC3339Nano, task.NextAt)
				if err != nil {
					return errors.Join(append(failures, err)...)
				}
				if e.now().Before(next) {
					continue
				}
			}
			// Favor stage progress, but bound each drain call to avoid starvation
			// of capture or IPC when a large historical import is present.
			if selected == nil || selected.Kind == "relations" && task.Kind == "stage" {
				copy := task
				selected = &copy
			}
		}
		if selected == nil {
			break
		}
		if helped, err := e.helpBefore(ctx, *selected); helped {
			if err != nil {
				failures = append(failures, err)
			}
			continue
		} else if err != nil {
			failures = append(failures, err)
		}
		if err := e.Process(ctx, *selected); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
