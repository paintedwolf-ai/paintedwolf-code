package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

// MatchInput describes one producer observation to record against a watch.
type MatchInput struct {
	SourceKind   string
	OccurrenceID string
	Outcome      string
	Evidence     map[string]any
	RetainedRef  string
	Restricted   bool
}

// RecordedMatch is a persisted match row.
type RecordedMatch struct {
	MatchID      string          `json:"match_id"`
	WatchID      string          `json:"watch_id"`
	Handle       string          `json:"handle,omitempty"`
	Seq          int             `json:"seq"`
	SourceKind   string          `json:"source_kind"`
	OccurrenceID string          `json:"occurrence_id"`
	Outcome      string          `json:"outcome"`
	Evidence     json.RawMessage `json:"evidence"`
	RetainedRef  string          `json:"retained_ref,omitempty"`
	Restricted   bool            `json:"restricted,omitempty"`
}

// RecordMatch commits a match, advances optional cursors, compacts overflow and settles selected waits.
func (s *Store) RecordMatch(ctx context.Context, watchID string, input MatchInput, cursorUpdates map[string]string) (RecordedMatch, bool, error) {
	if !s.Configured() {
		return RecordedMatch{}, false, fmt.Errorf("watch store is not configured")
	}
	now := s.clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecordedMatch{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	preReg, err := loadRegistrationByIDTx(ctx, tx, strings.TrimSpace(watchID))
	if err != nil {
		return RecordedMatch{}, false, err
	}
	if isTreeStopping(s, preReg.Owner.SessionID) {
		return RecordedMatch{}, false, nil
	}
	if stopped, err := isStoppedTx(ctx, tx, preReg.Owner.SessionID); err != nil || stopped {
		return RecordedMatch{}, false, err
	}

	recorded, inserted, err := insertMatchTx(ctx, tx, strings.TrimSpace(watchID), input, cursorUpdates, now)
	if err != nil || !inserted {
		if err != nil {
			return RecordedMatch{}, false, err
		}
		return RecordedMatch{}, false, nil
	}
	wokeLeases, err := settleSelectedLeasesForWatchTx(ctx, tx, recorded.WatchID, now)
	if err != nil {
		return RecordedMatch{}, false, err
	}
	reg, err := loadRegistrationByIDTx(ctx, tx, recorded.WatchID)
	if err != nil {
		return RecordedMatch{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return RecordedMatch{}, false, err
	}
	if reg.Lifecycle.State == StateTerminal {
		retireWatchRuntime(s, reg.WatchID)
	}
	for _, woke := range wokeLeases {
		s.heap.Remove(KindWaitDeadline, woke.lease.ID)
		notifyLeaseWake(s, ctx, woke.lease, woke.winner)
	}
	if len(wokeLeases) == 0 && reg.Owner.Kind == OwnerSession && reg.Lifecycle.Delivery == DeliveryWake {
		_, _ = s.TriggerAmbientWake(ctx, reg.Owner.SessionID)
	}
	return recorded, true, nil
}

// ProcessPresentationOutcome summarizes how process completion matched watches and presentation claims.
type ProcessPresentationOutcome struct {
	Matches         []RecordedMatch
	Claimed         bool
	TransferredKick bool
}

// RecordProcessDone delivers a process_done completion to every matching live watch in the session.
func (s *Store) RecordProcessDone(ctx context.Context, sessionID string, fact ProcessCompletionFact) ([]RecordedMatch, error) {
	res, err := s.RecordProcessCompletion(ctx, sessionID, fact)
	return res.Matches, err
}

// RecordProcessCompletion records process_done matches and coordinates presentation claims per owner.
func (s *Store) RecordProcessCompletion(ctx context.Context, sessionID string, fact ProcessCompletionFact) (ProcessPresentationOutcome, error) {
	if !s.Configured() {
		return ProcessPresentationOutcome{}, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	handle := strings.TrimSpace(fact.Handle)
	if sessionID == "" || handle == "" {
		return ProcessPresentationOutcome{}, nil
	}
	targets, err := matchingProcessWatches(s, ctx, sessionID, handle)
	if err != nil || len(targets) == 0 {
		return ProcessPresentationOutcome{}, err
	}
	occ := strings.TrimSpace(fact.OccurrenceID)
	if occ == "" {
		occ = "process_done:" + handle
	}
	outcome := strings.TrimSpace(fact.Outcome)
	if outcome == "" {
		outcome = "satisfied"
	}
	var res ProcessPresentationOutcome
	for _, reg := range targets {
		decision, err := ClaimPresentation(ctx, s, reg.Owner, occ, ClaimWatchEvidence, false, "")
		if err != nil {
			return res, err
		}
		if decision.TransferredKick {
			res.TransferredKick = true
		}
		if decision.AlreadyAdmitted {
			_, _ = recordAlreadyAdmittedMatch(s, ctx, reg.WatchID, MatchInput{
				SourceKind:   ConditionProcessDone,
				OccurrenceID: occ,
				Outcome:      outcome,
				RetainedRef:  decision.ExistingReceiptID,
				Evidence: map[string]any{
					"kind":             ConditionProcessDone,
					"handle":           handle,
					"outcome":          outcome,
					"code":             fact.Code,
					"admitted_receipt": decision.ExistingReceiptID,
				},
			}, map[string]string{"process_done:" + handle: occ}, decision.ExistingReceiptID)
			res.Claimed = true
			continue
		}
		if !decision.Granted {
			continue
		}
		match, ok, err := s.RecordMatch(ctx, reg.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: occ,
			Outcome:      outcome,
			Evidence: map[string]any{
				"kind":    ConditionProcessDone,
				"handle":  handle,
				"outcome": outcome,
				"code":    fact.Code,
				"report":  fact.Report,
			},
		}, map[string]string{"process_done:" + handle: occ})
		if err != nil {
			return res, err
		}
		if ok {
			res.Matches = append(res.Matches, match)
			res.Claimed = true
		}
	}
	return res, nil
}

func recordAlreadyAdmittedMatch(s *Store, ctx context.Context, watchID string, input MatchInput, cursorUpdates map[string]string, existingReceipt string) (bool, error) {
	now := s.clock.Now()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	recorded, inserted, err := insertMatchTx(ctx, tx, watchID, input, cursorUpdates, now)
	if err != nil || !inserted {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_matches SET reservation_state = 'delivered', delivery_id = NULLIF(?, '') WHERE match_id = ?`,
		strings.TrimSpace(existingReceipt), recorded.MatchID); err != nil {
		return false, err
	}
	reg, err := loadRegistrationByIDTx(ctx, tx, watchID)
	if err == nil {
		_ = releaseCompletedOneShotSlotTx(ctx, tx, &reg, nowStr)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if reg.Lifecycle.State == StateTerminal {
		retireWatchRuntime(s, reg.WatchID)
	}
	return true, nil
}

func matchingProcessWatches(s *Store, ctx context.Context, sessionID, handle string) ([]Registration, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND state = 'live' ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var matched []Registration
	for rows.Next() {
		reg, err := scanRegistration(rows)
		if err != nil {
			return nil, err
		}
		for _, c := range reg.Conditions {
			if c.Kind != ConditionProcessDone {
				continue
			}
			if len(c.Handles) == 0 || containsHandle(c.Handles, handle) {
				matched = append(matched, reg)
				break
			}
		}
	}
	return matched, rows.Err()
}

func containsHandle(handles []string, want string) bool {
	for _, h := range handles {
		if strings.TrimSpace(h) == want {
			return true
		}
	}
	return false
}

func insertMatchTx(ctx context.Context, tx *sql.Tx, watchID string, input MatchInput, cursorUpdates map[string]string, now time.Time) (RecordedMatch, bool, error) {
	reg, err := loadRegistrationByIDTx(ctx, tx, watchID)
	if err != nil {
		return RecordedMatch{}, false, err
	}
	if reg.Lifecycle.State != StateLive {
		return RecordedMatch{}, false, nil
	}
	occ := strings.TrimSpace(input.OccurrenceID)
	if occ == "" {
		return RecordedMatch{}, false, fmt.Errorf("source occurrence id is required")
	}
	var dup int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM watch_matches WHERE watch_id = ? AND source_occurrence_id = ?`,
		watchID, occ).Scan(&dup); err != nil {
		return RecordedMatch{}, false, err
	}
	if dup > 0 {
		return RecordedMatch{}, false, nil
	}

	evidenceBytes, retainedRef, err := boundInlineEvidence(input)
	if err != nil {
		return RecordedMatch{}, false, err
	}
	seq := reg.Lifecycle.MatchSeq + 1
	matchID := uuid.NewString()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	restrictedInt := 0
	if input.Restricted {
		restrictedInt = 1
	}
	outcome := strings.TrimSpace(input.Outcome)
	if outcome == "" {
		outcome = "matched"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_matches (
		match_id, watch_id, seq, source_kind, source_occurrence_id, outcome,
		evidence_json, retained_ref, restricted, reservation_state, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, 'pending', ?)`,
		matchID, watchID, seq, strings.TrimSpace(input.SourceKind), occ, outcome,
		string(evidenceBytes), retainedRef, restrictedInt, nowStr,
	); err != nil {
		return RecordedMatch{}, false, err
	}

	for k, v := range cursorUpdates {
		reg.Cursors[k] = v
	}
	cursorJSON, err := json.Marshal(reg.Cursors)
	if err != nil {
		return RecordedMatch{}, false, err
	}
	nextState := reg.Lifecycle.State
	var nextOutcome any
	var endedAt any
	if !reg.Lifecycle.Repeat {
		nextState = StateTerminal
		nextOutcome = string(OutcomeCompleted)
		endedAt = nowStr
	} else if reg.Lifecycle.Outcome != "" {
		nextOutcome = string(reg.Lifecycle.Outcome)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations
		SET match_seq = ?, cursors_json = ?, state = ?, terminal_outcome = COALESCE(?, terminal_outcome),
		    ended_at = COALESCE(?, ended_at), revision = revision + 1, updated_at = ?
		WHERE watch_id = ?`,
		seq, string(cursorJSON), string(nextState), nextOutcome, endedAt, nowStr, watchID,
	); err != nil {
		return RecordedMatch{}, false, err
	}

	if err := compactBufferedMatchesTx(ctx, tx, reg, nowStr); err != nil {
		return RecordedMatch{}, false, err
	}
	return RecordedMatch{
		MatchID:      matchID,
		WatchID:      watchID,
		Handle:       reg.Handle,
		Seq:          seq,
		SourceKind:   strings.TrimSpace(input.SourceKind),
		OccurrenceID: occ,
		Outcome:      outcome,
		Evidence:     evidenceBytes,
		RetainedRef:  retainedRef,
		Restricted:   input.Restricted,
	}, true, nil
}

func boundInlineEvidence(input MatchInput) ([]byte, string, error) {
	retainedRef := strings.TrimSpace(input.RetainedRef)
	if input.Restricted {
		if retainedRef == "" {
			retainedRef = "restricted:" + input.OccurrenceID
		}
		redacted, err := json.Marshal(map[string]any{
			"kind":         input.SourceKind,
			"outcome":      "redacted",
			"restricted":   true,
			"retained_ref": retainedRef,
		})
		return redacted, retainedRef, err
	}
	payload := input.Evidence
	if payload == nil {
		payload = map[string]any{"kind": input.SourceKind, "outcome": input.Outcome}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) <= MaxInlineMatchBytes {
		return encoded, retainedRef, nil
	}
	if retainedRef == "" {
		retainedRef = "retained:" + input.OccurrenceID
	}
	summary := map[string]any{
		"kind":         input.SourceKind,
		"outcome":      input.Outcome,
		"truncated":    true,
		"retained_ref": retainedRef,
	}
	if report, ok := payload["report"].(string); ok && report != "" {
		summary["excerpt"] = runeclamp.ClampBytes(report, 1024)
	}
	clamped, err := json.Marshal(summary)
	if err != nil {
		return nil, "", err
	}
	return clamped, retainedRef, nil
}

func compactBufferedMatchesTx(ctx context.Context, tx *sql.Tx, reg Registration, nowStr string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT match_id, seq, outcome FROM watch_matches WHERE watch_id = ? AND reservation_state = 'pending' ORDER BY seq ASC`,
		reg.WatchID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type pendingRow struct {
		id      string
		seq     int
		outcome string
	}
	var pending []pendingRow
	for rows.Next() {
		var r pendingRow
		if err := rows.Scan(&r.id, &r.seq, &r.outcome); err != nil {
			return err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	if len(pending) <= MaxBufferedMatches {
		return nil
	}
	evictCount := len(pending) - MaxBufferedMatches
	summary := OverflowSummary{}
	if reg.Lifecycle.Overflow != nil {
		summary = *reg.Lifecycle.Overflow
	}
	seenOutcomes := map[string]bool{}
	for _, o := range summary.Outcomes {
		seenOutcomes[o] = true
	}
	for i := 0; i < evictCount; i++ {
		ev := pending[i]
		if summary.FirstSeq == 0 || ev.seq < summary.FirstSeq {
			summary.FirstSeq = ev.seq
		}
		if ev.seq > summary.LastSeq {
			summary.LastSeq = ev.seq
		}
		summary.CompactedCount++
		summary.LatestOutcome = ev.outcome
		if !seenOutcomes[ev.outcome] {
			seenOutcomes[ev.outcome] = true
			summary.Outcomes = append(summary.Outcomes, ev.outcome)
		}
		if isFailureOutcome(ev.outcome) {
			summary.HadFailure = true
		} else if summary.HadFailure {
			summary.HadRecovery = true
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM watch_matches WHERE match_id = ?`, ev.id); err != nil {
			return err
		}
	}
	for i := evictCount; i < len(pending); i++ {
		if summary.HadFailure && !isFailureOutcome(pending[i].outcome) {
			summary.HadRecovery = true
		}
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE watch_registrations SET overflow_count = ?, overflow_summary_json = ?, updated_at = ? WHERE watch_id = ?`,
		summary.CompactedCount, string(encoded), nowStr, reg.WatchID)
	return err
}

func isFailureOutcome(outcome string) bool {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "failed", "error", "refused", "fenced", "source_lost":
		return true
	default:
		return false
	}
}

func loadRegistrationByIDTx(ctx context.Context, tx *sql.Tx, watchID string) (Registration, error) {
	return scanRegistration(tx.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE watch_id = ?`, watchID))
}

func countPendingMatchesTx(ctx context.Context, tx *sql.Tx, watchID string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM watch_matches WHERE watch_id = ? AND reservation_state = 'pending'`, watchID).Scan(&n)
	return n, err
}
