package distribution

import (
	"regexp"
	"strings"
)

var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// CompareVersions compares complete semantic versions, ignoring build metadata.
// Unknown development versions never trigger an automatic process replacement.
func CompareVersions(left, right string) (int, bool) {
	a, b := releaseVersion.FindStringSubmatch(left), releaseVersion.FindStringSubmatch(right)
	if a == nil || b == nil {
		return 0, false
	}
	for _, v := range []string{a[4], b[4]} {
		for _, id := range strings.Split(v, ".") {
			if numericVersionID(id) && len(id) > 1 && id[0] == '0' {
				return 0, false
			}
		}
	}
	for i := 1; i <= 3; i++ {
		if c := compareNumericVersionID(a[i], b[i]); c != 0 {
			return c, true
		}
	}
	if a[4] == b[4] {
		return 0, true
	}
	if a[4] == "" {
		return 1, true
	}
	if b[4] == "" {
		return -1, true
	}
	x, y := strings.Split(a[4], "."), strings.Split(b[4], ".")
	for i := 0; i < min(len(x), len(y)); i++ {
		if x[i] == y[i] {
			continue
		}
		n, m := numericVersionID(x[i]), numericVersionID(y[i])
		if n && m {
			return compareNumericVersionID(x[i], y[i]), true
		}
		if n {
			return -1, true
		}
		if m {
			return 1, true
		}
		return strings.Compare(x[i], y[i]), true
	}
	if len(x) < len(y) {
		return -1, true
	}
	return 1, true
}
func numericVersionID(v string) bool {
	if v == "" {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func compareNumericVersionID(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
