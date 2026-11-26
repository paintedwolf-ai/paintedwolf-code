package loopwake

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
)

// WakeResult is how a host wake ended.
type WakeResult string

const (
	// WakeDelivered opened a coordinator turn; TurnID names it.
	WakeDelivered WakeResult = "delivered"
	// WakeRouted was handed to the session's wait, whose resume runs the turn.
	WakeRouted WakeResult = "routed"
	// WakeParked opened no turn; the session waits for a later wake or input.
	WakeParked WakeResult = "parked"
	// WakeDropped opened no turn because the wake no longer applies.
	WakeDropped WakeResult = "dropped"
)

// TurnResult is how a turn a wake opened ended.
type TurnResult string

const (
	TurnCompleted  TurnResult = "completed"
	TurnFailed     TurnResult = "failed"
	TurnCancelled  TurnResult = "cancelled"
	TurnNotStarted TurnResult = "not_started"
)

// WakeOutcome is the one terminal record of a host wake. Seq identifies the wake
// within its loop.
type WakeOutcome struct {
	Seq     uint64
	Wake    anchor.ID
	Inform  anchor.ID
	LegID   string
	JobID   string
	Result  WakeResult
	Reason  string
	TurnID  string
	Turn    TurnResult
	TurnWhy string
}

// wakeLedgerCapacity bounds the outcomes kept per session.
const wakeLedgerCapacity = 32

// WakeOutcomes records each wake's terminal outcome and, for delivered wakes, how
// their turn ended. Subscribers receive every record for their session in order.
type WakeOutcomes struct {
	mu          sync.Mutex
	open        map[uint64]string
	ledger      map[string][]WakeOutcome
	subscribers map[string]map[*WakeSubscription]struct{}
	violations  atomic.Uint64
}

func newWakeOutcomes() *WakeOutcomes {
	return &WakeOutcomes{open: map[uint64]string{}, ledger: map[string][]WakeOutcome{}, subscribers: map[string]map[*WakeSubscription]struct{}{}}
}

// Violations counts wakes decided twice, or left by a decision without an outcome.
func (o *WakeOutcomes) Violations() uint64 {
	if o == nil {
		return 0
	}
	return o.violations.Load()
}

// Ledger returns the session's recent records, oldest first.
func (o *WakeOutcomes) Ledger(sessionID string) []WakeOutcome {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.ledger[sessionID])
}

func (o *WakeOutcomes) opened(sessionID string, seq uint64) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.open[seq] = sessionID
}

// settle closes an open wake with its terminal record.
func (o *WakeOutcomes) settle(sessionID string, p pendingLoopWake, result WakeResult, reason, turnID string) {
	loopLogNudge(sessionID, p.wake, p.inform, p.legID, p.completingJobID, string(result)+":"+reason)
	if o == nil {
		return
	}
	o.mu.Lock()
	if owner, ok := o.open[p.seq]; !ok || owner != sessionID {
		o.mu.Unlock()
		o.violations.Add(1)
		slog.Error("host wake settled twice or never opened", "component", loopLogComponent, "session_id", sessionID, "seq", p.seq, "result", result, "reason", reason)
		return
	}
	delete(o.open, p.seq)
	o.mu.Unlock()
	o.publish(sessionID, WakeOutcome{Seq: p.seq, Wake: p.wake, Inform: p.inform, LegID: p.legID, JobID: p.completingJobID, Result: result, Reason: reason, TurnID: turnID})
}

// turnEnded records how a delivered wake's turn ended.
func (o *WakeOutcomes) turnEnded(sessionID string, p pendingLoopWake, turnID string, res *promptresult.Result, err error) {
	turn, why := TurnCompleted, ""
	switch {
	case errors.Is(err, context.Canceled):
		turn, why = TurnCancelled, "context_canceled"
	case err != nil:
		turn, why = TurnFailed, "error"
	case res == nil:
		turn, why = TurnNotStarted, "not_admitted"
	case res.Outcome != promptresult.OutcomeCompleted:
		why = string(res.Outcome)
	}
	if o == nil {
		return
	}
	o.publish(sessionID, WakeOutcome{Seq: p.seq, Wake: p.wake, Inform: p.inform, LegID: p.legID, JobID: p.completingJobID, Result: WakeDelivered, Reason: "run_prompt", TurnID: turnID, Turn: turn, TurnWhy: why})
}

func (o *WakeOutcomes) publish(sessionID string, record WakeOutcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ledger := append(o.ledger[sessionID], record)
	if len(ledger) > wakeLedgerCapacity {
		ledger = slices.Clone(ledger[len(ledger)-wakeLedgerCapacity:])
	}
	o.ledger[sessionID] = ledger
	for sub := range o.subscribers[sessionID] {
		sub.push(record)
	}
}

