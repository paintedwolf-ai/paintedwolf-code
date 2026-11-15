package orchestration

// RunIDForBatchForTest observes an admitted run while synchronous Run is blocked.
func RunIDForBatchForTest(o *OrchestratorImpl, batchID string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for id, state := range o.runs {
		if state.batchID == batchID {
			return id
		}
	}
	return ""
}
