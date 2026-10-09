package loopwake

import (
	"sync"
)

type PromptObservationsDeps struct {
}
type PromptObservations struct {
	depsMu            sync.RWMutex
	deps              PromptObservationsDeps
	promptObservedSeq sync.Map
	promptWorkflow    sync.Map
	Nudges            *Nudges
	Deliveries        *WaitDeliveries
	Subscriptions     *WaitSubscriptions
}

func (l *PromptObservations) setDeps(deps PromptObservationsDeps) {
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}
func (l *PromptObservations) loopDeps() PromptObservationsDeps {
	if l == nil {
		return PromptObservationsDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *PromptObservations) lastPromptObservedSeq(sessionID string) uint64 {
	if v, ok := l.promptObservedSeq.Load(sessionID); ok {
		seq, _ := v.(uint64)
		return seq
	}
	return 0
}
