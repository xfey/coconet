package relay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
)

func TestTemporaryExpiryProtectsReadersAndReclaimsCapacity(t *testing.T) {
	ctx := context.Background()
	backend := &memoryDirect{data: map[string][]byte{}}
	s, err := OpenWithOptions(t.TempDir(), Options{Backend: backend, ContentTTL: time.Hour, TransferCapacity: 8192})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	data := []byte("fixed replica")
	digest := contentstore.ObjectDigest(data)
	input := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: digest, Size: int64(len(data))}
	target, err := s.BeginUpload(ctx, "project", "owner", input)
	if err != nil {
		t.Fatal(err)
	}
	stage := strings.TrimPrefix(target.URL, "https://objects.invalid/")
	backend.data[stage] = data
	input.UploadID = target.UploadID
	if _, err := s.FinishUpload(ctx, "project", "owner", input); err != nil {
		t.Fatal(err)
	}
	other := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: contentstore.ObjectDigest([]byte("other")), Size: 5}
	if _, err := s.BeginUpload(ctx, "another", "owner", other); !errors.Is(err, ErrQuota) {
		t.Fatal("global staging capacity ignored", err)
	}
	now = now.Add(17 * time.Minute)
	if n, err := s.MaintainTemporary(ctx, 64); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if len(backend.data) != 1 {
		t.Fatal("staging leaked", len(backend.data))
	}
	// New manifests renew their whole closure, including reused prefix chunks.
	now = now.Add(40 * time.Minute)
	if _, err := s.Missing("project", contracts.RelayObjectCheck{SchemaVersion: 1, Objects: []contracts.RelayObject{{SHA256: digest, Size: int64(len(data))}}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if n, err := s.MaintainTemporary(ctx, 64); err != nil || n != 0 {
		t.Fatal("renewed prefix expired", n, err)
	}
	now = now.Add(56 * time.Minute)
	lease, err := s.ReadLease("project", "reader", contracts.RelayReadLeaseRequest{SchemaVersion: 1, DeviceID: "peer", Revision: 0})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.MaintainTemporary(ctx, 64); err != nil || n != 0 {
		t.Fatal("reader protection failed", n, err)
	}
	if _, err := s.DownloadTarget(ctx, "project", digest); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReadLease("project", "reader", "peer", lease.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if n, err := s.MaintainTemporary(ctx, 64); err != nil || n != 0 {
		t.Fatal("signed URL protection failed", n, err)
	}
	now = now.Add(2 * time.Minute)
	if n, err := s.MaintainTemporary(ctx, 64); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if len(backend.data) != 0 {
		t.Fatal("expired bytes leaked")
	}
	if _, err := s.DownloadTarget(ctx, "project", digest); !errors.Is(err, ErrObject) {
		t.Fatal("expired content advertised", err)
	}
	if _, err := s.BeginUpload(ctx, "another", "owner", other); err != nil {
		t.Fatal("quota not reclaimed", err)
	}
}

func TestExpiredReservationSurvivesRestartAndIsReclaimed(t *testing.T) {
	ctx := context.Background()
	backend := &memoryDirect{data: map[string][]byte{}}
	root := t.TempDir()
	s, err := OpenWithOptions(root, Options{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	input := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: contentstore.ObjectDigest([]byte("partial")), Size: 7}
	target, err := s.BeginUpload(ctx, "project", "owner", input)
	if err != nil {
		t.Fatal(err)
	}
	backend.data[strings.TrimPrefix(target.URL, "https://objects.invalid/")] = []byte("part")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(root, Options{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now = now.Add(17 * time.Minute)
	s.now = func() time.Time { return now }
	if n, err := s.MaintainTemporary(ctx, 64); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if len(backend.data) != 0 {
		t.Fatal("partial upload leaked")
	}
	next, err := s.BeginUpload(ctx, "project", "other", input)
	if err != nil || next.UploadID == target.UploadID {
		t.Fatal("new generation unavailable", err)
	}
}

func TestRepairClaimsAreScopedCoalescedAndExpire(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	digest := contentstore.ObjectDigest([]byte("repair"))
	if err := s.RequestRepair("project", digest); err != nil {
		t.Fatal(err)
	}
	request := contracts.RelayRepairRequest{SchemaVersion: 1, SHA256: digest, DeviceID: "one"}
	claimed, err := s.ClaimRepair("project", "owner", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestRepair("project", digest); err != nil {
		t.Fatal(err)
	}
	page, err := s.Repairs("project", "")
	if err != nil || len(page.Requests) != 1 || page.Requests[0].ClaimedUntil != claimed.ClaimedUntil {
		t.Fatal(page, err)
	}
	request.DeviceID = "two"
	if _, err := s.ClaimRepair("project", "peer", request); !errors.Is(err, ErrBusy) {
		t.Fatal("live claim stolen", err)
	}
	if _, err := s.ClaimRepair("foreign", "peer", request); !errors.Is(err, ErrObject) {
		t.Fatal("foreign request leaked", err)
	}
	now = now.Add(3 * time.Minute)
	if _, err := s.ClaimRepair("project", "peer", request); err != nil {
		t.Fatal("expired claim could not be taken over", err)
	}
	now = now.Add(8 * 24 * time.Hour)
	page, err = s.Repairs("project", "")
	if err != nil || len(page.Requests) != 0 {
		t.Fatal("abandoned request remained active", page, err)
	}
}

func TestRepairExpiryDoesNotDropTheNextLiveRequest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	old := contentstore.ObjectDigest([]byte("old"))
	live := contentstore.ObjectDigest([]byte("live"))
	if err := s.RequestRepair("project", old); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * 24 * time.Hour)
	if err := s.RequestRepair("project", live); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * 24 * time.Hour)
	if err := s.pruneRepairs(64); err != nil {
		t.Fatal(err)
	}
	page, err := s.Repairs("project", "")
	if err != nil || len(page.Requests) != 1 || page.Requests[0].SHA256 != live {
		t.Fatal(page, err)
	}
}
