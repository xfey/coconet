package graphstore

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/workgraph"
)

func archiveFixture(t *testing.T) (*Store, workgraph.Commit) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	entries := []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "t", Role: "user", Kind: "message", Text: "离线恢复后也能查找重试边界"}}}
	built, err := conversation.Build(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range built.Objects {
		if _, err := s.PutObject(data); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(built.Manifest)
	digest, err := s.PutObject(data)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := s.SaveArtifactBytes([]byte("exact native session fixture\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := snapshotCommit()
	c.Snapshot.Conversation = digest
	c.Snapshot.Artifact = artifact
	c.Snapshot.Turns = 1
	if _, err := s.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	return s, c
}
func TestReplicaArchiveOfflineRestoreAndIndexRebuild(t *testing.T) {
	ctx := context.Background()
	s, c := archiveFixture(t)
	path := filepath.Join(t.TempDir(), "project.tar")
	exported, err := s.ExportReplica(ctx, "project", path)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Sources != 1 || exported.Objects < 4 {
		t.Fatal(exported)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("archive is not private")
	}
	if _, err := s.ExportReplica(ctx, "project", path); err == nil {
		t.Fatal("overwrote existing archive")
	}
	target, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	restored, err := target.RestoreReplica(ctx, "project", path)
	if err != nil || !restored.Offline || restored.Sources != 1 {
		t.Fatal(restored, err)
	}
	state, _ := s.State("project")
	replay, err := target.Replay("project")
	if err != nil || workgraph.Digest(state) != workgraph.Digest(replay) {
		t.Fatal("offline replay differs", err)
	}
	file, err := target.ArtifactFile(c.Snapshot.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "exact native session fixture\n" {
		t.Fatal("raw backup differs")
	}
	coverage, err := target.IndexProject(ctx, "project", 64)
	if err != nil || !coverage.Complete {
		t.Fatal(coverage, err)
	}
	hits, err := target.SearchContent(ctx, state, "重试边界", 0, 20, coverage)
	if err != nil || len(hits.Matches) != 1 {
		t.Fatal(hits, err)
	}
	if err := target.BindMode("relay/another-device"); err == nil {
		t.Fatal("offline recovery silently attached to server")
	}
}
func TestReplicaArchiveConnectedRestoreDoesNotReplaceNewerGraph(t *testing.T) {
	ctx := context.Background()
	s, c := archiveFixture(t)
	path := filepath.Join(t.TempDir(), "project.tar")
	if _, err := s.ExportReplica(ctx, "project", path); err != nil {
		t.Fatal(err)
	}
	target, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := target.BindMode("relay/device"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.RestoreReplica(ctx, "project", path); err == nil {
		t.Fatal("connected empty project bypassed graph sync")
	}
	if _, err := target.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	stage := workgraph.Commit{Schema: 1, ID: "stage", ProjectID: "project", Actor: c.Actor, Base: 1, RunID: "stage", At: c.At, Stage: &workgraph.StageDecision{Source: c.Snapshot.Source, Action: "start", NewNodeID: "node", Text: &workgraph.Text{Objective: "恢复检查"}, Reason: "任务明确"}}
	if _, err := target.Apply(stage, false); err != nil {
		t.Fatal(err)
	}
	before, _ := target.State("project")
	result, err := target.RestoreReplica(ctx, "project", path)
	if err != nil || result.Offline || result.Revision != 2 {
		t.Fatal(result, err)
	}
	after, _ := target.State("project")
	if workgraph.Digest(before) != workgraph.Digest(after) {
		t.Fatal("restore rolled graph back")
	}
}
func TestReplicaArchiveRejectsTruncationCorruptionTraversalAndForeignProject(t *testing.T) {
	ctx := context.Background()
	s, _ := archiveFixture(t)
	path := filepath.Join(t.TempDir(), "project.tar")
	if _, err := s.ExportReplica(ctx, "project", path); err != nil {
		t.Fatal(err)
	}
	full, _ := os.ReadFile(path)
	cases := map[string][]byte{"truncated": full[:len(full)/2]}
	for _, kind := range []string{"corrupt", "traversal", "duplicate"} {
		var output bytes.Buffer
		tw := tar.NewWriter(&output)
		tr := tar.NewReader(bytes.NewReader(full))
		changed := false
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(tr)
			if h.Name != "replica.json" && !changed {
				if kind == "corrupt" {
					data[0] ^= 1
				} else if kind == "traversal" {
					h.Name = "../outside"
				}
				changed = true
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(data); err != nil {
				t.Fatal(err)
			}
			if kind == "duplicate" && h.Name != "replica.json" {
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(data); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		cases[kind] = output.Bytes()
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			target, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			bad := filepath.Join(t.TempDir(), "bad.tar")
			os.WriteFile(bad, data, 0600)
			if _, err := target.RestoreReplica(ctx, "project", bad); err == nil {
				t.Fatal("accepted bad archive")
			}
			state, _ := target.State("project")
			if state.Revision != 0 {
				t.Fatal("partial restore published graph")
			}
		})
	}
	target, _ := Open(t.TempDir())
	defer target.Close()
	if _, err := target.RestoreReplica(ctx, "foreign", path); err == nil {
		t.Fatal("foreign archive accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := target.RestoreReplica(cancelled, "project", path); err == nil {
		t.Fatal("cancelled restore accepted")
	}
}

func TestReplicaArchiveCannotReviveRetiredSourcesAndQuotaIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, c := archiveFixture(t)
	path := filepath.Join(t.TempDir(), "project.tar")
	if _, err := s.ExportReplica(ctx, "project", path); err != nil {
		t.Fatal(err)
	}
	first := workgraph.Commit{Schema: 1, ID: "first-stage", ProjectID: "project", Actor: c.Actor, Base: 1, RunID: "first-stage", At: c.At, Stage: &workgraph.StageDecision{Source: c.Snapshot.Source, Action: "start", NewNodeID: "node", Text: &workgraph.Text{Objective: "关键阶段"}, Reason: "开始阶段"}}
	if _, err := s.Apply(first, false); err != nil {
		t.Fatal(err)
	}
	next := c
	next.ID = "capture2"
	next.Base = 2
	next.RunID = "capture2"
	snapshot := *c.Snapshot
	snapshot.Version = 2
	snapshot.Digest = ObjectDigest([]byte("new source"))
	next.Snapshot = &snapshot
	if _, err := s.Apply(next, false); err != nil {
		t.Fatal(err)
	}
	state, _ := s.State("project")
	stage := workgraph.Commit{Schema: 1, ID: "stage", ProjectID: "project", Actor: c.Actor, Base: 3, RunID: "stage", At: c.At, Stage: &workgraph.StageDecision{Source: snapshot.Source, SessionRevision: state.Sessions[snapshot.SessionID].Revision, Action: "continue", Text: &workgraph.Text{Objective: "关键阶段"}, Reason: "独立阶段"}}
	if _, err := s.Apply(stage, false); err != nil {
		t.Fatal(err)
	}
	retire := workgraph.Commit{Schema: 1, ID: "retire", ProjectID: "project", Actor: c.Actor, Base: 4, RunID: "retire", At: c.At, Retire: &workgraph.Retirement{Sources: []workgraph.Source{c.Snapshot.Source}}}
	if _, err := s.Apply(retire, false); err != nil {
		t.Fatal(err)
	}
	result, err := s.RestoreReplica(ctx, "project", path)
	if err != nil || result.Sources != 0 {
		t.Fatal(result, err)
	}
	after, _ := s.State("project")
	if _, ok := after.Snapshots[c.Snapshot.Source.Key()]; ok {
		t.Fatal("retired source revived")
	}
	target, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	target.objectLimit.Store(1)
	if _, err := target.RestoreReplica(ctx, "project", path); err != ErrLocalObjectQuota {
		t.Fatal("quota ignored", err)
	}
	after, _ = target.State("project")
	if after.Revision != 0 {
		t.Fatal("quota failure installed graph")
	}
}
