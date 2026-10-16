package replication

import (
	"context"
	"net/url"
	"strconv"

	"github.com/xfey/coconet/packages/contracts"
)

type ProcessingTransport interface {
	RegisterProcessing(context.Context, string, contracts.ProcessingRegister) (contracts.ProcessingTask, error)
	ProcessingTasks(context.Context, string, int64) (contracts.ProcessingPage, error)
	ProcessingTask(context.Context, string, string) (contracts.ProcessingTask, error)
	ActProcessing(context.Context, string, string, contracts.ProcessingAction) (contracts.ProcessingTask, error)
}

func (t HTTP) RegisterProcessing(ctx context.Context, project string, input contracts.ProcessingRegister) (contracts.ProcessingTask, error) {
	var out contracts.ProcessingTask
	err := t.json(ctx, "POST", project, "processing", input, &out)
	return out, err
}
func (t HTTP) ProcessingTasks(ctx context.Context, project string, after int64) (contracts.ProcessingPage, error) {
	var out contracts.ProcessingPage
	err := t.json(ctx, "GET", project, "processing?after="+strconv.FormatInt(after, 10), nil, &out)
	return out, err
}
func (t HTTP) ProcessingTask(ctx context.Context, project, id string) (contracts.ProcessingTask, error) {
	var out contracts.ProcessingTask
	err := t.json(ctx, "GET", project, "processing/"+url.PathEscape(id), nil, &out)
	return out, err
}
func (t HTTP) ActProcessing(ctx context.Context, project, id string, input contracts.ProcessingAction) (contracts.ProcessingTask, error) {
	var out contracts.ProcessingTask
	err := t.json(ctx, "POST", project, "processing/"+url.PathEscape(id), input, &out)
	return out, err
}

func (c *Client) SharedProcessing() bool { _, ok := c.Remote.(ProcessingTransport); return ok }
func (c *Client) RegisterProcessing(ctx context.Context, project string, spec contracts.ProcessingSpec) (contracts.ProcessingTask, error) {
	return c.Remote.(ProcessingTransport).RegisterProcessing(ctx, project, contracts.ProcessingRegister{SchemaVersion: 1, DeviceID: c.Device, Spec: spec})
}
func (c *Client) ProcessingTasks(ctx context.Context, project string, after int64) (contracts.ProcessingPage, error) {
	return c.Remote.(ProcessingTransport).ProcessingTasks(ctx, project, after)
}
func (c *Client) ProcessingTask(ctx context.Context, project, id string) (contracts.ProcessingTask, error) {
	return c.Remote.(ProcessingTransport).ProcessingTask(ctx, project, id)
}
func (c *Client) ActProcessing(ctx context.Context, project, id, operation, lease, reason string) (contracts.ProcessingTask, error) {
	return c.Remote.(ProcessingTransport).ActProcessing(ctx, project, id, contracts.ProcessingAction{SchemaVersion: 1, DeviceID: c.Device, Operation: operation, Lease: lease, Reason: reason})
}
