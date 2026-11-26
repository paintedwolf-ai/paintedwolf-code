package pythonadmission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

var (
	// ErrLeaseNotFound is returned when an approval lease ID does not exist.
	ErrLeaseNotFound = errors.New("approval lease not found")
	// ErrLeaseAlreadyConsumed is returned when a one-use lease is presented a second time.
	ErrLeaseAlreadyConsumed = errors.New("approval lease already consumed")
	// ErrLeaseAlreadySettled is returned when trying to decide an already-settled lease.
	ErrLeaseAlreadySettled = errors.New("approval lease already settled")
	// ErrLeaseLostOnRestart is returned when a lease was invalidated by host restart.
	ErrLeaseLostOnRestart = errors.New("approval lease lost on host restart")
	// ErrStaleApprovalAfterRestart is returned when a stale approval arrives after guest loss.
	ErrStaleApprovalAfterRestart = errors.New("stale approval rejected after host restart; cannot start replacement execution")
	// ErrLeaseBindingMismatch is returned when consumption identity does not match the lease binding.
	ErrLeaseBindingMismatch = errors.New("approval lease binding mismatch")
)

// LeaseState tracks the lifecycle of one one-use approval lease.
type LeaseState string

const (
	LeasePending       LeaseState = "pending"
	LeaseApproved      LeaseState = "approved"
	LeaseConsumed      LeaseState = "consumed"
	LeaseDeclined      LeaseState = "declined"
	LeaseCancelled     LeaseState = "cancelled"
	LeaseLostOnRestart LeaseState = "lost_on_restart"
)

// ApprovalLease binds one-use human permission to a single live execution,
// operation, authoritative resolved target, and reviewed arguments digest.
type ApprovalLease struct {
	LeaseID             string                        `json:"lease_id"`
	ExecutionID         string                        `json:"execution_id"`
	OperationID         string                        `json:"operation_id"`
	CallID              string                        `json:"call_id,omitempty"`
	Actor               string                        `json:"actor,omitempty"`
	View                string                        `json:"view,omitempty"`
	Surface             string                        `json:"surface,omitempty"`
	Family              pythonruntime.EffectFamily    `json:"family"`
	Operation           string                        `json:"operation"`
	Target              pythonruntime.CanonicalTarget `json:"target"`
	AuthoritativeTarget pythonruntime.CanonicalTarget `json:"authoritative_target"`
	ArgsDigest          string                        `json:"args_digest"`
	State               LeaseState                    `json:"state"`
	Reason              string                        `json:"reason,omitempty"`
	CreatedAt           time.Time                     `json:"created_at"`
	SettledAt           time.Time                     `json:"settled_at,omitempty"`
}

// LeaseRegistry stores and enforces one-use semantics for ApprovalLeases.
type LeaseRegistry struct {
	mu     sync.Mutex
	leases map[string]*ApprovalLease
	order  []string
}

// NewLeaseRegistry constructs an empty LeaseRegistry.
func NewLeaseRegistry() *LeaseRegistry {
	return &LeaseRegistry{
		leases: make(map[string]*ApprovalLease),
	}
}

// Create registers a new pending ApprovalLease.
func (r *LeaseRegistry) Create(lease ApprovalLease) error {
	if lease.LeaseID == "" {
		return errors.New("approval lease missing lease_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.leases[lease.LeaseID]; exists {
		return fmt.Errorf("approval lease %q already exists", lease.LeaseID)
	}
	copied := lease
	if copied.State == "" {
		copied.State = LeasePending
	}
	r.leases[lease.LeaseID] = &copied
	r.order = append(r.order, lease.LeaseID)
	return nil
}

// Get returns a copy of the lease with leaseID.
func (r *LeaseRegistry) Get(leaseID string) (ApprovalLease, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[leaseID]
	if !ok {
		return ApprovalLease{}, false
	}
	return *l, true
}

// List returns snapshot copies of all leases in creation order.
func (r *LeaseRegistry) List() []ApprovalLease {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ApprovalLease, 0, len(r.order))
	for _, id := range r.order {
		if l, ok := r.leases[id]; ok {
			out = append(out, *l)
		}
	}
	return out
}

