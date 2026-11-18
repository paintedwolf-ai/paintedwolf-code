package webresearch

import "time"

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

// resetPoliteness clears gates and robots caches; tests shrink the pacing
// interval so httptest crawls stay fast.
func resetPoliteness(interval time.Duration) {
	if interval > 0 {
		politeHostInterval = interval
	}
	politeGates.Lock()
	politeGates.m = make(map[string]*hostGate)
	politeGates.Unlock()
	robotsBodies.reset()
}
