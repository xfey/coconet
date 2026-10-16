package main

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRequestLimiterUsesFixedBoundedWindows(t *testing.T) {
	limiter := newRequestLimiter()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	if !limiter.allow("bootstrap", "192.0.2.1", 2, time.Minute) || !limiter.allow("bootstrap", "192.0.2.1", 2, time.Minute) || limiter.allow("bootstrap", "192.0.2.1", 2, time.Minute) {
		t.Fatal("fixed window limit was not enforced")
	}
	if !limiter.allow("bootstrap", "192.0.2.2", 2, time.Minute) {
		t.Fatal("independent key shared a window")
	}
	if !limiter.allow("bootstrap:192.0.2", "2", 1, time.Hour) {
		t.Fatal("scope and key tuple collided with a concatenated value")
	}
	if !limiter.allow("hourly", "192.0.2.1", 1, time.Hour) || limiter.allow("hourly", "192.0.2.1", 1, time.Hour) {
		t.Fatal("hourly window limit was not enforced")
	}
	now = now.Add(time.Minute)
	if !limiter.allow("bootstrap", "192.0.2.1", 2, time.Minute) {
		t.Fatal("expired window was not reset")
	}
	if limiter.allow("hourly", "192.0.2.1", 1, time.Hour) {
		t.Fatal("a shorter window expired an unrelated hourly window")
	}
}

func TestClientAddressIgnoresUntrustedForwardingHeaders(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.20")
	if actual := clientAddress(request); actual != "192.0.2.10" {
		t.Fatalf("clientAddress = %q", actual)
	}
}

func TestClientAddressUsesOnlyConfiguredTrustedProxyChain(t *testing.T) {
	resolver, err := parseTrustedProxyCIDRs("127.0.0.1/32, 10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.20, 10.0.0.8")
	if actual := resolver.resolve(request); actual != "203.0.113.20" {
		t.Fatalf("trusted proxy client address = %q", actual)
	}

	request.RemoteAddr = "192.0.2.10:1234"
	if actual := resolver.resolve(request); actual != "192.0.2.10" {
		t.Fatalf("untrusted peer controlled the forwarding chain: %q", actual)
	}
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("X-Forwarded-For", "not-an-ip")
	if actual := resolver.resolve(request); actual != "127.0.0.1" {
		t.Fatalf("malformed forwarding chain was trusted: %q", actual)
	}
	if _, err := parseTrustedProxyCIDRs("127.0.0.1"); err == nil {
		t.Fatal("trusted proxy configuration accepted a bare IP")
	}
}

func TestRequestLimiterBoundsTrackedKeys(t *testing.T) {
	limiter := newRequestLimiter()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for index := 0; index < maxRateLimitKeys; index++ {
		if !limiter.allow("bounded", strconv.Itoa(index), 1, time.Minute) {
			t.Fatalf("key %d was rejected before the bound", index)
		}
	}
	if limiter.allow("bounded", "overflow", 1, time.Minute) {
		t.Fatal("limiter tracked more keys than its memory bound")
	}
	now = now.Add(time.Minute)
	if !limiter.allow("bounded", "after-expiry", 1, time.Minute) {
		t.Fatal("expired keys did not release limiter capacity")
	}
}
