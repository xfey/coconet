package distribution

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		want  int
		valid bool
	}{
		{"0.30.9", "0.31.0", -1, true}, {"0.31.0", "0.30.9", 1, true}, {"1.0.0", "1.0.0+build", 0, true},
		{"1.0.0-alpha.9", "1.0.0-alpha.10", -1, true}, {"1.0.0-alpha", "1.0.0", -1, true}, {"1.0.0-a", "1.0.0-a.1", -1, true},
		{"1.0.0-999999999999999999999999", "1.0.0-1000000000000000000000000", -1, true},
		{"dev", "1.0.0", 0, false}, {"1.0.0-a..b", "1.0.0", 0, false}, {"1.0.0-01", "1.0.0", 0, false}, {"1.01.0", "1.0.0", 0, false},
	} {
		got, ok := CompareVersions(c.a, c.b)
		if got != c.want || ok != c.valid {
			t.Errorf("%s vs %s: %d,%v", c.a, c.b, got, ok)
		}
	}
}
