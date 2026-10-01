package oar

import (
	"context"
	"fmt"
)

// TraceOutcome is one applied-rules trace row.
type TraceOutcome string

const (
	TraceFired   TraceOutcome = "fired"
	TracePassed  TraceOutcome = "passed"
	TraceErrored TraceOutcome = "errored"
	// Monitor outcomes preserve the observed result ([OAR-OPS-9]).
	TraceMonitoredFired  TraceOutcome = "monitored_fired"
	TraceMonitoredPassed TraceOutcome = "monitored_passed"
	// TraceMonitoredErrored records a raised monitor rule ([OAR-EVAL-10]).
	TraceMonitoredErrored TraceOutcome = "monitored_errored"
	// TraceSuppressed records a rule another rule's overrides withdrew for this
	// occurrence: no decision, no side-effect, but still traced ([OAR-EVAL-14]).
	TraceSuppressed TraceOutcome = "suppressed"
)

// TraceEntry records one rule evaluation for telemetry.
type TraceEntry struct {
	Rule    string
	Stage   Stage
	Outcome TraceOutcome
	Effect  Effect
	Error   string
}

// AppliedRulesTrace is the observability contract pairing with enforcement: monitor.
type AppliedRulesTrace struct {
	Entries []TraceEntry
}

// Add appends a trace row.
func (t *AppliedRulesTrace) Add(e TraceEntry) {
	if t == nil {
		return
	}
	t.Entries = append(t.Entries, e)
}

// OnFireEvent is the portable record emitted by on_fire: publish_event.
type OnFireEvent struct {
	SessionID string
	Rule      string
	Anchor    string
	Effect    Effect
}

type EventPublisher interface {
	Publish(context.Context, OnFireEvent) error
}

type unavailablePublisher struct{}

func (unavailablePublisher) Publish(context.Context, OnFireEvent) error {
	return fmt.Errorf("OAR event publisher is unavailable")
}

type acceptPublisher struct{}

func (acceptPublisher) Publish(context.Context, OnFireEvent) error { return nil }

// ExecuteOnFire runs declared side effects.
func ExecuteOnFire(ctx context.Context, store *CounterStore, pub EventPublisher, event OnFireEvent, code string, actions []OnFireAction) ([]OnFireAction, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	if store == nil {
		store = NewCounterStore()
	}
	if pub == nil {
		pub = unavailablePublisher{}
	}
	applied := make([]OnFireAction, 0, len(actions))
	for _, a := range actions {
		switch a {
		case OnFireIncrementCounter:
			store.Increment(event.SessionID, code, CounterFire, 1)
		case OnFireResetCounter:
			store.Reset(event.SessionID, code, CounterFire)
		case OnFireIncrementBreaker:
			store.Increment(event.SessionID, code, CounterBreaker, 1)
		case OnFireResetBreaker:
			store.Reset(event.SessionID, code, CounterBreaker)
		case OnFirePublishEvent:
			if err := pub.Publish(ctx, event); err != nil {
				return applied, err
			}
		}
		applied = append(applied, a)
	}
	return applied, nil
}
