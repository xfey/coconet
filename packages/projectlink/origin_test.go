package projectlink

import "testing"

func TestParseServerOrigin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input    string
		allow    bool
		expected string
	}{
		{"https://API.COCONET.SPACE/", false, HostedEndpoint},
		{"https://example.test:443", false, "https://example.test"},
		{"https://192.0.2.1:8443", false, "https://192.0.2.1:8443"},
		{"https://[2001:db8::1]:8443", false, "https://[2001:db8::1]:8443"},
		{"http://127.0.0.1:8787", false, "http://127.0.0.1:8787"},
		{"http://dev.example.test:8787", true, "http://dev.example.test:8787"},
	} {
		actual, err := ParseServerOrigin(test.input, test.allow)
		if err != nil || actual != test.expected {
			t.Fatalf("ParseServerOrigin(%q) = %q, %v; want %q", test.input, actual, err, test.expected)
		}
	}
}

func TestParseServerOriginRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"", " example.test", "example.test", "HTTPS://example.test", "ftp://example.test",
		"https://user@example.test", "https://example.test/path", "https://example.test?q=1",
		"https://example.test#fragment", "https://bad_host.test", "https://example.test.",
		"http://example.test:8787", "https://[fe80::1%25en0]",
	} {
		if _, err := ParseServerOrigin(value, false); err == nil {
			t.Fatalf("ParseServerOrigin accepted %q", value)
		}
	}
}