// Consume atomically consumes an approved one-use lease after verifying its
// execution, operation, authoritative target, and args digest binding.
func (r *LeaseRegistry) Consume(
	leaseID, executionID, operationID string,
	authoritativeTarget pythonruntime.CanonicalTarget,
	argsDigest string,
	now time.Time,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[leaseID]
	if !ok {
		return ErrLeaseNotFound
	}
	switch l.State {
	case LeaseConsumed:
		return ErrLeaseAlreadyConsumed
	case LeaseLostOnRestart:
		return ErrLeaseLostOnRestart
	case LeaseApproved:
		// proceed to binding verification
	default:
		return fmt.Errorf("%w: state=%s", ErrLeaseAlreadySettled, l.State)
	}
	if l.ExecutionID != executionID ||
		l.OperationID != operationID ||
		l.AuthoritativeTarget != authoritativeTarget ||
		l.ArgsDigest != argsDigest {
		return fmt.Errorf(
			"%w: lease=%s exec=%q/%q op=%q/%q",
			ErrLeaseBindingMismatch, leaseID, l.ExecutionID, executionID, l.OperationID, operationID,
		)
	}
	l.State = LeaseConsumed
	l.SettledAt = now.UTC()
	return nil
}

func (r *LeaseRegistry) transition(leaseID string, from []LeaseState, to LeaseState, reason string, now time.Time) (ApprovalLease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[leaseID]
	if !ok {
		return ApprovalLease{}, ErrLeaseNotFound
	}
	if l.State == LeaseLostOnRestart && to == LeaseApproved {
		return *l, ErrStaleApprovalAfterRestart
	}
	allowed := false
	for _, st := range from {
		if l.State == st {
			allowed = true
			break
		}
	}
	if !allowed {
		if l.State == LeaseConsumed {
			return *l, ErrLeaseAlreadyConsumed
		}
		return *l, fmt.Errorf("%w: current state=%s", ErrLeaseAlreadySettled, l.State)
	}
	l.State = to
	if reason != "" {
		l.Reason = reason
	}
	if to != LeaseApproved {
		l.SettledAt = now.UTC()
	}
	return *l, nil
}

func (r *LeaseRegistry) updateAuthoritativeTarget(leaseID string, target pythonruntime.CanonicalTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.leases[leaseID]; ok {
		l.AuthoritativeTarget = target
	}
}

