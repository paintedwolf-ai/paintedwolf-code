package webresearch

import (
	"regexp"
	"strings"
)

// queryOps is a query with its search operators parsed out. Operators are
// host-side contracts; the seed LLM never sees them, since it invents paths
// from them.
type queryOps struct {
	// cleaned is the query with site: tokens removed; quoted phrases stay in
	// place so the scorer can extract them.
	cleaned string
	// siteBases are normalized scheme+host bases from site: tokens. When
	// non-empty the seed LLM is skipped entirely and the crawl is pinned here.
	siteBases []string
}

var siteOpRe = regexp.MustCompile(`(?i)(^|\s)site:(\S+)`)

// parseQueryOperators extracts site: tokens from a query. Tokens that don't
// normalize to a fetchable base stay in the query as plain text.
func parseQueryOperators(query string) queryOps {
	ops := queryOps{}
	seen := make(map[string]struct{})
	cleaned := siteOpRe.ReplaceAllStringFunc(query, func(m string) string {
		sub := siteOpRe.FindStringSubmatch(m)
		token := strings.Trim(sub[2], ".,;:!?\"'")
		base := normalizeSiteBase(token)
		if base == "" || !strings.Contains(urlHost(base), ".") {
			return m
		}
		key := strings.ToLower(base)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			ops.siteBases = append(ops.siteBases, base)
		}
		return sub[1]
	})
	ops.cleaned = strings.Join(strings.Fields(cleaned), " ")
	if ops.cleaned == "" {
		ops.cleaned = strings.TrimSpace(query)
	}
	return ops
}

var quotedPhraseRe = regexp.MustCompile(`"([^"]{2,120})"`)

// quotedPhrases extracts explicit multi-word phrases from a query. The scorer
// already builds adjacent-token bigrams; quotes let the caller demand longer
// exact spans.
func quotedPhrases(query string) []string {
	var out []string
	for _, m := range quotedPhraseRe.FindAllStringSubmatch(query, -1) {
		phrase := strings.Join(strings.Fields(strings.ToLower(m[1])), " ")
		if strings.Contains(phrase, " ") {
			out = append(out, phrase)
		}
	}
	return out
}
