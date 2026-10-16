package replication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

type HTTP struct {
	BaseURL string
	Token   string
	Client  *http.Client
	// Credentials are resolved for each operation so logout and credential
	// refresh also affect already-running devices. Never put them in payloads.
	Credential func(context.Context, string) (string, error)
}

func (t HTTP) request(ctx context.Context, method, project, suffix string, body io.Reader, size int64) (*http.Response, error) {
	token := t.Token
	if t.Credential != nil {
		var err error
		token, err = t.Credential(ctx, project)
		if err != nil {
			return nil, err
		}
	}
	base, err := url.Parse(t.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || token == "" {
		return nil, errors.New("invalid relay endpoint or credential")
	}
	ip := net.ParseIP(base.Hostname())
	local := base.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if base.Scheme != "https" && !(base.Scheme == "http" && local) {
		return nil, errors.New("relay requires HTTPS except for loopback development")
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/v1/projects/" + url.PathEscape(project) + "/relay/" + suffix
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.ContentLength = size
		request.Header.Set("Content-Type", "application/json")
		if method == http.MethodPut {
			request.Header.Set("Content-Type", "application/octet-stream")
		}
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copy.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == 200 {
		return response, nil
	}
	defer response.Body.Close()
	var failure contracts.ErrorEnvelope
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&failure)
	if response.StatusCode == 410 && failure.Code == "relay_history_compacted" {
		return nil, contracts.ErrRelayHistoryCompacted
	}
	if response.StatusCode == 404 && failure.Code == "relay_receipt_missing" {
		return nil, contracts.ErrRelayReceiptMissing
	}
	if response.StatusCode == 409 {
		switch failure.Code {
		case "processing_busy":
			return nil, contracts.ErrProcessingBusy
		case "processing_stale":
			return nil, contracts.ErrProcessingStale
		case "processing_yielded":
			return nil, contracts.ErrProcessingYielded
		case "processing_paused":
			return nil, contracts.ErrProcessingPaused
		}
	}
	if response.StatusCode == 409 && failure.Code == "relay_revision_conflict" {
		return nil, ErrConflict
	}
	if response.StatusCode == 409 && failure.Code == "relay_content_busy" {
		return nil, ErrContentBusy
	}
	if response.StatusCode == 409 && failure.Code == "relay_collection_grace" {
		return nil, ErrCollectionGrace
	}
	if response.StatusCode == 404 && failure.Code == "relay_object_missing" {
		return nil, ErrObjectMissing
	}
	return nil, fmt.Errorf("relay request failed (HTTP %d, code %s)", response.StatusCode, failure.Code)
}
func (t HTTP) json(ctx context.Context, method, project, suffix string, input, output any) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	response, err := t.request(ctx, method, project, suffix, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limit := int64(6 << 20)
	if suffix == "checkpoint" {
		limit = 24 << 20
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("relay returned trailing JSON")
	}
	return nil
}
func (t HTTP) Append(ctx context.Context, project string, input contracts.RelayCommit) (contracts.RelayReceipt, error) {
	var out contracts.RelayReceipt
	err := t.json(ctx, "POST", project, "commits", input, &out)
	return out, err
}
func (t HTTP) Since(ctx context.Context, project string, after int64) (contracts.RelayPage, error) {
	var out contracts.RelayPage
	err := t.json(ctx, "GET", project, "commits?after="+strconv.FormatInt(after, 10)+"&limit=100", nil, &out)
	return out, err
}
func (t HTTP) Missing(ctx context.Context, project string, input contracts.RelayObjectCheck) (contracts.RelayMissingObjects, error) {
	var out contracts.RelayMissingObjects
	err := t.json(ctx, "POST", project, "objects/check", input, &out)
	return out, err
}
func (t HTTP) Put(ctx context.Context, project, digest string, size int64, body io.Reader) error {
	if handled, err := t.directPut(ctx, project, digest, size, body); handled {
		return err
	}
	response, err := t.request(ctx, "PUT", project, "objects/"+url.PathEscape(digest), body, size)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return err
}
func (t HTTP) Get(ctx context.Context, project, digest string) (io.ReadCloser, error) {
	if body, handled, err := t.directGet(ctx, project, digest); handled {
		return body, err
	}
	response, err := t.request(ctx, "GET", project, "objects/"+url.PathEscape(digest), nil, 0)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}
