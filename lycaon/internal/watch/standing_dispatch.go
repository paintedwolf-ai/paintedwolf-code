package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

func (e *StandingEngine) DispatchProjectRuns(ctx context.Context, projectID string) error {
	if e == nil || !e.store.Configured() || strings.TrimSpace(projectID) == "" {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	now := e.store.clock.Now()
	rows, err := e.store.db.QueryContext(ctx, `SELECT r.run_id
		FROM watch_runs r
		JOIN watch_registrations wr ON wr.watch_id = r.watch_id
		WHERE r.project_id = ? AND r.status IN ('queued', 'pending')
		ORDER BY r.scheduled_for ASC, wr.created_at ASC, r.created_at ASC`, projectID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	for _, runID := range ids {
		if err := tryDispatchOneStandingRun(ctx, e, projectID, runID, now); err != nil {
			return err
		}
	}
	return nil
}

func tryDispatchOneStandingRun(ctx context.Context, e *StandingEngine, projectID, runID string, now time.Time) error {
	run, err := getWatchRunByID(ctx, e.store, runID)
	if err != nil || (run.Status != RunPending && run.Status != RunQueued) {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	sw, err := e.Get(ctx, projectID, run.WatchID)
	if err != nil {
		return nil
	}
	isManual := strings.HasPrefix(run.RunKey, "manual:")
	if sw.State != StateEnabled && !(isManual && sw.State == StatePaused) {
		return nil
	}
	if run.Status == RunPending {
		if txCheck, cErr := e.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}); cErr == nil {
			_, hasActive := activeRunForWatchTx(ctx, txCheck, sw.WatchID)
			_ = txCheck.Rollback()
			if hasActive {
				return nil
			}
		}
		if strings.HasPrefix(run.RunKey, "change:") {
			var lastStartedStr string
			_ = e.store.db.QueryRowContext(ctx, `SELECT COALESCE(last_started_at, '') FROM standing_watch_settings WHERE watch_id = ?`, sw.WatchID).Scan(&lastStartedStr)
			if lastStartedStr != "" {
				if lastStarted, pErr := time.Parse(time.RFC3339Nano, lastStartedStr); pErr == nil {
					if gapEnd := lastStarted.Add(time.Duration(sw.MinGapMinutes) * time.Minute); now.Before(gapEnd) {
						e.store.heap.Upsert(KindScheduleRun, standingGapPrefix+sw.WatchID, gapEnd)
						return nil
					}
				}
			}
		}
	}

	allowed, err := checkPersonAccess(ctx, e, projectID, sw.OwnerPersonID)
	if err != nil {
		return err
	}
	if !allowed {
		return disableWatchOnRevokedOwner(ctx, e, sw, run.RunID, now)
	}
	if sw.Target.Kind == "existing_chat" {
		exists, busy, tErr := checkSessionTarget(ctx, e, projectID, sw.Target.SessionID)
		if tErr != nil {
			return tErr
		}
		if !exists {
			return pauseStandingTargetUnavailableWithRun(ctx, e, sw, run.RunID, now)
		}
		if busy {
			return nil
		}
	}

	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var curStatus string
	if tx.QueryRowContext(ctx, `SELECT status FROM watch_runs WHERE run_id = ?`, runID).Scan(&curStatus) != nil ||
		(curStatus != string(RunPending) && curStatus != string(RunQueued)) {
		return nil
	}
	var runningCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watch_runs WHERE project_id = ? AND status = 'running'`, projectID).Scan(&runningCount); err != nil {
		return err
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	if runningCount >= MaxConcurrentProjectRuns {
		if curStatus == string(RunPending) {
			if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'queued' WHERE run_id = ?`, runID); err != nil {
				return err
			}
			publishWatchEventTx(ctx, e, tx, "watch_run.updated", projectID, "", sw.WatchID, runID, sw.Revision)
			return commitStandingTx(e, tx)
		}
		return nil
	}
	dispatchedToday, err := countDispatchedRunsTodayTx(ctx, tx, sw.WatchID, sw.TimeZone, now)
	if err != nil {
		return err
	}
	if dispatchedToday >= sw.DailyRunCap {
		if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'skipped_daily_cap', ended_at = ? WHERE run_id = ?`, nowStr, runID); err != nil {
			return err
		}
		publishWatchEventTx(ctx, e, tx, "watch_run.updated", projectID, "", sw.WatchID, runID, sw.Revision)
		return commitStandingTx(e, tx)
	}
	if sw.Target.Kind == "existing_chat" {
		okBudget, retryAt, bErr := reserveExistingChatHourlyBudgetTx(ctx, tx, sw.Target.SessionID, now)
		if bErr != nil {
			return bErr
		}
		if !okBudget {
			e.store.heap.Upsert(KindScheduleRun, standingRetryPrefix+projectID, retryAt)
			return nil
		}
	}

	targetSession := ""
	if sw.Target.Kind == "existing_chat" {
		targetSession = sw.Target.SessionID
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_runs SET status = 'running', session_id = CASE WHEN ? != '' THEN ? ELSE session_id END,
		 started_at = COALESCE(started_at, ?) WHERE run_id = ?`,
		targetSession, targetSession, nowStr, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE standing_watch_settings SET last_started_at = ? WHERE watch_id = ?`, nowStr, sw.WatchID); err != nil {
		return err
	}
	publishWatchEventTx(ctx, e, tx, "watch_run.updated", projectID, targetSession, sw.WatchID, runID, sw.Revision)
	if err := commitStandingTx(e, tx); err != nil {
		return err
	}
	dispatchedRun, err := getWatchRunByID(ctx, e.store, runID)
	if err != nil {
		return err
	}
	return invokeStandingDispatcher(ctx, e, sw, dispatchedRun, now)
}

func invokeStandingDispatcher(ctx context.Context, e *StandingEngine, sw StandingWatch, run WatchRun, now time.Time) error {
	dispatcher := e.callbacks().RunDispatcher
	if dispatcher == nil {
		return nil
	}
	out, err := dispatcher(ctx, sw, run, BuildStandingRunPrompt(sw, run))
	if out.BusyRetry {
		_, rErr := e.store.db.ExecContext(ctx, `UPDATE watch_runs SET status = 'pending', started_at = NULL WHERE run_id = ? AND status = 'running'`, run.RunID)
		return rErr
	}
	if out.SessionID != "" && out.SessionID != run.SessionID {
		if _, uErr := e.store.db.ExecContext(ctx, `UPDATE watch_runs SET session_id = ? WHERE run_id = ?`, out.SessionID, run.RunID); uErr != nil {
			slog.WarnContext(ctx, "record standing watch run session", "run_id", run.RunID, "err", uErr)
		}
	}
	if out.ModelUnavailable || err != nil {
		sid := out.SessionID
		if sid == "" {
			sid = run.SessionID
		}
		return settleStandingRunByID(ctx, e, run.RunID, sid, RunFailed, now)
	}
	return nil
}

// BuildStandingRunPrompt formats the provenance header and stored prompt for a standing-watch run.
func BuildStandingRunPrompt(sw StandingWatch, run WatchRun) string {
	var detail map[string]any
	_ = json.Unmarshal(run.ConditionDetail, &detail)
	trigger := summarizeRunConditionDetail(detail, run.ScheduledFor)
	header := fmt.Sprintf("[standing-watch run · watch_id=%s · name=%q · run_id=%s · owner=%s · trigger=%s",
		sw.WatchID, sw.Name, run.RunID, sw.OwnerPersonID, trigger)
	if run.MissedCount > 0 {
		header += fmt.Sprintf(" · missed_runs=%d", run.MissedCount)
	}
	header += "]\nDo the stored task and report the outcome briefly. To propose a change to this standing watch, call watch with standing.replaces set to watch_id.\n\n"
	prompt := run.PromptSnapshot
	if strings.TrimSpace(prompt) == "" {
		prompt = sw.Prompt
	}
	return header + prompt
}

func summarizeRunConditionDetail(detail map[string]any, scheduledFor time.Time) string {
	switch detail["kind"] {
	case "git_ref":
		ref, _ := detail["ref"].(string)
		return "git_ref:" + ref
	case "file_change":
		p, _ := detail["path"].(string)
		return "file_change:" + p
	case "manual":
		return "run_now"
	default:
		if s, ok := detail["scheduled_for"].(string); ok && s != "" {
			return "timer:" + s
		}
		return "timer:" + scheduledFor.UTC().Format(time.RFC3339)
	}
}

func (e *StandingEngine) MarkWaitingForOwner(ctx context.Context, sessionID string) error {
	if e == nil || !e.store.Configured() || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	var runID, watchID, projectID string
	err := e.store.db.QueryRowContext(ctx,
		`SELECT run_id, watch_id, project_id FROM watch_runs WHERE session_id = ? AND status = 'running' LIMIT 1`,
		strings.TrimSpace(sessionID)).Scan(&runID, &watchID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'waiting_for_owner' WHERE run_id = ? AND status = 'running'`, runID); err != nil {
		return err
	}
	publishWatchEventTx(ctx, e, tx, "watch_run.updated", projectID, sessionID, watchID, runID, 0)
	if err := commitStandingTx(e, tx); err != nil {
		return err
	}
	return e.DispatchProjectRuns(ctx, projectID)
}

