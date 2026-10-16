package replication

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
)

type RetentionTransport interface {
	ReadLease(context.Context, string, contracts.RelayReadLeaseRequest) (contracts.RelayReadLease, error)
	ReleaseReadLease(context.Context, string, string, string) error
	MarkCollection(context.Context, string, contracts.RelayCollectionPlan) (contracts.RelayCollection, error)
	Collect(context.Context, string, string, string) (contracts.RelayCollection, error)
}

func (t HTTP) ReadLease(ctx context.Context, project string, input contracts.RelayReadLeaseRequest) (contracts.RelayReadLease, error) {
	var output contracts.RelayReadLease
	err := t.json(ctx, "POST", project, "leases", input, &output)
	return output, err
}
func (t HTTP) ReleaseReadLease(ctx context.Context, project, device, id string) error {
	var output struct {
		Released bool `json:"released"`
	}
	err := t.json(ctx, "DELETE", project, "leases/"+url.PathEscape(id)+"?device="+url.QueryEscape(device), nil, &output)
	if err == nil && !output.Released {
		return errors.New("Relay did not acknowledge lease release")
	}
	return err
}
func (t HTTP) MarkCollection(ctx context.Context, project string, input contracts.RelayCollectionPlan) (contracts.RelayCollection, error) {
	var output contracts.RelayCollection
	err := t.json(ctx, "POST", project, "collections", input, &output)
	return output, err
}
func (t HTTP) Collect(ctx context.Context, project, device, id string) (contracts.RelayCollection, error) {
	var output contracts.RelayCollection
	err := t.json(ctx, "POST", project, "collections/"+url.PathEscape(id)+"?device="+url.QueryEscape(device), nil, &output)
	return output, err
}

func (c *Client) sourceReadLease(ctx context.Context, project string, revision int64) (context.Context, func() error, error) {
	return c.sourceReadLeaseEvery(ctx, project, revision, 40*time.Second)
}

func validateReadLease(lease contracts.RelayReadLease, revision int64) error {
	if lease.SchemaVersion != 1 || lease.ID == "" || len(lease.ID) > 200 || lease.Revision != revision {
		return errors.New("invalid Relay read lease")
	}
	expires, err := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	if err != nil || !expires.After(time.Now().Add(60*time.Second)) {
		return errors.New("Relay read lease expired or has insufficient protection; check device and server clocks")
	}
	return nil
}

func (c *Client) sourceReadLeaseEvery(ctx context.Context, project string, revision int64, interval time.Duration) (context.Context, func() error, error) {
	remote, ok := c.Remote.(RetentionTransport)
	if !ok {
		return ctx, func() error { return nil }, nil
	}
	request := contracts.RelayReadLeaseRequest{SchemaVersion: 1, DeviceID: c.Device, Revision: revision}
	lease, err := remote.ReadLease(ctx, project, request)
	if err != nil {
		return ctx, nil, err
	}
	if err := validateReadLease(lease, revision); err != nil {
		return ctx, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	request.LeaseID = lease.ID
	go func() {
		timer := time.NewTicker(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-timer.C:
				attempt, stop := context.WithTimeout(ctx, 15*time.Second)
				next, err := remote.ReadLease(attempt, project, request)
				stop()
				if ctx.Err() != nil {
					done <- nil
					return
				}
				if err == nil {
					err = validateReadLease(next, revision)
					if next.ID != lease.ID {
						err = errors.New("Relay changed active read lease")
					}
				}
				if err != nil {
					cancel()
					done <- err
					return
				}
			}
		}
	}()
	finish := func() error {
		cancel()
		renewalErr := <-done
		releaseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := remote.ReleaseReadLease(releaseCtx, project, c.Device, lease.ID); err != nil {
			// Expiry still bounds protection; preserve the actionable transport failure
			// without labelling already verified content as corrupt.
			archiveErr := c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "read_lease_release", Status: "failed", Error: err.Error(), Revision: revision})
			renewalErr = errors.Join(renewalErr, archiveErr)
		}
		return renewalErr
	}
	return ctx, finish, nil
}
