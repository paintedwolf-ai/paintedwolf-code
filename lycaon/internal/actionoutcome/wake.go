package actionoutcome

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type WakeRecord struct {
	Sequence  uint64         `json:"sequence"`
	SessionID string         `json:"session_id"`
	Anchor    string         `json:"anchor"`
	State     api.WakeState  `json:"state"`
	Reason    api.WakeReason `json:"reason,omitempty"`
	TurnID    string         `json:"turn_id,omitempty"`
	LeaseID   string         `json:"lease_id,omitempty"`
}

type Ticket struct {
	ack     *atomic.Bool
	history *History[WakeRecord]
	key     string
	path    *Path
}

func (s *Session) Receive(ctx context.Context, sessionID string, seq uint64, anchor string) *Ticket {
	key := strconv.FormatUint(seq, 10)
	s.Wakes.Open(key, WakeRecord{Sequence: seq, SessionID: sessionID, Anchor: anchor, State: api.WakeStateReceived})
	return &Ticket{history: s.Wakes, key: key, path: Register(ctx, sessionID)}
}

func (t *Ticket) Await(ctx context.Context) (WakeRecord, error) {
	if t == nil {
		return WakeRecord{}, ErrUnavailable
	}
	return t.history.Await(ctx, t.key)
}

func (t *Ticket) Record(state api.WakeState, reason api.WakeReason, turnID, leaseID string) {
	if t == nil {
		return
	}
	if t.ack != nil {
		t.ack.Store(true)
	}
	terminal := state != api.WakeStateQueued && state != api.WakeStateDeferred
	ok := t.history.Change(t.key, func(r WakeRecord) (WakeRecord, bool) {
		r.State = state
		r.Reason = reason
		r.TurnID = turnID
		r.LeaseID = leaseID
		slog.Debug("wake outcome", "session_id", r.SessionID, "sequence", r.Sequence, "state", state, "reason", reason)
		return r, terminal
	})
	if !ok {
		reportWakeDefect(t.key, "duplicate terminal disposition")
		return
	}
	if terminal && state != api.WakeStateDelivered && state != api.WakeStateConsumed && state != api.WakeStateRoutedToWait {
		t.path.Resolve(WakeResult(reason))
	}
}

type ticketKey struct{}

func WithTicket(ctx context.Context, t *Ticket) context.Context {
	return context.WithValue(ctx, ticketKey{}, t)
}

func (t *Ticket) RefuseBeforeAdmission(reason api.WakeReason) {
	if t == nil {
		return
	}
	if t.ack != nil {
		t.ack.Store(true)
	}
	if t.history.Change(t.key, func(r WakeRecord) (WakeRecord, bool) {
		r.State = api.WakeStateDenied
		r.Reason = reason
		return r, true
	}) {
		t.path.Resolve(WakeResult(reason))
	}
}

// Attempt requires each queue or drain claim to record its disposition.
func (t *Ticket) Attempt() (*Ticket, func()) {
	if t == nil {
		return nil, func() {}
	}
	var seen atomic.Bool
	claim := *t
	claim.ack = &seen
	return &claim, func() {
		if seen.Load() {
			return
		}
		t.Record(api.WakeStateDropped, api.WakeReasonDefect, "", "")
		reportWakeDefect(t.key, "missing disposition")
	}
}

func reportWakeDefect(ticket, problem string) {
	slog.Error("wake transition defect", "ticket", ticket, "problem", problem)
	if testing.Testing() {
		panic(problem)
	}
}

func (t *Ticket) QueuedRetry() {
	if t != nil && t.ack != nil {
		t.ack.Store(true)
	}
}
