package await

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// PhaseRecord captures value-free timing and correlation facts across wait admission,
// process report publication, exact-handle evaluation, and transcript delivery.
type PhaseRecord struct {
	SessionID   string
	ToolCallID  string
	LeaseID     string
	Handle      string
	Tool        string
	Phase       string
	Outcome     string
	Status      string
	DurationMS  int64
	RecordedAt  time.Time
	ReferenceAt time.Time
	Failed      bool
}

type phaseObserverRegistry struct {
	mu      sync.RWMutex
	nextID  uint64
	entries map[uint64]func(PhaseRecord)
}

var phaseObservers = phaseObserverRegistry{
	entries: make(map[uint64]func(PhaseRecord)),
}

// RegisterPhaseObserver subscribes fn to value-free wait delivery phase records.
func RegisterPhaseObserver(fn func(PhaseRecord)) func() {
	if fn == nil {
		return func() {}
	}
	phaseObservers.mu.Lock()
	phaseObservers.nextID++
	id := phaseObservers.nextID
	phaseObservers.entries[id] = fn
	phaseObservers.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			phaseObservers.mu.Lock()
			delete(phaseObservers.entries, id)
			phaseObservers.mu.Unlock()
		})
	}
}

// EmitPhaseRecord logs a value-free debug record and notifies registered observers.
func EmitPhaseRecord(ctx context.Context, rec PhaseRecord) {
	if rec.RecordedAt.IsZero() {
		rec.RecordedAt = time.Now().UTC()
	}
	slog.DebugContext(ctx, "wait delivery phase",
		"session_id", rec.SessionID,
		"tool_call_id", rec.ToolCallID,
		"lease_id", rec.LeaseID,
		"handle", rec.Handle,
		"tool", rec.Tool,
		"phase", rec.Phase,
		"outcome", rec.Outcome,
		"status", rec.Status,
		"duration_ms", rec.DurationMS,
		"recorded_at", rec.RecordedAt.UTC().Format(time.RFC3339Nano),
		"failed", rec.Failed,
	)
	phaseObservers.mu.RLock()
	listeners := make([]func(PhaseRecord), 0, len(phaseObservers.entries))
	for _, fn := range phaseObservers.entries {
		listeners = append(listeners, fn)
	}
	phaseObservers.mu.RUnlock()
	for _, fn := range listeners {
		fn(rec)
	}
}

type dispatchTimeKey struct{}

// WithDispatchTime binds the model dispatch/finish timestamp preceding tool admission.
func WithDispatchTime(ctx context.Context, dispatchedAt time.Time) context.Context {
	if dispatchedAt.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, dispatchTimeKey{}, dispatchedAt.UTC())
}

// DispatchTimeFrom extracts any bound model dispatch/finish timestamp.
func DispatchTimeFrom(ctx context.Context) time.Time {
	t, _ := ctx.Value(dispatchTimeKey{}).(time.Time)
	return t
}

// Clock provides injectable time and timer primitives for deterministic wait tests.
type Clock struct {
	NowFunc      func() time.Time
	NewTimerFunc func(time.Duration) (<-chan time.Time, func())
}

// Now returns the clock's current UTC timestamp, defaulting to time.Now().UTC().
func (c Clock) Now() time.Time {
	if c.NowFunc != nil {
		return c.NowFunc().UTC()
	}
	return time.Now().UTC()
}

// Timer opens a timer channel and stop function, defaulting to time.NewTimer.
func (c Clock) Timer(d time.Duration) (<-chan time.Time, func()) {
	if c.NewTimerFunc != nil {
		return c.NewTimerFunc(d)
	}
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}
