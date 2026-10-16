package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

type localOperationIssue struct {
	Agent           string `json:"agent,omitempty"`
	NativeSessionID string `json:"native_session_id,omitempty"`
	Workspace       string `json:"workspace,omitempty"`
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	State           string `json:"state"`
	Attempts        int    `json:"attempts"`
	NextAt          string `json:"next_at,omitempty"`
	Error           string `json:"error,omitempty"`
	Summary         string `json:"summary"`
	RunID           string `json:"run_id,omitempty"`
	Action          string `json:"action"`
}

// Add recovery guidance while error identity is still available, before the
// local HTTP/CLI boundary turns it into text. Never infer a cause from prose.
func localDAGFailureMessage(err error) string {
	action := ""
	switch {
	case errors.Is(err, syscall.ENOSPC):
		action = "Free disk space on this computer, then retry. Keep original Agent history and retained Session copies."
	case errors.Is(err, localdag.ErrSelectionChanged), errors.Is(err, workgraph.ErrSource):
		action = "Refresh the project work graph and select an available source. Do not silently use another version."
	case errors.Is(err, workgraph.ErrPermission):
		action = "This change requires the Session owner's account. Check the selected node or Session owner and your current account; ask the owner to make the change or select your own work."
	case errors.Is(err, replication.ErrWaitingForReplica):
		action = "A replacement copy has been requested. Keep a connected device holding this Session running Coconet. Background copying retries while the Runtime is running; retry this read or Fork after the copy arrives."
	case errors.Is(err, context.DeadlineExceeded):
		action = "Check coconet health --json for current state before retrying. Reuse the original request_id and arguments for Fork or Library operations."
	}
	if action == "" {
		return err.Error()
	}
	return err.Error() + "\nNext step: " + action
}

func localReplicaIssue(id string, progress graphstore.ReplicaState) localOperationIssue {
	issue := localOperationIssue{Kind: "replica", ID: id, State: "retry", Attempts: progress.Attempts, NextAt: progress.NextAt, Error: progress.Error,
		Summary: "Copying this Session failed.", Action: "Check the recorded error and coconet storage status on this computer. Copying retries automatically while the Runtime is running, no earlier than next_at."}
	// This field persists the exact controlled sentinel, not arbitrary provider
	// prose. Other failures (disk, network, integrity) must not imply a missing copy.
	if progress.Error == replication.ErrWaitingForReplica.Error() {
		issue.State = "waiting"
		issue.Summary = "The relay copy is unavailable; a replacement has been requested."
		issue.Action = "Keep a connected device holding this Session running Coconet. Copying retries automatically while the Runtime is running, no earlier than next_at."
	}
	return issue
}

func localTaskIssue(task graphstore.Task) localOperationIssue {
	issue := localOperationIssue{Kind: task.Kind, ID: task.ID, State: task.Status, Attempts: task.Attempts, NextAt: task.NextAt, Error: task.Error, RunID: task.RunID,
		Summary: "Processing is queued.", Action: "The task can continue while the Runtime is running and processing is enabled."}
	switch task.Status {
	case "retry":
		issue.Summary = "The last processing attempt failed; a retry is scheduled."
		issue.Action = "Check the recorded error. Processing retries automatically while the Runtime is running and processing is enabled, no earlier than next_at."
	case "running":
		issue.Summary = "A processing attempt is running; any recorded error is from an earlier attempt."
		issue.Action = "Check status again for the result."
	case "publish_pending":
		issue.Summary = "The prepared update has not been published yet."
		issue.Action = "Publication retries while the Runtime is running, no earlier than next_at if set. Resending the prepared update does not call the model again."
	case "paused":
		issue.Summary = "This task is paused. Review the recorded error, resolve its cause, then use the retry command."
		issue.NextAt = "" // A backoff timestamp is not an active retry schedule.
		issue.Action = shellCommand("coconet", []string{"projection", "retry", "--task", task.ID})
	case "budget_wait":
		issue.Summary = "Processing is waiting for an explicitly configured call budget."
		issue.Action = "Inspect coconet inference status and coconet history list; changing or restarting the Runtime does not reset call limits."
	}
	if task.Kind == "capture" || task.Status == "publish_pending" {
		issue.Attempts = task.PublishAttempts
	}
	return issue
}

type localOperationStatus struct {
	Replicas        graphstore.ReplicaCoverage `json:"replicas"`
	SearchIndex     graphstore.IndexCoverage   `json:"search_index"`
	ProjectID       string                     `json:"project_id"`
	Revision        int64                      `json:"accepted_revision"`
	Published       int64                      `json:"last_confirmed_checkpoint_revision"`
	PendingCommits  int                        `json:"pending_commits"`
	Tasks           map[string]int             `json:"tasks"`
	CapturePending  int                        `json:"capture_pending"`
	CapturePaused   int                        `json:"capture_paused"`
	Issues          []localOperationIssue      `json:"issues"`
	IssueCount      int                        `json:"issue_count"`
	Note            string                     `json:"note"`
	RecentTransfers []localTransferSummary     `json:"recent_transfers"`
}