func (e *StandingEngine) MarkResumed(ctx context.Context, sessionID string) error {
	if e == nil || !e.store.Configured() || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	var runID, watchID, projectID string
	err := e.store.db.QueryRowContext(ctx,
		`SELECT run_id, watch_id, project_id FROM watch_runs WHERE session_id = ? AND status = 'waiting_for_owner' LIMIT 1`,
		strings.TrimSpace(sessionID)).Scan(&runID, &watchID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'running' WHERE run_id = ? AND status = 'waiting_for_owner'`, runID); err != nil {
		return err
	}
	publishWatchEventTx(ctx, e, tx, "watch_run.updated", projectID, sessionID, watchID, runID, 0)
	return commitStandingTx(e, tx)
}

func (e *StandingEngine) SettleRun(ctx context.Context, sessionID string, status WatchRunStatus) error {
	if e == nil || !e.store.Configured() || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	var runID string
	err := e.store.db.QueryRowContext(ctx,
		`SELECT run_id FROM watch_runs WHERE session_id = ? AND status IN ('running', 'waiting_for_owner') ORDER BY created_at DESC LIMIT 1`,
		strings.TrimSpace(sessionID)).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		var pid string
		if e.store.db.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id = ?`, strings.TrimSpace(sessionID)).Scan(&pid) == nil && pid != "" {
			return e.DispatchProjectRuns(ctx, pid)
		}
		return nil
	}
	if err != nil {
		return err
	}
	return settleStandingRunByID(ctx, e, runID, sessionID, status, e.store.clock.Now())
}

