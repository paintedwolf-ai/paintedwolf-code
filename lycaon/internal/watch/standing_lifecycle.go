package watch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

func (e *StandingEngine) Pause(ctx context.Context, projectID, watchID string, baseRevision int) (StandingWatch, error) {
	if e == nil || !e.store.Configured() {
		return StandingWatch{}, fmt.Errorf("watch store is not configured")
	}
	projectID, watchID = strings.TrimSpace(projectID), strings.TrimSpace(watchID)
	current, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return StandingWatch{}, err
	}
	if baseRevision <= 0 || current.Revision != baseRevision {
		return StandingWatch{}, &RevisionConflictError{WatchID: watchID, CurrentRevision: current.Revision}
	}
	nowStr := e.store.clock.Now().UTC().Format(time.RFC3339Nano)
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return StandingWatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var curRev int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM watch_registrations WHERE watch_id = ?`, watchID).Scan(&curRev); err != nil {
		return StandingWatch{}, err
	}
	if curRev != baseRevision {
		return StandingWatch{}, &RevisionConflictError{WatchID: watchID, CurrentRevision: curRev}
	}
	nextRev := curRev + 1
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_registrations SET state = 'paused', state_reason = 'person',
		 cursors_json = json_remove(cursors_json, '$.standing:pending_change'),
		 revision = ?, updated_at = ? WHERE watch_id = ?`,
		nextRev, nowStr, watchID); err != nil {
		return StandingWatch{}, err
	}
	canceledIDs, err := cancelPendingAndQueuedRunsTx(ctx, tx, watchID, nowStr)
	if err != nil {
		return StandingWatch{}, err
	}
	publishWatchEventTx(ctx, e, tx, "watch.updated", projectID, "", watchID, "", nextRev)
	for _, rID := range canceledIDs {
		publishWatchEventTx(ctx, e, tx, "watch_run.updated", projectID, "", watchID, rID, nextRev)
	}
	if err := tx.Commit(); err != nil {
		return StandingWatch{}, err
	}
	retireWatchRuntime(e.store, watchID)
	return e.Get(ctx, projectID, watchID)
}

func (e *StandingEngine) Resume(ctx context.Context, actorPersonID, projectID, watchID string, baseRevision int) (StandingWatch, error) {
	if e == nil || !e.store.Configured() {
		return StandingWatch{}, fmt.Errorf("watch store is not configured")
	}
	actorPersonID, projectID, watchID = strings.TrimSpace(actorPersonID), strings.TrimSpace(projectID), strings.TrimSpace(watchID)
	current, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return StandingWatch{}, err
	}
	if current.OwnerPersonID != actorPersonID {
		return StandingWatch{}, &NotOwnerError{WatchID: watchID}
	}
	if baseRevision <= 0 || current.Revision != baseRevision {
		return StandingWatch{}, &RevisionConflictError{WatchID: watchID, CurrentRevision: current.Revision}
	}
	if current.Target.Kind == "existing_chat" {
		exists, _, tErr := checkSessionTarget(ctx, e, projectID, current.Target.SessionID)
		if tErr != nil {
			return StandingWatch{}, tErr
		}
		if !exists {
			return StandingWatch{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "target.session_id", Message: "choose an available target chat before resuming"}
		}
	}
	now := e.store.clock.Now()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return StandingWatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var curRev int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM watch_registrations WHERE watch_id = ?`, watchID).Scan(&curRev); err != nil {
		return StandingWatch{}, err
	}
	if curRev != baseRevision {
		return StandingWatch{}, &RevisionConflictError{WatchID: watchID, CurrentRevision: curRev}
	}
	nextRev := curRev + 1
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_registrations SET state = 'enabled', state_reason = NULL,
		 cursors_json = json_set(cursors_json, '$.standing:last_eval', ?), revision = ?, updated_at = ? WHERE watch_id = ?`,
		nowStr, nextRev, nowStr, watchID); err != nil {
		return StandingWatch{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE standing_watch_settings SET interval_anchor = ?, consecutive_failures = 0 WHERE watch_id = ?`,
		nowStr, watchID); err != nil {
		return StandingWatch{}, err
	}
	publishWatchEventTx(ctx, e, tx, "watch.updated", projectID, "", watchID, "", nextRev)
	if err := tx.Commit(); err != nil {
		return StandingWatch{}, err
	}
	emitStandingNotice(ctx, e, projectID, watchID, current.Name, NoticeStandingWatchPaused, "", true)
	emitStandingNotice(ctx, e, projectID, watchID, current.Name, NoticeStandingWatchDisabled, "", true)
	sw, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return StandingWatch{}, err
	}
	armStandingWatchRuntime(e, sw, now)
	return sw, nil
}

func (e *StandingEngine) Delete(ctx context.Context, projectID, watchID string) error {
	if e == nil || !e.store.Configured() {
		return &UnknownHandleError{Handle: watchID}
	}
	projectID, watchID = strings.TrimSpace(projectID), strings.TrimSpace(watchID)
	current, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return err
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM watch_registrations WHERE watch_id = ? AND project_id = ? AND owner_kind = 'person'`, watchID, projectID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return &UnknownHandleError{Handle: watchID}
	}
	publishWatchEventTx(ctx, e, tx, "watch.updated", projectID, "", watchID, "", current.Revision+1)
	if err := tx.Commit(); err != nil {
		return err
	}
	retireWatchRuntime(e.store, watchID)
	emitStandingNotice(ctx, e, projectID, watchID, current.Name, NoticeStandingWatchPaused, "", true)
	emitStandingNotice(ctx, e, projectID, watchID, current.Name, NoticeStandingWatchDisabled, "", true)
	_ = e.DispatchProjectRuns(ctx, projectID)
	return nil
}

