package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
)

// ProcessOutputLookup screens retained command output for handle past since.
type ProcessOutputLookup func(ctx context.Context, sessionID, handle string, since int64) (bgprocess.ProcessOutputState, error)

func processOutputCursorKey(handle string) string {
	return "process_output:" + strings.TrimSpace(handle)
}

func collectInitialProcessOutputMatches(ctx context.Context, s *Store, req *RegisterRequest) ([]MatchInput, error) {
	var out []MatchInput
	for _, cond := range req.Conditions {
		if cond.Kind != "process_output" {
			continue
		}
		handle := strings.TrimSpace(cond.Handle)
		if handle == "" {
			continue
		}
		key := processOutputCursorKey(handle)
		if cond.Cursor == nil {
			state, ok, err := s.lookupProcessOutput(ctx, req.Owner.SessionID, handle, 0)
			if err != nil {
				return nil, err
			}
			if ok && !state.Lost {
				req.Cursors[key] = strconv.FormatInt(state.NextCursor, 10)
			} else if req.Cursors[key] == "" {
				req.Cursors[key] = "0"
			}
			continue
		}

		since := max(0, *cond.Cursor)
		req.Cursors[key] = strconv.FormatInt(since, 10)
		state, ok, err := s.lookupProcessOutput(ctx, req.Owner.SessionID, handle, since)
		if err != nil {
			return nil, err
		}
		if !ok || state.Lost || !state.HasProcessBytes || state.ProcessEndCursor <= since {
			continue
		}
		req.Cursors[key] = strconv.FormatInt(state.ProcessEndCursor, 10)
		ev := buildProcessOutputEvidence(handle, since, state)
		out = append(out, MatchInput{
			SourceKind:   "process_output",
			OccurrenceID: fmt.Sprintf("process_output:%s:%d..%d", handle, since, state.ProcessEndCursor),
			Outcome:      "satisfied",
			Evidence:     ev,
		})
		if !req.Repeat {
			return out, nil
		}
	}
	return out, nil
}

func buildProcessOutputEvidence(handle string, fromCursor int64, state bgprocess.ProcessOutputState) map[string]any {
	ev := map[string]any{
		"kind":        "process_output",
		"handle":      handle,
		"from_cursor": fromCursor,
		"end_cursor":  state.ProcessEndCursor,
		"running":     state.Running,
		"exited":      state.HasExit,
	}
	if state.Truncated {
		ev["truncated"] = true
	}
	if state.HasExit {
		ev["exit_code"] = state.ExitCode
	}
	return ev
}

