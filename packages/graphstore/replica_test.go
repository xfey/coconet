package graphstore

import (
	"errors"
	"testing"

	"github.com/xfey/coconet/packages/workgraph"
)

func TestReplicaCompletionRequiresExactAcceptedSource(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	commit := snapshotCommit()
	if _, err := s.Apply(commit, false); err != nil {
		t.Fatal(err)
	}
	p := ReplicaState{Source: commit.Snapshot.Source, Digest: ObjectDigest([]byte("wrong")), TextReady: true, NativeReady: true}
	if err := s.SaveReplica("project", p); !errors.Is(err, workgraph.ErrSource) {
		t.Fatal("different bytes marked complete", err)
	}
	p.Digest = commit.Snapshot.Digest
	if err := s.SaveReplica("project", p); err != nil {
		t.Fatal(err)
	}
	p.Source.Version++
	if err := s.SaveReplica("project", p); !errors.Is(err, workgraph.ErrSource) {
		t.Fatal("unaccepted version marked complete", err)
	}
	coverage, err := s.ReplicaCoverage("project", "alice")
	if err != nil || coverage.Revision != 1 || coverage.Expected != 1 || coverage.TextReady != 1 || coverage.PendingUploads != 1 {
		t.Fatal(coverage, err)
	}
}
