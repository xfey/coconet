package relay

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func registerProcessing(t *testing.T, s *Store, session string) contracts.ProcessingTask {
	t.Helper()
	body := []byte(`{"opaque":"fixed input"}`)
	task, err := s.RegisterProcessing("project", "alice", contracts.ProcessingRegister{SchemaVersion: 1, DeviceID: "a", Spec: contracts.ProcessingSpec{Key: "stage/" + session + "/1", Kind: "stage", Owner: "alice.a", SessionID: session, Version: 1, Payload: body, SHA256: contentstore.ObjectDigest(body)}})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func processingAction(device, operation, lease string) contracts.ProcessingAction {
	return contracts.ProcessingAction{SchemaVersion: 1, DeviceID: device, Operation: operation, Lease: lease}
}

func TestProcessingClaimHandoffFencesOldResultAndRetainsIdempotency(t *testing.T) {
	s := fixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	task := registerProcessing(t, s, "source")
	a, err := s.ActProcessing("project", "alice", task.ID, processingAction("a", "claim", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActProcessing("project", "bob", task.ID, processingAction("b", "claim", "")); !errors.Is(err, contracts.ErrProcessingBusy) {
		t.Fatal("live task stolen", err)
	}
	now = now.Add(processingLease + time.Second)
	b, err := s.ActProcessing("project", "bob", task.ID, processingAction("b", "claim", ""))
	if err != nil {
		t.Fatal(err)
	}
	old := proposal("old", 0)
	old.DeviceID = "a"
	old.Codec = "coconet-json-v3"
	auth := a.Authorization()
	old.Processing = &auth
	if _, err := s.Append("project", "alice", old); !errors.Is(err, contracts.ErrProcessingStale) {
		t.Fatal("expired worker published", err)
	}
	input := proposal("winner", 0)
	input.DeviceID = "b"
	input.Codec = "coconet-json-v3"
	auth = b.Authorization()
	input.Processing = &auth
	first, err := s.Append("project", "bob", input)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := s.ProcessingTask("project", task.ID)
	if err != nil || finished.Status != "completed" || len(finished.Spec.Payload) != 0 {
		t.Fatal("body survived completion", finished, err)
	}
	other := input
	other.ID = "duplicate"
	other.Base = 1
	if _, err := s.Append("project", "bob", other); !errors.Is(err, contracts.ErrProcessingStale) {
		t.Fatal("task accepted twice", err)
	}
	now = now.Add(processingLifetime)
	if err := s.pruneProcessing(256); err != nil {
		t.Fatal(err)
	}
	again, err := s.Append("project", "bob", input)
	if err != nil || again.Revision != first.Revision {
		t.Fatal("lost reply became new execution", err)
	}
}

func TestProcessingConcurrentClaimHasOneWinnerAndOtherSessionCanProceed(t *testing.T) {
	s := fixture(t)
	a := registerProcessing(t, s, "one")
	b := registerProcessing(t, s, "two")
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, account := range []string{"bob", "carol"} {
		go func(account string) {
			ready.Done()
			<-start
			_, err := s.ActProcessing("project", account, a.ID, processingAction("device", "claim", ""))
			results <- err
		}(account)
	}
	ready.Wait()
	close(start)
	x, y := <-results, <-results
	if !(x == nil && errors.Is(y, contracts.ErrProcessingBusy) || y == nil && errors.Is(x, contracts.ErrProcessingBusy)) {
		t.Fatal(x, y)
	}
	if _, err := s.ActProcessing("project", "dave", b.ID, processingAction("d", "claim", "")); err != nil {
		t.Fatal("unrelated session serialized", err)
	}
}

func TestProcessingFailuresShareBudgetAndExplicitRetry(t *testing.T) {
	s := fixture(t)
	task := registerProcessing(t, s, "source")
	for _, account := range []string{"alice", "bob", "carol"} {
		claimed, err := s.ActProcessing("project", account, task.ID, processingAction(account, "claim", ""))
		if err != nil {
			t.Fatal(err)
		}
		failed, err := s.ActProcessing("project", account, task.ID, processingAction(account, "fail", claimed.Lease))
		if err != nil {
			t.Fatal(err)
		}
		if account == "carol" && failed.Status != "paused" {
			t.Fatal("unbounded shared attempts")
		}
	}
	if _, err := s.ActProcessing("project", "dave", task.ID, processingAction("d", "claim", "")); !errors.Is(err, contracts.ErrProcessingPaused) {
		t.Fatal(err)
	}
	if _, err := s.ActProcessing("project", "alice", task.ID, processingAction("a", "retry", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActProcessing("project", "dave", task.ID, processingAction("d", "claim", "")); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedOwnerFailuresReserveAnAttemptForALaterPeer(t *testing.T) {
	s := fixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	task := registerProcessing(t, s, "late-peer")
	for i := 0; i < 2; i++ {
		claim, err := s.ActProcessing("project", "alice", task.ID, processingAction("a", "claim", ""))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ActProcessing("project", "alice", task.ID, processingAction("a", "fail", claim.Lease)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Minute)
	}
	if _, err := s.ActProcessing("project", "alice", task.ID, processingAction("a", "claim", "")); !errors.Is(err, contracts.ErrProcessingYielded) {
		t.Fatal("owner consumed every handoff opportunity", err)
	}
	now = now.Add(time.Hour)
	peer, err := s.ActProcessing("project", "bob", task.ID, processingAction("b", "claim", ""))
	if err != nil || peer.Attempts != 3 {
		t.Fatal("later peer cannot take over", err)
	}
}

func TestProcessingQuotaExpiryAndOpaqueInput(t *testing.T) {
	s := fixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	task := registerProcessing(t, s, "one")
	page, err := s.ProcessingTasks("project", 0)
	if err != nil || len(page.Tasks) != 1 || len(page.Tasks[0].Spec.Payload) != 0 {
		t.Fatal("list leaked input", err)
	}
	if _, err := s.ProcessingTask("other", task.ID); !errors.Is(err, contracts.ErrProcessingStale) {
		t.Fatal("cross-project task", err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("processing-usage")).Put(key("project", "bytes"), encodedNumber(maxProcessingProjectBytes))
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.RegisterProcessing("project", "a", contracts.ProcessingRegister{SchemaVersion: 1, DeviceID: "a", Spec: contracts.ProcessingSpec{Key: "stage/two/1", Kind: "stage", Owner: "a.a", SessionID: "two", Version: 1, Payload: []byte("opaque"), SHA256: contentstore.ObjectDigest([]byte("opaque"))}})
	if !errors.Is(err, ErrQuota) {
		t.Fatal("unbounded task storage", err)
	}
	now = now.Add(processingLifetime + time.Second)
	if err := s.pruneProcessing(256); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessingTask("project", task.ID); !errors.Is(err, contracts.ErrProcessingStale) {
		t.Fatal("expired capsule retained", err)
	}
}

func TestProcessingRenewSameSessionExclusionAndExpiredRecreation(t *testing.T) {
	s := fixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	first := registerProcessing(t, s, "serial")
	nextSpec := first.Spec
	nextSpec.Key, nextSpec.Version = "stage/serial/2", 2
	next, err := s.RegisterProcessing("project", "alice", contracts.ProcessingRegister{SchemaVersion: 1, DeviceID: "a", Spec: nextSpec})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ActProcessing("project", "alice", first.ID, processingAction("a", "claim", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActProcessing("project", "bob", next.ID, processingAction("b", "claim", "")); !errors.Is(err, contracts.ErrProcessingBusy) {
		t.Fatal("same session ran concurrently", err)
	}
	now = now.Add(time.Minute)
	renewed, err := s.ActProcessing("project", "alice", first.ID, processingAction("a", "renew", claimed.Lease))
	if err != nil || renewed.Attempts != 1 || renewed.Lease != claimed.Lease {
		t.Fatal(renewed, err)
	}
	now = now.Add(time.Minute)
	if _, err := s.ActProcessing("project", "bob", first.ID, processingAction("b", "claim", "")); !errors.Is(err, contracts.ErrProcessingBusy) {
		t.Fatal("renewed claim stolen", err)
	}
	now = now.Add(processingLifetime)
	fresh := registerProcessing(t, s, "serial")
	if fresh.Sequence <= first.Sequence || fresh.Attempts != 0 || fresh.Lease != "" || fresh.Status != "queued" {
		t.Fatal("expired record not recreated", fresh)
	}
	if err := s.pruneProcessing(256); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProcessingTask("project", fresh.ID)
	if err != nil || len(got.Spec.Payload) == 0 {
		t.Fatal("old expiry deleted fresh capsule", err)
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		u := tx.Bucket([]byte("processing-usage"))
		id := key("project", fresh.ID)
		size := int64(len(tx.Bucket([]byte("processing")).Get(id)) + len(tx.Bucket([]byte("processing-inputs")).Get(id)))
		if number(u.Get(key("project", "count"))) != 1 || number(u.Get(key("project", "bytes"))) != size || number(u.Get([]byte("global"))) != size {
			t.Fatal("usage accounting drifted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingSurvivesReopenWithClaimAndInput(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	task := registerProcessing(t, s, "durable")
	claimed, err := s.ActProcessing("project", "alice", task.ID, processingAction("a", "claim", ""))
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
	got, err := s.ProcessingTask("project", task.ID)
	if err != nil || string(got.Spec.Payload) != string(task.Spec.Payload) || got.Lease != claimed.Lease || got.Attempts != 1 {
		t.Fatal("restart lost task", err)
	}
	if _, err := s.ActProcessing("project", "bob", task.ID, processingAction("b", "claim", "")); !errors.Is(err, contracts.ErrProcessingBusy) {
		t.Fatal("restart lost fence", err)
	}
}

func TestOwnerWithdrawalRevokesHelperLeaseAndRejectsUnrelatedActors(t *testing.T) {
	s := fixture(t)
	task := registerProcessing(t, s, "source")
	helper, err := s.ActProcessing("project", "bob", task.ID, processingAction("b", "claim", ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range [][2]string{{"carol", "c"}, {"alice", "other-device"}, {"bob", "b"}} {
		if _, err := s.ActProcessing("project", identity[0], task.ID, processingAction(identity[1], "supersede", "")); err == nil {
			t.Fatal("unrelated actor revoked task", identity)
		}
	}
	revoked, err := s.ActProcessing("project", "alice", task.ID, processingAction("a", "supersede", ""))
	if err != nil || revoked.Status != "superseded" {
		t.Fatal(revoked, err)
	}
	if _, err := s.ActProcessing("project", "bob", task.ID, processingAction("b", "renew", helper.Lease)); !errors.Is(err, contracts.ErrProcessingStale) {
		t.Fatal("revoked lease renewed", err)
	}
	proposal := proposal("late", 0)
	proposal.DeviceID, proposal.Codec = "b", "coconet-json-v4"
	auth := helper.Authorization()
	proposal.Processing = &auth
	if _, err := s.Append("project", "bob", proposal); !errors.Is(err, contracts.ErrProcessingStale) {
		t.Fatal("late helper published", err)
	}
	stored, err := s.ProcessingTask("project", task.ID)
	if err != nil || len(stored.Spec.Payload) != 0 {
		t.Fatal("withdrawn payload retained", err)
	}
}
