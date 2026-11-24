package watch

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrSessionStopped reports an operation fenced by an active or durable session stop.
var ErrSessionStopped = errors.New("session is stopped")

// StopSummary describes the watch cleanup performed across a stopped session tree.
type StopSummary struct {
	CanceledCount   int
	CanceledHandles []string
}

// FenceSessions writes a durable stop fence across sessionIDs.
func (s *Store) FenceSessions(ctx context.Context, sessionIDs []string) error {
	if !s.Configured() {
		return nil
	}
	ids := cleanSessionIDs(sessionIDs)
	if len(ids) == 0 {
		return nil
	}
	nowStr := s.clock.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO watch_session_state
			(session_id, next_seq, stop_fence_seq, stopped_at, wake_retry_due_at, queued_wake_batch_id)
			VALUES (?, 1, 1, ?, NULL, NULL)
			ON CONFLICT(session_id) DO UPDATE SET
				stop_fence_seq = watch_session_state.stop_fence_seq + 1,
				stopped_at = excluded.stopped_at,
				wake_retry_due_at = NULL`,
			id, nowStr); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, id := range ids {
		s.heap.Remove(KindTimerTick, "wake-retry:"+id)
	}
	return nil
}

// FenceAndCancelSessions fences sessionIDs durably and reconciles their watches and uncommitted reservations.
func (s *Store) FenceAndCancelSessions(ctx context.Context, sessionIDs []string) (StopSummary, error) {
	if err := s.FenceSessions(ctx, sessionIDs); err != nil {
		return StopSummary{}, err
	}
	return s.ReconcileStoppedSessions(ctx, sessionIDs...)
}

// ReconcileStoppedSessions cancels live watches and uncommitted reservations in stopped sessions.
func (s *Store) ReconcileStoppedSessions(ctx context.Context, sessionIDs ...string) (StopSummary, error) {
	if !s.Configured() {
		return StopSummary{}, nil
	}
	ids := cleanSessionIDs(sessionIDs)
	if len(ids) == 0 {
		return StopSummary{}, nil
	}
	now := s.clock.Now()
	nowStr := now.UTC().Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StopSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var summary StopSummary
	var expiredIDs, leaseIDs []string
	for _, id := range ids {
		armedIDs, err := interruptArmedSessionLeasesTx(ctx, tx, id, nowStr)
		if err != nil {
			return StopSummary{}, err
		}
		leaseIDs = append(leaseIDs, armedIDs...)
		if err := releaseSessionUncommittedDeliveriesTx(ctx, tx, id); err != nil {
			return StopSummary{}, err
		}
		regs, err := loadSessionLiveRegistrationsTx(ctx, tx, id)
		if err != nil {
			return StopSummary{}, err
		}
		for _, reg := range regs {
			if _, _, err := cancelRegistrationTx(ctx, tx, reg, OutcomeCanceled, now); err != nil {
				return StopSummary{}, err
			}
			expiredIDs = append(expiredIDs, reg.WatchID)
			summary.CanceledCount++
			summary.CanceledHandles = append(summary.CanceledHandles, reg.Handle)
		}
	}
	if err := tx.Commit(); err != nil {
		return StopSummary{}, err
	}
	for _, wID := range expiredIDs {
		retireWatchRuntime(s, wID)
	}
	for _, lID := range leaseIDs {
		s.heap.Remove(KindWaitDeadline, lID)
	}
	for _, id := range ids {
		s.heap.Remove(KindTimerTick, "wake-retry:"+id)
		_ = s.Standing().SettleRun(ctx, id, RunCanceled)
	}
	return summary, nil
}

// ClearSessionStopFence clears the stopped_at gate for a new human turn without reviving canceled watches.
func (s *Store) ClearSessionStopFence(ctx context.Context, sessionID string) error {
	if !s.Configured() {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE watch_session_state SET stopped_at = NULL WHERE session_id = ?`,
		sessionID)
	return err
}

// IsSessionStopped reports whether sessionID is currently stopping in memory or fenced durably.
func (s *Store) IsSessionStopped(ctx context.Context, sessionID string) (bool, error) {
	if !s.Configured() {
		return false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	if isTreeStopping(s, sessionID) {
		return true, nil
	}
	var stoppedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT stopped_at FROM watch_session_state WHERE session_id = ?`,
		sessionID).Scan(&stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stoppedAt.Valid && strings.TrimSpace(stoppedAt.String) != "", nil
}

func isStoppedTx(ctx context.Context, tx *sql.Tx, sessionID string) (bool, error) {
	var stoppedAt sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT stopped_at FROM watch_session_state WHERE session_id = ?`,
		strings.TrimSpace(sessionID)).Scan(&stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stoppedAt.Valid && strings.TrimSpace(stoppedAt.String) != "", nil
}

func releaseSessionUncommittedDeliveriesTx(ctx context.Context, tx *sql.Tx, sessionID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT delivery_id FROM watch_deliveries WHERE session_id = ? AND state = 'reserved'`,
		sessionID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE watch_matches SET reservation_state = 'pending', delivery_id = NULL WHERE delivery_id = ? AND reservation_state = 'reserved'`,
			id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM watch_deliveries WHERE delivery_id = ? AND state = 'reserved'`,
			id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE watch_session_state SET queued_wake_batch_id = NULL, wake_retry_due_at = NULL WHERE session_id = ?`,
		sessionID)
	return err
}

func loadSessionLiveRegistrationsTx(ctx context.Context, tx *sql.Tx, sessionID string) ([]Registration, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND state = 'live' ORDER BY seq ASC`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Registration
	for rows.Next() {
		reg, err := scanRegistration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

func interruptArmedSessionLeasesTx(ctx context.Context, tx *sql.Tx, sessionID, nowStr string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM wait_leases WHERE session_id = ? AND status = 'armed'`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE wait_leases SET status = 'canceled', resolved_at = ? WHERE id = ? AND status = 'armed'`,
			nowStr, id); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM watch_wait_selections WHERE lease_id = ?`,
			id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func cleanSessionIDs(raw []string) []string {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
