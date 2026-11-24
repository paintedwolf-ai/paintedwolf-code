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
	awaitstore "github.com/lycaon/lycaon/internal/await"
)

// SelectRequest asks to either claim ready matches/terminal states in-turn or install an armed wait lease.
type SelectRequest struct {
	Owner        Owner
	Handles      []string
	ParkLease    awaitstore.Lease
	AllowPark    bool
	DirectWinner *awaitstore.Condition
}

// SelectResult describes either an immediate delivery or an armed wait lease.
type SelectResult struct {
	Parked         bool                 `json:"parked"`
	DeliveryID     string               `json:"delivery_id,omitempty"`
	Matches        []RecordedMatch      `json:"matches,omitempty"`
	Terminal       *TerminalSelection   `json:"terminal,omitempty"`
	ActiveHandles  []string             `json:"active_handles,omitempty"`
	Lease          awaitstore.Lease     `json:"-"`
	Winner         awaitstore.Condition `json:"winner,omitempty"`
	OverflowNotice *OverflowSummary     `json:"overflow,omitempty"`
}

// TerminalSelection reports a selected handle that ended without undelivered matches.
type TerminalSelection struct {
	Handle    string          `json:"handle"`
	Outcome   TerminalOutcome `json:"outcome"`
	ReceiptID string          `json:"receipt_id,omitempty"`
}

type wokeLease struct {
	lease  awaitstore.Lease
	winner awaitstore.Condition
}

// SelectOrPark atomically reserves ready watch matches/outcomes or installs the session's wait lease.
func (s *Store) SelectOrPark(ctx context.Context, req SelectRequest) (SelectResult, error) {
	if !s.Configured() {
		return SelectResult{}, fmt.Errorf("watch store is not configured")
	}
	now := s.clock.Now()
	req.Owner = req.Owner.Normalized()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SelectResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	regs, err := resolveSelectedHandlesTx(ctx, tx, req.Owner, req.Handles)
	if err != nil {
		return SelectResult{}, err
	}
	for i := range regs {
		if regs[i].Lifecycle.State == StateLive && !regs[i].Lifecycle.ExpiresAt.After(now) {
			if err := transitionTerminalTx(ctx, tx, regs[i], OutcomeExpired, now); err != nil {
				return SelectResult{}, err
			}
			regs[i], err = loadRegistrationByIDTx(ctx, tx, regs[i].WatchID)
			if err != nil {
				return SelectResult{}, err
			}
		}
	}

	destID := strings.TrimSpace(req.ParkLease.ToolCallID)
	if destID == "" {
		destID = uuid.NewString()
	}
	immediate, ready, err := claimSelectedImmediateTx(ctx, tx, req.Owner, regs, DestinationRequest, destID, now)
	if err != nil {
		return SelectResult{}, err
	}
	if ready {
		if err := tx.Commit(); err != nil {
			return SelectResult{}, err
		}
		for _, reg := range regs {
			if reg.Lifecycle.State == StateTerminal {
				retireWatchRuntime(s, reg.WatchID)
			}
			if allowed, reason := checkWatchAuthority(s, ctx, reg); !allowed {
				for idx := range immediate.Matches {
					if immediate.Matches[idx].WatchID == reg.WatchID {
						immediate.Matches[idx] = redactRecordedMatch(immediate.Matches[idx], reg.WatchID, reason)
					}
				}
				immediate.Winner = buildWatchWinner(immediate.Matches, immediate.Terminal, immediate.ActiveHandles, immediate.DeliveryID, immediate.OverflowNotice)
			}
		}
		return immediate, nil
	}
	if req.DirectWinner != nil {
		res := SelectResult{
			Parked:        false,
			Winner:        *req.DirectWinner,
			ActiveHandles: activeHandleNames(regs),
		}
		if err := tx.Commit(); err != nil {
			return SelectResult{}, err
		}
		return res, nil
	}
	if !req.AllowPark {
		if err := tx.Commit(); err != nil {
			return SelectResult{}, err
		}
		return SelectResult{Parked: false, ActiveHandles: activeHandleNames(regs)}, nil
	}

	lease, earliestExp, err := armLeaseWithSelectionsTx(ctx, tx, req.ParkLease, regs, now)
	if err != nil {
		return SelectResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SelectResult{}, err
	}
	scheduleDeadline := lease.Deadline
	if !earliestExp.IsZero() && (scheduleDeadline.IsZero() || earliestExp.Before(scheduleDeadline)) {
		scheduleDeadline = earliestExp
	}
	if !scheduleDeadline.IsZero() {
		s.heap.Upsert(KindWaitDeadline, lease.ID, scheduleDeadline)
	}
	return SelectResult{
		Parked:        true,
		Lease:         lease,
		ActiveHandles: activeHandleNames(regs),
	}, nil
}

