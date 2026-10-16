package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func TestAppendCheckpointRollsBackCommitWhenCheckpointCannotFit(t *testing.T) {
	s := fixture(t)
	payload := []byte("opaque checkpoint")
	commit := proposal("atomic", 0)
	commit.StateSHA256 = contentstore.ObjectDigest(payload)
	cp := contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: commit.DeviceID, Revision: 1, CommitID: commit.ID, Codec: "coconet-state-json-v3", SHA256: commit.StateSHA256, Payload: payload}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("usage")).Put([]byte("p"), encodedNumber(MaxProjectBytes))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCheckpoint("p", "a", commit, cp); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	head, _ := s.CheckpointHead("p")
	if head.Head != 0 || head.Checkpoint != nil || head.Generation != 0 {
		t.Fatal("partial commit", head)
	}
	if _, err := s.Receipt("p", commit.ID); !errors.Is(err, contracts.ErrRelayReceiptMissing) {
		t.Fatal("failed edit retained receipt", err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("usage")).Delete([]byte("p")) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCheckpoint("p", "a", commit, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCheckpoint("p", "a", commit, cp); err != nil {
		t.Fatal(err)
	}
	head, _ = s.CheckpointHead("p")
	if head.Head != 1 || head.Checkpoint.Revision != 1 || head.Generation != 1 {
		t.Fatal("retry changed publication", head)
	}
}

func TestCheckpointOpaqueReplacementQuotaAndReopen(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately not graph JSON. Storage must not interpret it.
	payload := []byte("client-owned checkpoint")
	digest := contentstore.ObjectDigest(payload)
	commit := proposal("one", 0)
	commit.StateSHA256 = digest
	if _, err := s.Append("p", "a", commit); err != nil {
		t.Fatal(err)
	}
	cp := contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "d", Revision: 1, CommitID: "one", Codec: "coconet-state-json-v1", SHA256: digest, Payload: payload}
	r, err := s.PutCheckpoint("p", "a", cp)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(r)
	if err := s.db.View(func(tx *bolt.Tx) error {
		if number(tx.Bucket([]byte("usage")).Get([]byte("p"))) != int64(len(data)) {
			t.Fatal("checkpoint not charged")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if again, err := s.PutCheckpoint("p", "peer", cp); err != nil || again.AcceptedAt != r.AcceptedAt {
		t.Fatal("retry not idempotent")
	}
	if _, err := s.PutCheckpoint("q", "a", cp); err == nil {
		t.Fatal("cross-project anchor accepted")
	}
	bad := cp
	bad.Payload = []byte("changed")
	bad.SHA256 = contentstore.ObjectDigest(bad.Payload)
	if _, err := s.PutCheckpoint("p", "a", bad); err == nil {
		t.Fatal("anchor digest mismatch accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.Checkpoint("p")
	if err != nil || page.Checkpoint == nil || !bytes.Equal(page.Checkpoint.Checkpoint.Payload, payload) {
		t.Fatal("checkpoint not durable")
	}
	commit = proposal("two", 1)
	commit.StateSHA256 = digest
	if _, err := s.Append("p", "a", commit); err != nil {
		t.Fatal(err)
	}
	cp.Revision = 2
	cp.CommitID = "two"
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("usage")).Put([]byte("p"), encodedNumber(MaxProjectBytes-512))
	}); err != nil {
		t.Fatal(err)
	}
	// Replacement succeeds near full quota: old size is released. Allow 512 bytes
	// for timestamp and anchor-envelope size variation.
	if _, err := s.PutCheckpoint("p", "a", cp); err != nil {
		t.Fatal(err)
	}
	commit = proposal("three", 2)
	bad = cp
	bad.Revision = 3
	bad.CommitID = "three"
	bad.Payload = bytes.Repeat([]byte("x"), 1024)
	bad.SHA256 = contentstore.ObjectDigest(bad.Payload)
	commit.StateSHA256 = bad.SHA256
	if _, err := s.Append("p", "a", commit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCheckpoint("p", "a", bad); !errors.Is(err, ErrQuota) {
		t.Fatal("checkpoint bypassed quota", err)
	}
	page, _ = s.Checkpoint("p")
	if page.Checkpoint.Checkpoint.Revision != 2 {
		t.Fatal("failed replacement destroyed prior checkpoint")
	}
}

func TestCheckpointSizeLimitPreservesPublishedStateAndCanRecover(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	put := func(id string, base int64, size int) error {
		payload := bytes.Repeat([]byte("x"), size)
		digest := contentstore.ObjectDigest(payload)
		commit := proposal(id, base)
		commit.StateSHA256 = digest
		if _, err := s.Append("p", "a", commit); err != nil {
			return err
		}
		_, err := s.PutCheckpoint("p", "a", contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "d", Revision: base + 1, CommitID: id, Codec: "coconet-state-json-v1", Payload: payload, SHA256: digest})
		return err
	}
	if err := put("maximum", 0, contracts.MaxRelayCheckpointBytes); err != nil {
		t.Fatal(err)
	}
	before, err := s.Checkpoint("p")
	if err != nil {
		t.Fatal(err)
	}
	if err := put("too_large", 1, contracts.MaxRelayCheckpointBytes+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized checkpoint accepted: %v", err)
	}
	after, err := s.Checkpoint("p")
	if err != nil || after.Head != 2 || after.Generation != before.Generation || after.Checkpoint.Checkpoint.SHA256 != before.Checkpoint.Checkpoint.SHA256 {
		t.Fatalf("failed publication destroyed prior state: %v", err)
	}
	if err := put("recovered", 2, 1024); err != nil {
		t.Fatal(err)
	}
	after, err = s.Checkpoint("p")
	if err != nil || after.Checkpoint.Checkpoint.Revision != 3 {
		t.Fatalf("replacement did not recover: %v", err)
	}
}
