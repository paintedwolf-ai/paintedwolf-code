package watch

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
)

// DerivedCommandHandoff carries the quiet derived watch handoff for an unjoined command job or promoted program.
type DerivedCommandHandoff struct {
	Handle        string               `json:"handle"`
	WatchHandle   string               `json:"watch_handle"`
	WaitCondition awaitstore.Condition `json:"wait_condition"`
	OutputCursor  int64                `json:"output_cursor"`
}

// HandoffDerivedCommand registers or replays a quiet one-shot completion watch for commandHandle
// without consuming explicit watch quota.
func (s *Store) HandoffDerivedCommand(
	ctx context.Context,
	owner Owner,
	authority Authority,
	commandHandle string,
	outputCursor int64,
) (DerivedCommandHandoff, error) {
	commandHandle = strings.TrimSpace(commandHandle)
	receipt, err := s.Register(ctx, RegisterRequest{
		Owner:                owner,
		DerivedCommandHandle: commandHandle,
		Authority:            authority,
		Delivery:             DeliveryCollect,
		Conditions: []awaitstore.Condition{{
			Kind:    ConditionProcessDone,
			Handles: []string{commandHandle},
		}},
	})
	if err != nil {
		return DerivedCommandHandoff{}, err
	}
	return DerivedCommandHandoff{
		Handle:        commandHandle,
		WatchHandle:   receipt.Handle,
		WaitCondition: receipt.WaitCondition,
		OutputCursor:  max(0, outputCursor),
	}, nil
}

// ReconcileShellJoin reconciles a shell wait join against presentation claims and any derived watch
// on commandHandle so no duplicate fallback kick, collected frame, or ambient wake is delivered.
func (s *Store) ReconcileShellJoin(ctx context.Context, owner Owner, commandHandle string) error {
	if !s.Configured() {
		return nil
	}
	owner = owner.Normalized()
	commandHandle = strings.TrimSpace(commandHandle)
	if owner.SessionID == "" || commandHandle == "" {
		return nil
	}
	joinReceipt := "shell_wait:" + commandHandle
	if fact, done, err := lookupProcess(s, ctx, owner.SessionID, commandHandle); err == nil && done && fact.OccurrenceID != "" {
		dec, cErr := ClaimPresentation(ctx, s, owner, fact.OccurrenceID, ClaimDirectWait, true, joinReceipt)
		if cErr == nil && dec.TransferredKick {
			notifyProcessKickTransfer(s, owner.SessionID, commandHandle)
		}
	}
	notifyProcessObserve(s, owner.SessionID, commandHandle)

	now := s.clock.Now()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	reg, found, err := findByDerivedCommandTx(ctx, tx, owner, commandHandle)
	if err != nil || !found {
		return err
	}
	pending, err := loadPendingMatchesTx(ctx, tx, reg.WatchID)
	if err != nil {
		return err
	}
	var matchIDs []string
	for _, m := range pending {
		matchIDs = append(matchIDs, m.MatchID)
		if _, err := tx.ExecContext(ctx,
			`UPDATE watch_matches SET reservation_state = 'delivered', retained_ref = ? WHERE match_id = ?`,
			joinReceipt, m.MatchID); err != nil {
			return err
		}
	}
	if reg.Lifecycle.State == StateLive || len(matchIDs) > 0 {
		deliveryID := uuid.NewString()
		mJSON, _ := json.Marshal(matchIDs)
		wJSON, _ := json.Marshal([]string{reg.WatchID})
		pJSON, _ := json.Marshal(map[string]any{"joined_by": joinReceipt, "handle": reg.Handle})
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO watch_deliveries
			(delivery_id, session_id, worker_job_id, destination_kind, destination_id, match_ids_json, watch_ids_json, payload_json, state, created_at, committed_at)
			VALUES (?, ?, NULLIF(?, ''), 'request', ?, ?, ?, ?, 'committed', ?, ?)`,
			deliveryID, owner.SessionID, owner.WorkerJobID, joinReceipt,
			string(mJSON), string(wJSON), string(pJSON), nowStr, nowStr); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations
			SET state = 'terminal', terminal_outcome = 'completed', slot_released = 1,
			    ended_at = COALESCE(ended_at, ?), revision = revision + 1, updated_at = ?
			WHERE watch_id = ?`, nowStr, nowStr, reg.WatchID); err != nil {
			return err
		}
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
		notifyLeaseWake(s, ctx, w.lease, w.winner)
	}
	return nil
}

// WatchedCommandHandles returns a map from command handle to watch handle for active or undrained
// process_done watches in sessionID.
func (s *Store) WatchedCommandHandles(ctx context.Context, sessionID string) map[string]string {
	if !s.Configured() || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT handle, COALESCE(derived_command_handle, ''), conditions_json
		FROM watch_registrations wr
		WHERE wr.session_id = ?
		  AND (wr.state = 'live' OR EXISTS (
		      SELECT 1 FROM watch_matches wm WHERE wm.watch_id = wr.watch_id AND wm.reservation_state = 'pending'
		  ))
		ORDER BY wr.seq ASC`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var watchHandle, derivedHandle, condJSON string
		if err := rows.Scan(&watchHandle, &derivedHandle, &condJSON); err != nil {
			continue
		}
		if derivedHandle != "" {
			out[derivedHandle] = watchHandle
			continue
		}
		var conds []awaitstore.Condition
		if json.Unmarshal([]byte(condJSON), &conds) != nil {
			continue
		}
		for _, c := range conds {
			if c.Kind != ConditionProcessDone {
				continue
			}
			for _, h := range c.Handles {
				if h = strings.TrimSpace(h); h != "" && out[h] == "" {
					out[h] = watchHandle
				}
			}
		}
	}
	return out
}
