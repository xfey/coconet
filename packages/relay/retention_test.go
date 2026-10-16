package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func collectionFixture(t *testing.T, s *Store, project string) contracts.RelayCollectionPlan {
	t.Helper()
	body := []byte("unreferenced immutable snapshot content")
	digest := contentstore.ObjectDigest(body)
	if err := s.PutObject(project, digest, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("opaque checkpoint after source retirement")
	sha := contentstore.ObjectDigest(payload)
	commit := proposal("initial", 0)
	commit.StateSHA256 = sha
	if _, err := s.Append(project, "account", commit); err != nil {
		t.Fatal(err)
	}
	cp := contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "device", Revision: 1, CommitID: commit.ID, Codec: "coconet-state-json-v1", SHA256: sha, Payload: payload}
	if _, err := s.PutCheckpoint(project, "account", cp); err != nil {
		t.Fatal(err)
	}
	return contracts.RelayCollectionPlan{SchemaVersion: 1, ID: "plan", DeviceID: "device", Revision: 1, CheckpointSHA256: sha, Objects: []string{digest}}
}
func TestCollectionGraceIdempotenceAndPhysicalDeletion(t *testing.T) {
	s := fixture(t)
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	plan := collectionFixture(t, s, "p")
	first, err := s.MarkCollection("p", "account", plan)
	if err != nil || first.Status != "marked" {
		t.Fatal(first, err)
	}
	again, err := s.MarkCollection("p", "account", plan)
	if err != nil || again.NotBefore != first.NotBefore {
		t.Fatal(again, err)
	}
	if _, err := s.Collect("p", "other", "device", plan.ID); err == nil {
		t.Fatal("foreign account collected content")
	}
	if _, err := s.Collect("p", "account", "device", plan.ID); !errors.Is(err, ErrGrace) {
		t.Fatal(err)
	}
	now = now.Add(collectionGrace)
	result, err := s.Collect("p", "account", "device", plan.ID)
	if err != nil || result.Status != "deleted" || result.Deleted != 1 {
		t.Fatal(result, err)
	}
	if _, err := s.Object("p", plan.Objects[0]); !errors.Is(err, ErrObject) {
		t.Fatal("logical object remains", err)
	}
	if f, err := s.objects.OpenObject(plan.Objects[0]); err == nil {
		f.Close()
		t.Fatal("physical bytes remain")
	}
	if again, err := s.Collect("p", "account", "device", plan.ID); err != nil || again.Deleted != 1 {
		t.Fatal(again, err)
	}
}

