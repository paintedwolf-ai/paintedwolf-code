package webresearch

import (
	"context"
	"sync/atomic"
	"time"
)

func searchDirect(ctx context.Context, discoverer DirectDiscoverer, query string, period Period, maxResults int) providerOutcome {
	providerID := directWireProviderID
	if discoverer == nil {
		return providerOutcome{providerID: providerID, reason: "discoverer_unavailable", detail: "no direct search discoverer configured"}
	}
	ctx, cancel := context.WithTimeout(ctx, directChainTimeout)
	defer cancel()

	pageBudget := maxResults
	if pageBudget <= 0 {
		pageBudget = defaultPageBudget
	}

	hits, err := discoverer.Search(ctx, DirectRequest{Query: query, Period: period, MaxResults: pageBudget})
	if err != nil {
		return providerOutcome{providerID: providerID, reason: "search_error", detail: err.Error()}
	}
	if len(hits) == 0 {
		return providerOutcome{providerID: providerID, reason: "fetch_failed", detail: "no live hits"}
	}
	return providerOutcome{providerID: providerID, ok: true, reason: "ok", hits: hits}
}

// directDiscoverSem bounds concurrent direct searches' network activity. A
// search holds one slot from its first phase-2 host crawl through frontier end.
var directDiscoverSem = make(chan struct{}, directDiscoverMaxParallel)

// activeUserSearches counts in-flight user-origin direct searches. The warm
// engine consults it to keep background Summarizer traffic out of interactive
// storms.
var activeUserSearches atomic.Int64

// interactiveSearchActive reports whether any user-origin direct search is in
// flight right now.
func interactiveSearchActive() bool {
	return activeUserSearches.Load() > 0
}

func acquireDirectSlot(ctx context.Context) error {
	select {
	case directDiscoverSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tryAcquireDirectSlot takes a discovery slot only if one is free — the warm
// engine's priority floor: background warming never waits on (or delays) a
// live search.
func tryAcquireDirectSlot() bool {
	select {
	case directDiscoverSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseDirectSlot() {
	select {
	case <-directDiscoverSem:
	default:
		// An unbalanced release, or resetDirectState swapping the semaphore
		// under an in-flight test search; warn rather than panic.
		wrlog.Warn("unbalanced direct-search slot release")
	}
}

// resetDirectState reinitializes the discover semaphore and clears the site
// index cache, seed-plan cache, and seen-URL memory; tests call it for
// isolation.
func resetDirectState(parallel int) {
	if parallel <= 0 {
		parallel = directDiscoverMaxParallel
	}
	directDiscoverSem = make(chan struct{}, parallel)
	resetSeedCallSems()
	activeUserSearches.Store(0)
	resetPoliteness(5 * time.Millisecond)
	siteIndexCache.reset()
	providerSeedCache.reset()
	resetDirectMemory()
}