func settleStandingRunByID(ctx context.Context, e *StandingEngine, runID, sessionID string, status WatchRunStatus, now time.Time) error {
	run, err := getWatchRunByID(ctx, e.store, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || (run.Status != RunRunning && run.Status != RunWaitingForOwner && run.Status != RunPending && run.Status != RunQueued) {
		return err
	}
	sw, swErr := e.Get(ctx, run.ProjectID, run.WatchID)
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_runs SET status = ?, session_id = CASE WHEN ? != '' THEN ? ELSE session_id END, ended_at = ? WHERE run_id = ?`,
		string(status), strings.TrimSpace(sessionID), strings.TrimSpace(sessionID), nowStr, runID); err != nil {
		return err
	}

	pausedByFailures, completedOneShot, nextRev := false, false, sw.Revision
	if swErr == nil {
		switch status {
		case RunCompleted:
			if _, err := tx.ExecContext(ctx,
				`UPDATE standing_watch_settings SET consecutive_failures = 0, last_outcome = 'completed',
				 gate_baseline_json = COALESCE(NULLIF(?, ''), gate_baseline_json) WHERE watch_id = ?`,
				run.GateSnapshot, run.WatchID); err != nil {
				return err
			}
			if sw.Target.Kind == "new_chat" && strings.TrimSpace(sessionID) != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET seen_at = NULL WHERE id = ?`, strings.TrimSpace(sessionID)); err != nil {
					return err
				}
			}
			if isAtOnlyStandingWatch(sw.Conditions) {
				if next, _ := PreviewStandingSchedule(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, now, 1); len(next) == 0 {
					completedOneShot, nextRev = true, nextRev+1
					if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations SET state = 'completed', state_reason = NULL, ended_at = ?, revision = ?, updated_at = ? WHERE watch_id = ?`, nowStr, nextRev, nowStr, sw.WatchID); err != nil {
						return err
					}
				}
			}
		case RunFailed, RunFenced:
			newFails := sw.ConsecutiveFailures + 1
			if _, err := tx.ExecContext(ctx, `UPDATE standing_watch_settings SET consecutive_failures = ?, last_outcome = ? WHERE watch_id = ?`, newFails, string(status), run.WatchID); err != nil {
				return err
			}
			if newFails >= MaxConsecutiveFailures && sw.State == StateEnabled {
				pausedByFailures, nextRev = true, nextRev+1
				if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations SET state = 'paused', state_reason = 'consecutive_failures', revision = ?, updated_at = ? WHERE watch_id = ?`, nextRev, nowStr, sw.WatchID); err != nil {
					return err
				}
				_, _ = cancelPendingAndQueuedRunsTx(ctx, tx, sw.WatchID, nowStr)
			}
		case RunCanceled:
			if _, err := tx.ExecContext(ctx, `UPDATE standing_watch_settings SET last_outcome = 'canceled' WHERE watch_id = ?`, run.WatchID); err != nil {
				return err
			}
		case RunPending, RunQueued, RunRunning, RunWaitingForOwner,
			RunSkippedOverlap, RunSkippedUnchanged, RunSkippedDailyCap, RunSkippedBusy, RunMissed:
		}
		publishWatchEventTx(ctx, e, tx, "watch_run.updated", run.ProjectID, sessionID, run.WatchID, runID, nextRev)
		if pausedByFailures || completedOneShot {
			publishWatchEventTx(ctx, e, tx, "watch.updated", run.ProjectID, "", run.WatchID, "", nextRev)
		}
	}
	if err := commitStandingTx(e, tx); err != nil {
		return err
	}
	if pausedByFailures {
		retireWatchRuntime(e.store, sw.WatchID)
		emitStandingNotice(ctx, e, sw.ProjectID, sw.WatchID, sw.Name, NoticeStandingWatchPaused, "consecutive_failures", false)
	} else if completedOneShot {
		retireWatchRuntime(e.store, sw.WatchID)
	}
	return e.DispatchProjectRuns(ctx, run.ProjectID)
}

