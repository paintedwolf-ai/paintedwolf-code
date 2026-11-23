package watch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CancelReceipt is the single final receipt returned when a watch is canceled.
type CancelReceipt struct {
	ReceiptID         string           `json:"receipt_id"`
	WatchID           string           `json:"watch_id"`
	Handle            string           `json:"handle"`
	State             State            `json:"state"`
	Outcome           TerminalOutcome  `json:"outcome"`
	UnclaimedMatches  []RecordedMatch  `json:"unclaimed_matches,omitempty"`
	Overflow          *OverflowSummary `json:"overflow,omitempty"`
	PriorDeliveryRefs []string         `json:"prior_delivery_refs,omitempty"`
}

// InspectView describes a watch without acknowledging pending matches.
type InspectView struct {
	WatchID         string           `json:"watch_id"`
	Handle          string           `json:"handle"`
	State           State            `json:"state"`
	Outcome         TerminalOutcome  `json:"outcome,omitempty"`
	Delivery        DeliveryMode     `json:"delivery"`
	Destination     string           `json:"destination"`
	ExpiresAt       string           `json:"expires_at"`
	PendingMatches  int              `json:"pending_matches"`
	LatestMatch     *RecordedMatch   `json:"latest_match,omitempty"`
	Overflow        *OverflowSummary `json:"overflow,omitempty"`
	LastDeliveryRef string           `json:"last_delivery_ref,omitempty"`
	RetainedRefs    []string         `json:"retained_refs,omitempty"`
	FinalReceiptID  string           `json:"final_receipt_id,omitempty"`
	Revision        int              `json:"revision"`
}

// Cancel fences observations, frees capacity immediately and returns one idempotent final receipt.
func (s *Store) Cancel(ctx context.Context, owner Owner, handle string) (CancelReceipt, error) {
	if !s.Configured() {
		return CancelReceipt{}, fmt.Errorf("watch store is not configured")
	}
	now := s.clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CancelReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := loadRegistrationByHandleTx(ctx, tx, owner, handle)
	if err != nil {
		return CancelReceipt{}, err
	}
	receipt, woke, err := cancelRegistrationTx(ctx, tx, reg, OutcomeCanceled, now)
	if err != nil {
		return CancelReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return CancelReceipt{}, err
	}
	s.heap.Remove(KindWatchExpiry, reg.WatchID)
	_ = WithdrawAmbientWakeIfNoDemand(ctx, s, owner.SessionID)
	for _, w := range woke {
		s.heap.Remove(KindWaitDeadline, w.lease.ID)
		s.notifyLeaseWake(ctx, w.lease, w.winner)
	}
	return receipt, nil
}

