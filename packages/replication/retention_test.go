package replication

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
)

type leaseTransport struct {
	*localTransport
	failRenew    bool
	failRelease  bool
	expired      bool
	acquisitions atomic.Int32
	releases     atomic.Int32
}

func (r *leaseTransport) ReadLease(_ context.Context, p string, req contracts.RelayReadLeaseRequest) (contracts.RelayReadLease, error) {
	r.acquisitions.Add(1)
	if req.LeaseID != "" && r.failRenew {
		return contracts.RelayReadLease{}, errors.New("renewal interrupted")
	}
	lease, err := r.s.ReadLease(p, r.account, req)
	if errors.Is(err, relay.ErrConflict) {
		err = ErrConflict
	}
	if r.expired {
		lease.ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	}
	return lease, err
}
func (r *leaseTransport) ReleaseReadLease(_ context.Context, p, device, id string) error {
	r.releases.Add(1)
	if r.failRelease {
		return errors.New("release offline")
	}
	return r.s.ReleaseReadLease(p, r.account, device, id)
}
func (r *leaseTransport) MarkCollection(_ context.Context, p string, v contracts.RelayCollectionPlan) (contracts.RelayCollection, error) {
	return r.s.MarkCollection(p, r.account, v)
}
func (r *leaseTransport) Collect(_ context.Context, p, d, id string) (contracts.RelayCollection, error) {
	return r.s.Collect(p, r.account, d, id)
}

func TestPullLeaseCatchesUpAndReportsReleaseFailureSeparately(t *testing.T) {
	remote, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	owner, producer, _ := replica(t, remote, "owner", "producer")
	client, _, transport := replica(t, remote, "peer", "reader")
	lease := &leaseTransport{localTransport: transport, failRelease: true}
	client.Remote = lease
	fixed, err := producer.Capture(context.Background(), source("first", "inspect status"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.SyncContents(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	if err := client.Pull(context.Background(), "project", fixed, true); err != nil {
		t.Fatal(err)
	}
	if lease.acquisitions.Load() != 2 || lease.releases.Load() != 1 {
		t.Fatal("stale replica did not reacquire at current head")
	}
	state, _ := client.Store.State("project")
	if _, err := client.Store.ArtifactFile(state.Snapshots[fixed.Key()].Artifact); err != nil {
		t.Fatal(err)
	}
	logs, err := client.Store.Transfers("project", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, log := range logs {
		if log.Operation == "read_lease_release" && log.Status == "failed" && strings.Contains(log.Error, "release offline") {
			found = true
		}
	}
	if !found {
		t.Fatal("lease release failure missing from diagnostics")
	}
}
func TestReadLeaseRenewalFailureCancelsDownloadAndReleases(t *testing.T) {
	remote, err := relay.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	client, _, transport := replica(t, remote, "owner", "reader")
	lease := &leaseTransport{localTransport: transport, failRenew: true}
	client.Remote = lease
	ctx, finish, err := client.sourceReadLeaseEvery(context.Background(), "project", 0, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("failed renewal did not cancel")
	}
	if err := finish(); err == nil || !strings.Contains(err.Error(), "renewal interrupted") {
		t.Fatal(err)
	}
	if lease.releases.Load() != 1 {
		t.Fatal("failed renewal did not release")
	}
	lease.expired = true
	if _, _, err := client.sourceReadLease(context.Background(), "project", 0); err == nil {
		t.Fatal("expired protection accepted")
	}
}