func (e *StandingEngine) NotifySessionClosedOrDeleted(ctx context.Context, sessionID string) error {
	if e == nil || !e.store.Configured() || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	_ = e.SettleRun(ctx, sessionID, RunCanceled)
	rows, err := e.store.db.QueryContext(ctx,
		`SELECT wr.watch_id, wr.project_id, sws.name, wr.revision
		 FROM watch_registrations wr JOIN standing_watch_settings sws ON sws.watch_id = wr.watch_id
		 WHERE wr.owner_kind = 'person' AND wr.state IN ('enabled', 'paused')
		   AND json_extract(sws.target_json, '$.kind') = 'existing_chat'
		   AND json_extract(sws.target_json, '$.session_id') = ?`, sessionID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type targetRow struct {
		watchID, projectID, name string
		rev                      int
	}
	var targets []targetRow
	for rows.Next() {
		var r targetRow
		if rows.Scan(&r.watchID, &r.projectID, &r.name, &r.rev) == nil {
			targets = append(targets, r)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	nowStr := e.store.clock.Now().UTC().Format(time.RFC3339Nano)
	for _, t := range targets {
		tx, bErr := e.store.db.BeginTx(ctx, nil)
		if bErr != nil {
			continue
		}
		nextRev := t.rev + 1
		if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations SET state = 'paused', state_reason = 'target_unavailable', revision = ?, updated_at = ? WHERE watch_id = ?`, nextRev, nowStr, t.watchID); err != nil {
			_ = tx.Rollback()
			continue
		}
		_, _ = cancelPendingAndQueuedRunsTx(ctx, tx, t.watchID, nowStr)
		publishWatchEventTx(ctx, e, tx, "watch.updated", t.projectID, "", t.watchID, "", nextRev)
		if commitStandingTx(e, tx) == nil {
			retireWatchRuntime(e.store, t.watchID)
			emitStandingNotice(ctx, e, t.projectID, t.watchID, t.name, NoticeStandingWatchPaused, "target_unavailable", false)
		} else {
			_ = tx.Rollback()
		}
	}
	return nil
}

func pauseStandingTargetUnavailable(ctx context.Context, e *StandingEngine, watchID string) error {
	var projectID string
	if err := e.store.db.QueryRowContext(ctx, `SELECT project_id FROM watch_registrations WHERE watch_id = ?`, watchID).Scan(&projectID); err != nil {
		return err
	}
	sw, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return err
	}
	return pauseStandingTargetUnavailableWithRun(ctx, e, sw, "", e.store.clock.Now())
}

func pauseStandingTargetUnavailableWithRun(ctx context.Context, e *StandingEngine, sw StandingWatch, runID string, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	nextRev := sw.Revision + 1
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations SET state = 'paused', state_reason = 'target_unavailable', revision = ?, updated_at = ? WHERE watch_id = ?`, nextRev, nowStr, sw.WatchID); err != nil {
		return err
	}
	if runID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'canceled', ended_at = ? WHERE run_id = ?`, nowStr, runID); err != nil {
			return err
		}
		publishWatchEventTx(ctx, e, tx, "watch_run.updated", sw.ProjectID, "", sw.WatchID, runID, nextRev)
	}
	_, _ = cancelPendingAndQueuedRunsTx(ctx, tx, sw.WatchID, nowStr)
	publishWatchEventTx(ctx, e, tx, "watch.updated", sw.ProjectID, "", sw.WatchID, "", nextRev)
	if err := commitStandingTx(e, tx); err != nil {
		return err
	}
	retireWatchRuntime(e.store, sw.WatchID)
	emitStandingNotice(ctx, e, sw.ProjectID, sw.WatchID, sw.Name, NoticeStandingWatchPaused, "target_unavailable", false)
	return nil
}

func disableWatchOnRevokedOwner(ctx context.Context, e *StandingEngine, sw StandingWatch, runID string, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	nextRev := sw.Revision + 1
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations SET state = 'disabled', state_reason = 'owner_access_revoked', revision = ?, updated_at = ? WHERE watch_id = ?`, nextRev, nowStr, sw.WatchID); err != nil {
		return err
	}
	if runID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'canceled', ended_at = ? WHERE run_id = ?`, nowStr, runID); err != nil {
			return err
		}
		publishWatchEventTx(ctx, e, tx, "watch_run.updated", sw.ProjectID, "", sw.WatchID, runID, nextRev)
	}
	_, _ = cancelPendingAndQueuedRunsTx(ctx, tx, sw.WatchID, nowStr)
	publishWatchEventTx(ctx, e, tx, "watch.updated", sw.ProjectID, "", sw.WatchID, "", nextRev)
	if err := commitStandingTx(e, tx); err != nil {
		return err
	}
	retireWatchRuntime(e.store, sw.WatchID)
	emitStandingNotice(ctx, e, sw.ProjectID, sw.WatchID, sw.Name, NoticeStandingWatchDisabled, "owner_access_revoked", false)
	return nil
}

func markStandingWatchCompleted(ctx context.Context, e *StandingEngine, watchID, projectID string, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var rev int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM watch_registrations WHERE watch_id = ?`, watchID).Scan(&rev); err != nil {
		return err
	}
	nextRev := rev + 1
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations SET state = 'completed', state_reason = NULL, ended_at = ?, revision = ?, updated_at = ? WHERE watch_id = ?`, nowStr, nextRev, nowStr, watchID); err != nil {
		return err
	}
	publishWatchEventTx(ctx, e, tx, "watch.updated", projectID, "", watchID, "", nextRev)
	if err := commitStandingTx(e, tx); err != nil {
		return err
	}
	retireWatchRuntime(e.store, watchID)
	return nil
}

func pruneCompletedStandingWatches(ctx context.Context, s *Store, now time.Time) error {
	cutoff := now.Add(-CompletedRetentionWindow).UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM watch_registrations WHERE owner_kind = 'person' AND state = 'completed' AND ended_at IS NOT NULL AND ended_at <= ?`, cutoff)
	return err
}

