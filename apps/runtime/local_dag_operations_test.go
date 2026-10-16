package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
)

func TestLocalOperationsAreProjectScopedBoundedAndActionable(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: "writer"}
	if err := store.RecordTransfer(graphstore.Transfer{ProjectID: "project", Operation: "checkpoint_publish", Status: "failed", Error: "synthetic network failure", Receipt: &contracts.RelayReceipt{Commit: contracts.RelayCommit{Payload: []byte("private-payload")}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		task := graphstore.Task{ID: fmt.Sprintf("stage/%03d", i), ProjectID: "project", Kind: "stage", Status: "paused", Error: "synthetic provider error", RunID: "run", Attempts: 3}
		if err := store.PutTask(task); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutTask(graphstore.Task{ID: "other-only", ProjectID: "other", Kind: "stage", Status: "paused", Error: "private other failure"}); err != nil {
		t.Fatal(err)
	}
	if err := enqueueLocalDAGObservation(root, localDAGObservationRequest{ProjectID: "project", NativeID: "native"}); err != nil {
		t.Fatal(err)
	}
	paths, _ := localDAGInboxFiles(root)
	event, _ := readLocalDAGInbox(paths[0])
	event.Paused, event.Attempts, event.Error = true, 8, "native unavailable"
	if err := atomicWriteJSON(paths[0], event, 0600); err != nil {
		t.Fatal(err)
	}
	status, err := localOperations(context.Background(), root, engine, "project")
	if err != nil || status.IssueCount != 106 || len(status.Issues) != 100 || status.CapturePaused != 1 || status.Tasks["stage/paused"] != 105 {
		t.Fatal("bad bounded status", status, err)
	}
	encoded, _ := json.Marshal(status)
	if len(status.RecentTransfers) != 1 || strings.Contains(string(encoded), `"payload"`) || strings.Contains(string(encoded), `"receipt"`) {
		t.Fatal("ordinary status included raw transport receipt")
	}
	for _, issue := range status.Issues {
		if issue.ID == "other-only" || issue.RunID != "run" || issue.Action != shellCommand("coconet", []string{"projection", "retry", "--task", issue.ID}) {
			t.Fatal("wrong project or retry phase", issue)
		}
	}
	for i := 0; i < 105; i++ {
		if err := store.DeleteTask("project", fmt.Sprintf("stage/%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	status, err = localOperations(context.Background(), root, engine, "project")
	if err != nil || len(status.Issues) != 1 || status.Issues[0].Action != shellCommand("coconet", []string{"projection", "retry", "--capture", event.ID}) {
		t.Fatal("capture retry not exposed", status, err)
	}
	if err := retryLocalDAGInboxProject(root, event.ID, "other"); err == nil {
		t.Fatal("cross-project capture retry accepted")
	}
	if err := retryLocalDAGInboxProject(root, event.ID, "project"); err != nil {
		t.Fatal(err)
	}
	event, err = readLocalDAGInbox(filepath.Join(root, "inbox", event.ID+".json"))
	if err != nil || event.Paused {
		t.Fatal("capture retry lost queue", err)
	}
}

func TestLocalIssueGuidanceDistinguishesWaitingFailureAndPausedRetries(t *testing.T) {
	progress := graphstore.ReplicaState{Error: replication.ErrWaitingForReplica.Error(), Attempts: 2, NextAt: "2026-10-12T00:00:00Z"}
	waiting := localReplicaIssue("session/1", progress)
	if waiting.State != "waiting" || !strings.Contains(waiting.Action, "connected device") {
		t.Fatal("missing copy lost recovery guidance", waiting)
	}
	for _, cause := range []string{"no space left on device", "content hash mismatch", "HTTP 401", "network unavailable"} {
		progress.Error = cause
		failed := localReplicaIssue("session/1", progress)
		if failed.State != "retry" || strings.Contains(failed.Action, "connected device") || failed.Error != cause {
			t.Fatal("copy failure mislabeled as missing replica", failed)
		}
	}
	task := graphstore.Task{ID: "stage/id", Kind: "stage", Status: "retry", Error: "provider failed", NextAt: progress.NextAt, Attempts: 2}
	retry := localTaskIssue(task)
	if retry.NextAt != progress.NextAt || !strings.Contains(retry.Action, "automatically") {
		t.Fatal("retry lost schedule", retry)
	}
	task.Status = "paused"
	paused := localTaskIssue(task)
	if paused.NextAt != "" || !strings.Contains(paused.Action, "--task") || strings.Contains(paused.Action, "automatically") {
		t.Fatal("paused task falsely promised automatic retry", paused)
	}
	task.Status, task.PublishAttempts = "publish_pending", 4
	publishing := localTaskIssue(task)
	if publishing.Attempts != 4 || !strings.Contains(publishing.Action, "does not call the model again") {
		t.Fatal("publication confused with model retry", publishing)
	}
	task.Status = "running"
	if !strings.Contains(localTaskIssue(task).Summary, "earlier attempt") {
		t.Fatal("historical failure presented as current running failure")
	}
}

func TestLocalFailureGuidanceUsesIdentityInsteadOfGuessingFromText(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("download: %w", replication.ErrWaitingForReplica), "replacement copy has been requested"},
		{fmt.Errorf("write: %w", syscall.ENOSPC), "Free disk space"},
		{localdag.ErrSelectionChanged, "Do not silently use another version"},
		{context.DeadlineExceeded, "Reuse the original request_id"},
	} {
		message := localDAGFailureMessage(test.err)
		if !strings.Contains(message, test.err.Error()) || !strings.Contains(message, test.want) {
			t.Fatal("missing original cause or recovery action", message)
		}
	}
	for _, err := range []error{errors.New("unknown provider failure"), errors.New(replication.ErrWaitingForReplica.Error())} {
		if localDAGFailureMessage(err) != err.Error() {
			t.Fatal("guessed error identity from prose")
		}
	}
}
