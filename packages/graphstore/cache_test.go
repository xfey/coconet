package graphstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
)

func TestCacheQuotaBoundsKnownAndStreamingWritesAcrossRestart(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err := s.SetObjectLimit(16 << 20); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("x"), 16<<20)
	digest, err := s.PutObject(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(body); err != nil {
		t.Fatal("duplicate content consumes quota", err)
	}
	if err := s.Receive(bytes.NewReader([]byte("y")), ObjectDigest([]byte("y"))); !errors.Is(err, ErrLocalObjectQuota) {
		t.Fatal("stream exceeded quota", err)
	}
	if _, err := s.objects.Stat(ObjectDigest([]byte("y"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected stream published content", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if usage := s.CacheUsage(); usage.ObjectsBytes != 16<<20 || usage.ObjectsLimit != 16<<20 {
		t.Fatal(usage)
	}
	if file, err := s.OpenObject(digest); err != nil {
		t.Fatal("full cache stopped existing reads", err)
	} else {
		file.Close()
	}
	if result, err := s.PruneCache(context.Background(), time.Now().Add(8*24*time.Hour), "owner", CacheRoots{}); err != nil || result.Removed != 1 {
		t.Fatal(result, err)
	}
	if usage := s.CacheUsage(); usage.ObjectsBytes != 0 {
		t.Fatal(usage)
	}
	if err := s.Receive(bytes.NewReader([]byte("y")), ObjectDigest([]byte("y"))); err != nil {
		t.Fatal(err)
	}
}
func TestCacheCollectionProtectsGraphInboxAndOpenReaders(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prepared, err := conversation.Build([]conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: "retained work"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range prepared.Objects {
		if _, err := s.PutObject(data); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(prepared.Manifest)
	manifest, err := s.PutObject(data)
	if err != nil {
		t.Fatal(err)
	}
	c := snapshotCommit()
	c.Snapshot.Conversation = manifest
	c.Snapshot.Turns = 1
	if _, err := s.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	staged, err := s.SaveArtifactBytes([]byte("pending inbox content"))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := s.PutObject([]byte(`{"prepared":true}`))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.PutObject([]byte("unreferenced old transient"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.OpenObject(orphan)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(8 * 24 * time.Hour)
	extra := CacheRoots{Objects: []string{observation}, Artifacts: []string{staged}}
	if result, err := s.PruneCache(context.Background(), now, "alice", extra); err != nil || !result.Busy {
		t.Fatal("active reader was not protected", result, err)
	}
	file.Close()
	if result, err := s.PruneCache(context.Background(), now, "alice", extra); err != nil || result.Removed != 1 {
		t.Fatal(result, err)
	}
	if _, err := s.Object(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan remains", err)
	}
	if _, err := s.Object(observation); err != nil {
		t.Fatal("inbox pointer removed", err)
	}
	if err := s.ValidateArtifact(staged); err != nil {
		t.Fatal("inbox artifact closure removed", err)
	}
	if _, err := s.ConversationFile(context.Background(), manifest, 1); err != nil {
		t.Fatal("retained graph content removed", err)
	}
}
