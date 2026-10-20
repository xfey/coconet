package projectlink

import "testing"

func TestParseServerEndpoint(t *testing.T) {
	for _, pair := range [][2]string{
		{"https://EXAMPLE.test:443/", "https://example.test"},
		{"https://EXAMPLE.test/teams/demo/", "https://example.test/teams/demo"},
		{"http://localhost:7788/demo-1_2.3~", "http://localhost:7788/demo-1_2.3~"},
		{"https://[2001:db8::1]:9443/team", "https://[2001:db8::1]:9443/team"},
	} {
		got, err := ParseServerEndpoint(pair[0], false)
		if err != nil || got != pair[1] {
			t.Fatalf("%q: %q, %v", pair[0], got, err)
		}
	}
	for _, input := range []string{"", "https://user:pw@example.test/a", "https://example.test/a?", "https://example.test/a#", "https://example.test//", "https://example.test/a//b", "https://example.test/a/./b", "https://example.test/a/../b", "https://example.test/%2e%2e", "https://example.test/a%2Fb", "https://example.test/a\\b", "https://example.test/a b", "https://example.test/中文", " https://example.test", "http://example.test/a", "HTTPS://example.test/a"} {
		if got, err := ParseServerEndpoint(input, false); err == nil {
			t.Errorf("accepted %q as %q", input, got)
		}
	}
}
