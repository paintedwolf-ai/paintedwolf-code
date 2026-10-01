package webresearch

import (
	"strconv"
	"time"
)

const (
	providerSeedCacheTTL = 3 * time.Minute
	providerSeedCacheMax = 256
)

// providerSeedCache reuses recent provider Search outcomes for identical
// seed probes. Concurrent coordinator searches often re-ask near-duplicate
// queries; caching cuts paced contention and duplicate RTT without changing
// who initiates the search.
var providerSeedCache = newTTLCache[providerOutcome](providerSeedCacheTTL, providerSeedCacheMax)

func providerSeedCacheKey(providerID, query string, maxResults int, settings Settings) string {
	return providerID + "\x00" + normalizeQueryKey(query) + "\x00" + strconv.Itoa(maxResults) +
		"\x00" + searchSettingsFingerprint(settings, providerID)
}

func cachedProviderSeed(providerID, query string, maxResults int, settings Settings) (providerOutcome, bool) {
	out, ok := providerSeedCache.get(providerSeedCacheKey(providerID, query, maxResults, settings))
	if !ok {
		return providerOutcome{}, false
	}
	if len(out.hits) > 0 {
		out.hits = append([]WebHit(nil), out.hits...)
	}
	return out, true
}

func storeProviderSeed(providerID, query string, maxResults int, settings Settings, out providerOutcome) {
	if !out.ok {
		return
	}
	// Clone hits so callers cannot mutate the cached slice.
	cached := out
	if len(out.hits) > 0 {
		cached.hits = append([]WebHit(nil), out.hits...)
	}
	providerSeedCache.put(providerSeedCacheKey(providerID, query, maxResults, settings), cached)
}
