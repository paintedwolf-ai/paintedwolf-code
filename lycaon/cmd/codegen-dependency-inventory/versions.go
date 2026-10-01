package main

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	versionLikePattern = regexp.MustCompile(`^v?\d+(\.\d+)+([+-][0-9A-Za-z.-]+)?$`)
	digitRuns          = regexp.MustCompile(`\d+`)
	commitPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shortCommitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// versionLike reports whether s is a release number rather than a
// range, commit, or build id.
func versionLike(s string) bool {
	return versionLikePattern.MatchString(s)
}

// compareVersions orders two release strings by their numeric components, so
// four-part versions and build suffixes compare too.
func compareVersions(a, b string) int {
	x, y := digitRuns.FindAllString(a, -1), digitRuns.FindAllString(b, -1)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q uint64
		if i < len(x) {
			p, _ = strconv.ParseUint(x[i], 10, 64)
		}
		if i < len(y) {
			q, _ = strconv.ParseUint(y[i], 10, 64)
		}
		switch {
		case p < q:
			return -1
		case p > q:
			return 1
		}
	}
	return 0
}

// sameVersion treats a leading v and an abbreviated commit as equal.
func sameVersion(a, b string) bool {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	if a == "" || b == "" {
		return false
	}
	return a == b || (shortCommitPattern.MatchString(a) && shortCommitPattern.MatchString(b) &&
		(strings.HasPrefix(a, b) || strings.HasPrefix(b, a)))
}

// display shortens full commit hashes.
func display(s string) string {
	if commitPattern.MatchString(s) {
		return s[:10]
	}
	return s
}

// cargoMatches reports whether version satisfies a Cargo requirement such as
// "2", "^0.13", "~1.2", "=0.27.4", ">=1", or "*". Only the first comparator
// of a compound requirement is considered.
func cargoMatches(req, version string) bool {
	req = strings.TrimSpace(strings.SplitN(req, ",", 2)[0])
	if req == "" || req == "*" {
		return true
	}
	if strings.Contains(version, "-") {
		return false
	}
	v, _ := triple(version)
	switch {
	case strings.HasPrefix(req, ">="):
		r, _ := triple(req[2:])
		return compareTriples(v, r) >= 0
	case strings.HasPrefix(req, "="):
		r, parts := triple(req[1:])
		return v[0] == r[0] && (parts < 2 || v[1] == r[1]) && (parts < 3 || v[2] == r[2])
	case strings.HasPrefix(req, "~"):
		r, parts := triple(req[1:])
		return compareTriples(v, r) >= 0 && v[0] == r[0] && (parts < 2 || v[1] == r[1])
	}
	r, parts := triple(strings.TrimPrefix(req, "^"))
	if compareTriples(v, r) < 0 {
		return false
	}
	switch {
	case r[0] > 0 || parts == 1:
		return v[0] == r[0]
	case r[1] > 0 || parts == 2:
		return v[0] == 0 && v[1] == r[1]
	default:
		return v == r
	}
}

func triple(s string) ([3]int, int) {
	var out [3]int
	fields := strings.Split(strings.TrimSpace(s), ".")
	parts := 0
	for i := 0; i < len(fields) && i < 3; i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			break
		}
		out[i] = n
		parts++
	}
	return out, parts
}

func compareTriples(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