func TestReadLeaseInvalidatesPlanAndReplanningPreservesProtectionAge(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	plan := collectionFixture(t, s, "p")
	marked, err := s.MarkCollection("p", "account", plan)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.RelayReadLeaseRequest{SchemaVersion: 1, DeviceID: "reader", Revision: 1}
	lease, err := s.ReadLease("p", "peer", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReadLease("p", "wrong", "reader", lease.ID); err == nil {
		t.Fatal("foreign release accepted")
	}
	request.LeaseID = lease.ID
	if _, err := s.ReadLease("p", "peer", request); err != nil {
		t.Fatal(err)
	}
	plan.ID = "while_reading"
	if _, err := s.MarkCollection("p", "account", plan); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	result, err := s.Collect("p", "account", "device", "plan")
	if err != nil || result.Status != "cancelled" {
		t.Fatal(result, err)
	}
	if err := s.ReleaseReadLease("p", "peer", "reader", lease.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(collectionGrace)
	plan.ID = "fresh"
	next, err := s.MarkCollection("p", "account", plan)
	if err != nil {
		t.Fatal(err)
	}
	if next.NotBefore != now.Format(time.RFC3339Nano) || marked.NotBefore != next.NotBefore {
		t.Fatal("unrelated reads restarted the grace period", marked, next)
	}
	if result, err := s.Collect("p", "account", "device", plan.ID); err != nil || result.Status != "deleted" {
		t.Fatal(result, err)
	}
}

func TestRawObjectReadAndUploadProtectCollection(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	plan := collectionFixture(t, s, "p")
	if _, err := s.MarkCollection("p", "account", plan); err != nil {
		t.Fatal(err)
	}
	now = now.Add(collectionGrace)
	file, err := s.Object("p", plan.Objects[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Collect("p", "account", "device", plan.ID); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if result, err := s.Collect("p", "account", "device", plan.ID); err != nil || result.Status != "deleted" {
		t.Fatal(result, err)
	}
	if err := s.PutObject("p", plan.Objects[0], int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	plan.ID = "second"
	if _, err := s.MarkCollection("p", "account", plan); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { done <- s.PutObject("p", plan.Objects[0], int64(len(content)), reader) }()
	deadline := time.Now().Add(time.Second)
	for {
		s.objectMu.Lock()
		active := s.active[plan.Objects[0]]
		s.objectMu.Unlock()
		if active > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("upload did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := s.Collect("p", "account", "device", plan.ID); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result, err := s.Collect("p", "account", "device", plan.ID); err != nil || result.Status != "cancelled" {
		t.Fatal(result, err)
	}
	plan.ID = "new_upload"
	next, err := s.MarkCollection("p", "account", plan)
	if err != nil {
		t.Fatal(err)
	}
	due, _ := time.Parse(time.RFC3339Nano, next.NotBefore)
	if !due.Equal(now.Add(collectionGrace)) {
		t.Fatal("reused object did not receive fresh protection")
	}
}

func TestCollectionRejectsChangedHeadAndProtectsSharedProjectBytes(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	plan := collectionFixture(t, s, "p")
	other := collectionFixture(t, s, "q")
	if _, err := s.MarkCollection("p", "account", plan); err != nil {
		t.Fatal(err)
	}
	commit := proposal("concurrent", 1)
	commit.StateSHA256 = plan.CheckpointSHA256
	if _, err := s.Append("p", "account", commit); err != nil {
		t.Fatal(err)
	}
	now = now.Add(collectionGrace)
	if result, err := s.Collect("p", "account", "device", plan.ID); err != nil || result.Status != "cancelled" {
		t.Fatal(result, err)
	}
	plan.ID = "new"
	plan.Revision = 2
	if _, err := s.MarkCollection("p", "account", plan); !errors.Is(err, ErrConflict) {
		t.Fatal("collection accepted before checkpoint publication", err)
	}
	page, _ := s.Checkpoint("p")
	cp := page.Checkpoint.Checkpoint
	cp.Revision = 2
	cp.CommitID = commit.ID
	if _, err := s.PutCheckpoint("p", "account", cp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkCollection("p", "account", plan); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Collect("p", "account", "device", plan.ID); err != nil || result.Status != "deleted" {
		t.Fatal(result, err)
	}
	f, err := s.Object("q", other.Objects[0])
	if err != nil {
		t.Fatal("another project's object was deleted", err)
	}
	f.Close()
	if _, err := s.MarkCollection("q", "account", other); err != nil {
		t.Fatal(err)
	}
	now = now.Add(collectionGrace)
	if result, err := s.Collect("q", "account", "device", other.ID); err != nil || result.Status != "deleted" {
		t.Fatal(result, err)
	}
	if f, err := s.objects.OpenObject(plan.Objects[0]); err == nil {
		f.Close()
		t.Fatal("unreferenced global bytes remain")
	}
}

func TestCollectionRecoveryAfterLogicalDeletionAndLostResponse(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := collectionFixture(t, s, "p")
	marked, err := s.MarkCollection("p", "account", plan)
	if err != nil {
		t.Fatal(err)
	}
	// Crash boundary: the deletion transaction committed, and one file was
	// removed, but the usage/final receipt transaction has not committed yet.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("objects"))
		var record objectRecord
		if err := json.Unmarshal(bucket.Get(key("p", plan.Objects[0])), &record); err != nil {
			return err
		}
		record.Deleting = plan.ID
		data, _ := json.Marshal(record)
		if err := bucket.Put(key("p", plan.Objects[0]), data); err != nil {
			return err
		}
		marked.Status = "deleting"
		data, _ = json.Marshal(marked)
		return tx.Bucket([]byte("collections")).Put(key("p", plan.ID), data)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.objects.Remove(plan.Objects[0]); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body := []byte("unreferenced immutable snapshot content")
	if err := s.PutObject("p", plan.Objects[0], int64(len(body)), bytes.NewReader(body)); !errors.Is(err, ErrUploading) {
		t.Fatal("deleting object revived", err)
	}
	// The original collecting device may never return. Catalog maintenance by
	// its replacement can finish the already accepted logical deletion.
	if _, err := s.ObjectCatalog("p", "", 512); err != nil {
		t.Fatal(err)
	}
	if f, err := s.objects.OpenObject(plan.Objects[0]); err == nil {
		f.Close()
		t.Fatal("catalog did not recover started deletion")
	}
	if result, err := s.Collect("p", "account", "device", plan.ID); err != nil || result.Status != "deleted" {
		t.Fatal(result, err)
	}
	if result, err := s.Collect("p", "account", "device", plan.ID); err != nil || result.Deleted != 1 {
		t.Fatal("lost deletion receipt is not replayable", result, err)
	}
	// Reusing equal byte content later creates a new record. Old completed
	// deletion plans cannot delete that new upload generation.
	if err := s.PutObject("p", plan.Objects[0], int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Collect("p", "account", "device", plan.ID); err != nil {
		t.Fatal(err)
	}
	f, err := s.Object("p", plan.Objects[0])
	if err != nil {
		t.Fatal("old plan deleted a new object generation", err)
	}
	f.Close()
}

func TestReadLeaseExpirySurvivesServerRestart(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	plan := collectionFixture(t, s, "p")
	request := contracts.RelayReadLeaseRequest{SchemaVersion: 1, DeviceID: "reader", Revision: 1}
	lease, err := s.ReadLease("p", "peer", request)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = func() time.Time { return now }
	if _, err := s.MarkCollection("p", "account", plan); !errors.Is(err, ErrBusy) {
		t.Fatal("restart lost lease", err)
	}
	now = now.Add(readLeaseDuration + time.Second)
	request.LeaseID = lease.ID
	if _, err := s.ReadLease("p", "peer", request); !errors.Is(err, ErrConflict) {
		t.Fatal("expired lease renewed", err)
	}
	if _, err := s.MarkCollection("p", "account", plan); err != nil {
		t.Fatal(err)
	}
	request.LeaseID = ""
	request.Revision = 0
	if _, err := s.ReadLease("p", "peer", request); !errors.Is(err, ErrConflict) {
		t.Fatal("stale replica obtained lease", err)
	}
}

func TestRawReadsDoNotWriteAndSharedCollectionsConverge(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	p := collectionFixture(t, s, "p")
	q := collectionFixture(t, s, "q")
	var before, after int
	readID := func(target *int) {
		t.Helper()
		if err := s.db.View(func(tx *bolt.Tx) error { *target = tx.ID(); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	readID(&before)
	for i := 0; i < 100; i++ {
		f, err := s.Object("p", p.Objects[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	readID(&after)
	if before != after {
		t.Fatal("raw GET wrote database")
	}
	if _, err := s.MarkCollection("p", "account", p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkCollection("q", "account", q); err != nil {
		t.Fatal(err)
	}
	now = now.Add(collectionGrace)
	done := make(chan error, 2)
	for _, project := range []string{"p", "q"} {
		go func(project string) { _, err := s.Collect(project, "account", "device", "plan"); done <- err }(project)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil && !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
	}
	for _, project := range []string{"p", "q"} {
		if _, err := s.Collect(project, "account", "device", "plan"); err != nil {
			t.Fatal(err)
		}
	}
	if f, err := s.objects.OpenObject(p.Objects[0]); err == nil {
		f.Close()
		t.Fatal("last shared object not removed")
	}
	t.Log("100 raw GETs: 0 write transactions; concurrent shared collection converged")
}
