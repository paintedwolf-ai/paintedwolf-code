package webresearch

import (
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/textrank"
)

const shortenProviderQueryMaxTerms = 6

// shortenProviderQuery shortens a long natural-language search string into the
// analyzed primary terms vertical indexes (man pages, RFC corpora) rank well
// against. Short queries pass through unchanged. Year tokens are kept when
// present so versioned docs still match.
func shortenProviderQuery(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return q
	}
	// Already short enough that a vertical index can use it as-is.
	if len(strings.Fields(q)) <= shortenProviderQueryMaxTerms {
		return q
	}
	terms := textrank.AnalyzeQuery(q, false, true)
	if len(terms) == 0 {
		return q
	}
	out := make([]string, 0, shortenProviderQueryMaxTerms)
	for _, t := range terms {
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(out) >= shortenProviderQueryMaxTerms {
			break
		}
	}
	if len(out) == 0 {
		return q
	}
	return strings.Join(out, " ")
}

// looksLikeRFCName reports whether query names an RFC document id (rfc9110 /
// "RFC 9110"). Document `name` filters on datatracker expect this shape; NL
// title/abstract filters do not.
func looksLikeRFCName(query string) bool {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return false
	}
	q = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, q)
	if !strings.HasPrefix(q, "rfc") {
		return false
	}
	num := strings.TrimPrefix(q, "rfc")
	if num == "" {
		return false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