// DigestArgs computes a deterministic SHA-256 hex digest over reviewed operation
// arguments so a lease cannot be replayed with altered arguments.
func DigestArgs(args map[string]any) string {
	if len(args) == 0 {
		sum := sha256.Sum256([]byte("{}"))
		return hex.EncodeToString(sum[:])
	}
	// Go's encoding/json sorts map keys lexicographically.
	raw, err := json.Marshal(args)
	if err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%v", args)))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RecheckFunc revalidates live authority and resolves the authoritative target
// identity after a held approval wait, before consuming the one-use lease.
type RecheckFunc func(ctx context.Context, lease ApprovalLease) (pythonruntime.CanonicalTarget, error)

// HoldRequest describes a reached operation entering a held approval wait.
type HoldRequest struct {
	ExecutionID         string
	OperationID         string
	CallID              string
	Actor               string
	View                string
	Surface             string
	Family              pythonruntime.EffectFamily
	Operation           string
	Target              pythonruntime.CanonicalTarget
	AuthoritativeTarget pythonruntime.CanonicalTarget
	Args                map[string]any
	Reason              string
	Reservation         pythonruntime.ParkedGuestReservation
	StopLatch           *pythonruntime.StopLatch
	Recheck             RecheckFunc
}

// HoldResult summarizes how a held approval wait settled.
type HoldResult struct {
	LeaseID             string
	State               LeaseState
	AuthoritativeTarget pythonruntime.CanonicalTarget
	Reason              string
}

// RestartSettlementReport summarizes host restart settlement of interrupted
// guests, pending leases, and parked reservations.
type RestartSettlementReport struct {
	SettledLeaseIDs        []string  `json:"settled_lease_ids,omitempty"`
	ReleasedParkedGuests   int       `json:"released_parked_guests"`
	LatchedStopsTriggered  int       `json:"latched_stops_triggered"`
	SettledAt              time.Time `json:"settled_at"`
}

type approvalResolution struct {
	state  LeaseState
	reason string
}

type activeWaiter struct {
	leaseID string
	execID  string
	latch   *pythonruntime.StopLatch
	ch      chan approvalResolution
}

// ApprovalBroker coordinates held approvals, one-use leases, parked guest
// capacity tracking, cancellation, and host-restart settlement.
type ApprovalBroker struct {
	registry      *LeaseRegistry
	store         DurableContinuationStore
	parked        *pythonruntime.ParkedGuestTracker
	clock         pythonruntime.Clock
	onHoldCreated func(ApprovalLease)

	seq atomic.Uint64

	mu      sync.Mutex
	waiters map[string]*activeWaiter
}

// ApprovalBrokerConfig configures an ApprovalBroker.
type ApprovalBrokerConfig struct {
	Registry      *LeaseRegistry
	Store         DurableContinuationStore
	ParkedTracker *pythonruntime.ParkedGuestTracker
	Clock         pythonruntime.Clock
	OnHoldCreated func(ApprovalLease)
}

// NewApprovalBroker constructs an ApprovalBroker.
func NewApprovalBroker(cfg ApprovalBrokerConfig) *ApprovalBroker {
	reg := cfg.Registry
	if reg == nil {
		reg = NewLeaseRegistry()
	}
	clk := cfg.Clock
	if clk == nil {
		clk = pythonruntime.NewSystemClock()
	}
	return &ApprovalBroker{
		registry:      reg,
		store:         cfg.Store,
		parked:        cfg.ParkedTracker,
		clock:         clk,
		onHoldCreated: cfg.OnHoldCreated,
		waiters:       make(map[string]*activeWaiter),
	}
}

// Registry returns the underlying one-use LeaseRegistry.
func (b *ApprovalBroker) Registry() *LeaseRegistry {
	return b.registry
}

// ParkedTracker returns the aggregate parked-guest tracker, if configured.
func (b *ApprovalBroker) ParkedTracker() *pythonruntime.ParkedGuestTracker {
	return b.parked
}

// WaitForApproval parks the guest internally, issues a pending one-use
// ApprovalLease, and blocks until the lease is approved, declined, cancelled,
// or invalidated by a host restart.
func (b *ApprovalBroker) WaitForApproval(ctx context.Context, req HoldRequest) (HoldResult, error) {
	if req.ExecutionID == "" {
		return HoldResult{}, errors.New("hold request missing execution_id")
	}
	if req.OperationID == "" {
		return HoldResult{}, errors.New("hold request missing operation_id")
	}

	res := req.Reservation
	if res.ExecutionID == "" {
		res.ExecutionID = req.ExecutionID
	}
	if res.ReviewLeases <= 0 {
		res.ReviewLeases = 1
	}
	if b.parked != nil {
		if err := b.parked.Park(res); err != nil {
			return HoldResult{}, err
		}
		defer b.parked.Release(res.ExecutionID)
	}

	authTarget := req.AuthoritativeTarget
	if authTarget.Namespace == "" && authTarget.RelPath == "" {
		authTarget = req.Target
	}
	argsDigest := DigestArgs(req.Args)
	leaseID := fmt.Sprintf("lease-%s-%d", req.ExecutionID, b.seq.Add(1))
	lease := ApprovalLease{
		LeaseID:             leaseID,
		ExecutionID:         req.ExecutionID,
		OperationID:         req.OperationID,
		CallID:              req.CallID,
		Actor:               req.Actor,
		View:                req.View,
		Surface:             req.Surface,
		Family:              req.Family,
		Operation:           req.Operation,
		Target:              req.Target,
		AuthoritativeTarget: authTarget,
		ArgsDigest:          argsDigest,
		State:               LeasePending,
		Reason:              req.Reason,
		CreatedAt:           b.clock.Now().UTC(),
	}
	if err := b.registry.Create(lease); err != nil {
		return HoldResult{}, err
	}
	if b.store != nil {
		if err := b.store.SaveLease(ctx, lease); err != nil {
			return HoldResult{}, fmt.Errorf("persist pending lease: %w", err)
		}
	}

	waiter := &activeWaiter{
		leaseID: leaseID,
		execID:  req.ExecutionID,
		latch:   req.StopLatch,
		ch:      make(chan approvalResolution, 1),
	}
	b.mu.Lock()
	b.waiters[leaseID] = waiter
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiters, leaseID)
		b.mu.Unlock()
	}()

	if b.onHoldCreated != nil {
		b.onHoldCreated(lease)
	}

	var resolution approvalResolution
	select {
	case resolution = <-waiter.ch:
	case <-ctx.Done():
		if updated, err := b.registry.transition(leaseID, []LeaseState{LeasePending}, LeaseCancelled, ctx.Err().Error(), b.clock.Now()); err == nil && b.store != nil {
			_ = b.store.SaveLease(context.Background(), updated)
		}
		return HoldResult{
			LeaseID:             leaseID,
			State:               LeaseCancelled,
			AuthoritativeTarget: authTarget,
			Reason:              ctx.Err().Error(),
		}, nil
	}

	if resolution.state != LeaseApproved {
		return HoldResult{
			LeaseID:             leaseID,
			State:               resolution.state,
			AuthoritativeTarget: authTarget,
			Reason:              resolution.reason,
		}, nil
	}

	// Recheck live authority and authoritative resolved target after wait.
	if req.Recheck != nil {
		recheckedTarget, err := req.Recheck(ctx, lease)
		if err != nil {
			if updated, tErr := b.registry.transition(leaseID, []LeaseState{LeaseApproved}, LeaseDeclined, err.Error(), b.clock.Now()); tErr == nil && b.store != nil {
				_ = b.store.SaveLease(context.Background(), updated)
			}
			return HoldResult{
				LeaseID:             leaseID,
				State:               LeaseDeclined,
				AuthoritativeTarget: authTarget,
				Reason:              err.Error(),
			}, nil
		}
		if recheckedTarget.Namespace != "" || recheckedTarget.RelPath != "" {
			authTarget = recheckedTarget
			b.registry.updateAuthoritativeTarget(leaseID, authTarget)
		}
	}

	if err := b.registry.Consume(leaseID, req.ExecutionID, req.OperationID, authTarget, argsDigest, b.clock.Now()); err != nil {
		return HoldResult{
			LeaseID:             leaseID,
			State:               LeaseDeclined,
			AuthoritativeTarget: authTarget,
			Reason:              err.Error(),
		}, err
	}
	if b.store != nil {
		if consumed, ok := b.registry.Get(leaseID); ok {
			_ = b.store.SaveLease(context.Background(), consumed)
		}
	}

	return HoldResult{
		LeaseID:             leaseID,
		State:               LeaseConsumed,
		AuthoritativeTarget: authTarget,
		Reason:              resolution.reason,
	}, nil
}

