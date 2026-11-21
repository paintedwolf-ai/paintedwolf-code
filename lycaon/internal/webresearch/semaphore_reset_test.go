package webresearch

func resetSeedCallSems() {
	seedSemMu.Lock()
	seedSems = map[string]chan struct{}{}
	seedSemMu.Unlock()
}
