package projectlink

import (
	"errors"
	"net/url"
	"strings"
)

// ParseServerEndpoint returns an origin followed by an optional path prefix,
// without a trailing slash. Prefixes use unescaped URL path segments so that
// clients, browsers and reverse proxies agree on the deployment's identity.
func ParseServerEndpoint(input string, allowHTTP bool) (string, error) {
	parsed, err := url.Parse(input)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || strings.ContainsAny(input, "?#%\\") || strings.TrimSpace(input) != input {
		return "", errors.New("server endpoint must be an HTTP(S) URL without credentials, query, fragment or escaped path")
	}
	origin, err := ParseServerOrigin(strings.TrimSuffix(input, parsed.Path), allowHTTP)
	if err != nil {
		return "", err
	}
	prefix := strings.TrimSuffix(parsed.Path, "/")
	if prefix == "" {
		return origin, nil
	}
	for _, segment := range strings.Split(strings.TrimPrefix(prefix, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("server path prefix must not contain empty or dot segments")
		}
		for _, c := range segment {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
				return "", errors.New("server path prefix must use letters, digits, hyphens, dots, underscores or tildes")
			}
		}
	}
	return origin + prefix, nil
}