func getWatchRunByID(ctx context.Context, s *Store, runID string) (WatchRun, error) {
	return scanWatchRun(s.db.QueryRowContext(ctx, `SELECT run_id, watch_id, project_id, run_key, status,
		scheduled_for, COALESCE(session_id, ''), missed_count, condition_detail_json,
		COALESCE(gate_snapshot_json, ''), prompt_snapshot, spend_limit_usd, allow_unpriced,
		created_at, COALESCE(started_at, ''), COALESCE(ended_at, '')
		FROM watch_runs WHERE run_id = ?`, strings.TrimSpace(runID)))
}

func scanWatchRun(row scanner) (WatchRun, error) {
	var r WatchRun
	var status, schedStr, detailStr, createdStr, startedStr, endedStr string
	var allowInt int
	if err := row.Scan(
		&r.RunID, &r.WatchID, &r.ProjectID, &r.RunKey, &status, &schedStr, &r.SessionID,
		&r.MissedCount, &detailStr, &r.GateSnapshot, &r.PromptSnapshot,
		&r.SpendLimitUSD, &allowInt, &createdStr, &startedStr, &endedStr,
	); err != nil {
		return WatchRun{}, err
	}
	r.Status, r.AllowUnpriced, r.ConditionDetail = WatchRunStatus(status), allowInt == 1, json.RawMessage(detailStr)
	if parsed, err := time.Parse(time.RFC3339Nano, schedStr); err == nil {
		r.ScheduledFor = parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
		r.CreatedAt = parsed.UTC()
	}
	if startedStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, startedStr); err == nil {
			u := parsed.UTC()
			r.StartedAt = &u
		}
	}
	if endedStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, endedStr); err == nil {
			u := parsed.UTC()
			r.EndedAt = &u
		}
	}
	return r, nil
}

