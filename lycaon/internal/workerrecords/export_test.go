package workerrecords

// CachedRoundFrameCount reports how many worker round frames reside in cache.
func (s *Store) CachedRoundFrameCount() int {
	if s == nil || s.frameCache == nil {
		return 0
	}
	s.frameCache.mu.Lock()
	defer s.frameCache.mu.Unlock()
	return s.frameCache.lru.Len()
}

// RoundFrameCacheStats reports round-frame cache hits and misses.
func (s *Store) RoundFrameCacheStats() (hits, misses uint64) {
	if s == nil || s.frameCache == nil {
		return 0, 0
	}
	s.frameCache.mu.Lock()
	defer s.frameCache.mu.Unlock()
	return s.frameCache.hits, s.frameCache.misses
}
