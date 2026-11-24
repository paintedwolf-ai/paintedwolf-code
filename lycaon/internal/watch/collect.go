package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CollectedBatch holds matches reserved for one model request and committed after its response.
type CollectedBatch struct {
	DeliveryID     string                       `json:"delivery_id"`
	RequestKey     string                       `json:"request_key"`
	Matches        []RecordedMatch              `json:"matches"`
	Overflow       *OverflowSummary             `json:"overflow,omitempty"`
	ActiveHandles  []string                     `json:"active_handles,omitempty"`
	DecisionStatus map[string]map[string]string `json:"decision_status,omitempty"`
}

func redactRecordedMatch(m RecordedMatch, watchID, reason string) RecordedMatch {
	ref := strings.TrimSpace(m.RetainedRef)
	if ref == "" {
		ref = "restricted:" + strings.TrimSpace(watchID)
	}
	payload := map[string]any{
		"kind":         m.SourceKind,
		"outcome":      "redacted",
		"restricted":   true,
		"retained_ref": ref,
	}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = strings.TrimSpace(reason)
	}
	raw, _ := json.Marshal(payload)
	m.Outcome = "revoked"
	m.Restricted = true
	m.RetainedRef = ref
	m.Evidence = raw
	return m
}

// ReserveCollectBatch reserves a bounded batch of pending matches for an eligible model request.
// Replaying the same requestKey returns the identical frozen batch.
func ReserveCollectBatch(ctx context.Context, s *Store, owner Owner, requestKey string) (CollectedBatch, bool, error) {
	if !s.Configured() {
		return CollectedBatch{}, false, nil
	}
	owner = owner.Normalized()
	requestKey = strings.TrimSpace(requestKey)
	if owner.SessionID == "" || requestKey == "" {
		return CollectedBatch{}, false, nil
	}
	now := s.clock.Now()
	nowStr := now.UTC().Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectedBatch{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if existing, ok, err := findRequestBatchTx(ctx, tx, owner.SessionID, requestKey); err != nil || ok {
		if err != nil {
			return CollectedBatch{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return CollectedBatch{}, false, err
		}
		return existing, len(existing.Matches) > 0, nil
	}

	regs, err := loadOwnerRegistrationsTx(ctx, tx, owner)
	if err != nil {
		return CollectedBatch{}, false, err
	}
	var claimed []RecordedMatch
	var matchIDs, watchIDs []string
	var overflow *OverflowSummary
	var decisionStatus map[string]map[string]string
	usedBytes := 0

	for _, reg := range regs {
		pending, err := loadPendingMatchesTx(ctx, tx, reg.WatchID)
		if err != nil {
			return CollectedBatch{}, false, err
		}
		if len(pending) == 0 {
			continue
		}
		allowed, revokeReason := s.checkWatchAuthority(ctx, reg)
		if overflow == nil && reg.Lifecycle.Overflow != nil {
			cp := *reg.Lifecycle.Overflow
			overflow = &cp
		}
		tookFromWatch := false
		for _, m := range pending {
			m.Handle = reg.Handle
			if !allowed {
				m = redactRecordedMatch(m, reg.WatchID, revokeReason)
			}
			entryBytes := len(m.Evidence) + 64
			if len(claimed) > 0 && usedBytes+entryBytes > MaxBatchBytes {
				break
			}
			claimed = append(claimed, m)
			matchIDs = append(matchIDs, m.MatchID)
			usedBytes += entryBytes
			tookFromWatch = true
		}
		if tookFromWatch {
			watchIDs = append(watchIDs, reg.WatchID)
			if ds := extractDecisionStatusMap(reg.Cursors); len(ds) > 0 {
				if decisionStatus == nil {
					decisionStatus = make(map[string]map[string]string)
				}
				decisionStatus[reg.Handle] = ds
			}
		}
	}
	if len(claimed) == 0 {
		return CollectedBatch{}, false, nil
	}

	deliveryID := uuid.NewString()
	for _, id := range matchIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE watch_matches SET reservation_state = 'reserved', delivery_id = ? WHERE match_id = ? AND reservation_state = 'pending'`,
			deliveryID, id); err != nil {
			return CollectedBatch{}, false, err
		}
	}
	batch := CollectedBatch{
		DeliveryID:     deliveryID,
		RequestKey:     requestKey,
		Matches:        claimed,
		Overflow:       overflow,
		ActiveHandles:  activeHandleNames(regs),
		DecisionStatus: decisionStatus,
	}
	mJSON, _ := json.Marshal(matchIDs)
	wJSON, _ := json.Marshal(watchIDs)
	pJSON, err := json.Marshal(batch)
	if err != nil {
		return CollectedBatch{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_deliveries
		(delivery_id, session_id, worker_job_id, destination_kind, destination_id, match_ids_json, watch_ids_json, payload_json, state, created_at)
		VALUES (?, ?, NULLIF(?, ''), 'request', ?, ?, ?, ?, 'reserved', ?)`,
		deliveryID, owner.SessionID, owner.WorkerJobID, requestKey,
		string(mJSON), string(wJSON), string(pJSON), nowStr); err != nil {
		return CollectedBatch{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return CollectedBatch{}, false, err
	}
	for _, m := range claimed {
		_, _ = ClaimPresentation(ctx, s, owner, m.OccurrenceID, ClaimCollectedFrame, false, deliveryID)
	}
	return batch, true, nil
}

// CommitCollectBatch marks a reserved request batch delivered after the model response succeeds.
func CommitCollectBatch(ctx context.Context, s *Store, sessionID, deliveryID string) error {
	if !s.Configured() || strings.TrimSpace(deliveryID) == "" {
		return nil
	}
	nowStr := s.clock.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var watchIDsJSON, payloadJSON, workerJobID, state string
	err = tx.QueryRowContext(ctx,
		`SELECT watch_ids_json, payload_json, COALESCE(worker_job_id, ''), state FROM watch_deliveries WHERE delivery_id = ? AND session_id = ?`,
		strings.TrimSpace(deliveryID), strings.TrimSpace(sessionID)).Scan(&watchIDsJSON, &payloadJSON, &workerJobID, &state)
	if errors.Is(err, sql.ErrNoRows) || state == "committed" {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_matches SET reservation_state = 'delivered' WHERE delivery_id = ? AND reservation_state = 'reserved'`,
		deliveryID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_deliveries SET state = 'committed', committed_at = ? WHERE delivery_id = ?`,
		nowStr, deliveryID); err != nil {
		return err
	}
	var watchIDs []string
	_ = json.Unmarshal([]byte(watchIDsJSON), &watchIDs)
	for _, wID := range watchIDs {
		reg, err := loadRegistrationByIDTx(ctx, tx, wID)
		if err == nil {
			if err := releaseCompletedOneShotSlotTx(ctx, tx, &reg, nowStr); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	owner := SessionOwner(sessionID)
	if workerJobID != "" {
		owner = JobOwner(sessionID, workerJobID)
	}
	var batch CollectedBatch
	if json.Unmarshal([]byte(payloadJSON), &batch) == nil {
		for _, m := range batch.Matches {
			_ = MarkPresentationAdmitted(ctx, s, owner, m.OccurrenceID, deliveryID)
		}
	}
	return nil
}

// ReleaseCollectBatch releases an uncommitted request reservation on render failure or turn abort.
func ReleaseCollectBatch(ctx context.Context, s *Store, sessionID, deliveryID string) error {
	if !s.Configured() || strings.TrimSpace(deliveryID) == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var state, payloadJSON, workerJobID string
	err = tx.QueryRowContext(ctx,
		`SELECT state, payload_json, COALESCE(worker_job_id, '') FROM watch_deliveries WHERE delivery_id = ? AND session_id = ?`,
		strings.TrimSpace(deliveryID), strings.TrimSpace(sessionID)).Scan(&state, &payloadJSON, &workerJobID)
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
	if err := tx.Commit(); err != nil {
		return err
	}
	owner := SessionOwner(sessionID)
	if workerJobID != "" {
		owner = JobOwner(sessionID, workerJobID)
	}
	var batch CollectedBatch
	if json.Unmarshal([]byte(payloadJSON), &batch) == nil {
		for _, m := range batch.Matches {
			_, _ = ClaimPresentation(ctx, s, owner, m.OccurrenceID, ClaimWatchEvidence, false, "")
		}
	}
	return nil
}

// HasActionableWatches reports whether owner still has live watches or undrained matches after any active reservation.
func HasActionableWatches(ctx context.Context, s *Store, owner Owner) (bool, error) {
	if !s.Configured() {
		return false, nil
	}
	owner = owner.Normalized()
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM watch_registrations wr
		WHERE wr.session_id = ? AND wr.owner_kind = ? AND COALESCE(wr.worker_job_id, '') = ?
		  AND (wr.state = 'live' OR EXISTS (
		      SELECT 1 FROM watch_matches wm WHERE wm.watch_id = wr.watch_id AND wm.reservation_state = 'pending'
		  ))`,
		owner.SessionID, string(owner.Kind), owner.WorkerJobID).Scan(&count)
	return count > 0, err
}

// FormatCollectedBatch formats a reserved watch batch as a trusted host block.
func FormatCollectedBatch(batch CollectedBatch) string {
	if len(batch.Matches) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Watch observations (receipt %s):", batch.DeliveryID)
	for _, m := range batch.Matches {
		fmt.Fprintf(&b, "\n- %s [%s/%s]: %s", m.Handle, m.SourceKind, m.Outcome, string(m.Evidence))
	}
	if batch.Overflow != nil && batch.Overflow.CompactedCount > 0 {
		fmt.Fprintf(&b, "\n- compacted: %d earlier matches (seq %d..%d, outcomes=%s, had_failure=%v, had_recovery=%v)",
			batch.Overflow.CompactedCount, batch.Overflow.FirstSeq, batch.Overflow.LastSeq,
			strings.Join(batch.Overflow.Outcomes, ","), batch.Overflow.HadFailure, batch.Overflow.HadRecovery)
	}
	if len(batch.DecisionStatus) > 0 {
		rawStatus, _ := json.Marshal(batch.DecisionStatus)
		fmt.Fprintf(&b, "\n- decision_status: %s", string(rawStatus))
	}
	return b.String()
}

func findRequestBatchTx(ctx context.Context, tx *sql.Tx, sessionID, requestKey string) (CollectedBatch, bool, error) {
	var payload, state string
	err := tx.QueryRowContext(ctx,
		`SELECT payload_json, state FROM watch_deliveries WHERE session_id = ? AND destination_kind = 'request' AND destination_id = ?`,
		sessionID, requestKey).Scan(&payload, &state)
	if errors.Is(err, sql.ErrNoRows) || state == "released" {
		return CollectedBatch{}, false, nil
	}
	if err != nil {
		return CollectedBatch{}, false, err
	}
	var batch CollectedBatch
	if err := json.Unmarshal([]byte(payload), &batch); err != nil {
		return CollectedBatch{}, false, err
	}
	return batch, true, nil
}

func loadOwnerRegistrationsTx(ctx context.Context, tx *sql.Tx, owner Owner) ([]Registration, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+registrationColumns+` FROM watch_registrations
		WHERE session_id = ? AND owner_kind = ? AND COALESCE(worker_job_id, '') = ?
		ORDER BY seq ASC`,
		owner.SessionID, string(owner.Kind), owner.WorkerJobID)
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