func activeRunForWatchTx(ctx context.Context, tx *sql.Tx, watchID string) (string, bool) {
	var runID string
	err := tx.QueryRowContext(ctx,
		`SELECT run_id FROM watch_runs WHERE watch_id = ? AND status IN ('queued', 'running', 'waiting_for_owner') ORDER BY created_at DESC LIMIT 1`,
		watchID).Scan(&runID)
	return runID, err == nil && runID != ""
}

func countDispatchedRunsTodayTx(ctx context.Context, tx *sql.Tx, watchID, tz string, now time.Time) (int, error) {
	loc, err := loadConditionLocation(tz)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	localNow := now.In(loc)
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).UTC().Format(time.RFC3339Nano)
	dayEnd := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).UTC().Format(time.RFC3339Nano)
	var count int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM watch_runs WHERE watch_id = ? AND started_at IS NOT NULL AND started_at >= ? AND started_at < ?`,
		watchID, dayStart, dayEnd).Scan(&count)
	return count, err
}

func reserveExistingChatHourlyBudgetTx(ctx context.Context, tx *sql.Tx, sessionID string, now time.Time) (bool, time.Time, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT wake_admissions_json FROM watch_session_state WHERE session_id = ?`, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		raw = "[]"
		if _, iErr := tx.ExecContext(ctx, `INSERT INTO watch_session_state (session_id) VALUES (?)`, sessionID); iErr != nil {
			return false, time.Time{}, iErr
		}
	} else if err != nil {
		return false, time.Time{}, err
	}
	var list []string
	_ = json.Unmarshal([]byte(raw), &list)
	cutoff := now.Add(-time.Hour)
	var active []time.Time
	var kept []string
	for _, ts := range list {
		if parsed, pErr := time.Parse(time.RFC3339Nano, ts); pErr == nil && parsed.After(cutoff) {
			active, kept = append(active, parsed), append(kept, ts)
		}
	}
	if len(active) >= DefaultHourlyWakeBudget {
		earliest := active[0]
		for _, t := range active[1:] {
			if t.Before(earliest) {
				earliest = t
			}
		}
		return false, earliest.Add(time.Hour), nil
	}
	kept = append(kept, now.UTC().Format(time.RFC3339Nano))
	encoded, err := json.Marshal(kept)
	if err != nil {
		return false, time.Time{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE watch_session_state SET wake_admissions_json = ? WHERE session_id = ?`, string(encoded), sessionID)
	return err == nil, time.Time{}, err
}

func checkUniqueStandingNameTx(ctx context.Context, tx *sql.Tx, projectID, name, excludeWatchID string) error {
	var existingID string
	err := tx.QueryRowContext(ctx,
		`SELECT watch_id FROM standing_watch_settings WHERE project_id = ? AND name_folded = ? AND watch_id != ? LIMIT 1`,
		projectID, strings.ToLower(strings.TrimSpace(name)), excludeWatchID).Scan(&existingID)
	if err == nil && existingID != "" {
		return &NameTakenError{Name: name}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func cancelPendingAndQueuedRunsTx(ctx context.Context, tx *sql.Tx, watchID, nowStr string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT run_id FROM watch_runs WHERE watch_id = ? AND status IN ('pending', 'queued')`, watchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'canceled', ended_at = ? WHERE watch_id = ? AND status IN ('pending', 'queued')`, nowStr, watchID); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func mapStandingSQLiteErr(err error, name string) error {
	if err != nil && strings.Contains(err.Error(), "standing_watch_settings.project_id, standing_watch_settings.name_folded") {
		return &NameTakenError{Name: name}
	}
	return err
}

func sameOptionalCondition(a, b *awaitstore.Condition) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return canonicalStandingConditionKey(*a) == canonicalStandingConditionKey(*b)
}

func hasStandingTimer(conds []awaitstore.Condition) bool {
	for _, c := range conds {
		if c.Kind == "timer" {
			return true
		}
	}
	return false
}

func isAtOnlyStandingWatch(conds []awaitstore.Condition) bool {
	if len(conds) == 0 {
		return false
	}
	for _, c := range conds {
		if c.Kind != "timer" || strings.TrimSpace(c.At) == "" {
			return false
		}
	}
	return true
}

func hasStandingSourceCondition(conds []awaitstore.Condition) bool {
	for _, c := range conds {
		if c.Kind == "file_change" || c.Kind == "git_ref" {
			return true
		}
	}
	return false
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func checkModelPriced(ctx context.Context, e *StandingEngine, projectID, modelID string) (bool, error) {
	if fn := e.callbacks().ModelPriceCheck; fn != nil {
		return fn(ctx, projectID, modelID)
	}
	return true, nil
}

func checkPersonAccess(ctx context.Context, e *StandingEngine, projectID, personID string) (bool, error) {
	if fn := e.callbacks().PersonAccessCheck; fn != nil {
		return fn(ctx, projectID, personID)
	}
	return true, nil
}

func checkSessionTarget(ctx context.Context, e *StandingEngine, projectID, sessionID string) (bool, bool, error) {
	if fn := e.callbacks().SessionTargetCheck; fn != nil {
		return fn(ctx, projectID, sessionID)
	}
	var pid, status string
	err := e.store.db.QueryRowContext(ctx, `SELECT project_id, status FROM sessions WHERE id = ?`, strings.TrimSpace(sessionID)).Scan(&pid, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil || pid != strings.TrimSpace(projectID) {
		return false, false, err
	}
	return true, status == "busy" || status == "preparing", nil
}

func lookupProjectDefaults(ctx context.Context, e *StandingEngine, projectID string) (string, string, string, Authority) {
	if fn := e.callbacks().ProjectDefaults; fn != nil {
		return fn(ctx, projectID)
	}
	return "", "", "UTC", queryFallbackProjectRoots(ctx, e.store, projectID)
}

func queryFallbackProjectRoots(ctx context.Context, s *Store, projectID string) Authority {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, path FROM project_roots WHERE project_id = ? ORDER BY created_at ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return Authority{}
	}
	defer func() { _ = rows.Close() }()
	var auth Authority
	for rows.Next() {
		var id, label, p string
		if rows.Scan(&id, &label, &p) == nil {
			cleanLabel := strings.TrimPrefix(strings.TrimSpace(label), "@")
			if cleanLabel == "" {
				cleanLabel = id
			}
			if auth.ProjectDir == "" {
				auth.ProjectDir = p
			}
			auth.RootPaths = append(auth.RootPaths, p)
			auth.Roots = append(auth.Roots, WatchRootGrant{Label: cleanLabel, RootID: id, Path: p})
		}
	}
	if rows.Err() != nil {
		return Authority{}
	}
	return auth
}

func emitStandingNotice(ctx context.Context, e *StandingEngine, projectID, watchID, watchName, code, reason string, clear bool) {
	if fn := e.callbacks().NoticeEmitter; fn != nil {
		fn(ctx, projectID, watchID, watchName, code, reason, clear)
	}
}

func publishWatchEventTx(ctx context.Context, e *StandingEngine, tx *sql.Tx, topic, projectID, sessionID, watchID, runID string, revision int) {
	if fn := e.callbacks().EventPublisher; fn != nil {
		fn(ctx, tx, topic, projectID, sessionID, watchID, runID, revision)
	}
}

func commitStandingTx(e *StandingEngine, tx *sql.Tx) error {
	err := tx.Commit()
	if err == nil && e != nil {
		NotifyEvents(e.store)
	}
	return err
}