func resolveSelectedHandlesTx(ctx context.Context, tx *sql.Tx, owner Owner, handles []string) ([]Registration, error) {
	seen := map[string]bool{}
	var out []Registration
	for _, raw := range handles {
		h := strings.TrimSpace(raw)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		reg, err := loadRegistrationByHandleTx(ctx, tx, owner, h)
		if err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, nil
}

func activeHandleNames(regs []Registration) []string {
	var out []string
	for _, r := range regs {
		if r.Lifecycle.State == StateLive {
			out = append(out, r.Handle)
		}
	}
	return out
}

func claimSelectedImmediateTx(ctx context.Context, tx *sql.Tx, owner Owner, regs []Registration, destKind DestinationKind, destID string, now time.Time) (SelectResult, bool, error) {
	if len(regs) == 0 {
		return SelectResult{}, false, nil
	}
	if existing, ok, err := findCommittedDeliveryTx(ctx, tx, owner.SessionID, destKind, destID); err != nil || ok {
		return existing, ok, err
	}

	var claimed []RecordedMatch
	var matchIDs, watchIDs []string
	usedBytes := 0
	var overflow *OverflowSummary

	for _, reg := range regs {
		pending, err := loadPendingMatchesTx(ctx, tx, reg.WatchID)
		if err != nil {
			return SelectResult{}, false, err
		}
		if len(pending) == 0 {
			continue
		}
		if overflow == nil && reg.Lifecycle.Overflow != nil {
			cp := *reg.Lifecycle.Overflow
			overflow = &cp
		}
		watchClaimed := false
		for _, m := range pending {
			m.Handle = reg.Handle
			entryBytes := len(m.Evidence) + 64
			if len(claimed) > 0 && usedBytes+entryBytes > MaxBatchBytes {
				break
			}
			claimed = append(claimed, m)
			matchIDs = append(matchIDs, m.MatchID)
			usedBytes += entryBytes
			watchClaimed = true
		}
		if watchClaimed {
			watchIDs = append(watchIDs, reg.WatchID)
		}
	}

	if len(claimed) > 0 {
		deliveryID := uuid.NewString()
		nowStr := now.UTC().Format(time.RFC3339Nano)
		for _, id := range matchIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE watch_matches SET reservation_state = 'delivered', delivery_id = ? WHERE match_id = ?`,
				deliveryID, id); err != nil {
				return SelectResult{}, false, err
			}
		}
		for i := range regs {
			if err := releaseCompletedOneShotSlotTx(ctx, tx, &regs[i], nowStr); err != nil {
				return SelectResult{}, false, err
			}
		}
		active := activeHandleNames(regs)
		winner := buildWatchWinner(claimed, nil, active, deliveryID, overflow)
		res := SelectResult{
			Parked:         false,
			DeliveryID:     deliveryID,
			Matches:        claimed,
			ActiveHandles:  active,
			Winner:         winner,
			OverflowNotice: overflow,
		}
		if err := insertCommittedDeliveryTx(ctx, tx, deliveryID, owner, destKind, destID, matchIDs, watchIDs, res, nowStr); err != nil {
			return SelectResult{}, false, err
		}
		return res, true, nil
	}

	for _, reg := range regs {
		if reg.Lifecycle.State != StateTerminal {
			continue
		}
		receiptID := reg.Lifecycle.FinalReceiptID
		if receiptID == "" {
			var err error
			receiptID, err = latestDeliveryForWatchTx(ctx, tx, reg.WatchID)
			if err != nil {
				return SelectResult{}, false, err
			}
		}
		term := &TerminalSelection{
			Handle:    reg.Handle,
			Outcome:   reg.Lifecycle.Outcome,
			ReceiptID: receiptID,
		}
		active := activeHandleNames(regs)
		deliveryID := uuid.NewString()
		nowStr := now.UTC().Format(time.RFC3339Nano)
		winner := buildWatchWinner(nil, term, active, receiptID, nil)
		res := SelectResult{
			Parked:        false,
			DeliveryID:    deliveryID,
			Terminal:      term,
			ActiveHandles: active,
			Winner:        winner,
		}
		if err := insertCommittedDeliveryTx(ctx, tx, deliveryID, owner, destKind, destID, []string{}, []string{reg.WatchID}, res, nowStr); err != nil {
			return SelectResult{}, false, err
		}
		return res, true, nil
	}

	return SelectResult{}, false, nil
}

func armLeaseWithSelectionsTx(ctx context.Context, tx *sql.Tx, lease awaitstore.Lease, regs []Registration, now time.Time) (awaitstore.Lease, time.Time, error) {
	lease.ID = uuid.NewString()
	lease.Status = "armed"
	lease.SessionID = strings.TrimSpace(lease.SessionID)
	lease.ProjectID = strings.TrimSpace(lease.ProjectID)
	lease.ProfileID = strings.TrimSpace(lease.ProfileID)
	lease.Deadline = lease.Deadline.UTC()
	conditions, err := json.Marshal(lease.Conditions)
	if err != nil {
		return awaitstore.Lease{}, time.Time{}, err
	}
	loopbackPorts, err := json.Marshal(lease.LoopbackPorts)
	if err != nil {
		return awaitstore.Lease{}, time.Time{}, err
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(lease.WorkerJobID) == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE wait_leases SET resume_delivered_at = ?, updated_at = ?
			WHERE session_id = ? AND worker_job_id IS NULL AND status IN ('resolved', 'timed_out') AND resume_delivered_at IS NULL`,
			nowStr, nowStr, lease.SessionID); err != nil {
			return awaitstore.Lease{}, time.Time{}, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE wait_leases SET status = 'canceled', updated_at = ?, resolved_at = ? WHERE session_id = ? AND status = 'armed'`,
		nowStr, nowStr, lease.SessionID); err != nil {
		return awaitstore.Lease{}, time.Time{}, err
	}
	var deadlineVal any
	if !lease.Deadline.IsZero() {
		deadlineVal = lease.Deadline.Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO wait_leases
		(id, session_id, root_session_id, project_id, project_dir, tool_call_id, worker_job_id, profile_id, status, deadline_at, until_complete, conditions_json, loopback_ports_json, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, 'armed', ?, ?, ?, ?, ?, ?, ?)`,
		lease.ID, lease.SessionID, lease.RootSessionID, lease.ProjectID, lease.ProjectDir, lease.ToolCallID,
		lease.WorkerJobID, lease.ProfileID, deadlineVal, lease.UntilComplete, string(conditions),
		string(loopbackPorts), lease.Reason, nowStr, nowStr); err != nil {
		return awaitstore.Lease{}, time.Time{}, err
	}
	var earliest time.Time
	for _, reg := range regs {
		expStr := reg.Lifecycle.ExpiresAt.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO watch_wait_selections (lease_id, watch_id, handle, expires_at) VALUES (?, ?, ?, ?)`,
			lease.ID, reg.WatchID, reg.Handle, expStr); err != nil {
			return awaitstore.Lease{}, time.Time{}, err
		}
		if earliest.IsZero() || reg.Lifecycle.ExpiresAt.Before(earliest) {
			earliest = reg.Lifecycle.ExpiresAt
		}
	}
	return lease, earliest, nil
}

func settleSelectedLeasesForWatchTx(ctx context.Context, tx *sql.Tx, watchID string, now time.Time) ([]wokeLease, error) {
	rows, err := tx.QueryContext(ctx, `SELECT wl.id, wl.session_id, COALESCE(wl.worker_job_id, ''), wl.project_id
		FROM watch_wait_selections wws
		JOIN wait_leases wl ON wl.id = wws.lease_id
		WHERE wws.watch_id = ? AND wl.status = 'armed'`, watchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type candidate struct {
		leaseID, sessionID, workerJobID, projectID string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.leaseID, &c.sessionID, &c.workerJobID, &c.projectID); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	var woke []wokeLease
	nowStr := now.UTC().Format(time.RFC3339Nano)
	for _, c := range candidates {
		regs, err := loadLeaseSelectedRegsTx(ctx, tx, c.leaseID)
		if err != nil {
			return nil, err
		}
		owner := Owner{ProjectID: c.projectID, SessionID: c.sessionID, WorkerJobID: c.workerJobID}.Normalized()
		res, ready, err := claimSelectedImmediateTx(ctx, tx, owner, regs, DestinationLease, c.leaseID, now)
		if err != nil || !ready {
			if err != nil {
				return nil, err
			}
			continue
		}
		winnerBytes, err := json.Marshal(res.Winner)
		if err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE wait_leases SET status = 'resolved', winner_json = ?, updated_at = ?, resolved_at = ? WHERE id = ? AND status = 'armed'`,
			string(winnerBytes), nowStr, nowStr, c.leaseID)
		if err != nil {
			return nil, err
		}
		if n, _ := result.RowsAffected(); n == 1 {
			woke = append(woke, wokeLease{
				lease:  awaitstore.Lease{ID: c.leaseID, SessionID: c.sessionID, WorkerJobID: c.workerJobID, ProjectID: c.projectID},
				winner: res.Winner,
			})
		}
	}
	return woke, nil
}

