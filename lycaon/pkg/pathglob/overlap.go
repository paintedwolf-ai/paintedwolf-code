package pathglob

import (
	"path/filepath"
	"strings"
)

// Overlap reports whether two scope patterns can name a common path. Covers is
// one-directional — `internal/**` covers `internal/db`, not the reverse — so both
// directions are checked. Root-shaped patterns (`.`, `**`, empty) overlap everything.
func Overlap(a, b string) bool {
	na, ra := normalizePattern(a), rootShaped(a)
	nb, rb := normalizePattern(b), rootShaped(b)
	if ra || rb {
		return true
	}
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	return Covers(na, nb) || Covers(nb, na) || globPrefixOverlap(na, nb)
}

func rootShaped(value string) bool {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	switch value {
	case "", ".", "**", "*", "**/*":
		return true
	}
	return false
}

// globPrefixOverlap catches two globs that share a literal prefix, where neither
// Covers the other because both carry wildcards — `internal/**/*.go` against
// `internal/db/**` both live under `internal/`.
func globPrefixOverlap(a, b string) bool {
	if !strings.ContainsAny(a, "*?[") || !strings.ContainsAny(b, "*?[") {
		return false
	}
	return Covers(literalPrefix(a), literalPrefix(b)) || Covers(literalPrefix(b), literalPrefix(a))
}

// literalPrefix returns the leading wildcard-free segments of a pattern.
func literalPrefix(pattern string) string {
	segments := strings.Split(normalizePattern(pattern), "/")
	var out []string
	for _, seg := range segments {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}