// Inspect reads watch state and latest evidence without acknowledging or consuming matches.
func (s *Store) Inspect(ctx context.Context, owner Owner, handle string) (InspectView, error) {
	if !s.Configured() {
		return InspectView{}, fmt.Errorf("watch store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InspectView{}, err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := loadRegistrationByHandleTx(ctx, tx, owner, handle)
	if err != nil {
		return InspectView{}, err
	}
	pendingCount, err := countPendingMatchesTx(ctx, tx, reg.WatchID)
	if err != nil {
		return InspectView{}, err
	}
	latest, refs, err := inspectMatchesSummaryTx(ctx, tx, reg.WatchID)
	if err != nil {
		return InspectView{}, err
	}
	lastDelivery, err := latestDeliveryForWatchTx(ctx, tx, reg.WatchID)
	if err != nil {
		return InspectView{}, err
	}
	return InspectView{
		WatchID:         reg.WatchID,
		Handle:          reg.Handle,
		State:           reg.Lifecycle.State,
		Outcome:         reg.Lifecycle.Outcome,
		Delivery:        reg.Lifecycle.Delivery,
		Destination:     reg.Owner.Destination(),
		ExpiresAt:       reg.Lifecycle.ExpiresAt.UTC().Format(time.RFC3339),
		PendingMatches:  pendingCount,
		LatestMatch:     latest,
		Overflow:        reg.Lifecycle.Overflow,
		LastDeliveryRef: lastDelivery,
		RetainedRefs:    refs,
		FinalReceiptID:  reg.Lifecycle.FinalReceiptID,
		Revision:        reg.Lifecycle.Revision,
	}, nil
}

// SetDelivery updates a live watch's delivery mode.
func (s *Store) SetDelivery(ctx context.Context, owner Owner, handle string, mode DeliveryMode) (DeliveryMode, error) {
	if !s.Configured() {
		return "", fmt.Errorf("watch store is not configured")
	}
	owner = owner.Normalized()
	if mode != DeliveryCollect && mode != DeliveryWake {
		return "", &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "delivery", Message: "delivery must be collect or wake"}
	}
	if owner.Kind == OwnerJob && mode == DeliveryWake {
		return "", &ValidationError{Code: "WATCH_WAKE_NOT_ALLOWED", Field: "delivery", Message: "worker watches cannot request ambient wake"}
	}
	nowStr := s.clock.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := loadRegistrationByHandleTx(ctx, tx, owner, handle)
	if err != nil {
		return "", err
	}
	if reg.Lifecycle.State == StateTerminal {
		return "", &TerminalError{Handle: reg.Handle, Outcome: reg.Lifecycle.Outcome}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_registrations SET delivery = ?, revision = revision + 1, updated_at = ? WHERE watch_id = ?`,
		string(mode), nowStr, reg.WatchID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if mode == DeliveryCollect {
		_ = WithdrawAmbientWakeIfNoDemand(ctx, s, owner.SessionID)
	} else if owner.Kind == OwnerSession && mode == DeliveryWake {
		_, _ = s.TriggerAmbientWake(ctx, owner.SessionID)
	}
	return mode, nil
}

// MarkSourceLost settles a live watch with outcome source_lost and wakes any selected wait.
func (s *Store) MarkSourceLost(ctx context.Context, watchID string) error {
	if !s.Configured() {
		return nil
	}
	now := s.clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := loadRegistrationByIDTx(ctx, tx, strings.TrimSpace(watchID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := transitionTerminalTx(ctx, tx, reg, OutcomeSourceLost, now); err != nil {
		return err
	}
	woke, err := settleSelectedLeasesForWatchTx(ctx, tx, reg.WatchID, now)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.heap.Remove(KindWatchExpiry, reg.WatchID)
	for _, w := range woke {
		s.heap.Remove(KindWaitDeadline, w.lease.ID)
		s.notifyLeaseWake(ctx, w.lease, w.winner)
	}
	return nil
}

// AdvanceDue processes overdue watch expiries and wait deadlines in deterministic priority order.
func (s *Store) AdvanceDue(ctx context.Context) error {
	if !s.Configured() {
		return nil
	}
	now := s.clock.Now()
	due := s.heap.PopDue(now)
	for _, entry := range due {
		switch entry.Key.Kind {
		case KindWatchExpiry:
			if err := expireWatch(ctx, s, entry.Key.ID, now); err != nil {
				return err
			}
		case KindWaitDeadline:
			if err := settleDueLease(ctx, s, entry.Key.ID, now); err != nil {
				return err
			}
		case KindTimerTick:
			if sessionID, ok := strings.CutPrefix(entry.Key.ID, "wake-retry:"); ok && sessionID != "" {
				_, _ = s.TriggerAmbientWake(ctx, sessionID)
			}
		case KindScheduleRun, KindSamplerProbe:
		}
	}
	return nil
}

// Recover reconstructs the deadline heap from committed rows and settles overdue items in priority order.
func (s *Store) Recover(ctx context.Context) error {
	if !s.Configured() {
		return nil
	}
	s.heap.Reset()
	rows, err := s.db.QueryContext(ctx, `SELECT watch_id, expires_at FROM watch_registrations WHERE state = 'live'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, expStr string
		if err := rows.Scan(&id, &expStr); err != nil {
			_ = rows.Close()
			return err
		}
		if exp, err := time.Parse(time.RFC3339Nano, expStr); err == nil {
			s.heap.Upsert(KindWatchExpiry, id, exp)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	leaseRows, err := s.db.QueryContext(ctx, `SELECT wl.id, COALESCE(wl.deadline_at, ''), COALESCE(MIN(wws.expires_at), '')
		FROM wait_leases wl
		LEFT JOIN watch_wait_selections wws ON wws.lease_id = wl.id
		WHERE wl.status = 'armed'
		GROUP BY wl.id, wl.deadline_at`)
	if err != nil {
		return err
	}
	for leaseRows.Next() {
		var leaseID, deadlineStr, minExpStr string
		if err := leaseRows.Scan(&leaseID, &deadlineStr, &minExpStr); err != nil {
			_ = leaseRows.Close()
			return err
		}
		var due time.Time
		if deadlineStr != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, deadlineStr); err == nil {
				due = parsed
			}
		}
		if minExpStr != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, minExpStr); err == nil && (due.IsZero() || parsed.Before(due)) {
				due = parsed
			}
		}
		if !due.IsZero() {
			s.heap.Upsert(KindWaitDeadline, leaseID, due)
		}
	}
	_ = leaseRows.Close()
	if err := leaseRows.Err(); err != nil {
		return err
	}

	retryRows, err := s.db.QueryContext(ctx,
		`SELECT session_id, wake_retry_due_at FROM watch_session_state WHERE wake_retry_due_at IS NOT NULL AND wake_retry_due_at != ''`)
	if err != nil {
		return err
	}
	for retryRows.Next() {
		var sessionID, retryStr string
		if err := retryRows.Scan(&sessionID, &retryStr); err != nil {
			_ = retryRows.Close()
			return err
		}
		if retryAt, err := time.Parse(time.RFC3339Nano, retryStr); err == nil {
			s.heap.Upsert(KindTimerTick, "wake-retry:"+sessionID, retryAt)
		}
	}
	_ = retryRows.Close()
	if err := retryRows.Err(); err != nil {
		return err
	}
	return s.AdvanceDue(ctx)
}
