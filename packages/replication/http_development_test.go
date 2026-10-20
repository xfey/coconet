package replication

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type developmentTransport func(*http.Request) (*http.Response, error)

func (f developmentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPRelayUsesConfiguredEndpointWithoutEnvironment(t *testing.T) {
	calls := 0
	remote := HTTP{BaseURL: "http://dev.example.test:7788", Token: "test-token", Client: &http.Client{Transport: developmentTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Host != "dev.example.test:7788" {
			t.Fatal("unexpected relay request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}}
	response, err := remote.request(context.Background(), "GET", "project", "status", nil, 0)
	if err != nil || calls != 1 {
		t.Fatal("configured HTTP relay failed", err)
	}
	response.Body.Close()
}
