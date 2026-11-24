package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/db"
)

// ProcessCompletionFact describes an already-finished command job for reconciliation.
type ProcessCompletionFact struct {
	OccurrenceID string
	Handle       string
	Outcome      string
	Code         string
	Report       string
}

// ProcessLookup queries terminal receipts for exact command handles.
type ProcessLookup func(ctx context.Context, sessionID, handle string) (ProcessCompletionFact, bool, error)

// LeaseWakeFunc notifies runtime delivery when a parked wait lease settles.
type LeaseWakeFunc func(ctx context.Context, lease awaitstore.Lease, winner awaitstore.Condition)

// Store manages durable watch registrations, matches, deliveries and wait selections.
type Store struct {
	db                    db.Handle
	clock                 Clock
	heap                  *DeadlineHeap
	sampler               *Sampler
	mu                    sync.RWMutex
	processLookup         ProcessLookup
	coordinatorLookup     CoordinatorStateLookup
	authorityChecker      AuthorityChecker
	onLeaseWake           LeaseWakeFunc
	wakeGate              AmbientWakeGate
	treeStopping          func(sessionID string) bool
	onProcessKickTransfer func(sessionID, handle string)
	onProcessObserve      func(sessionID, handle string)
}

func NewStore(database db.Handle, clock Clock) *Store {
	if clock == nil {
		clock = RealClock{}
	}
	return &Store{
		db:      database,
		clock:   clock,
		heap:    NewDeadlineHeap(),
		sampler: newSampler(),
	}
}

func (s *Store) Clock() Clock        { return s.clock }
func (s *Store) Heap() *DeadlineHeap { return s.heap }
func (s *Store) Database() db.Handle { return s.db }
func (s *Store) Configured() bool    { return s != nil && s.db != nil }

func (s *Store) SetProcessLookup(fn ProcessLookup) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.processLookup = fn
	s.mu.Unlock()
}

func (s *Store) SetCoordinatorStateLookup(fn CoordinatorStateLookup) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.coordinatorLookup = fn
	s.mu.Unlock()
}

func (s *Store) SetAuthorityChecker(fn AuthorityChecker) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.authorityChecker = fn
	s.mu.Unlock()
}

func (s *Store) lookupCoordinatorState(ctx context.Context, sessionID string) (WorkerWatchStateSnapshot, bool, error) {
	s.mu.RLock()
	fn := s.coordinatorLookup
	s.mu.RUnlock()
	if fn == nil {
		return WorkerWatchStateSnapshot{}, false, nil
	}
	snap, err := fn(ctx, sessionID)
	return snap, true, err
}

func (s *Store) checkWatchAuthority(ctx context.Context, reg Registration) (bool, string) {
	s.mu.RLock()
	fn := s.authorityChecker
	s.mu.RUnlock()
	if fn == nil {
		return true, ""
	}
	return fn(ctx, reg)
}

func (s *Store) retireWatchRuntime(watchID string) {
	if s == nil || watchID == "" {
		return
	}
	s.heap.Remove(KindWatchExpiry, watchID)
	s.heap.Remove(KindTimerTick, watchTimerPrefix+watchID)
	if s.sampler != nil {
		s.sampler.unsubscribeWatch(watchID, s.heap)
	}
}

// NextDueWait returns the bounded duration until the next deadline heap entry falls due.
func (s *Store) NextDueWait(maxWait time.Duration) time.Duration {
	if s == nil || s.heap == nil {
		return maxWait
	}
	entry, ok := s.heap.Peek()
	if !ok {
		return maxWait
	}
	wait := entry.DueAt.Sub(s.clock.Now())
	if wait <= 0 {
		return 10 * time.Millisecond
	}
	if wait > maxWait {
		return maxWait
	}
	return wait
}

func (s *Store) SetProcessKickTransfer(fn func(sessionID, handle string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onProcessKickTransfer = fn
	s.mu.Unlock()
}

func (s *Store) SetProcessObserver(fn func(sessionID, handle string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onProcessObserve = fn
	s.mu.Unlock()
}

func (s *Store) notifyProcessKickTransfer(sessionID, handle string) {
	s.mu.RLock()
	fn := s.onProcessKickTransfer
	s.mu.RUnlock()
	if fn != nil {
		fn(sessionID, handle)
	}
}

func (s *Store) notifyProcessObserve(sessionID, handle string) {
	s.mu.RLock()
	fn := s.onProcessObserve
	s.mu.RUnlock()
	if fn != nil {
		fn(sessionID, handle)
	}
}

func (s *Store) SetLeaseWakeHandler(fn LeaseWakeFunc) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onLeaseWake = fn
	s.mu.Unlock()
}

func (s *Store) SetAmbientWakeGate(gate AmbientWakeGate) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.wakeGate = gate
	s.mu.Unlock()
}

