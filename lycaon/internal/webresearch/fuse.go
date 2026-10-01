package webresearch

import (
	"net/url"
	"sort"
	"strings"
)

// rrfK is the standard Reciprocal Rank Fusion damping constant; higher
// values flatten the score gap between adjacent ranks.
const rrfK = 60

// fuseHits merges provider result lists with Reciprocal Rank Fusion:
// each URL scores sum(1/(rrfK+rank)) across the providers that returned
// it, so cross-provider agreement boosts a hit instead of being dropped
// as a duplicate. Equal scores keep provider registration order.
func fuseHits(outcomes []providerOutcome, maxResults int) []WebHit {
	type fusedHit struct {
		hit   WebHit
		score float64
		order int
	}
	byURL := make(map[string]*fusedHit)
	order := 0
	for _, outcome := range outcomes {
		if !outcome.ok {
			continue
		}
		for rank, hit := range outcome.hits {
			key := canonicalURL(hit.URL)
			entry, ok := byURL[key]
			if !ok {
				entry = &fusedHit{hit: hit, order: order}
				order++
				byURL[key] = entry
			}
			entry.score += 1 / float64(rrfK+rank+1)
		}
	}
	fused := make([]*fusedHit, 0, len(byURL))
	for _, entry := range byURL {
		fused = append(fused, entry)
	}
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].score != fused[j].score {
			return fused[i].score > fused[j].score
		}
		return fused[i].order < fused[j].order
	})
	if len(fused) > maxResults {
		fused = fused[:maxResults]
	}
	merged := make([]WebHit, 0, len(fused))
	for _, entry := range fused {
		merged = append(merged, entry.hit)
	}
	return merged
}

// canonicalURL is the dedup key shared by fusion, the frontier, seen-URL memory,
// and anchor harvesting: scheme and www collapse; default ports, trailing
// slashes, fragments, and tracking parameters drop. Unparseable input is used
// verbatim.
func canonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimSuffix(host, ":80")
	host = strings.TrimSuffix(host, ":443")
	query := u.Query()
	for param := range query {
		lower := strings.ToLower(param)
		if strings.HasPrefix(lower, "utm_") || lower == "gclid" || lower == "fbclid" {
			delete(query, param)
		}
	}
	key := "https://" + host + strings.TrimSuffix(u.Path, "/")
	if encoded := query.Encode(); encoded != "" {
		key += "?" + encoded
	}
	return key
}
