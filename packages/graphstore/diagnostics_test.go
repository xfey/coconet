package graphstore

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/workgraph"
)

func TestDiagnosticRetentionKeepsAuditAndExplicitlyMarksPrunedContent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	for _, id := range []string{"ordinary", "pinned", "pending", "running"} {
		if err := s.Begin(Run{ID: id, TaskID: id, ProjectID: "p", Kind: "stage", Status: "running", StartedAt: old, Input: json.RawMessage(`{"dialogue":"private input"}`)}); err != nil {
			t.Fatal(err)
		}
		if id == "running" {
			continue
		}
		if err := s.SaveResponse(id, "visible output", nil); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(id, "completed", "", old); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PinRun("foreign", "pinned", true); err == nil {
		t.Fatal("cross-project pin accepted")
	}
	if err := s.PinRun("p", "pinned", true); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(Task{ID: "pending", ProjectID: "p", RunID: "pending", Status: "paused"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneDiagnostics(time.Now()); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs("p")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == "ordinary" {
			if run.Archive == nil || run.Archive.InputSHA256 != ObjectDigest([]byte(`{"dialogue":"private input"}`)) || run.Output != "" || string(run.Input) != "null" || run.Status != "completed" {
				t.Fatal("pruning erased audit or retained content", run)
			}
		} else if run.Archive != nil {
			t.Fatal("protected archive pruned", run.ID)
		}
	}
	if err := s.PinRun("p", "ordinary", true); err == nil {
		t.Fatal("missing content silently pinned")
	}
	if _, err := s.Run("foreign", "pinned"); err == nil {
		t.Fatal("foreign run read")
	}
}
func TestDiagnosticBudgetDegradesDetailsWithoutBlockingDurableWork(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err := s.SetDiagnosticPolicy(DiagnosticPolicy{Days: 7, MaxBytes: 32 << 20}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	begin := func(id string) error {
		return s.Begin(Run{ID: id, TaskID: id, ProjectID: "p", Kind: "stage", Status: "running", StartedAt: stamp, Input: json.RawMessage(`{"prompt":"original"}`), Protected: []workgraph.Source{{SessionID: "source", Version: 1}}})
	}
	// Fill the diagnostic budget with explicitly pinned completed archives.
	for i := range 2 {
		id := fmt.Sprint(i)
		if err := begin(id); err != nil {
			t.Fatal(err)
		}
		if err := s.PinRun("p", id, true); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveResponse(id, strings.Repeat("\x00", 3<<20), nil); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(id, "completed", "", stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := begin("over-budget"); err != nil {
		t.Fatal("diagnostics blocked work", err)
	}
	if err := s.SaveResponse("over-budget", "valid result", json.RawMessage(`{"tokens":9}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("over-budget", "ready", "", stamp); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Run("p", "over-budget")
	if err != nil || run.Status != "ready" || run.Archive == nil || run.Archive.InputSHA256 != ObjectDigest([]byte(`{"prompt":"original"}`)) || run.Archive.OutputSHA256 != ObjectDigest([]byte("valid result")) || len(run.Protected) != 1 || run.Output != "" || run.ArchiveReserved != 0 {
		t.Fatalf("lost minimal recovery state: %+v %v", run, err)
	}
	protected, err := s.ProtectedSources("p")
	if err != nil || !protected[workgraph.Source{SessionID: "source", Version: 1}.Key()] {
		t.Fatal("lost active source root", protected, err)
	}
	if err := begin("after-restart"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("over-budget", "superseded", "", stamp); err != nil {
		t.Fatal(err)
	}
	usage, err := s.DiagnosticUsage()
	if err != nil || usage.Reserved != 0 || usage.Pinned != 2 || usage.Pruned != 2 {
		t.Fatal(usage, err)
	}
}

func TestDiagnosticMetadataPaginationKeepsEqualTimeRecords(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := s.Begin(Run{ID: id, TaskID: id, ProjectID: "p", Kind: "stage", Status: "running", StartedAt: stamp, Input: json.RawMessage(`{"prompt":"secret"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.RunSummaries("p", "", 2)
	if err != nil || len(first.Runs) != 2 || first.Runs[0].ID != "d" || first.Next != "c" {
		t.Fatal(first, err)
	}
	second, err := s.RunSummaries("p", first.Next, 2)
	if err != nil || len(second.Runs) != 2 || second.Runs[0].ID != "b" || second.Next != "" {
		t.Fatal(second, err)
	}
	data, _ := json.Marshal(first)
	if strings.Contains(string(data), "secret") {
		t.Fatal("list contains private provider input")
	}
	if _, err := s.RunSummaries("q", first.Next, 2); err == nil {
		t.Fatal("foreign cursor accepted")
	}
}
