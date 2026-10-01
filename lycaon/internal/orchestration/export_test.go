package orchestration

// RunIDForDelegationForTest observes an admitted run while synchronous Run is blocked.
func RunIDForDelegationForTest(o *OrchestratorImpl, delegationID string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for id, state := range o.runs {
		if state.delegationID == delegationID {
			return id
		}
	}
	return ""
}