func loadLeaseSelectedRegsTx(ctx context.Context, tx *sql.Tx, leaseID string) ([]Registration, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE watch_id IN (SELECT watch_id FROM watch_wait_selections WHERE lease_id = ?) ORDER BY seq ASC`,
		leaseID)
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

type watchWinnerReport struct {
	DeliveryID    string             `json:"delivery_id"`
	ActiveHandles []string           `json:"active_handles"`
	Matches       []RecordedMatch    `json:"matches,omitempty"`
	Terminal      *TerminalSelection `json:"terminal,omitempty"`
	Overflow      *OverflowSummary   `json:"overflow,omitempty"`
}

func buildWatchWinner(matches []RecordedMatch, term *TerminalSelection, active []string, receiptID string, overflow *OverflowSummary) awaitstore.Condition {
	handles := make([]string, 0, len(matches)+1)
	seen := map[string]bool{}
	outcome := "matched"
	if term != nil {
		handles = append(handles, term.Handle)
		outcome = string(term.Outcome)
	}
	for _, m := range matches {
		if m.Handle != "" && !seen[m.Handle] {
			seen[m.Handle] = true
			handles = append(handles, m.Handle)
		}
	}
	reportBytes, err := json.Marshal(watchWinnerReport{
		DeliveryID:    receiptID,
		ActiveHandles: active,
		Matches:       matches,
		Terminal:      term,
		Overflow:      overflow,
	})
	if err != nil {
		reportBytes = []byte("{}")
	}
	return awaitstore.Condition{
		Kind:    ConditionWatchMatch,
		Outcome: outcome,
		Handles: handles,
		Report:  string(reportBytes),
	}
}

func loadPendingMatchesTx(ctx context.Context, tx *sql.Tx, watchID string) ([]RecordedMatch, error) {
	rows, err := tx.QueryContext(ctx, `SELECT match_id, watch_id, seq, source_kind, source_occurrence_id,
		outcome, evidence_json, COALESCE(retained_ref, ''), restricted
		FROM watch_matches WHERE watch_id = ? AND reservation_state = 'pending' ORDER BY seq ASC`, watchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RecordedMatch
	for rows.Next() {
		var m RecordedMatch
		var evidenceStr string
		var restricted int
		if err := rows.Scan(&m.MatchID, &m.WatchID, &m.Seq, &m.SourceKind, &m.OccurrenceID,
			&m.Outcome, &evidenceStr, &m.RetainedRef, &restricted); err != nil {
			return nil, err
		}
		m.Evidence = json.RawMessage(evidenceStr)
		m.Restricted = restricted == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

func releaseCompletedOneShotSlotTx(ctx context.Context, tx *sql.Tx, reg *Registration, nowStr string) error {
	if reg.Lifecycle.State != StateTerminal || reg.Lifecycle.Outcome != OutcomeCompleted || reg.Lifecycle.SlotReleased {
		return nil
	}
	pending, err := countPendingMatchesTx(ctx, tx, reg.WatchID)
	if err != nil || pending > 0 {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_registrations SET slot_released = 1, updated_at = ? WHERE watch_id = ?`,
		nowStr, reg.WatchID); err != nil {
		return err
	}
	reg.Lifecycle.SlotReleased = true
	return nil
}

