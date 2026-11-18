package webresearch

func resetDirectMemory() {
	seedPlanCache.reset()
	searchResultCache.reset()
	searchMemory.Lock()
	searchMemory.scopes = make(map[string]*seenScope)
	searchMemory.Unlock()
}
