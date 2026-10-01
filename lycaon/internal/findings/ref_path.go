package findings

import (
	"path/filepath"
	"strconv"
	"strings"
)

// RefNamesPath reports whether a finding ref's structured location tokens
// include path. It compares normalized paths only — never summary prose.
func RefNamesPath(ref, path string) bool {
	want := normalizeRefPath(path)
	if want == "" {
		return false
	}
	for _, token := range splitRefTokens(strings.TrimSpace(ref)) {
		refPath, _, ok := splitRefPathLines(token)
		if !ok {
			refPath = token
		}
		if normalizeRefPath(refPath) == want {
			return true
		}
	}
	return false
}

// splitRefTokens separates a ref into individual path[:lines] tokens. Multiple
// locations are comma+space separated; a single location may carry a comma-
// separated line list after the colon (no space), e.g. "AGENTS.md:1,81".
func splitRefTokens(ref string) []string {
	if !strings.Contains(ref, ", ") {
		return []string{ref}
	}
	parts := strings.Split(ref, ", ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitRefPathLines splits "path:lines" where lines is a comma-separated list of
// positive integers (e.g. "AGENTS.md:1,81" -> "AGENTS.md", [1,81]). Returns ok
// false when there is no colon or the tail is not a digit/comma list.
func splitRefPathLines(token string) (path string, lines []int, ok bool) {
	i := strings.LastIndex(token, ":")
	if i <= 0 {
		return "", nil, false
	}
	tail := token[i+1:]
	if tail == "" {
		return "", nil, false
	}
	for _, c := range tail {
		if (c < '0' || c > '9') && c != ',' {
			return "", nil, false
		}
	}
	parts := strings.Split(tail, ",")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p == "" {
			continue // tolerate trailing or double commas in a line list
		}
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return "", nil, false
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		return "", nil, false
	}
	return token[:i], nums, true
}

func normalizeRefPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(p, "./")
	p = filepath.ToSlash(p)
	p = filepath.Clean(p)
	return p
}
