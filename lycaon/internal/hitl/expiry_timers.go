package hitl

import (
	"sync"
	"time"
)

// expiryTimers holds the pending fail-safe expiries so shutdown can disarm
// them; an armed timer keeps the manager and its store reachable until it fires.
type expiryTimers struct {
	mu      sync.Mutex
	stopped bool
	next    uint64
	timers  map[uint64]*time.Timer
}

func (e *expiryTimers) schedule(d time.Duration, expire func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return
	}
	if e.timers == nil {
		e.timers = make(map[uint64]*time.Timer)
	}
	e.next++
	id := e.next
	e.timers[id] = time.AfterFunc(d, func() {
		e.mu.Lock()
		delete(e.timers, id)
		e.mu.Unlock()
		expire()
	})
}

func (e *expiryTimers) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopped = true
	for id, timer := range e.timers {
		timer.Stop()
		delete(e.timers, id)
	}
}

// StopExpiryTimers disarms pending expiries at host shutdown. Rows stay
// pending in the store; a later host resolves them from durable state.
func (m *Manager) StopExpiryTimers() {
	if m == nil {
		return
	}
	m.expiries.stop()
}
