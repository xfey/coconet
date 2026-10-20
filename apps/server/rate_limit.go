package main

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

const maxRateLimitKeys = 4096

type rateWindow struct {
	expires time.Time
	count   int
}

type rateLimitKey struct {
	scope string
	key   string
}

type requestLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[rateLimitKey]rateWindow
}

type clientAddressResolver struct {
	trustedProxies []*net.IPNet
}

func newRequestLimiter() *requestLimiter {
	return &requestLimiter{now: time.Now, windows: make(map[rateLimitKey]rateWindow)}
}

func (limiter *requestLimiter) allow(scope, key string, limit int, window time.Duration) bool {
	return limiter.reserve(scope, key, limit, window) == 0
}

func (limiter *requestLimiter) reserve(scope, key string, limit int, window time.Duration) time.Duration {
	if limiter == nil || limit <= 0 || window <= 0 {
		return 0
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now().UTC()
	for existingKey, existing := range limiter.windows {
		if !now.Before(existing.expires) {
			delete(limiter.windows, existingKey)
		}
	}
	windowKey := rateLimitKey{scope: scope, key: key}
	current, exists := limiter.windows[windowKey]
	if !exists {
		if len(limiter.windows) >= maxRateLimitKeys {
			wait := window
			for _, existing := range limiter.windows {
				if remaining := existing.expires.Sub(now); remaining < wait {
					wait = remaining
				}
			}
			return wait
		}
		limiter.windows[windowKey] = rateWindow{expires: now.Add(window), count: 1}
		return 0
	}
	if current.count >= limit {
		return current.expires.Sub(now)
	}
	current.count++
	limiter.windows[windowKey] = current
	return 0
}

func clientAddress(request *http.Request) string {
	return (clientAddressResolver{}).resolve(request)
}

func parseTrustedProxyCIDRs(value string) (clientAddressResolver, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return clientAddressResolver{}, nil
	}
	parts := strings.Split(value, ",")
	result := clientAddressResolver{trustedProxies: make([]*net.IPNet, 0, len(parts))}
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		_, network, err := net.ParseCIDR(candidate)
		if err != nil || candidate == "" {
			return clientAddressResolver{}, errors.New("COCONET_TRUSTED_PROXY_CIDRS must contain comma-separated IP CIDRs")
		}
		result.trustedProxies = append(result.trustedProxies, network)
	}
	return result, nil
}

func (resolver clientAddressResolver) resolve(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return "unknown"
	}
	if !resolver.trusted(peer) {
		return peer.String()
	}
	forwarded := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
	chain := make([]net.IP, 0, len(forwarded)+1)
	for _, value := range forwarded {
		candidate := net.ParseIP(strings.TrimSpace(value))
		if candidate == nil {
			return peer.String()
		}
		chain = append(chain, candidate)
	}
	chain = append(chain, peer)
	for index := len(chain) - 1; index >= 0; index-- {
		if !resolver.trusted(chain[index]) {
			return chain[index].String()
		}
	}
	return peer.String()
}

func (resolver clientAddressResolver) trusted(address net.IP) bool {
	for _, network := range resolver.trustedProxies {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

func rateLimitError() *authorityError {
	return &authorityError{
		HTTPStatus: http.StatusTooManyRequests, Status: contracts.StatusUnavailable,
		Code: "rate_limit_exceeded", Message: "request rate limit was reached", Retryable: true,
	}
}