func (e *StandingEngine) Overlapping(ctx context.Context, projectID string, conditions []awaitstore.Condition) []OverlappingStandingWatch {
	if e == nil || !e.store.Configured() || strings.TrimSpace(projectID) == "" || len(conditions) == 0 {
		return []OverlappingStandingWatch{}
	}
	watches, err := e.List(ctx, projectID, false)
	if err != nil || len(watches) == 0 {
		return []OverlappingStandingWatch{}
	}
	out := make([]OverlappingStandingWatch, 0)
	for _, sw := range watches {
		if (sw.State == StateEnabled || sw.State == StatePaused) && standingConditionsOverlap(sw.Conditions, conditions) {
			out = append(out, OverlappingStandingWatch{WatchID: sw.WatchID, Name: sw.Name, State: string(sw.State)})
		}
	}
	return out
}

func standingConditionsOverlap(existing, candidate []awaitstore.Condition) bool {
	for _, ex := range existing {
		for _, cand := range candidate {
			if ex.Kind != cand.Kind {
				continue
			}
			switch ex.Kind {
			case "timer":
				if canonicalStandingConditionKey(ex) == canonicalStandingConditionKey(cand) {
					return true
				}
			case "git_ref":
				if strings.TrimSpace(ex.Root) == strings.TrimSpace(cand.Root) && strings.TrimSpace(ex.Ref) == strings.TrimSpace(cand.Ref) {
					return true
				}
			case "file_change":
				c := path.Clean(strings.TrimSpace(ex.Path))
				t := path.Clean(strings.TrimSpace(cand.Path))
				if strings.TrimSpace(ex.Root) == strings.TrimSpace(cand.Root) && (c == "." || c == "" || c == t || strings.HasPrefix(t, c+"/")) {
					return true
				}
			}
		}
	}
	return false
}