// Approve approves a pending live lease. Stale approvals arriving after host
// restart are rejected with ErrStaleApprovalAfterRestart and never start a
// replacement execution.
func (b *ApprovalBroker) Approve(leaseID, reason string) error {
	return b.resolve(leaseID, LeaseApproved, reason)
}

// Decline declines a pending live lease.
func (b *ApprovalBroker) Decline(leaseID, reason string) error {
	return b.resolve(leaseID, LeaseDeclined, reason)
}

// Cancel cancels a pending live lease.
func (b *ApprovalBroker) Cancel(leaseID, reason string) error {
	return b.resolve(leaseID, LeaseCancelled, reason)
}

func (b *ApprovalBroker) resolve(leaseID string, target LeaseState, reason string) error {
	updated, err := b.registry.transition(leaseID, []LeaseState{LeasePending}, target, reason, b.clock.Now())
	if err != nil {
		return err
	}
	if b.store != nil {
		if saveErr := b.store.SaveLease(context.Background(), updated); saveErr != nil {
			return saveErr
		}
	}
	b.mu.Lock()
	w, ok := b.waiters[leaseID]
	b.mu.Unlock()
	if ok {
		select {
		case w.ch <- approvalResolution{state: target, reason: reason}:
		default:
		}
	}
	return nil
}

