package projectlink

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const HostedEndpoint = "https://api.coconet.space"
const MaxDNSHostLength = 96

type parsedOrigin struct {
	scheme string
	host   string
	port   uint16
	ip     net.IP
}

// ParseServerOrigin returns a canonical origin. Plaintext is limited to
// loopback unless allowHTTP is explicitly true.
func ParseServerOrigin(input string, allowHTTP bool) (string, error) {
	origin, err := parseOrigin(input, allowHTTP)
	if err != nil {
		return "", err
	}
	return origin.string(), nil
}

func parseOrigin(input string, allowHTTP bool) (parsedOrigin, error) {
	if strings.TrimSpace(input) != input || input == "" {
		return parsedOrigin{}, errors.New("server endpoint must be a non-empty origin")
	}
	if !strings.HasPrefix(input, "https://") && !strings.HasPrefix(input, "http://") {
		return parsedOrigin{}, errors.New("server endpoint scheme must be lowercase HTTP or HTTPS")
	}
	parsed, err := url.Parse(input)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != "" && parsed.Path != "/" {
		return parsedOrigin{}, errors.New("server endpoint must be an origin without credentials, path, query, or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" || parsed.Scheme != scheme {
		return parsedOrigin{}, errors.New("server endpoint scheme must be lowercase HTTP or HTTPS")
	}
	host := parsed.Hostname()
	if host == "" || strings.Contains(host, "%") {
		return parsedOrigin{}, errors.New("server endpoint host is invalid")
	}
	port := defaultPort(scheme)
	if value := parsed.Port(); value != "" {
		parsedPort, parseErr := strconv.ParseUint(value, 10, 16)
		if parseErr != nil || parsedPort == 0 {
			return parsedOrigin{}, errors.New("server endpoint port is invalid")
		}
		port = uint16(parsedPort)
	}
	var ip net.IP
	if candidate := net.ParseIP(host); candidate != nil {
		ip = candidate
		host = candidate.String()
	} else {
		host = strings.ToLower(host)
		if err := validateDNSName(host); err != nil {
			return parsedOrigin{}, err
		}
	}
	loopback := host == "localhost" || ip != nil && ip.IsLoopback()
	if scheme == "http" && !loopback && !allowHTTP {
		return parsedOrigin{}, errors.New("this endpoint requires HTTPS")
	}
	return parsedOrigin{scheme: scheme, host: host, port: port, ip: ip}, nil
}

func (origin parsedOrigin) string() string {
	host := origin.host
	if origin.ip != nil && origin.ip.To4() == nil {
		host = "[" + host + "]"
	}
	if origin.port != defaultPort(origin.scheme) {
		host = net.JoinHostPort(origin.host, strconv.Itoa(int(origin.port)))
	}
	return origin.scheme + "://" + host
}

func defaultPort(scheme string) uint16 {
	if scheme == "http" {
		return 80
	}
	return 443
}

func validateDNSName(host string) error {
	if len(host) == 0 || len(host) > MaxDNSHostLength || host != strings.ToLower(host) || strings.HasSuffix(host, ".") {
		return errors.New("DNS endpoint host is not canonical")
	}
	for _, character := range []byte(host) {
		if character > 0x7f {
			return errors.New("DNS endpoint host must be ASCII or Punycode")
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("DNS endpoint label is invalid")
		}
		for _, character := range []byte(label) {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return errors.New("DNS endpoint label contains invalid character")
			}
		}
	}
	return nil
}
