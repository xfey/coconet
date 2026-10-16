package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
)

type serverError struct {
	HTTPStatus    int
	CorrelationID string
	Envelope      contracts.ErrorEnvelope
}

func (e *serverError) Error() string {
	return e.Envelope.Message
}

func (e *serverError) FailureCategory() contracts.FailureCategory {
	return observability.CategoryForResponse(e.Envelope.Status, e.Envelope.Code, e.HTTPStatus)
}

func (e *serverError) FailureCode() string { return e.Envelope.Code }

func (e *serverError) FailureRetryable() bool { return e.Envelope.Retryable }

func (e *serverError) FailureCorrelationID() string { return e.CorrelationID }

type apiClient struct {
	capabilities []string
	baseURL      string
	token        string
	httpClient   *http.Client
}

func newAPIClient(baseURL, token string) (*apiClient, error) {
	normalized, err := normalizeServerURL(baseURL)
	if err != nil {
		return nil, err
	}
	client, err := coconetHTTPClient(15 * time.Second)
	if err != nil {
		return nil, err
	}
	return &apiClient{
		baseURL: normalized, token: token, httpClient: client,
	}, nil
}

func (c *apiClient) account(ctx context.Context) (contracts.Account, error) {
	var result contracts.Account
	err := c.do(ctx, http.MethodGet, "/v1/account", nil, &result, true)
	return result, err
}

func (c *apiClient) createProject(ctx context.Context, name, requestID string) (contracts.ProjectAccess, error) {
	var result contracts.ProjectAccess
	err := c.do(ctx, http.MethodPost, "/v1/projects", contracts.CreateProjectRequest{
		SchemaVersion: contracts.SchemaVersion,
		RequestID:     requestID,
		Name:          name,
	}, &result, true)
	return result, err
}

func (c *apiClient) projectAccess(ctx context.Context, projectID string) (contracts.ProjectAccess, error) {
	var result contracts.ProjectAccess
	err := c.do(ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(projectID)+"/access", nil, &result, true)
	return result, err
}

func (c *apiClient) do(ctx context.Context, method, path string, input, output any, authenticated bool) error {
	request, err := c.newRequest(ctx, method, path, input, authenticated)
	if err != nil {
		return err
	}
	return c.send(request, output)
}

func (c *apiClient) newRequest(ctx context.Context, method, path string, input any, authenticated bool) (*http.Request, error) {
	var body io.Reader
	if input != nil {
		content, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(content)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Request-ID", newRequestID())
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		if c.token == "" {
			return nil, errors.New("local account credential is missing")
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	return request, nil
}

func (c *apiClient) send(request *http.Request, output any) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("contact Coconet Server: %w", err)
	}
	defer response.Body.Close()
	limit := int64(1 << 20)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		switch output.(type) {
		case *contracts.PrepareSyncResponse, *contracts.CommitSyncResponse, *contracts.PullSessionResponse, *contracts.PrepareForkResponse, *contracts.CompleteForkResponse:
			// Up to 1024 compressed blocks, each with a signed object URL and
			// Manifest metadata, can exceed the ordinary 1 MiB API envelope.
			limit = 8 << 20
		}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, limit))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope contracts.ErrorEnvelope
		if err := decoder.Decode(&envelope); err != nil {
			return fmt.Errorf("Coconet Server returned HTTP %d", response.StatusCode)
		}
		return &serverError{HTTPStatus: response.StatusCode, CorrelationID: response.Header.Get("X-Coconet-Correlation-ID"), Envelope: envelope}
	}
	if output == nil {
		return nil
	}
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode Coconet Server response: %w", err)
	}
	return nil
}
