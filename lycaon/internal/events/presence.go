package events

import (
	"sync"
	"time"
)

// DefaultUserActionWindow bounds how long user activity establishes presence.
const DefaultUserActionWindow = 15 * time.Minute

// Presence requires an attached event stream and a recent user action.
type Presence struct {
	hub    EventHub
	window time.Duration

	mu   sync.Mutex
	now  func() time.Time
	last time.Time
}

// NewPresence tracks activity over window; nonpositive windows remain inactive.
func NewPresence(hub EventHub, window time.Duration) *Presence {
	return &Presence{hub: hub, window: window, now: time.Now}
}

// setClock overrides the stamp clock so tests can move time without sleeping.
func (p *Presence) setClock(now func() time.Time) {
	if p == nil || now == nil {
		return
	}
	p.mu.Lock()
	p.now = now
	p.mu.Unlock()
}

// wallNow includes time spent asleep by discarding the monotonic reading.
// The caller holds mu.
func (p *Presence) wallNow() time.Time {
	if p.now == nil {
		return time.Now().Round(0)
	}
	return p.now().Round(0)
}

// MarkUserAction is called for person-initiated mutations.
func (p *Presence) MarkUserAction() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.last = p.wallNow()
	p.mu.Unlock()
}

// attached reports whether any client holds the event stream open.
func (p *Presence) attached() bool {
	if p == nil || p.hub == nil {
		return false
	}
	return p.hub.SubscriberCount() > 0
}

// LastUserAction returns the last stamped user action; zero when none has
// landed this process lifetime.
func (p *Presence) LastUserAction() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// Live requires an attached client and a user action within the activity window.
func (p *Presence) Live() bool {
	if p == nil || !p.attached() {
		return false
	}
	p.mu.Lock()
	last, elapsed := p.last, p.wallNow().Sub(p.last)
	p.mu.Unlock()
	if last.IsZero() {
		return false
	}
	// Negative elapsed time does not establish presence.
	return elapsed >= 0 && elapsed < p.window
}
