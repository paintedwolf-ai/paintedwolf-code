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
	active  sync.WaitGroup
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
	e.active.Add(1)
	e.timers[id] = time.AfterFunc(d, func() {
		e.mu.Lock()
		delete(e.timers, id)
		stopped := e.stopped
		e.mu.Unlock()
		defer e.active.Done()
		if stopped {
			return
		}
		_ = expire(expiryCtx, checkpointID, expiryReason)
	})
}

func (e *expiryTimers) stop(ctx context.Context) error {
	e.mu.Lock()
	e.stopped = true
	for id, timer := range e.timers {
		if timer.Stop() {
			e.active.Done()
		}
		delete(e.timers, id)
	}
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { e.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StopExpiryTimers disarms pending expiries and drains callbacks at host shutdown. Rows stay
// pending in the store; RestorePending re-arms them in the next host.
func (m *Checkpoints) StopExpiryTimers(ctx context.Context) error {
	if m == nil {
		return nil
	}
	return m.expiries.stop(ctx)
}
