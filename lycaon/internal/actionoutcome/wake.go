package actionoutcome

import (
	"context"
	"flag"
	"log/slog"
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/lycaon/lycaon/pkg/api"
)

type WakeStep struct {
	State  api.WakeState  `json:"state"`
	Reason api.WakeReason `json:"reason,omitempty"`
}
type WakeDefect struct {
	Sequence uint64   `json:"sequence"`
	Attempt  WakeStep `json:"attempt"`
	Problem  string   `json:"problem"`
}
type WakeRecord struct {
	ContinuationID string         `json:"continuation_id,omitempty"`
	Steps          []WakeStep     `json:"steps"`
	Sequence       uint64         `json:"sequence"`
	SessionID      string         `json:"session_id"`
	Anchor         string         `json:"anchor"`
	State          api.WakeState  `json:"state"`
	Reason         api.WakeReason `json:"reason,omitempty"`
	TurnID         string         `json:"turn_id,omitempty"`
	LeaseID        string         `json:"lease_id,omitempty"`
}

type Ticket struct {
	session *Session
	seq     uint64
	ack     *atomic.Bool
	history *History[WakeRecord]
	key     string
	path    *Path
}

func (s *Session) Receive(ctx context.Context, sessionID string, seq uint64, anchor string) *Ticket {
	key := strconv.FormatUint(seq, 10)
	path := Register(ctx, sessionID)
	record := WakeRecord{Sequence: seq, SessionID: sessionID, Anchor: anchor, State: api.WakeStateReceived, Steps: []WakeStep{{State: api.WakeStateReceived}}}
	if path != nil {
		record.ContinuationID = path.action.ID()
	}
	ticket := &Ticket{session: s, seq: seq, history: s.Wakes, key: key, path: path}
	if !s.Wakes.Open(key, record) {
		ticket.reportDefect(WakeStep{State: api.WakeStateReceived}, "duplicate ticket")
	}
	return ticket
}

// ClosedTicket answers a caller that arrives during session retirement.
func ClosedTicket(ctx context.Context, sessionID string, seq uint64, anchor string) *Ticket {
	s := &Session{Wakes: NewHistory[WakeRecord](1)}
	t := s.Receive(ctx, sessionID, seq, anchor)
	t.Record(api.WakeStateDropped, api.WakeReasonSessionClosed, "", "")
	return t
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
	step := WakeStep{state, reason}
	var valid bool
	switch state {
	case api.WakeStateDelivered, api.WakeStateConsumed:
		valid = reason == "" && turnID != ""
	case api.WakeStateRoutedToWait:
		valid = reason == "" && leaseID != ""
	case api.WakeStateQueued:
		valid = reason == ""
	default:
		valid = state != "" && reasonState(reason) == state
	}
	if !valid {
		state, reason = api.WakeStateDropped, api.WakeReasonDefect
		defer t.reportDefect(step, "invalid wake transition")
	}
	terminal := state != api.WakeStateQueued && state != api.WakeStateDeferred
	ok := t.history.Change(t.key, func(r WakeRecord) (WakeRecord, bool) {
		r.Steps = append(slices.Clip(r.Steps), WakeStep{state, reason})
		r.State = state
		r.Reason = reason
		r.TurnID = turnID
		r.LeaseID = leaseID
		slog.Debug("wake outcome", "session_id", r.SessionID, "sequence", r.Sequence, "state", state, "reason", reason)
		return r, terminal
	})
	if !ok {
		t.reportDefect(WakeStep{state, reason}, "duplicate terminal disposition")
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
		r.State = reasonState(reason)
		r.Steps = append(slices.Clip(r.Steps), WakeStep{r.State, reason})
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
		t.reportDefect(WakeStep{api.WakeStateDropped, api.WakeReasonDefect}, "missing disposition")
	}
}

func (t *Ticket) reportDefect(step WakeStep, problem string) {
	s := t.session
	s.defectsMu.Lock()
	if len(s.defects) == 32 {
		s.defects = slices.Delete(s.defects, 0, 1)
	}
	s.defects = append(s.defects, WakeDefect{Sequence: t.seq, Attempt: step, Problem: problem})
	s.defectsMu.Unlock()
	slog.Error("wake transition defect", "ticket", t.key, "problem", problem)
	if flag.Lookup("test.v") != nil {
		panic(problem)
	}
}

func (t *Ticket) QueuedRetry() {
	if t != nil && t.ack != nil {
		t.ack.Store(true)
	}
}