// NotifyProcessOutput evaluates retained-output advancement, exit, and handle loss for process_output watches.
func (s *Store) NotifyProcessOutput(ctx context.Context, sessionID, handle string) error {
	if !s.Configured() {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	handle = strings.TrimSpace(handle)
	if sessionID == "" || handle == "" {
		return nil
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+registrationColumns+` FROM watch_registrations wr
		WHERE wr.session_id = ?
		  AND wr.conditions_json LIKE '%process_output%'
		  AND (wr.state = 'live' OR EXISTS (
		      SELECT 1 FROM watch_matches wm
		      WHERE wm.watch_id = wr.watch_id AND wm.source_kind = 'process_output' AND wm.reservation_state = 'pending'
		  ))
		ORDER BY wr.seq ASC`, sessionID)
	if err != nil {
		return err
	}
	var regs []Registration
	for rows.Next() {
		reg, sErr := scanRegistration(rows)
		if sErr != nil {
			_ = rows.Close()
			return sErr
		}
		for _, c := range reg.Conditions {
			if c.Kind == "process_output" && strings.TrimSpace(c.Handle) == handle {
				regs = append(regs, reg)
				break
			}
		}
	}
	_ = rows.Close()
	if len(regs) == 0 {
		return nil
	}

	for _, reg := range regs {
		key := processOutputCursorKey(handle)
		since, _ := strconv.ParseInt(reg.Cursors[key], 10, 64)
		state, ok, err := s.lookupProcessOutput(ctx, sessionID, handle, since)
		if err != nil || !ok {
			continue
		}
		if state.Lost {
			if reg.Lifecycle.State == StateLive {
				_ = s.MarkSourceLost(ctx, reg.WatchID)
			}
			continue
		}
		if state.HasProcessBytes && state.ProcessEndCursor > since {
			coalesced, cErr := s.coalescePendingProcessOutputMatch(ctx, reg.WatchID, handle, state)
			if cErr != nil {
				return cErr
			}
			if coalesced {
				continue
			}
			if reg.Lifecycle.State == StateLive {
				_, _, err = s.RecordMatch(ctx, reg.WatchID, MatchInput{
					SourceKind:   "process_output",
					OccurrenceID: fmt.Sprintf("process_output:%s:%d..%d", handle, since, state.ProcessEndCursor),
					Outcome:      "satisfied",
					Evidence:     buildProcessOutputEvidence(handle, since, state),
				}, map[string]string{key: strconv.FormatInt(state.ProcessEndCursor, 10)})
				if err != nil {
					return err
				}
			}
			continue
		}
		if !state.Running && state.HasExit && reg.Lifecycle.State == StateLive && len(reg.Conditions) == 1 {
			_ = s.completeSilentExitedOutputWatch(ctx, reg.WatchID)
		}
	}
	return nil
}

func (s *Store) coalescePendingProcessOutputMatch(ctx context.Context, watchID, handle string, state bgprocess.ProcessOutputState) (bool, error) {
	nowStr := s.clock.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var matchID, evStr string
	err = tx.QueryRowContext(ctx, `SELECT match_id, evidence_json FROM watch_matches
		WHERE watch_id = ? AND source_kind = 'process_output' AND reservation_state = 'pending'
		ORDER BY seq DESC LIMIT 1`, watchID).Scan(&matchID, &evStr)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var prev map[string]any
	if json.Unmarshal([]byte(evStr), &prev) != nil {
		return false, nil
	}
	if h, _ := prev["handle"].(string); h != handle {
		return false, nil
	}
	fromFloat, _ := prev["from_cursor"].(float64)
	fromCursor := int64(fromFloat)
	prevTrunc, _ := prev["truncated"].(bool)

	mergedState := state
	mergedState.Truncated = prevTrunc || state.Truncated
	newEv, err := json.Marshal(buildProcessOutputEvidence(handle, fromCursor, mergedState))
	if err != nil {
		return false, err
	}
	newOcc := fmt.Sprintf("process_output:%s:%d..%d", handle, fromCursor, state.ProcessEndCursor)
	if _, err := tx.ExecContext(ctx, `UPDATE watch_matches
		SET source_occurrence_id = ?, evidence_json = ?
		WHERE match_id = ? AND reservation_state = 'pending'`,
		newOcc, string(newEv), matchID); err != nil {
		return false, err
	}
	key := processOutputCursorKey(handle)
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations
		SET cursors_json = json_set(cursors_json, ?, ?), revision = revision + 1, updated_at = ?
		WHERE watch_id = ?`,
		"$."+key, strconv.FormatInt(state.ProcessEndCursor, 10), nowStr, watchID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) completeSilentExitedOutputWatch(ctx context.Context, watchID string) error {
	now := s.clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := loadRegistrationByIDTx(ctx, tx, watchID)
	if err != nil || reg.Lifecycle.State != StateLive {
		return err
	}
	if err := transitionTerminalTx(ctx, tx, reg, OutcomeCompleted, now); err != nil {
		return err
	}
	woke, err := settleSelectedLeasesForWatchTx(ctx, tx, watchID, now)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.retireWatchRuntime(watchID)
	for _, w := range woke {
		s.heap.Remove(KindWaitDeadline, w.lease.ID)
		s.notifyLeaseWake(ctx, w.lease, w.winner)
	}
	return nil
}