// forget drops a session's ledger and closes its open wakes without outcomes;
// the session no longer exists to wake.
func (o *WakeOutcomes) forget(sessionID string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.ledger, sessionID)
	for seq, owner := range o.open {
		if owner == sessionID {
			delete(o.open, seq)
		}
	}
}

// Subscribe returns the session's records from now on, until the subscription is closed.
func (o *WakeOutcomes) Subscribe(sessionID string) *WakeSubscription {
	sub := &WakeSubscription{owner: o, sessionID: sessionID, ready: make(chan struct{}, 1)}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.subscribers[sessionID] == nil {
		o.subscribers[sessionID] = map[*WakeSubscription]struct{}{}
	}
	o.subscribers[sessionID][sub] = struct{}{}
	return sub
}

// WakeSubscription delivers a session's wake records in the order they were made.
type WakeSubscription struct {
	owner     *WakeOutcomes
	sessionID string
	mu        sync.Mutex
	queued    []WakeOutcome
	ready     chan struct{}
}

func (s *WakeSubscription) push(record WakeOutcome) {
	s.mu.Lock()
	s.queued = append(s.queued, record)
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Next returns the next record, or false once ctx ends first.
func (s *WakeSubscription) Next(ctx context.Context) (WakeOutcome, bool) {
	for {
		s.mu.Lock()
		if len(s.queued) > 0 {
			record := s.queued[0]
			s.queued = s.queued[1:]
			s.mu.Unlock()
			return record, true
		}
		s.mu.Unlock()
		select {
		case <-s.ready:
		case <-ctx.Done():
			return WakeOutcome{}, false
		}
	}
}

// Close stops delivery.
func (s *WakeSubscription) Close() {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	delete(s.owner.subscribers[s.sessionID], s)
}

// wakeDecision is one pass over a wake. Every pass ends in exactly one call:
// settle (or deliver) when the wake ends, requeue when it waits in a queue.
type wakeDecision struct {
	outcomes  *WakeOutcomes
	sessionID string
	pending   pendingLoopWake
	calls     int
}

func (o *WakeOutcomes) decide(sessionID string, p pendingLoopWake) *wakeDecision {
	return &wakeDecision{outcomes: o, sessionID: sessionID, pending: p}
}

func (d *wakeDecision) count() bool {
	d.calls++
	if d.calls > 1 {
		d.outcomes.violation(d.sessionID, d.pending, "decided twice")
		return false
	}
	return true
}

func (d *wakeDecision) settle(result WakeResult, reason string) {
	if d.count() {
		d.outcomes.settle(d.sessionID, d.pending, result, reason, "")
	}
}

// deliver settles the wake as opening the turn it returns the identity of.
func (d *wakeDecision) deliver() string {
	turnID := uuid.NewString()
	if d.count() {
		d.outcomes.settle(d.sessionID, d.pending, WakeDelivered, "run_prompt", turnID)
	}
	return turnID
}

// requeue records that the wake waits in a queue for a later pass.
func (d *wakeDecision) requeue(reason string) {
	if d.count() {
		loopLogNudge(d.sessionID, d.pending.wake, d.pending.inform, d.pending.legID, d.pending.completingJobID, reason)
	}
}

// close guards the pass: a path that decided nothing drops the wake rather than
// leave it without an outcome.
func (d *wakeDecision) close() {
	if d.calls == 0 {
		d.outcomes.violation(d.sessionID, d.pending, "undecided")
		d.outcomes.settle(d.sessionID, d.pending, WakeDropped, "undecided", "")
	}
}

func (o *WakeOutcomes) violation(sessionID string, p pendingLoopWake, what string) {
	if o == nil {
		return
	}
	o.violations.Add(1)
	slog.Error("host wake decision broke its one-outcome rule", "component", loopLogComponent, "problem", what, "session_id", sessionID, "seq", p.seq, "wake", string(p.wake))
}

type loopWakeTurnKey struct{}

// WithLoopWakeTurn names the turn a loop wake opens, so its admission uses that identity.
func WithLoopWakeTurn(ctx context.Context, turnID string) context.Context {
	return context.WithValue(ctx, loopWakeTurnKey{}, strings.TrimSpace(turnID))
}

// LoopWakeTurn returns the turn identity a loop wake chose, if any.
func LoopWakeTurn(ctx context.Context) string {
	turnID, _ := ctx.Value(loopWakeTurnKey{}).(string)
	return turnID
}
