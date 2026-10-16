package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func TestCommitLogQuotaPreservesAcceptedHeadAndIdempotentReceipt(t *testing.T) {
	s := fixture(t)
	first := proposal("first", 0)
	if _, err := s.Append("p", "account", first); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("log-usage")).Put([]byte("p"), encodedNumber(MaxProjectLogBytes))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("p", "account", proposal("overflow", 1)); !errors.Is(err, ErrLogQuota) {
		t.Fatal(err)
	}
	page, err := s.Since("p", 0, 100)
	if err != nil || page.Head != 1 || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	if receipt, err := s.Append("p", "account", first); err != nil || receipt.Revision != 1 {
		t.Fatal("quota broke accepted retry", err)
	}
}
func TestCollectionArchiveQuotaAndExpiryPreserveDeletingState(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	plan := collectionFixture(t, s, "p")
	if err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("collections"))
		used := 0
		for i := 0; used < maxCollectionArchiveBytes; i++ {
			record := contracts.RelayCollection{SchemaVersion: 1, Plan: plan, Status: "cancelled", UpdatedAt: now.Format(time.RFC3339Nano)}
			record.Plan.Objects = make([]string, 8192)
			for j := range record.Plan.Objects {
				record.Plan.Objects[j] = plan.Objects[0]
			}
			data, _ := json.Marshal(record)
			used += len(data)
			if err := bucket.Put(key("p", fmt.Sprintf("old_%d", i)), data); err != nil {
				return err
			}
		}
		record := contracts.RelayCollection{SchemaVersion: 1, Plan: plan, Status: "deleting", UpdatedAt: now.Format(time.RFC3339Nano)}
		data, _ := json.Marshal(record)
		return bucket.Put(key("p", "keep_deleting"), data)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkCollection("p", "account", plan); !errors.Is(err, ErrQuota) {
		t.Fatal("archive quota not enforced", err)
	}
	now = now.Add(collectionReceiptRetention + time.Second)
	if _, err := s.MarkCollection("p", "account", plan); err != nil {
		t.Fatal("expired records blocked maintenance", err)
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("collections"))
		if bucket.Get(key("p", "keep_deleting")) == nil {
			t.Error("started deletion was expired")
		}
		return bucket.ForEach(func(k, _ []byte) error {
			if bytes.Contains(k, []byte("old_")) {
				t.Error("expired terminal record retained")
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}
func TestObjectCatalogPagesAreBoundedAndOnlyExposeMetadata(t *testing.T) {
	s := fixture(t)
	plan := collectionFixture(t, s, "p")
	page, err := s.ObjectCatalog("p", "", 1)
	if err != nil || len(page.Objects) != 1 || page.Objects[0].SHA256 != plan.Objects[0] {
		t.Fatal(page, err)
	}
	for _, limit := range []int{0, 513} {
		if _, err := s.ObjectCatalog("p", "", limit); err == nil {
			t.Fatal("unbounded page accepted")
		}
	}
	if _, err := s.ObjectCatalog("p", "../", 1); err == nil {
		t.Fatal("non-digest cursor accepted")
	}
}