func findCommittedDeliveryTx(ctx context.Context, tx *sql.Tx, sessionID string, destKind DestinationKind, destID string) (SelectResult, bool, error) {
	var payload string
	err := tx.QueryRowContext(ctx,
		`SELECT payload_json FROM watch_deliveries WHERE session_id = ? AND destination_kind = ? AND destination_id = ? AND state = 'committed'`,
		sessionID, string(destKind), destID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return SelectResult{}, false, nil
	}
	if err != nil {
		return SelectResult{}, false, err
	}
	var res SelectResult
	if err := json.Unmarshal([]byte(payload), &res); err != nil {
		return SelectResult{}, false, err
	}
	return res, true, nil
}

func insertCommittedDeliveryTx(ctx context.Context, tx *sql.Tx, deliveryID string, owner Owner, destKind DestinationKind, destID string, matchIDs, watchIDs []string, res SelectResult, nowStr string) error {
	mJSON, err := json.Marshal(matchIDs)
	if err != nil {
		return err
	}
	wJSON, err := json.Marshal(watchIDs)
	if err != nil {
		return err
	}
	pJSON, err := json.Marshal(res)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO watch_deliveries
		(delivery_id, session_id, worker_job_id, destination_kind, destination_id, match_ids_json, watch_ids_json, payload_json, state, created_at, committed_at)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, 'committed', ?, ?)`,
		deliveryID, owner.SessionID, owner.WorkerJobID, string(destKind), destID,
		string(mJSON), string(wJSON), string(pJSON), nowStr, nowStr)
	return err
}

func latestDeliveryForWatchTx(ctx context.Context, tx *sql.Tx, watchID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT delivery_id FROM watch_deliveries
		WHERE state = 'committed' AND destination_kind != 'final_receipt'
		  AND EXISTS (SELECT 1 FROM json_each(watch_ids_json) WHERE value = ?)
		ORDER BY created_at DESC, delivery_id DESC LIMIT 1`, watchID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
