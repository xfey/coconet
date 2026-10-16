package replication

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func TestDirectTransferDoesNotForwardCredentialsOrFollowRedirect(t *testing.T) {
	var redirected bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer other.Close()
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("Relay credentials leaked to storage")
		}
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer store.Close()
	client := store.Client()
	client.Jar, _ = cookiejar.New(nil)
	u, _ := url.Parse(store.URL)
	client.Jar.SetCookies(u, []*http.Cookie{{Name: "private", Value: "fixture-cookie"}})
	transport := HTTP{Token: "fixture-relay-token", Client: client}
	_, err := transport.transfer(context.Background(), "GET", contracts.RelayTransferTarget{URL: store.URL + "?signature=private-fixture", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}, nil)
	if err == nil || redirected || strings.Contains(err.Error(), "signature") || strings.Contains(err.Error(), "private-fixture") {
		t.Fatal("redirect or capability escaped transfer boundary", err)
	}
}