func (s *Store) SetTreeStoppingFunc(fn func(sessionID string) bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.treeStopping = fn
	s.mu.Unlock()
}

func (s *Store) isTreeStopping(sessionID string) bool {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	s.mu.RLock()
	fn := s.treeStopping
	s.mu.RUnlock()
	return fn != nil && fn(strings.TrimSpace(sessionID))
}

func (s *Store) TriggerAmbientWake(ctx context.Context, sessionID string) (bool, error) {
	if !s.Configured() {
		return false, nil
	}
	s.mu.RLock()
	gate := s.wakeGate
	s.mu.RUnlock()
	if gate.DispatchTurn == nil {
		return false, nil
	}
	return TryScheduleAmbientWake(ctx, s, sessionID, gate)
}

func (s *Store) lookupProcess(ctx context.Context, sessionID, handle string) (ProcessCompletionFact, bool, error) {
	s.mu.RLock()
	fn := s.processLookup
	s.mu.RUnlock()
	if fn == nil {
		return ProcessCompletionFact{}, false, nil
	}
	return fn(ctx, sessionID, handle)
}

func (s *Store) notifyLeaseWake(ctx context.Context, lease awaitstore.Lease, winner awaitstore.Condition) {
	s.mu.RLock()
	fn := s.onLeaseWake
	s.mu.RUnlock()
	if fn != nil {
		fn(ctx, lease, winner)
	}
}

const registrationColumns = `watch_id, project_id, owner_kind, session_id, COALESCE(worker_job_id, ''),
	handle, seq, COALESCE(derived_command_handle, ''), COALESCE(creating_invocation_id, ''),
	conditions_json, authority_json, cursors_json, delivery, repeat, expires_at, state,
	COALESCE(terminal_outcome, ''), slot_released, match_seq, overflow_count,
	COALESCE(overflow_summary_json, ''), COALESCE(final_receipt_id, ''), revision`

type scanner interface {
	Scan(dest ...any) error
}

func scanRegistration(row scanner) (Registration, error) {
	var reg Registration
	var ownerKind, conditionsJSON, authorityJSON, cursorsJSON string
	var delivery, expiresAt, state, outcome, overflowJSON string
	var repeat, slotReleased int
	if err := row.Scan(
		&reg.WatchID, &reg.Owner.ProjectID, &ownerKind, &reg.Owner.SessionID, &reg.Owner.WorkerJobID,
		&reg.Handle, &reg.Seq, &reg.DerivedCommandHandle, &reg.InvocationID,
		&conditionsJSON, &authorityJSON, &cursorsJSON, &delivery, &repeat, &expiresAt, &state,
		&outcome, &slotReleased, &reg.Lifecycle.MatchSeq, &reg.Lifecycle.OverflowCount,
		&overflowJSON, &reg.Lifecycle.FinalReceiptID, &reg.Lifecycle.Revision,
	); err != nil {
		return Registration{}, err
	}
	reg.Owner.Kind = OwnerKind(ownerKind)
	reg.Lifecycle.Delivery = DeliveryMode(delivery)
	reg.Lifecycle.Repeat = repeat == 1
	reg.Lifecycle.State = State(state)
	reg.Lifecycle.Outcome = TerminalOutcome(outcome)
	reg.Lifecycle.SlotReleased = slotReleased == 1
	parsedExp, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return Registration{}, fmt.Errorf("parse watch expires_at: %w", err)
	}
	reg.Lifecycle.ExpiresAt = parsedExp.UTC()
	if err := json.Unmarshal([]byte(conditionsJSON), &reg.Conditions); err != nil {
		return Registration{}, err
	}
	if err := json.Unmarshal([]byte(authorityJSON), &reg.Authority); err != nil {
		return Registration{}, err
	}
	reg.Cursors = map[string]string{}
	if cursorsJSON != "" {
		if err := json.Unmarshal([]byte(cursorsJSON), &reg.Cursors); err != nil {
			return Registration{}, err
		}
	}
	if overflowJSON != "" {
		var ov OverflowSummary
		if err := json.Unmarshal([]byte(overflowJSON), &ov); err != nil {
			return Registration{}, err
		}
		reg.Lifecycle.Overflow = &ov
	}
	return reg, nil
}

func loadRegistrationByHandleTx(ctx context.Context, tx *sql.Tx, owner Owner, handle string) (Registration, error) {
	owner = owner.Normalized()
	handle = strings.TrimSpace(handle)
	reg, err := scanRegistration(tx.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND handle = ?`,
		owner.SessionID, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, &UnknownHandleError{Handle: handle}
	}
	if err != nil {
		return Registration{}, err
	}
	if reg.Owner.Kind != owner.Kind || reg.Owner.WorkerJobID != owner.WorkerJobID {
		return Registration{}, &UnknownHandleError{Handle: handle}
	}
	return reg, nil
}
