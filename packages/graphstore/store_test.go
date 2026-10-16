package graphstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xfey/coconet/packages/workgraph"
)

func snapshotCommit() workgraph.Commit {
	return workgraph.Commit{Schema: 1, ID: "capture", ProjectID: "project", Actor: "alice", RunID: "run_capture", At: "2026-10-09T00:00:00Z", Snapshot: &workgraph.Snapshot{Source: workgraph.Source{SessionID: "session", Version: 1}, Owner: "alice", Agent: "codex", NativeID: "native", Digest: ObjectDigest([]byte("source")), Conversation: ObjectDigest([]byte("conversation")), Turns: 2}}
}

func TestRunsUseChronologicalOrderInsteadOfRandomIdentifiers(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, run := range []Run{{ID: "z-first", StartedAt: "2026-10-09T00:00:00Z"}, {ID: "a-later", StartedAt: "2026-10-09T00:00:00.1Z"}} {
		run.TaskID, run.ProjectID, run.Kind, run.Status = "task", "project", "stage", "running"
		run.Input = json.RawMessage(`{}`)
		if err := s.Begin(run); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs("project")
	if err != nil || len(runs) != 2 || runs[0].ID != "a-later" {
		t.Fatal("run ordering is not chronological", err)
	}
	if runs[0].Implementation.GoVersion == "" {
		t.Fatal("processing archive omitted implementation provenance")
	}
}

func readyRun(t *testing.T, s *Store, c workgraph.Commit) {
	t.Helper()
	if err := s.Begin(Run{ID: c.RunID, TaskID: "capture", ProjectID: c.ProjectID, Kind: "capture", Status: "running", StartedAt: c.At, Input: json.RawMessage(`{"fixed":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveResponse(c.RunID, `{"captured":true}`, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(c.RunID, "ready", "", c.At); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationRequiresEvidenceAndCommitsAuditTogether(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := snapshotCommit()
	if _, err := s.Apply(c, true); err == nil {
		t.Fatal("published without durable evidence")
	}
	state, _ := s.State(c.ProjectID)
	history, _ := s.History(c.ProjectID)
	if state.Revision != 0 || len(history) != 0 {
		t.Fatal("failed audit check changed graph")
	}
	readyRun(t, s, c)
	change, err := s.Apply(c, true)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = s.State(c.ProjectID)
	history, _ = s.History(c.ProjectID)
	runs, _ := s.Runs(c.ProjectID)
	if state.Revision != 1 || len(history) != 1 || change.After != 1 || runs[0].Status != "published" || runs[0].CommitID != c.ID {
		t.Fatal("graph, commit and processing record did not publish together")
	}
}

func TestReopenAndIdempotentPublication(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	c := snapshotCommit()
	readyRun(t, s, c)
	if err := s.SavePending(c); err != nil {
		t.Fatal(err)
	}
	first, err := s.Apply(c, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second, err := s.Apply(c, true)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("retry did not return original receipt: %v", err)
	}
	history, _ := s.History(c.ProjectID)
	pending, _ := s.Pending(c.ProjectID)
	if len(history) != 1 || len(pending) != 0 {
		t.Fatal("retry duplicated a commit or failed to acknowledge outbox")
	}
	replayed, err := s.Replay(c.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.State(c.ProjectID)
	if !reflect.DeepEqual(replayed, state) {
		t.Fatal("reopened history cannot reproduce state")
	}
	c.At = "2026-10-09T00:01:00Z"
	if _, err := s.Apply(c, false); err == nil {
		t.Fatal("different transaction reused the same ID")
	}
}

func TestInvalidModelOutputSurvivesFailureAndReopen(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"instructions":"short","evidence":["repair the retry"]}`)
	if err := s.Begin(Run{ID: "attempt", TaskID: "stage", ProjectID: "project", Kind: "stage", Status: "running", Input: input}); err != nil {
		t.Fatal(err)
	}
	output := "{broken JSON\n"
	if err := s.SaveResponse("attempt", output, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("attempt", "failed", "invalid_json", "2026-10-09T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveResponse("attempt", "overwrite", nil); err == nil {
		t.Fatal("old output overwritten")
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.Runs("project")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Output != output || string(runs[0].Input) != string(input) || runs[0].Status != "failed" {
		t.Fatal("failed input or raw output was lost")
	}
}

func TestRejectedCommitLeavesReadyAttemptAndOutbox(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := snapshotCommit()
	c.Base = 99
	readyRun(t, s, c)
	if err := s.SavePending(c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(c, true); err == nil {
		t.Fatal("stale commit published")
	}
	runs, _ := s.Runs(c.ProjectID)
	pending, _ := s.Pending(c.ProjectID)
	history, _ := s.History(c.ProjectID)
	if runs[0].Status != "ready" || len(pending) != 1 || len(history) != 0 {
		t.Fatal("conflict erased evidence or pending publication")
	}
}

func TestObjectsAreImmutableAndVerified(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := []byte("a fixed conversation copy")
	id, err := s.PutObject(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PutObject(data)
	if err != nil || second != id {
		t.Fatal("identical content did not reuse its object")
	}
	if _, err := s.Object("../escape"); err == nil {
		t.Fatal("unsafe object identity accepted")
	}
	path := filepath.Join(root, "objects", id[:2], id)
	if err := os.WriteFile(path, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Object(id); err == nil {
		t.Fatal("corruption accepted")
	}
	if _, err := s.PutObject(data); err == nil {
		t.Fatal("corruption silently overwritten")
	}
}

func TestCatchUpBeforePublicationReceiptStillFinishesLocalAttempt(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := snapshotCommit()
	readyRun(t, s, c)
	if _, err := s.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(c, true); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.Runs(c.ProjectID)
	if runs[0].Status != "published" || runs[0].CommitID != c.ID {
		t.Fatal("lost response left a successfully synchronized attempt unfinished")
	}
}
