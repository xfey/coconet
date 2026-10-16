package replication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func (t HTTP) ContentPolicy(ctx context.Context, project string) (contracts.RelayContentPolicy, error) {
	var result contracts.RelayContentPolicy
	err := t.json(ctx, "GET", project, "content-policy", nil, &result)
	if err == nil && result.SchemaVersion != 1 {
		err = errors.New("unsupported content policy")
	}
	return result, err
}

func (t HTTP) transfer(ctx context.Context, method string, target contracts.RelayTransferTarget, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(target.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid object transfer URL")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("object transfer requires HTTPS")
	}
	expires, err := time.Parse(time.RFC3339Nano, target.ExpiresAt)
	if err != nil || !expires.After(time.Now()) {
		return nil, errors.New("object transfer authorization expired; retry")
	}
	request, err := http.NewRequestWithContext(ctx, method, target.URL, body)
	if err != nil {
		return nil, errors.New("invalid object transfer request")
	}
	if body != nil {
		request.ContentLength = target.Size
		if target.Size == 0 {
			request.Body = http.NoBody
		}
		request.Header.Set("Content-Type", "application/octet-stream")
	}
	// Never attach Relay authorization or a cookie jar to an object-store URL.
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	copy := *client
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copy.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("object transfer failed; retry after checking connectivity")
	}
	if response.StatusCode != 200 && response.StatusCode != 204 {
		response.Body.Close()
		if response.StatusCode == 404 && method == http.MethodGet {
			return nil, ErrObjectMissing
		}
		return nil, fmt.Errorf("object transfer failed (HTTP %d)", response.StatusCode)
	}
	return response, nil
}

func (t HTTP) directPut(ctx context.Context, project, digest string, size int64, body io.Reader) (bool, error) {
	input := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: digest, Size: size}
	var target contracts.RelayTransferTarget
	if err := t.json(ctx, "POST", project, "transfers/upload", input, &target); err != nil {
		return true, err
	}
	if target.SchemaVersion != 1 || target.SHA256 != digest || target.Size != size {
		return true, errors.New("object upload target differs")
	}
	if target.Proxy {
		return false, nil
	}
	if target.Ready {
		return true, nil
	}
	if target.UploadID == "" {
		return true, errors.New("object upload reservation missing")
	}
	response, err := t.transfer(ctx, http.MethodPut, target, body)
	if err != nil {
		return true, err
	}
	_, drainErr := io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if drainErr != nil || closeErr != nil {
		return true, errors.New("object upload response incomplete")
	}
	input.UploadID = target.UploadID
	if err := t.json(ctx, "POST", project, "transfers/complete", input, &target); err != nil {
		return true, err
	}
	if target.SchemaVersion != 1 || target.SHA256 != digest || target.Size != size || !target.Ready {
		return true, errors.New("object upload was not finalized")
	}
	return true, nil
}

func (t HTTP) directGet(ctx context.Context, project, digest string) (io.ReadCloser, bool, error) {
	var target contracts.RelayTransferTarget
	input := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: digest}
	if err := t.json(ctx, "POST", project, "transfers/download", input, &target); err != nil {
		return nil, true, err
	}
	if target.SchemaVersion != 1 || target.SHA256 != digest || target.Size < 0 || target.Size > 256<<20 {
		return nil, true, errors.New("object download target differs")
	}
	if target.Proxy {
		return nil, false, nil
	}
	response, err := t.transfer(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, true, err
	}
	if response.ContentLength >= 0 && response.ContentLength != target.Size {
		response.Body.Close()
		return nil, true, errors.New("object download length differs")
	}
	return &boundedObjectReader{Reader: io.LimitReader(response.Body, target.Size+1), Closer: response.Body}, true, nil
}

type boundedObjectReader struct {
	io.Reader
	io.Closer
}