// ReconcileOnStartup hydrates a fresh ApprovalBroker from its
// DurableContinuationStore after a host restart, marks every orphaned
// pending/unconsumed lease as LeaseLostOnRestart, persists the settlement
// durably, and ensures any stale approval arriving afterward is rejected with
// ErrStaleApprovalAfterRestart.
func (b *ApprovalBroker) ReconcileOnStartup(ctx context.Context, reason string) (RestartSettlementReport, error) {
	if b.store == nil {
		return RestartSettlementReport{}, errors.New("approval broker requires DurableContinuationStore for startup reconciliation")
	}
	if reason == "" {
		reason = "host restarted; orphaned pending approval settled as lost"
	}
	snap, err := b.store.Load(ctx)
	if err != nil {
		return RestartSettlementReport{}, err
	}

	now := b.clock.Now().UTC()
	report := RestartSettlementReport{
		SettledAt: now,
	}
	var updatedLeases []ApprovalLease

	for _, lease := range snap.Leases {
		if lease.State == LeasePending || lease.State == LeaseApproved {
			lease.State = LeaseLostOnRestart
			lease.Reason = reason
			lease.SettledAt = now
			report.SettledLeaseIDs = append(report.SettledLeaseIDs, lease.LeaseID)
			updatedLeases = append(updatedLeases, lease)
		}
		if _, exists := b.registry.Get(lease.LeaseID); !exists {
			if err := b.registry.Create(lease); err != nil {
				return RestartSettlementReport{}, err
			}
		}
	}

	if b.parked != nil {
		report.ReleasedParkedGuests = b.parked.ReleaseAll()
	}
	if err := b.store.SaveRestartSettlement(ctx, report, updatedLeases); err != nil {
		return RestartSettlementReport{}, err
	}
	return report, nil
}

// SettleInterruptedOnRestart marks all in-flight leases lost on host restart,
// seals guest output via registered StopLatches, releases all parked-guest
// reservations, and unblocks any waiting goroutines. Any stale approval
// arriving afterward is rejected with ErrStaleApprovalAfterRestart.
func (b *ApprovalBroker) SettleInterruptedOnRestart(reason string) RestartSettlementReport {
	if reason == "" {
		reason = "host restarted while guest was parked awaiting approval"
	}
	now := b.clock.Now().UTC()
	report := RestartSettlementReport{
		SettledAt: now,
	}

	// Snapshot active waiters and invalidate all pending/unconsumed leases.
	b.mu.Lock()
	waiters := make([]*activeWaiter, 0, len(b.waiters))
	for _, w := range b.waiters {
		waiters = append(waiters, w)
	}
	b.mu.Unlock()

	var updatedLeases []ApprovalLease
	for _, lease := range b.registry.List() {
		if lease.State == LeasePending || lease.State == LeaseApproved {
			if updated, err := b.registry.transition(lease.LeaseID, []LeaseState{LeasePending, LeaseApproved}, LeaseLostOnRestart, reason, now); err == nil {
				report.SettledLeaseIDs = append(report.SettledLeaseIDs, lease.LeaseID)
				updatedLeases = append(updatedLeases, updated)
			}
		}
	}

	for _, w := range waiters {
		if w.latch != nil {
			if w.latch.Trigger(pythonruntime.TerminalStop{
				Code:      pythonruntime.StopCancelled,
				Reason:    reason,
				LatchedAt: now,
			}) {
				report.LatchedStopsTriggered++
			}
		}
		select {
		case w.ch <- approvalResolution{state: LeaseLostOnRestart, reason: reason}:
		default:
		}
	}

	if b.parked != nil {
		report.ReleasedParkedGuests = b.parked.ReleaseAll()
	}
	if b.store != nil {
		_ = b.store.SaveRestartSettlement(context.Background(), report, updatedLeases)
	}

	return report
}
