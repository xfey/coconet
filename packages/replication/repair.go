package replication

import (
	"context"
	"errors"
	"net/url"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
)

var ErrWaitingForReplica = errors.New("waiting for an authorized device holding this Session to upload a copy")

type repairTransport interface {
	RequestRepair(context.Context, string, string) error
	Repairs(context.Context, string, string) (contracts.RelayRepairPage, error)
	ClaimRepair(context.Context, string, contracts.RelayRepairRequest) (contracts.RelayRepair, error)
}

func (t HTTP) RequestRepair(ctx context.Context, project, digest string) error {
	var result map[string]bool
	return t.json(ctx, "POST", project, "repairs/request", contracts.RelayRepairRequest{SchemaVersion: 1, SHA256: digest}, &result)
}
func (t HTTP) Repairs(ctx context.Context, project, after string) (contracts.RelayRepairPage, error) {
	var result contracts.RelayRepairPage
	err := t.json(ctx, "GET", project, "repairs?after="+url.QueryEscape(after), nil, &result)
	return result, err
}
func (t HTTP) ClaimRepair(ctx context.Context, project string, input contracts.RelayRepairRequest) (contracts.RelayRepair, error) {
	var result contracts.RelayRepair
	err := t.json(ctx, "POST", project, "repairs/claim", input, &result)
	return result, err
}

// A request is not proof of project ownership. Only offer objects from this
// project's currently retained source closures, never arbitrary cached hashes.
func (c *Client) serveRepairs(ctx context.Context, project string, state workgraph.State) error {
	remote, ok := c.Remote.(repairTransport)
	if !ok {
		return nil
	}
	if c.repairCursor == nil {
		c.repairCursor = map[string]string{}
	}
	page, err := remote.Repairs(ctx, project, c.repairCursor[project])
	if err != nil {
		return err
	}
	if page.SchemaVersion != 1 || len(page.Requests) > 128 {
		return errors.New("invalid repair page")
	}
	c.repairCursor[project] = page.Next
	if len(page.Requests) == 0 {
		return nil
	}
	available := map[string]int64{}
	for source := range workgraph.RetainedSources(state) {
		snapshot, ok := state.Snapshots[source]
		if !ok {
			continue
		}
		objects, err := c.objects(workgraph.Commit{Snapshot: &snapshot})
		if err != nil {
			continue
		}
		for _, object := range objects {
			available[object.SHA256] = object.Size
		}
	}
	count := 0
	for _, request := range page.Requests {
		size, ok := available[request.SHA256]
		if !ok {
			continue
		}
		file, err := c.Store.OpenObject(request.SHA256)
		if err != nil {
			continue
		}
		_, err = remote.ClaimRepair(ctx, project, contracts.RelayRepairRequest{SchemaVersion: 1, SHA256: request.SHA256, DeviceID: c.Device})
		if err == nil {
			err = c.Remote.Put(ctx, project, request.SHA256, size, file)
		}
		file.Close()
		if errors.Is(err, ErrContentBusy) || errors.Is(err, ErrObjectMissing) {
			continue
		}
		if err != nil {
			return err
		}
		count++
		if count == 16 {
			break
		}
	}
	return nil
}
