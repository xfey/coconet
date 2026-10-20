package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/xfey/coconet/packages/projectlink"
)

// Browser navigation can use a separate origin without changing the Deployment
// identity or the API endpoint embedded in existing invitations and bindings.
func webOriginFromEnvironment(apiOrigin string) (string, error) {
	value := strings.TrimSpace(os.Getenv("COCONET_WEB_ORIGIN"))
	if value == "" {
		return apiOrigin, nil
	}
	origin, err := projectlink.ParseServerEndpoint(value, false)
	if err != nil || !strings.HasPrefix(origin, "https://") {
		return "", errors.New("COCONET_WEB_ORIGIN must be an HTTPS endpoint without credentials, query or fragment")
	}
	return origin, nil
}

func (h *serverHandler) browserEndpoint() string {
	if h.webOrigin != "" {
		return h.webOrigin
	}
	return h.advertisedEndpoint
}

func (h *serverHandler) browserHost(r *http.Request) bool {
	return h.webOrigin == "" || h.requestOrigin(r) == h.browserOrigin()
}

// HTTP login is key-only and pinned to the explicitly configured origin. Request
// headers cannot downgrade an HTTPS deployment or select the HTTP cookie jar.
func (h *serverHandler) dashboardHTTPAllowed(r *http.Request) bool {
	return h.auth.Kind == "key" &&
		!h.dashboardHTTPS(r) && strings.HasPrefix(h.browserOrigin(), "http://") &&
		h.requestOrigin(r) == h.browserOrigin()
}

// Origins are used for CSRF checks; endpoints also include the routing prefix.
func (h *serverHandler) browserOrigin() string {
	u, _ := url.Parse(h.browserEndpoint())
	return u.Scheme + "://" + u.Host
}

func endpointPath(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Path, "/")
}

func (h *serverHandler) browserPath() string { return endpointPath(h.browserEndpoint()) }

func (h *serverHandler) trustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && h.addressResolver.trusted(net.ParseIP(host))
}

func (h *serverHandler) requestOrigin(r *http.Request) string {
	scheme, host := "http", r.Host
	if h.dashboardHTTPS(r) {
		scheme = "https"
	}
	if h.trustedProxy(r) {
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			host = forwarded
		}
	}
	return scheme + "://" + host
}

// Accept a preserved prefix as well as a path already stripped by a gateway.
// Only configured prefixes are removed, once, at a complete segment boundary.
func (h *serverHandler) routeRequest(r *http.Request) *http.Request {
	prefixes := []string{endpointPath(h.advertisedEndpoint), h.browserPath()}
	if len(prefixes[1]) > len(prefixes[0]) {
		prefixes[0], prefixes[1] = prefixes[1], prefixes[0]
	}
	for _, prefix := range prefixes {
		if prefix == "" || r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
			continue
		}
		result := r.Clone(r.Context())
		result.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		if result.URL.Path == "" {
			result.URL.Path = "/"
		}
		result.URL.RawPath = ""
		return result
	}
	return r
}

func (h *serverHandler) cookiePath() string { return h.browserPath() + "/" }

func (h *serverHandler) cookieName(base string, secure bool) string {
	if h.browserPath() == "" {
		return base
	}
	digest := sha256.Sum256([]byte(h.browserPath()))
	name := strings.TrimPrefix(base, "__Host-")
	if secure {
		name = "__Secure-" + name
	}
	return fmt.Sprintf("%s_%x", name, digest[:8])
}
