package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
)

// RetireOwner cancels all remaining live watches belonging to a closed session or completed worker job.
func RetireOwner(ctx context.Context, s *Store, owner Owner) ([]CancelReceipt, error) {
	if !s.Configured() {
		return nil, nil
	}
	owner = owner.Normalized()
	now := s.clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT `+registrationColumns+` FROM watch_registrations
		WHERE session_id = ? AND owner_kind = ? AND COALESCE(worker_job_id, '') = ? AND state = 'live'
		ORDER BY seq ASC`,
		owner.SessionID, string(owner.Kind), owner.WorkerJobID)
	if err != nil {
		return nil, err
	}
	var regs []Registration
	for rows.Next() {
		reg, err := scanRegistration(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		regs = append(regs, reg)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var receipts []CancelReceipt
	var allWoke []wokeLease
	for _, reg := range regs {
		rcpt, woke, err := cancelRegistrationTx(ctx, tx, reg, OutcomeCanceled, now)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, rcpt)
		allWoke = append(allWoke, woke...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, reg := range regs {
		s.heap.Remove(KindWatchExpiry, reg.WatchID)
	}
	for _, w := range allWoke {
		s.heap.Remove(KindWaitDeadline, w.lease.ID)
		s.notifyLeaseWake(ctx, w.lease, w.winner)
	}
	return receipts, nil
}

func cancelRegistrationTx(ctx context.Context, tx *sql.Tx, reg Registration, outcome TerminalOutcome, now time.Time) (CancelReceipt, []wokeLease, error) {
	if reg.Lifecycle.FinalReceiptID != "" {
		var saved string
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(final_receipt_json, '') FROM watch_registrations WHERE watch_id = ?`,
			reg.WatchID).Scan(&saved)
		if err == nil && saved != "" {
			var receipt CancelReceipt
			if err := json.Unmarshal([]byte(saved), &receipt); err == nil {
				return receipt, nil, nil
			}
		}
	}

	unclaimed, err := loadPendingMatchesTx(ctx, tx, reg.WatchID)
	if err != nil {
		return CancelReceipt{}, nil, err
	}
	priorRefs, err := listDeliveryIDsForWatchTx(ctx, tx, reg.WatchID)
	if err != nil {
		return CancelReceipt{}, nil, err
	}

	nowStr := now.UTC().Format(time.RFC3339Nano)
	receiptID := uuid.NewString()
	finalOutcome := outcome
	if reg.Lifecycle.State == StateTerminal && reg.Lifecycle.Outcome != "" {
		finalOutcome = reg.Lifecycle.Outcome
	}

	var matchIDs []string
	var bounded []RecordedMatch
	usedBytes := 0
	for _, m := range unclaimed {
		m.Handle = reg.Handle
		matchIDs = append(matchIDs, m.MatchID)
		entryBytes := len(m.Evidence) + 64
		if len(bounded) == 0 || usedBytes+entryBytes <= MaxBatchBytes {
			bounded = append(bounded, m)
			usedBytes += entryBytes
		}
	}
	for _, id := range matchIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE watch_matches SET reservation_state = 'canceled', delivery_id = ? WHERE match_id = ?`,
			receiptID, id); err != nil {
			return CancelReceipt{}, nil, err
		}
	}

	receipt := CancelReceipt{
		ReceiptID:         receiptID,
		WatchID:           reg.WatchID,
		Handle:            reg.Handle,
		State:             StateTerminal,
		Outcome:           finalOutcome,
		UnclaimedMatches:  bounded,
		Overflow:          reg.Lifecycle.Overflow,
		PriorDeliveryRefs: priorRefs,
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		return CancelReceipt{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations
		SET state = 'terminal', terminal_outcome = ?, slot_released = 1,
		    final_receipt_id = ?, final_receipt_json = ?,
		    ended_at = COALESCE(ended_at, ?), revision = revision + 1, updated_at = ?
		WHERE watch_id = ?`,
		string(finalOutcome), receiptID, string(receiptBytes), nowStr, nowStr, reg.WatchID,
	); err != nil {
		return CancelReceipt{}, nil, err
	}

	matchIDJSON, _ := json.Marshal(matchIDs)
	watchIDJSON, _ := json.Marshal([]string{reg.WatchID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_deliveries
		(delivery_id, session_id, worker_job_id, destination_kind, destination_id, match_ids_json, watch_ids_json, payload_json, state, created_at, committed_at)
		VALUES (?, ?, NULLIF(?, ''), 'final_receipt', ?, ?, ?, ?, 'committed', ?, ?)`,
		receiptID, reg.Owner.SessionID, reg.Owner.WorkerJobID, reg.WatchID,
		string(matchIDJSON), string(watchIDJSON), string(receiptBytes), nowStr, nowStr,
	); err != nil {
		return CancelReceipt{}, nil, err
	}

	woke, err := settleSelectedLeasesForWatchTx(ctx, tx, reg.WatchID, now)
	if err != nil {
		return CancelReceipt{}, nil, err
	}
	return receipt, woke, nil
}

func transitionTerminalTx(ctx context.Context, tx *sql.Tx, reg Registration, outcome TerminalOutcome, now time.Time) error {
	if reg.Lifecycle.State == StateTerminal {
		return nil
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	pending, err := countPendingMatchesTx(ctx, tx, reg.WatchID)
	if err != nil {
		return err
	}
	slotReleased := 0
	if pending == 0 {
		slotReleased = 1
	}
	_, err = tx.ExecContext(ctx, `UPDATE watch_registrations
		SET state = 'terminal', terminal_outcome = ?, slot_released = CASE WHEN slot_released = 1 THEN 1 ELSE ? END,
		    ended_at = COALESCE(ended_at, ?), revision = revision + 1, updated_at = ?
		WHERE watch_id = ? AND state = 'live'`,
		string(outcome), slotReleased, nowStr, nowStr, reg.WatchID)
	return err
}

func expireWatch(ctx context.Context, s *Store, watchID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := loadRegistrationByIDTx(ctx, tx, watchID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if reg.Lifecycle.State != StateLive || reg.Lifecycle.ExpiresAt.After(now) {
		if reg.Lifecycle.State == StateLive {
			s.heap.Upsert(KindWatchExpiry, reg.WatchID, reg.Lifecycle.ExpiresAt)
		}
		return nil
	}
	if err := transitionTerminalTx(ctx, tx, reg, OutcomeExpired, now); err != nil {
		return err
	}
	woke, err := settleSelectedLeasesForWatchTx(ctx, tx, reg.WatchID, now)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, w := range woke {
		s.heap.Remove(KindWaitDeadline, w.lease.ID)
		s.notifyLeaseWake(ctx, w.lease, w.winner)
	}
	return nil
}

// settleDueLease settles an armed wait lease in exact priority order:
// committed pending evidence first, then selected handle expiry, then call timeout.
func settleDueLease(ctx context.Context, s *Store, leaseID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var sessionID, workerJobID, projectID, deadlineStr string
	err = tx.QueryRowContext(ctx, `SELECT session_id, COALESCE(worker_job_id, ''), project_id, COALESCE(deadline_at, '')
		FROM wait_leases WHERE id = ? AND status = 'armed'`, strings.TrimSpace(leaseID)).Scan(&sessionID, &workerJobID, &projectID, &deadlineStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	regs, err := loadLeaseSelectedRegsTx(ctx, tx, leaseID)
	if err != nil {
		return err
	}
	for i := range regs {
		if regs[i].Lifecycle.State == StateLive && !regs[i].Lifecycle.ExpiresAt.After(now) {
			if err := transitionTerminalTx(ctx, tx, regs[i], OutcomeExpired, now); err != nil {
				return err
			}
			regs[i], err = loadRegistrationByIDTx(ctx, tx, regs[i].WatchID)
			if err != nil {
				return err
			}
		}
	}

	nowStr := now.UTC().Format(time.RFC3339Nano)
	owner := Owner{ProjectID: projectID, SessionID: sessionID, WorkerJobID: workerJobID}.Normalized()
	if len(regs) > 0 {
		res, ready, err := claimSelectedImmediateTx(ctx, tx, owner, regs, DestinationLease, leaseID, now)
		if err != nil {
			return err
		}
		if ready {
			winnerBytes, err := json.Marshal(res.Winner)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx,
				`UPDATE wait_leases SET status = 'resolved', winner_json = ?, updated_at = ?, resolved_at = ? WHERE id = ? AND status = 'armed'`,
				string(winnerBytes), nowStr, nowStr, leaseID)
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			for _, r := range regs {
				if r.Lifecycle.State == StateTerminal {
					s.heap.Remove(KindWatchExpiry, r.WatchID)
				}
			}
			if n, _ := result.RowsAffected(); n == 1 {
				s.notifyLeaseWake(ctx, awaitstore.Lease{ID: leaseID, SessionID: sessionID, WorkerJobID: workerJobID, ProjectID: projectID}, res.Winner)
			}
			return nil
		}
	}

	var callDeadline time.Time
	if deadlineStr != "" {
		callDeadline, _ = time.Parse(time.RFC3339Nano, deadlineStr)
	}
	if !callDeadline.IsZero() && !callDeadline.After(now) {
		timeoutWinner := awaitstore.Condition{Kind: "timer", Outcome: "timed_out"}
		if active := activeHandleNames(regs); len(active) > 0 {
			rep, _ := json.Marshal(map[string]any{"active_handles": active})
			timeoutWinner.Report = string(rep)
		}
		winnerBytes, _ := json.Marshal(timeoutWinner)
		result, err := tx.ExecContext(ctx,
			`UPDATE wait_leases SET status = 'timed_out', winner_json = ?, updated_at = ?, resolved_at = ? WHERE id = ? AND status = 'armed'`,
			string(winnerBytes), nowStr, nowStr, leaseID)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 1 {
			s.notifyLeaseWake(ctx, awaitstore.Lease{ID: leaseID, SessionID: sessionID, WorkerJobID: workerJobID, ProjectID: projectID}, timeoutWinner)
		}
		return nil
	}

	var nextDue time.Time
	if !callDeadline.IsZero() {
		nextDue = callDeadline
	}
	for _, r := range regs {
		if r.Lifecycle.State == StateLive && (nextDue.IsZero() || r.Lifecycle.ExpiresAt.Before(nextDue)) {
			nextDue = r.Lifecycle.ExpiresAt
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !nextDue.IsZero() {
		s.heap.Upsert(KindWaitDeadline, leaseID, nextDue)
	}
	return nil
}

func listDeliveryIDsForWatchTx(ctx context.Context, tx *sql.Tx, watchID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT delivery_id FROM watch_deliveries
		WHERE state = 'committed' AND destination_kind != 'final_receipt'
		  AND EXISTS (SELECT 1 FROM json_each(watch_ids_json) WHERE value = ?)
		ORDER BY created_at ASC`, watchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func inspectMatchesSummaryTx(ctx context.Context, tx *sql.Tx, watchID string) (*RecordedMatch, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT match_id, watch_id, seq, source_kind, source_occurrence_id,
		outcome, evidence_json, COALESCE(retained_ref, ''), restricted
		FROM watch_matches WHERE watch_id = ? ORDER BY seq DESC`, watchID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var latest *RecordedMatch
	var refs []string
	seenRefs := map[string]bool{}
	for rows.Next() {
		var m RecordedMatch
		var evidenceStr string
		var restricted int
		if err := rows.Scan(&m.MatchID, &m.WatchID, &m.Seq, &m.SourceKind, &m.OccurrenceID,
			&m.Outcome, &evidenceStr, &m.RetainedRef, &restricted); err != nil {
			return nil, nil, err
		}
		m.Evidence = json.RawMessage(evidenceStr)
		m.Restricted = restricted == 1
		if latest == nil {
			cp := m
			latest = &cp
		}
		if m.RetainedRef != "" && !seenRefs[m.RetainedRef] {
			seenRefs[m.RetainedRef] = true
			refs = append(refs, m.RetainedRef)
		}
	}
	return latest, refs, rows.Err()
}
