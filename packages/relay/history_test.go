package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
)

func TestHistoryCompactionPreservesAnchorReceiptsAndRestart(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	now := time.Now().Add(-48 * time.Hour)
	s.now = func() time.Time { return now }
	var first contracts.RelayCommit
	cpBody := []byte("opaque verified checkpoint")
	for i := 0; i < 12; i++ {
		c := proposal(fmt.Sprintf("c%d", i), int64(i))
		c.StateSHA256 = contentstore.ObjectDigest(cpBody)
		if i == 0 {
			first = c
		}
		if _, err := s.Append("p", "a", c); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CompactHistory(context.Background(), time.Hour, 1024); err != nil || n != 0 {
		t.Fatal("deleted without checkpoint", n, err)
	}
	if _, err := s.PutCheckpoint("p", "a", contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "device", Revision: 10, CommitID: "c9", Codec: "coconet-state-json-v1", SHA256: contentstore.ObjectDigest(cpBody), Payload: cpBody}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(72 * time.Hour)
	if n, err := s.CompactHistory(context.Background(), 24*time.Hour, 3); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if _, err := s.Since("p", 0, 100); !errors.Is(err, contracts.ErrRelayHistoryCompacted) {
		t.Fatal(err)
	}
	if n, err := s.CompactHistory(context.Background(), 24*time.Hour, 1024); err != nil || n != 6 {
		t.Fatal(n, err)
	}
	page, err := s.Since("p", 9, 100)
	if err != nil || page.Head != 12 || len(page.Entries) != 3 {
		t.Fatal(page, err)
	}
	prior, err := s.Append("p", "a", first)
	if err != nil || prior.Revision != 1 {
		t.Fatal(prior, err)
	}
	if _, err := s.Append("p", "someone_else", first); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign retry", err)
	}
	first.Base = 12
	if _, err := s.Append("p", "a", first); !errors.Is(err, ErrInvalid) {
		t.Fatal("id reused after collection", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Since("p", 0, 100); !errors.Is(err, contracts.ErrRelayHistoryCompacted) {
		t.Fatal("floor lost", err)
	}
	e, err := s.Receipt("p", "c0")
	if err != nil || e.Revision != 1 || e.CanonicalSHA256 == "" {
		t.Fatal(e, err)
	}
}

func TestHistoryGraceAndCancellationDoNotRemoveRecentCommits(t *testing.T) {
	s := fixture(t)
	body := []byte("checkpoint")
	for i := 0; i < 2; i++ {
		c := proposal(fmt.Sprintf("c%d", i), int64(i))
		c.StateSHA256 = contentstore.ObjectDigest(body)
		if _, err := s.Append("p", "a", c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutCheckpoint("p", "a", contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "device", Revision: 2, CommitID: "c1", Codec: "coconet-state-json-v1", SHA256: contentstore.ObjectDigest(body), Payload: body}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CompactHistory(context.Background(), time.Hour, 10); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := s.CompactHistory(ctx, 0, 10); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatal(n, err)
	}
	if page, err := s.Since("p", 0, 100); err != nil || len(page.Entries) != 2 {
		t.Fatal(page, err)
	}
}
