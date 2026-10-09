package hitl

import (
	"context"
	"sync"
	"time"
)

const expiryReason = "approval request timed out — denied (fail-safe)"

// expiryTimers holds the armed fail-safe expiries so shutdown can disarm
// them; an armed timer keeps the manager and its store reachable until it fires.
type expiryTimers struct {
	mu      sync.Mutex
	stopped bool
	next    uint64
	timers  map[uint64]*time.Timer
}

// schedule denies a still-pending checkpoint after d. Nonpositive windows
// disable the timer.
func (e *expiryTimers) schedule(ctx context.Context, checkpointID string, d time.Duration, expire func(ctx context.Context, checkpointID, reason string) error) {
	if d <= 0 {
		return
	}
	// Expiry outlives the request that created the checkpoint.
	expiryCtx := context.WithoutCancel(ctx)
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
		_ = expire(expiryCtx, checkpointID, expiryReason)
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
// pending in the store; RestorePending re-arms them in the next host.
func (m *Checkpoints) StopExpiryTimers() {
	if m == nil {
		return
	}
	m.expiries.stop()
}
