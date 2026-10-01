package webresearch

import (
	"strings"
	"sync"
	"time"
)

const (
	// Research sessions issue bursts of rephrased queries; both memories decay
	// on the same horizon as the site index cache.
	seedPlanCacheTTL      = 15 * time.Minute
	seedPlanCacheMax      = 128
	seenMemoryTTL         = 15 * time.Minute
	seenMemoryMaxScopes   = 32
	seenMemoryMaxPerScope = 400
)

// seedPlanCache reuses successful plans for identical query context.
var seedPlanCache = newTTLCache[seedPlan](seedPlanCacheTTL, seedPlanCacheMax)

func normalizeQueryKey(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

// normalizeSeedPlanCacheKey keys the seed-plan cache. Period is in the key:
// the picker ranks one window, so the same words under two windows are two plans.
func normalizeSeedPlanCacheKey(query string, period Period, taskHint string) string {
	q := normalizeQueryKey(query) + "\x00" + period.String()
	h := normalizeQueryKey(taskHint)
	if h == "" {
		return q
	}
	return q + "\x00" + h
}

func cachedSeedPlan(query string, period Period, taskHint string) (seedPlan, bool) {
	return seedPlanCache.get(normalizeSeedPlanCacheKey(query, period, taskHint))
}

func storeSeedPlan(query string, period Period, taskHint string, plan seedPlan) {
	seedPlanCache.put(normalizeSeedPlanCacheKey(query, period, taskHint), plan)
}

// searchMemory tracks URLs already returned to a session so later queries in
// the same burst prefer pages the agent hasn't seen. It is a rank penalty,
// never a filter — a repeat that is genuinely the best answer still wins.
var searchMemory = struct {
	sync.Mutex
	scopes map[string]*seenScope
}{scopes: make(map[string]*seenScope)}

type seenScope struct {
	urls    map[string]time.Time
	touched time.Time
}

// seenURLs returns the canonical URLs already returned to scope.
func seenURLs(scope string) map[string]struct{} {
	searchMemory.Lock()
	defer searchMemory.Unlock()
	entry, ok := searchMemory.scopes[scope]
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(entry.urls))
	for u, at := range entry.urls {
		if time.Since(at) > seenMemoryTTL {
			delete(entry.urls, u)
			continue
		}
		out[u] = struct{}{}
	}
	return out
}

func recordSeen(scope string, hits []WebHit) {
	if len(hits) == 0 {
		return
	}
	searchMemory.Lock()
	defer searchMemory.Unlock()
	entry, ok := searchMemory.scopes[scope]
	if !ok {
		if len(searchMemory.scopes) >= seenMemoryMaxScopes {
			oldestKey, oldestAt := "", time.Time{}
			for k, e := range searchMemory.scopes {
				if oldestKey == "" || e.touched.Before(oldestAt) {
					oldestKey, oldestAt = k, e.touched
				}
			}
			delete(searchMemory.scopes, oldestKey)
		}
		entry = &seenScope{urls: make(map[string]time.Time)}
		searchMemory.scopes[scope] = entry
	}
	entry.touched = time.Now()
	for _, h := range hits {
		if len(entry.urls) >= seenMemoryMaxPerScope {
			break
		}
		entry.urls[canonicalURL(h.URL)] = entry.touched
	}
}

func resetDirectMemory() {
	seedPlanCache.reset()
	searchResultCache.reset()
	searchMemory.Lock()
	searchMemory.scopes = make(map[string]*seenScope)
	searchMemory.Unlock()
}
