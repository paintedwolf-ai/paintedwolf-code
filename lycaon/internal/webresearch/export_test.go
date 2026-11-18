package webresearch

import "time"

func resetCrawlState() {
	resetPoliteness(5 * time.Millisecond)
	siteIndexCache.reset()
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
