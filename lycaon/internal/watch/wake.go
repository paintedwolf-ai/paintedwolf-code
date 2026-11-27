package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/observability/watchdiag"
)

// AmbientWakeGate evaluates session and workflow eligibility before admitting an ambient wake turn.
type AmbientWakeGate struct {
	SessionEligible func(ctx context.Context, sessionID string) (eligible bool, retryable bool)
	WorkflowAdmit   func(ctx context.Context, sessionID string) (allow bool, hasWorkflow bool)
	DispatchTurn    func(ctx context.Context, sessionID, submissionID, text string, onAdmitted func() error) error
}

// TryScheduleAmbientWake reserves at most one ambient wake batch for sessionID within the rolling hourly budget.
func TryScheduleAmbientWake(ctx context.Context, s *Store, sessionID string, gate AmbientWakeGate) (bool, error) {
	if !s.Configured() || gate.DispatchTurn == nil {
		return false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || isTreeStopping(s, sessionID) {
		return false, nil
	}
	if gate.SessionEligible != nil {
		eligible, _ := gate.SessionEligible(ctx, sessionID)
		if !eligible {
			return false, nil
		}
	}

	now := s.clock.Now()
	batch, retryAt, hasWork, err := prepareAmbientWakeBatch(ctx, s, sessionID, now)
	if err != nil || !hasWork {
		return false, err
	}
	if !retryAt.IsZero() {
		s.heap.Upsert(KindTimerTick, "wake-retry:"+sessionID, retryAt)
		return false, nil
	}

	if gate.WorkflowAdmit != nil {
		allow, _ := gate.WorkflowAdmit(ctx, sessionID)
		if !allow {
			_ = releaseAmbientWakeBatch(ctx, s, sessionID, batch.DeliveryID)
			return false, nil
		}
	}

	admitted := false
	err = gate.DispatchTurn(ctx, sessionID, batch.DeliveryID, FormatCollectedBatch(batch), func() error {
		admitted = true
		return commitAmbientWakeAdmission(ctx, s, sessionID, batch.DeliveryID)
	})
	if err != nil || !admitted {
		_ = releaseAmbientWakeBatch(ctx, s, sessionID, batch.DeliveryID)
		return false, err
	}
	return true, nil
}

// WithdrawAmbientWakeIfNoDemand releases a queued ambient batch when no live/pending wake watch remains.
func WithdrawAmbientWakeIfNoDemand(ctx context.Context, s *Store, sessionID string) error {
	if !s.Configured() {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var demand int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watch_registrations wr
		JOIN watch_matches wm ON wm.watch_id = wr.watch_id
		WHERE wr.session_id = ? AND wr.owner_kind = 'session' AND wr.delivery = 'wake'
		  AND wm.reservation_state IN ('pending', 'reserved')`, sessionID).Scan(&demand); err != nil {
		return err
	}
	if demand > 0 {
		return nil
	}
	var queuedID sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT queued_wake_batch_id FROM watch_session_state WHERE session_id = ?`, sessionID).Scan(&queuedID)
	if errors.Is(err, sql.ErrNoRows) || !queuedID.Valid || queuedID.String == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if err := releaseAmbientTx(ctx, tx, sessionID, queuedID.String); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.heap.Remove(KindTimerTick, "wake-retry:"+sessionID)
	return nil
}

func prepareAmbientWakeBatch(ctx context.Context, s *Store, sessionID string, now time.Time) (CollectedBatch, time.Time, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectedBatch{}, time.Time{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO watch_session_state (session_id, next_seq) VALUES (?, 1) ON CONFLICT(session_id) DO NOTHING`,
		sessionID); err != nil {
		return CollectedBatch{}, time.Time{}, false, err
	}

	var stoppedAt, admissionsJSON, queuedID sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT stopped_at, wake_admissions_json, queued_wake_batch_id FROM watch_session_state WHERE session_id = ?`,
		sessionID).Scan(&stoppedAt, &admissionsJSON, &queuedID); err != nil {
		return CollectedBatch{}, time.Time{}, false, err
	}
	if stoppedAt.Valid && stoppedAt.String != "" {
		return CollectedBatch{}, time.Time{}, false, nil
	}
	if queuedID.Valid && queuedID.String != "" {
		var payload, state string
		err := tx.QueryRowContext(ctx,
			`SELECT payload_json, state FROM watch_deliveries WHERE delivery_id = ?`, queuedID.String).Scan(&payload, &state)
		if err == nil && state == "reserved" {
			var existing CollectedBatch
			if json.Unmarshal([]byte(payload), &existing) == nil {
				_ = tx.Commit()
				return existing, time.Time{}, true, nil
			}
		}
	}

	var armedWaits int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wait_leases WHERE session_id = ? AND COALESCE(worker_job_id, '') = '' AND status = 'armed'`,
		sessionID).Scan(&armedWaits); err != nil {
		return CollectedBatch{}, time.Time{}, false, err
	}
	if armedWaits > 0 {
		return CollectedBatch{}, time.Time{}, false, nil
	}

	hasWakePending, err := hasPendingWakeMatchesTx(ctx, tx, sessionID)
	if err != nil || !hasWakePending {
		return CollectedBatch{}, time.Time{}, false, err
	}

	pruned, retryAt := checkHourlyWakeBudget(admissionsJSON.String, now)
	prunedBytes, err := json.Marshal(pruned)
	if err != nil {
		return CollectedBatch{}, time.Time{}, false, err
	}
	if !retryAt.IsZero() {
		retryStr := retryAt.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx,
			`UPDATE watch_session_state SET wake_admissions_json = ?, wake_retry_due_at = ? WHERE session_id = ?`,
			string(prunedBytes), retryStr, sessionID); err != nil {
			return CollectedBatch{}, time.Time{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return CollectedBatch{}, time.Time{}, false, err
		}
		return CollectedBatch{}, retryAt, true, nil
	}

	batch, ok, err := reserveWakeMatchesTx(ctx, tx, sessionID, now)
	if err != nil || !ok {
		return CollectedBatch{}, time.Time{}, false, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_session_state SET wake_admissions_json = ?, wake_retry_due_at = NULL, queued_wake_batch_id = ? WHERE session_id = ?`,
		string(prunedBytes), batch.DeliveryID, sessionID); err != nil {
		return CollectedBatch{}, time.Time{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return CollectedBatch{}, time.Time{}, false, err
	}
	return batch, time.Time{}, true, nil
}

func hasPendingWakeMatchesTx(ctx context.Context, tx *sql.Tx, sessionID string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watch_registrations wr
		JOIN watch_matches wm ON wm.watch_id = wr.watch_id
		WHERE wr.session_id = ? AND wr.owner_kind = 'session' AND wr.delivery = 'wake' AND wm.reservation_state = 'pending'`,
		sessionID).Scan(&n)
	return n > 0, err
}

func checkHourlyWakeBudget(rawJSON string, now time.Time) ([]string, time.Time) {
	var stamps []string
	if rawJSON != "" {
		_ = json.Unmarshal([]byte(rawJSON), &stamps)
	}
	cutoff := now.Add(-time.Hour)
	var kept []string
	var earliest time.Time
	for _, s := range stamps {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil || !t.After(cutoff) {
			continue
		}
		kept = append(kept, t.UTC().Format(time.RFC3339Nano))
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	if len(kept) >= DefaultHourlyWakeBudget {
		return kept, earliest.Add(time.Hour)
	}
	return kept, time.Time{}
}

func reserveWakeMatchesTx(ctx context.Context, tx *sql.Tx, sessionID string, now time.Time) (CollectedBatch, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+registrationColumns+` FROM watch_registrations
		WHERE session_id = ? AND owner_kind = 'session' ORDER BY seq ASC`, sessionID)
	if err != nil {
		return CollectedBatch{}, false, err
	}
	defer func() { _ = rows.Close() }()
	var regs []Registration
	for rows.Next() {
		reg, err := scanRegistration(rows)
		if err != nil {
			return CollectedBatch{}, false, err
		}
		regs = append(regs, reg)
	}
	if err := rows.Err(); err != nil {
		return CollectedBatch{}, false, err
	}
	_ = rows.Close()

	var claimed []RecordedMatch
	var matchIDs, watchIDs []string
	var overflow *OverflowSummary
	usedBytes := 0
	for _, reg := range regs {
		pending, err := loadPendingMatchesTx(ctx, tx, reg.WatchID)
		if err != nil {
			return CollectedBatch{}, false, err
		}
		if len(pending) == 0 {
			continue
		}
		if overflow == nil && reg.Lifecycle.Overflow != nil {
			cp := *reg.Lifecycle.Overflow
			overflow = &cp
		}
		took := false
		for _, m := range pending {
			m.Handle = reg.Handle
			entryBytes := len(m.Evidence) + 64
			if len(claimed) > 0 && usedBytes+entryBytes > MaxBatchBytes {
				break
			}
			claimed = append(claimed, m)
			matchIDs = append(matchIDs, m.MatchID)
			usedBytes += entryBytes
			took = true
		}
		if took {
			watchIDs = append(watchIDs, reg.WatchID)
		}
	}
	if len(claimed) == 0 {
		return CollectedBatch{}, false, nil
	}
	deliveryID := uuid.NewString()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	for _, id := range matchIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE watch_matches SET reservation_state = 'reserved', delivery_id = ? WHERE match_id = ?`,
			deliveryID, id); err != nil {
			return CollectedBatch{}, false, err
		}
	}
	batch := CollectedBatch{
		DeliveryID:    deliveryID,
		RequestKey:    deliveryID,
		Matches:       claimed,
		Overflow:      overflow,
		ActiveHandles: activeHandleNames(regs),
	}
	mJSON, err := json.Marshal(matchIDs)
	if err != nil {
		return CollectedBatch{}, false, err
	}
	wJSON, err := json.Marshal(watchIDs)
	if err != nil {
		return CollectedBatch{}, false, err
	}
	pJSON, err := json.Marshal(batch)
	if err != nil {
		return CollectedBatch{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_deliveries
		(delivery_id, session_id, destination_kind, destination_id, match_ids_json, watch_ids_json, payload_json, state, created_at)
		VALUES (?, ?, 'ambient_wake', ?, ?, ?, ?, 'reserved', ?)`,
		deliveryID, sessionID, deliveryID, string(mJSON), string(wJSON), string(pJSON), nowStr); err != nil {
		return CollectedBatch{}, false, err
	}
	return batch, true, nil
}

func commitAmbientWakeAdmission(ctx context.Context, s *Store, sessionID, deliveryID string) error {
	now := s.clock.Now()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var watchIDsJSON, payloadJSON, state string
	err = tx.QueryRowContext(ctx,
		`SELECT watch_ids_json, payload_json, state FROM watch_deliveries WHERE delivery_id = ? AND session_id = ?`,
		deliveryID, sessionID).Scan(&watchIDsJSON, &payloadJSON, &state)
	if errors.Is(err, sql.ErrNoRows) || state == "committed" {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_matches SET reservation_state = 'delivered' WHERE delivery_id = ?`, deliveryID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_deliveries SET state = 'committed', committed_at = ? WHERE delivery_id = ?`,
		nowStr, deliveryID); err != nil {
		return err
	}
	diagEnabled := watchdiag.Enabled()
	var watchIDs []string
	_ = json.Unmarshal([]byte(watchIDsJSON), &watchIDs)
	var regOwners map[string]regOwnerFact
	if diagEnabled {
		regOwners = make(map[string]regOwnerFact, len(watchIDs))
	}
	for _, wID := range watchIDs {
		if reg, err := loadRegistrationByIDTx(ctx, tx, wID); err == nil {
			if diagEnabled {
				regOwners[reg.WatchID] = regOwnerFact{
					watchID:     reg.WatchID,
					handle:      reg.Handle,
					sessionID:   reg.Owner.SessionID,
					workerJobID: reg.Owner.WorkerJobID,
				}
			}
			_ = releaseCompletedOneShotSlotTx(ctx, tx, &reg, nowStr)
		}
	}

	var rawStamps string
	var stopFenceSeq int64
	var stoppedAtRaw sql.NullString
	if diagEnabled {
		_ = tx.QueryRowContext(ctx,
			`SELECT wake_admissions_json, stop_fence_seq, stopped_at FROM watch_session_state WHERE session_id = ?`, sessionID).
			Scan(&rawStamps, &stopFenceSeq, &stoppedAtRaw)
	} else {
		_ = tx.QueryRowContext(ctx,
			`SELECT wake_admissions_json FROM watch_session_state WHERE session_id = ?`, sessionID).Scan(&rawStamps)
	}
	kept, _ := checkHourlyWakeBudget(rawStamps, now)
	kept = append(kept, nowStr)
	stampsBytes, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_session_state SET wake_admissions_json = ?, queued_wake_batch_id = CASE WHEN queued_wake_batch_id = ? THEN NULL ELSE queued_wake_batch_id END WHERE session_id = ?`,
		string(stampsBytes), deliveryID, sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	var batch CollectedBatch
	if json.Unmarshal([]byte(payloadJSON), &batch) == nil {
		for _, m := range batch.Matches {
			_ = MarkPresentationAdmitted(ctx, s, SessionOwner(sessionID), m.OccurrenceID, deliveryID)
		}
		if diagEnabled {
			historicalFence := parseHistoricalStopFence(sessionID, stopFenceSeq, stoppedAtRaw, diagClockDomain(s.clock))
			observeCommittedDeliveryBoundary(s, sessionID, "", deliveryID, "ambient_wake", true, regOwners, historicalFence, batch, now.UTC())
		}
	}
	return nil
}

func releaseAmbientWakeBatch(ctx context.Context, s *Store, sessionID, deliveryID string) error {
	if !s.Configured() || deliveryID == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := releaseAmbientTx(ctx, tx, sessionID, deliveryID); err != nil {
		return err
	}
	return tx.Commit()
}

func releaseAmbientTx(ctx context.Context, tx *sql.Tx, sessionID, deliveryID string) error {
	var state string
	err := tx.QueryRowContext(ctx,
		`SELECT state FROM watch_deliveries WHERE delivery_id = ? AND session_id = ?`,
		deliveryID, sessionID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || state != "reserved" {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_matches SET reservation_state = 'pending', delivery_id = NULL WHERE delivery_id = ? AND reservation_state = 'reserved'`,
		deliveryID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM watch_deliveries WHERE delivery_id = ? AND state = 'reserved'`,
		deliveryID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE watch_session_state SET queued_wake_batch_id = NULL WHERE session_id = ? AND queued_wake_batch_id = ?`,
		sessionID, deliveryID)
	return err
}
