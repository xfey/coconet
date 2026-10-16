package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
)

type failingRemoval struct {
	*memoryDirect
	fail string
}

func (b *failingRemoval) Remove(ctx context.Context, key string) error {
	if key == b.fail {
		return errors.New("synthetic object deletion failure")
	}
	return b.memoryDirect.Remove(ctx, key)
}

func TestMaintenanceDrainsBacklogAndIsolatesObjectFailure(t *testing.T) {
	ctx := context.Background()
	backend := &failingRemoval{memoryDirect: &memoryDirect{data: map[string][]byte{}}}
	s, err := OpenWithOptions(t.TempDir(), Options{Backend: backend, ContentTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	for i := 0; i < 300; i++ {
		body := []byte(fmt.Sprint(i))
		target, err := s.BeginUpload(ctx, "p", "a", contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: contentstore.ObjectDigest(body), Size: int64(len(body))})
		if err != nil {
			t.Fatal(err)
		}
		key := strings.TrimPrefix(target.URL, "https://objects.invalid/")
		backend.data[key] = body
		if i == 0 {
			backend.fail = key
		}
	}
	now = now.Add(20 * time.Minute)
	before, err := s.MaintenanceStatus()
	if err != nil || before.DueRecords != 300 || before.OldestDueAt == "" || before.TransferReservedBytes != 300*8192 {
		t.Fatal(before, err)
	}
	if err := s.MaintenanceCycle(ctx); err == nil {
		t.Fatal("object failure hidden")
	}
	after, err := s.MaintenanceStatus()
	if err != nil || after.Processed != 299 || after.TransferReservedBytes != 8192 || !after.LastFailed || after.DueRecords != 0 {
		t.Fatal(after, err)
	}
	backend.fail = ""
	now = now.Add(2 * time.Minute)
	if err := s.MaintenanceCycle(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = s.MaintenanceStatus()
	if err != nil || after.TransferReservedBytes != 0 || after.LastFailed || after.LastSuccessAt == "" || len(backend.data) != 0 {
		t.Fatal(after, err)
	}
}