func (e *StandingEngine) RecordChatProvenance(ctx context.Context, prov StandingChatProvenance) error {
	if e == nil || !e.store.Configured() || strings.TrimSpace(prov.SessionID) == "" {
		return nil
	}
	createdAt := prov.CreatedAt
	if createdAt.IsZero() {
		createdAt = e.store.clock.Now()
	}
	_, err := e.store.db.ExecContext(ctx, `INSERT OR REPLACE INTO standing_watch_chat_provenance
		(session_id, project_id, watch_id, watch_name, run_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(prov.SessionID), strings.TrimSpace(prov.ProjectID),
		strings.TrimSpace(prov.WatchID), strings.TrimSpace(prov.WatchName),
		strings.TrimSpace(prov.RunID), createdAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (e *StandingEngine) LookupChatProvenance(ctx context.Context, sessionID string) (StandingChatProvenance, bool, bool, error) {
	if e == nil || !e.store.Configured() || strings.TrimSpace(sessionID) == "" {
		return StandingChatProvenance{}, false, false, nil
	}
	var prov StandingChatProvenance
	var createdStr string
	var existsInt int
	err := e.store.db.QueryRowContext(ctx, `SELECT p.session_id, p.project_id, p.watch_id, p.watch_name, p.run_id, p.created_at,
		EXISTS (SELECT 1 FROM watch_registrations wr WHERE wr.watch_id = p.watch_id AND wr.owner_kind = 'person')
		FROM standing_watch_chat_provenance p WHERE p.session_id = ?`, strings.TrimSpace(sessionID)).Scan(
		&prov.SessionID, &prov.ProjectID, &prov.WatchID, &prov.WatchName, &prov.RunID, &createdStr, &existsInt)
	if errors.Is(err, sql.ErrNoRows) {
		return StandingChatProvenance{}, false, false, nil
	}
	if err != nil {
		return StandingChatProvenance{}, false, false, err
	}
	if parsed, pErr := time.Parse(time.RFC3339Nano, createdStr); pErr == nil {
		prov.CreatedAt = parsed.UTC()
	}
	return prov, true, existsInt == 1, nil
}

func (e *StandingEngine) ActiveRunSpendLimit(ctx context.Context, sessionID string) (float64, bool) {
	if e == nil || !e.store.Configured() || strings.TrimSpace(sessionID) == "" {
		return 0, false
	}
	var limitUSD float64
	var allowUnpriced int
	err := e.store.db.QueryRowContext(ctx,
		`SELECT spend_limit_usd, allow_unpriced FROM watch_runs
		 WHERE session_id = ? AND status IN ('running', 'waiting_for_owner')
		 ORDER BY created_at DESC LIMIT 1`, strings.TrimSpace(sessionID)).Scan(&limitUSD, &allowUnpriced)
	if err != nil || allowUnpriced == 1 || limitUSD <= 0 {
		return 0, false
	}
	return limitUSD, true
}

func (e *StandingEngine) ListRuns(ctx context.Context, projectID, watchID, afterRunID string, limit int) ([]WatchRun, string, error) {
	if e == nil || !e.store.Configured() {
		return nil, "", nil
	}
	if _, err := e.Get(ctx, projectID, watchID); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	query := `SELECT run_id, watch_id, project_id, run_key, status, scheduled_for, COALESCE(session_id, ''),
		missed_count, condition_detail_json, COALESCE(gate_snapshot_json, ''), prompt_snapshot,
		spend_limit_usd, allow_unpriced, created_at, COALESCE(started_at, ''), COALESCE(ended_at, '')
		FROM watch_runs WHERE watch_id = ? AND project_id = ?`
	args := []any{strings.TrimSpace(watchID), strings.TrimSpace(projectID)}
	if strings.TrimSpace(afterRunID) != "" {
		var cursorSched, cursorCreated string
		if err := e.store.db.QueryRowContext(ctx, `SELECT scheduled_for, created_at FROM watch_runs WHERE run_id = ? AND watch_id = ?`,
			strings.TrimSpace(afterRunID), strings.TrimSpace(watchID)).Scan(&cursorSched, &cursorCreated); err == nil {
			query += ` AND (scheduled_for < ? OR (scheduled_for = ? AND created_at < ?) OR (scheduled_for = ? AND created_at = ? AND run_id < ?))`
			args = append(args, cursorSched, cursorSched, cursorCreated, cursorSched, cursorCreated, strings.TrimSpace(afterRunID))
		}
	}
	query += ` ORDER BY scheduled_for DESC, created_at DESC, run_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := e.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	var out []WatchRun
	for rows.Next() {
		r, sErr := scanWatchRun(rows)
		if sErr != nil {
			return nil, "", sErr
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	nextCursor := ""
	if len(out) > limit {
		nextCursor = out[limit-1].RunID
		out = out[:limit]
	}
	return out, nextCursor, nil
}
