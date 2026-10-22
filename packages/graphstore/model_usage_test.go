package graphstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func usageRunFixture(t *testing.T, s *Store, id, task, project, status string, at time.Time) {
	t.Helper()
	if err := s.Begin(Run{ID: id, TaskID: task, ProjectID: project, Kind: "stage", Provider: "codex", Model: "fixture-model", Status: "running", StartedAt: at.Format(time.RFC3339Nano), Input: json.RawMessage(`{"private":"prompt body"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModelResponse(id, `{"visible":"private model response"}`, json.RawMessage(`{"total":{"inputTokens":100,"cachedInputTokens":60,"outputTokens":12}}`), "fixture-model"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(id, status, "", at.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func TestUsageSurvivesPruningAndReadOnlyDoesNotMutateStore(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-9 * 24 * time.Hour)
	usageRunFixture(t, s, "failed", "task", "one", "failed", old)
	usageRunFixture(t, s, "retry", "task", "one", "completed", old.Add(time.Minute))
	usageRunFixture(t, s, "other", "other", "two", "completed", old)
	before, err := s.ModelUsage(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PruneDiagnostics(time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, row := range before {
		r, err := s.Run(row.ProjectID, row.ID)
		if err != nil || r.Archive == nil || r.Usage != nil || r.Output != "" || r.TokenUsage == nil {
			t.Fatal(r, err)
		}
	}
	s.Close()
	path := filepath.Join(root, "graph.db")
	first, _ := os.ReadFile(path)
	rows, err := ReadModelUsage(root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	x, _ := json.Marshal(before)
	y, _ := json.Marshal(rows)
	if !bytes.Equal(x, y) || !bytes.Equal(first, second) {
		t.Fatal("retention changed usage or read-only reporting wrote data")
	}
	rows, err = ReadModelUsage(root, old.Add(30*time.Second))
	if err != nil || len(rows) != 1 || !rows[0].Retry {
		t.Fatal(rows, err)
	}
	if _, err := ReadModelUsage(t.TempDir(), time.Time{}); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestLegacyUsageBackfillsBeforeRetentionAndDoesNotInventPrunedCounts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := time.Now().Add(-9 * 24 * time.Hour)
	usageRunFixture(t, s, "legacy", "task", "p", "completed", old)
	usageRunFixture(t, s, "gone", "gone", "p", "completed", old)
	// Simulate files written before normalized usage existed.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("runs"))
		for _, id := range []string{"legacy", "gone"} {
			var r Run
			if err := json.Unmarshal(b.Get([]byte(id)), &r); err != nil {
				return err
			}
			r.TokenUsage = nil
			if id == "gone" {
				r.Archive = &DiagnosticArchive{PrunedAt: old.Format(time.RFC3339Nano)}
				r.Usage = nil
				r.Output = ""
				r.Input = json.RawMessage(`null`)
			}
			data, _ := json.Marshal(r)
			if err := b.Put([]byte(id), data); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("settings")).Delete([]byte("diagnostic-bytes"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneDiagnostics(time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ModelUsage(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		value := r.Usage.Models[0].Tokens.Input
		if r.ID == "legacy" && (value == nil || *value != 100) {
			t.Fatal(r)
		}
		if r.ID == "gone" && value != nil {
			t.Fatal("pruned history became measured", r)
		}
	}
}

func TestUsageExcludesCaptureAndDeferredAndIncludesResultBeforeFinish(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stamp := time.Now().Format(time.RFC3339Nano)
	for _, id := range []string{"capture", "deferred", "running"} {
		r := Run{ID: id, TaskID: id, ProjectID: "p", Kind: "stage", Provider: "codex", Status: "running", StartedAt: stamp, Input: json.RawMessage(`{}`)}
		if id == "capture" {
			r.Kind = "capture"
		}
		if err := s.Begin(r); err != nil {
			t.Fatal(err)
		}
		if id == "deferred" {
			if err := s.Finish(id, "deferred", "", stamp); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := s.ModelUsage(time.Time{})
	if err != nil || len(rows) != 1 || rows[0].ID != "running" || rows[0].Usage.Models[0].Tokens.Input != nil {
		t.Fatal(rows, err)
	}
	if err := s.SaveModelResponse("running", `{}`, json.RawMessage(`{"total":{"inputTokens":4,"outputTokens":2,"cachedInputTokens":0}}`), "model"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ModelUsage(time.Time{})
	if err != nil || len(rows) != 1 || rows[0].Usage.Models[0].Tokens.Input == nil || *rows[0].Usage.Models[0].Tokens.Input != 4 || rows[0].Status != "responded" {
		t.Fatal(rows, err)
	}
	if err := s.SaveModelResponse("running", `{}`, nil, ""); err == nil {
		t.Fatal("duplicate result accepted")
	}
}

func TestUsageRetainedWhenResponseBodyExceedsArchiveLimit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Begin(Run{ID: "large", TaskID: "large", ProjectID: "p", Kind: "stage", Provider: "claude", Status: "running", Input: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	visible := `{"type":"result","result":"` + strings.Repeat("x", 4<<20) + `","modelUsage":{"main":{"inputTokens":10,"cacheReadInputTokens":50,"cacheCreationInputTokens":40,"outputTokens":20,"costUSD":0.01}}}`
	output, err := json.Marshal(map[string]string{"visible": visible})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModelResponse("large", string(output), nil, "main"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Run("p", "large")
	if err != nil || r.Archive == nil || r.Output != "" {
		t.Fatal("oversized response not pruned", err)
	}
	rows, err := s.ModelUsage(time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	u := rows[0].Usage
	if len(u.Models) != 1 || u.Models[0].Tokens.Input == nil || *u.Models[0].Tokens.Input != 100 || u.EstimatedUSD == nil || *u.EstimatedUSD != .01 {
		t.Fatal("pruning lost normalized usage", u)
	}
}
