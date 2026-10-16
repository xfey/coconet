package graphstore

import (
	"bytes"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func TestTransferArchiveBoundsRepeatedFailuresAndKeepsAcceptedHistory(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if _, err := s.Apply(snapshotCommit(), false); err != nil {
		t.Fatal(err)
	}
	receipt := &contracts.RelayReceipt{SchemaVersion: 1, ProjectID: "project", Revision: 2, Commit: contracts.RelayCommit{Payload: bytes.Repeat([]byte("x"), 1<<20)}}
	for i := 0; i < 55; i++ {
		if err := s.RecordTransfer(Transfer{ProjectID: "project", Operation: "sync", Status: "failed", Error: "fixture invalid receipt", Receipt: receipt}); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := s.DiagnosticUsage()
	if err != nil || usage.Transfers.Bytes > maxTransferArchiveBytes || usage.Transfers.Rows >= 55 {
		t.Fatal(usage.Transfers, err)
	}
	recent, err := s.Transfers("project", 1)
	if err != nil || len(recent) != 1 || recent[0].ID != 55 {
		t.Fatal("most recent failure missing", err)
	}
	expected := usage.Transfers
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = s.DiagnosticUsage()
	if err != nil || usage.Transfers != expected {
		t.Fatal("restart lost accounting", usage.Transfers, err)
	}
	if err := s.PruneTransfers(time.Now().Add(8 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	usage, err = s.DiagnosticUsage()
	if err != nil || usage.Transfers.Bytes != 0 || usage.Transfers.Rows != 0 {
		t.Fatal(usage.Transfers, err)
	}
	history, err := s.History("project")
	if err != nil || len(history) != 1 {
		t.Fatal("transport pruning removed accepted graph history", err)
	}
	if _, err := s.Replay("project"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTransfer(Transfer{ProjectID: "project", Operation: "sync", Status: "accepted"}); err != nil {
		t.Fatal(err)
	}
	recent, err = s.Transfers("project", 1)
	if err != nil || len(recent) != 1 || recent[0].ID != 56 {
		t.Fatal("pruning reset transfer identities", err)
	}
}
func TestTransferArchiveRebuildsItsIndexWithoutResettingRecords(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTransfer(Transfer{ProjectID: "project", Operation: "receive", Status: "validated"}); err != nil {
		t.Fatal(err)
	}
	expected, err := s.DiagnosticUsage()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte("settings")).Delete([]byte("transfer-usage")); err != nil {
			return err
		}
		return tx.DeleteBucket([]byte("transfer-order"))
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.DiagnosticUsage()
	if err != nil || got.Transfers != expected.Transfers {
		t.Fatal(got, err)
	}
	records, err := s.Transfers("project", 1)
	if err != nil || len(records) != 1 || records[0].ID != 1 {
		t.Fatal(records, err)
	}
}