type localTransferSummary struct {
	ID        uint64 `json:"id"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	At        string `json:"at"`
	Revision  int64  `json:"revision,omitempty"`
	Error     string `json:"error,omitempty"`
}

// A bounded, metadata-only view for ordinary CLI health and status. Detailed
// model input/output stays in an explicitly requested diagnostic export.
func localOperations(ctx context.Context, root string, engine *localdag.Engine, project string) (localOperationStatus, error) {
	result := localOperationStatus{ProjectID: project, Tasks: map[string]int{}, Issues: []localOperationIssue{}, Note: "Device-local processing state. Checkpoint revision is this device's last confirmation, not a live query of the Relay. Up to 100 issues shown; inspect diagnostics list or history status for details."}
	state, err := engine.Store.State(project)
	if err != nil {
		return result, err
	}
	result.Revision = state.Revision
	result.Replicas, err = engine.Store.ReplicaCoverage(project, engine.Actor)
	if err != nil {
		return result, err
	}
	result.SearchIndex, err = engine.Store.IndexCoverage(ctx, state)
	if err != nil {
		return result, err
	}
	result.Published, err = engine.Store.PublishedCheckpoint(project)
	if err != nil {
		return result, err
	}
	pending, err := engine.Store.Pending(project)
	if err != nil {
		return result, err
	}
	result.PendingCommits = len(pending)
	transfers, err := engine.Store.Transfers(project, 20)
	if err != nil {
		return result, err
	}
	result.RecentTransfers = []localTransferSummary{}
	for _, transfer := range transfers {
		result.RecentTransfers = append(result.RecentTransfers, localTransferSummary{ID: transfer.ID, Operation: transfer.Operation, Status: transfer.Status, At: transfer.At, Revision: transfer.Revision, Error: transfer.Error})
	}
	add := func(issue localOperationIssue) {
		result.IssueCount++
		if len(result.Issues) < 100 {
			result.Issues = append(result.Issues, issue)
		}
	}
	for source := range workgraph.RetainedSources(state) {
		snapshot, ok := state.Snapshots[source]
		if !ok {
			continue
		}
		progress, err := engine.Store.Replica(project, snapshot)
		if err != nil {
			return result, err
		}
		if progress.Error != "" {
			add(localReplicaIssue(source, progress))
		}
	}
	tasks, err := engine.Store.Tasks(project)
	if err != nil {
		return result, err
	}
	taskSessions := map[string]string{}
	for _, task := range tasks {
		taskSessions[task.ID] = historyTaskSession(task, state)
		result.Tasks[task.Kind+"/"+task.Status]++
		if task.Error == "" && task.Status != "paused" && task.Status != "budget_wait" {
			continue
		}
		add(localTaskIssue(task))
	}
	events, err := localDAGInbox(root)
	if err != nil {
		return result, err
	}
	for _, event := range events {
		if event.Request.ProjectID != project {
			continue
		}
		result.CapturePending++
		if event.Paused {
			result.CapturePaused++
		}
		if event.Error == "" {
			continue
		}
		status, action := "retry", "Capture retries automatically while the Runtime is running; native history is unchanged."
		summary, nextAt := "The last capture attempt failed.", event.NextAt
		if event.Error == localdag.ErrCapturePending.Error() {
			status, action = "waiting", "Waiting for the earlier snapshot to synchronize. Capture will continue automatically; the local conversation remains usable."
			summary = "This capture is waiting for an earlier snapshot, not a new processing failure."
		}
		if event.Paused {
			status, action = "paused", shellCommand("coconet", []string{"projection", "retry", "--capture", event.ID})
			summary, nextAt = "Capture is paused. Review the recorded error before retrying.", ""
		}
		if event.Error == adapters.ErrNativeSessionMissing.Error() {
			summary = "This session's original file was not found. Other sessions can continue synchronizing."
			if event.Paused {
				action = "Restore or locate this session in its original Agent profile before retrying: " + action
			}
		} else if event.Error == adapters.ErrNativeSessionUnreadable.Error() {
			summary = "This session's storage cannot be read. Check the Agent profile and file permissions."
		} else if event.Error == adapters.ErrNativeSessionAmbiguous.Error() {
			summary = "More than one original file uses this session ID. Resolve the duplicate before retrying."
		}
		add(localOperationIssue{Agent: string(event.Request.Agent), NativeSessionID: event.Request.NativeID, Workspace: event.Request.Root, Kind: "observation", ID: event.ID, State: status, Attempts: event.Attempts, NextAt: nextAt, Error: event.Error, Summary: summary, Action: action})
	}
	unlock, err := historyLock(ctx, root)
	if err != nil {
		return result, err
	}
	defer unlock()
	plans, err := readHistoryPlans(root)
	if err != nil {
		return result, err
	}
	for _, plan := range plans {
		if plan.ProjectID != project || plan.Status == "preview" || plan.Status == "complete" {
			continue
		}
		action := "status"
		if plan.Status == "failed" {
			action = "retry"
		} else if plan.Status == "paused" || strings.HasSuffix(plan.Status, "_wait") {
			action = "resume"
		}
		command := shellCommand("coconet", []string{"history", action, "--plan", plan.ID})
		if plan.Status == "budget_wait" {
			command += " --max-calls NEW_TOTAL"
		}
		if plan.Status == "configuration_wait" {
			command = "Restore the previewed provider configuration / sign-in, then " + command
		}
		for i := range result.Issues {
			if historyOwnsSession(plan, taskSessions[result.Issues[i].ID]) {
				result.Issues[i].Action = "Managed by history plan: " + command
				if plan.Status != "running" {
					result.Issues[i].Summary = "The history import plan is stopped or waiting. Follow its plan action before processing can continue."
					result.Issues[i].NextAt = ""
				}
			}
		}
		add(localOperationIssue{Kind: "history", ID: plan.ID, State: plan.Status, Attempts: plan.UsedCalls, Summary: "Historical import is managed by its own plan; follow the plan action below.", Action: command, Error: fmt.Sprintf("%d processing attempts; inspect history status for selected sessions and errors", plan.UsedCalls)})
	}
	return result, nil
}
