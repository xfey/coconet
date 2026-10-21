package replication

import (
	"context"
	"errors"
	"github.com/xfey/coconet/packages/contracts"
)

var ErrDocumentUnsupported = errors.New("server does not support project documents")

func (t HTTP) ProjectDocument(ctx context.Context, project string) (contracts.ProjectDocument, error) {
	var doc contracts.ProjectDocument
	err := t.json(ctx, "GET", project, "document", nil, &doc)
	var request *RequestError
	if errors.As(err, &request) && request.Status == 404 && request.Code == "route_not_found" {
		err = ErrDocumentUnsupported
	}
	return doc, err
}

func (c *Client) ProjectDocument(ctx context.Context, project string) (contracts.ProjectDocument, error) {
	if remote, ok := c.Remote.(interface {
		ProjectDocument(context.Context, string) (contracts.ProjectDocument, error)
	}); ok {
		return remote.ProjectDocument(ctx, project)
	}
	return contracts.ProjectDocument{}, ErrDocumentUnsupported
}
