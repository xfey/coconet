package main

import (
	"errors"
	"net/http"
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
	origin, err := projectlink.ParseServerOrigin(value, false)
	if err != nil || !strings.HasPrefix(origin, "https://") {
		return "", errors.New("COCONET_WEB_ORIGIN must be an HTTPS origin without a path, query or fragment")
	}
	return origin, nil
}

func (h *serverHandler) browserOrigin() string {
	if h.webOrigin != "" {
		return h.webOrigin
	}
	return h.advertisedEndpoint
}

func (h *serverHandler) browserHost(r *http.Request) bool {
	return h.webOrigin == "" || "https://"+r.Host == h.webOrigin
}
