package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/git"
)

func (e *StandingEngine) RunNow(ctx context.Context, actorPersonID, projectID, watchID string) (WatchRun, error) {
	if e == nil || !e.store.Configured() {
		return WatchRun{}, fmt.Errorf("watch store is not configured")
	}
	actorPersonID, projectID, watchID = strings.TrimSpace(actorPersonID), strings.TrimSpace(projectID), strings.TrimSpace(watchID)
	sw, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return WatchRun{}, err
	}
	if sw.OwnerPersonID != actorPersonID {
		return WatchRun{}, &NotOwnerError{WatchID: watchID}
	}
	if sw.State != StateEnabled && sw.State != StatePaused {
		return WatchRun{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "state", Message: "run now requires an enabled or paused standing watch"}
	}

	now := e.store.clock.Now()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	runID := uuid.NewString()
	runKey := "manual:" + runID
	detailBytes, _ := json.Marshal(map[string]any{"kind": "manual", "requested_at": now.UTC().Format(time.RFC3339)})

	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return WatchRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if activeID, hasActive := activeRunForWatchTx(ctx, tx, watchID); hasActive {
		return WatchRun{}, &RunActiveError{WatchID: watchID, RunID: activeID}
	}
	dispatchedToday, err := countDispatchedRunsTodayTx(ctx, tx, watchID, sw.TimeZone, now)
	if err != nil {
		return WatchRun{}, err
	}
	if dispatchedToday >= sw.DailyRunCap {
		return WatchRun{}, &DailyCapReachedError{WatchID: watchID, DailyRunCap: sw.DailyRunCap}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_runs (
		run_id, watch_id, project_id, run_key, status, scheduled_for, missed_count,
		condition_detail_json, prompt_snapshot, spend_limit_usd, allow_unpriced, created_at
	) VALUES (?, ?, ?, ?, 'pending', ?, 0, ?, ?, ?, ?, ?)`,
		runID, watchID, projectID, runKey, nowStr, string(detailBytes),
		sw.Prompt, sw.RunSpendLimitUSD, boolToInt(sw.AllowUnpriced), nowStr); err != nil {
		return WatchRun{}, err
	}
	publishWatchEventTx(ctx, e, tx, "watch_run.updated", projectID, "", watchID, runID, sw.Revision)
	if err := tx.Commit(); err != nil {
		return WatchRun{}, err
	}
	if err := e.DispatchProjectRuns(ctx, projectID); err != nil {
		return WatchRun{}, err
	}
	return getWatchRunByID(ctx, e.store, runID)
}

func armStandingWatchRuntime(e *StandingEngine, sw StandingWatch, now time.Time) {
	if sw.State != StateEnabled {
		retireWatchRuntime(e.store, sw.WatchID)
		return
	}
	if next, err := PreviewStandingSchedule(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, now, 1); err == nil && len(next) > 0 {
		e.store.heap.Upsert(KindScheduleRun, standingTimerPrefix+sw.WatchID, next[0])
	} else {
		e.store.heap.Remove(KindScheduleRun, standingTimerPrefix+sw.WatchID)
	}
	subscribeWatchSampler(e.store, Registration{
		WatchID:    sw.WatchID,
		Owner:      Owner{Kind: OwnerPerson, ProjectID: sw.ProjectID, OwnerPersonID: sw.OwnerPersonID},
		Conditions: sw.Conditions,
		Authority:  sw.Authority,
		Lifecycle:  Lifecycle{State: StateEnabled, Repeat: true},
	}, now)
}

func handleStandingScheduleEntry(ctx context.Context, e *StandingEngine, key string, now time.Time) error {
	switch {
	case key == standingRetentionKey:
		if err := pruneCompletedStandingWatches(ctx, e.store, now); err != nil {
			return err
		}
		e.store.heap.Upsert(KindScheduleRun, standingRetentionKey, now.Add(6*time.Hour))
		return nil
	case strings.HasPrefix(key, standingTimerPrefix):
		return fireStandingTimer(ctx, e, strings.TrimPrefix(key, standingTimerPrefix), now)
	case strings.HasPrefix(key, standingGapPrefix):
		return flushPendingStandingChange(ctx, e, strings.TrimPrefix(key, standingGapPrefix), now)
	case strings.HasPrefix(key, standingRetryPrefix):
		return e.DispatchProjectRuns(ctx, strings.TrimPrefix(key, standingRetryPrefix))
	default:
		return nil
	}
}

func recoverStandingWatches(ctx context.Context, e *StandingEngine, now time.Time) error {
	if err := pruneCompletedStandingWatches(ctx, e.store, now); err != nil {
		return err
	}
	e.store.heap.Upsert(KindScheduleRun, standingRetentionKey, now.Add(6*time.Hour))

	rows, err := e.store.db.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE owner_kind = 'person' AND state = 'enabled' ORDER BY created_at ASC`)
	if err != nil {
		return err
	}
	var enabledRegs []Registration
	for rows.Next() {
		reg, sErr := scanRegistration(rows)
		if sErr != nil {
			_ = rows.Close()
			return sErr
		}
		enabledRegs = append(enabledRegs, reg)
	}
	_ = rows.Close()

	projects := map[string]bool{}
	if rRows, rErr := e.store.db.QueryContext(ctx, `SELECT DISTINCT project_id FROM watch_runs WHERE status IN ('pending', 'queued', 'running')`); rErr == nil {
		for rRows.Next() {
			var pid string
			if rRows.Scan(&pid) == nil && pid != "" {
				projects[pid] = true
			}
		}
		_ = rRows.Close()
	}

	for _, reg := range enabledRegs {
		projects[reg.Owner.ProjectID] = true
		for altIdx, cond := range reg.Conditions {
			if cond.Kind == "file_change" {
				if snap, _ := decodePersistedManifest(reg.Cursors[fileManifestCursorKey(altIdx)]); snap != nil {
					e.store.manifests.set(reg.WatchID, altIdx, snap)
				}
			}
		}
		sw, gErr := e.Get(ctx, reg.Owner.ProjectID, reg.WatchID)
		if gErr != nil {
			continue
		}
		if err := reconcileMissedStandingTimersOnStart(ctx, e, reg, sw, now); err != nil {
			return err
		}
		if hasStandingSourceCondition(sw.Conditions) {
			_, _ = reconcileSourceWatch(e.store, ctx, reg)
		}
		if reg.Cursors[standingPendingChangeKey] != "" {
			_ = flushPendingStandingChange(ctx, e, reg.WatchID, now)
		}
		if updatedSW, uErr := e.Get(ctx, reg.Owner.ProjectID, reg.WatchID); uErr == nil && updatedSW.State == StateEnabled {
			armStandingWatchRuntime(e, updatedSW, now)
		}
	}

	for pid := range projects {
		_, _ = e.store.db.ExecContext(ctx,
			`UPDATE watch_runs SET status = 'pending', started_at = NULL WHERE project_id = ? AND status = 'running' AND (session_id IS NULL OR session_id = '')`, pid)
		if err := e.DispatchProjectRuns(ctx, pid); err != nil {
			return err
		}
	}
	return nil
}

func reconcileMissedStandingTimersOnStart(ctx context.Context, e *StandingEngine, reg Registration, sw StandingWatch, now time.Time) error {
	if !hasStandingTimer(sw.Conditions) {
		return nil
	}
	lastEval := sw.IntervalAnchor
	if raw := reg.Cursors[standingLastEvalCursorKey]; raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			lastEval = parsed.UTC()
		}
	}
	due, err := dueStandingTimerOccurrences(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, lastEval, now)
	if err != nil {
		return err
	}
	_ = updateWatchCursor(e.store, ctx, sw.WatchID, standingLastEvalCursorKey, now.UTC().Format(time.RFC3339Nano))
	if len(due) == 0 {
		return nil
	}

	latest := due[len(due)-1]
	period := min(standingTriggerPeriod(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, latest), MaxCatchupWindow)
	catchUp := now.Sub(latest) <= period

	missedSlice := due
	if catchUp {
		missedSlice = due[:len(due)-1]
	}
	for _, mTime := range missedSlice {
		_ = insertTerminalStandingTimerRun(ctx, e.store, sw, mTime, RunMissed, 0, map[string]any{
			"kind": "timer", "scheduled_for": mTime.UTC().Format(time.RFC3339),
		}, now)
	}
	if catchUp {
		missedCount := len(due) - 1
		return enqueueStandingTimerOccurrence(ctx, e, sw, latest, missedCount, map[string]any{
			"kind": "timer", "scheduled_for": latest.UTC().Format(time.RFC3339), "catch_up": true, "missed_count": missedCount,
		}, now)
	}
	if isAtOnlyStandingWatch(sw.Conditions) {
		if next, _ := PreviewStandingSchedule(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, now, 1); len(next) == 0 {
			_ = markStandingWatchCompleted(ctx, e, sw.WatchID, sw.ProjectID, now)
		}
	}
	return nil
}

func standingTriggerPeriod(conds []awaitstore.Condition, tz string, anchor, occurrence time.Time) time.Duration {
	best, found := MaxCatchupWindow, false
	for _, c := range conds {
		if c.Kind != "timer" {
			continue
		}
		d := MaxCatchupWindow
		switch {
		case c.EveryMinutes > 0:
			d = time.Duration(c.EveryMinutes) * time.Minute
		case c.Calendar != nil:
			cc := c
			if strings.TrimSpace(cc.TimeZone) == "" {
				cc.TimeZone = tz
			}
			if next, err := NextTimerOccurrences(cc, anchor, occurrence, 1); err == nil && len(next) > 0 {
				d = next[0].Sub(occurrence)
			}
		}
		if d > 0 && (!found || d < best) {
			best, found = d, true
		}
	}
	return best
}

func fireStandingTimer(ctx context.Context, e *StandingEngine, watchID string, now time.Time) error {
	txRead, err := e.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	reg, err := loadRegistrationByIDTx(ctx, txRead, watchID)
	_ = txRead.Rollback()
	if errors.Is(err, sql.ErrNoRows) || (err == nil && reg.Lifecycle.State != StateEnabled) {
		retireWatchRuntime(e.store, watchID)
		return nil
	}
	if err != nil {
		return err
	}
	sw, err := e.Get(ctx, reg.Owner.ProjectID, watchID)
	if err != nil {
		return err
	}

	lastEval := sw.IntervalAnchor
	if raw := reg.Cursors[standingLastEvalCursorKey]; raw != "" {
		if parsed, pErr := time.Parse(time.RFC3339Nano, raw); pErr == nil {
			lastEval = parsed.UTC()
		}
	}
	due, err := dueStandingTimerOccurrences(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, lastEval, now)
	if err != nil {
		return err
	}
	if len(due) > 0 {
		latest := due[len(due)-1]
		_ = updateWatchCursor(e.store, ctx, watchID, standingLastEvalCursorKey, latest.UTC().Format(time.RFC3339Nano))
		for _, occ := range due {
			if err := enqueueStandingTimerOccurrence(ctx, e, sw, occ, 0, map[string]any{
				"kind": "timer", "scheduled_for": occ.UTC().Format(time.RFC3339),
			}, now); err != nil {
				return err
			}
		}
	}
	if updatedSW, uErr := e.Get(ctx, sw.ProjectID, watchID); uErr == nil && updatedSW.State == StateEnabled {
		armStandingWatchRuntime(e, updatedSW, now)
	}
	return e.DispatchProjectRuns(ctx, sw.ProjectID)
}

func dueStandingTimerOccurrences(conds []awaitstore.Condition, tz string, anchor, after, upTo time.Time) ([]time.Time, error) {
	var all []time.Time
	for _, c := range conds {
		if c.Kind != "timer" {
			continue
		}
		cc := c
		if strings.TrimSpace(cc.TimeZone) == "" {
			cc.TimeZone = tz
		}
		cur := after
		for range 512 {
			next, err := NextTimerOccurrences(cc, anchor, cur, 8)
			if err != nil || len(next) == 0 {
				if err != nil {
					return nil, err
				}
				break
			}
			stopped := false
			for _, t := range next {
				if t.After(upTo) {
					stopped = true
					break
				}
				all = append(all, t)
				cur = t
			}
			if stopped {
				break
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Before(all[j]) })
	out := all[:0]
	for _, t := range all {
		if len(out) == 0 || !out[len(out)-1].Equal(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func enqueueStandingTimerOccurrence(ctx context.Context, e *StandingEngine, sw StandingWatch, scheduledFor time.Time, missedCount int, detail map[string]any, now time.Time) error {
	runKey := fmt.Sprintf("timer:%s:%s", sw.WatchID, scheduledFor.UTC().Format(time.RFC3339))
	var gateSnapshot string
	gateUnchanged := false
	if sw.Gate != nil {
		changed, snap, gErr := evaluateStandingGate(ctx, e, sw)
		if gErr != nil {
			return gErr
		}
		gateSnapshot, gateUnchanged = snap, !changed
	}

	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var existingID string
	if tx.QueryRowContext(ctx, `SELECT run_id FROM watch_runs WHERE run_key = ?`, runKey).Scan(&existingID) == nil {
		return nil
	}

	nowStr, schedStr := now.UTC().Format(time.RFC3339Nano), scheduledFor.UTC().Format(time.RFC3339Nano)
	detailBytes, _ := json.Marshal(detail)
	if sw.Target.Kind == "existing_chat" {
		_, _ = tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'skipped_busy', ended_at = ? WHERE watch_id = ? AND status = 'pending'`, nowStr, sw.WatchID)
	}

	status := RunPending
	var endedAt any
	if _, hasActive := activeRunForWatchTx(ctx, tx, sw.WatchID); hasActive {
		status, endedAt = RunSkippedOverlap, nowStr
	} else if gateUnchanged {
		status, endedAt = RunSkippedUnchanged, nowStr
	} else if dispatchedToday, cErr := countDispatchedRunsTodayTx(ctx, tx, sw.WatchID, sw.TimeZone, now); cErr != nil {
		return cErr
	} else if dispatchedToday >= sw.DailyRunCap {
		status, endedAt = RunSkippedDailyCap, nowStr
	}

	runID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_runs (
		run_id, watch_id, project_id, run_key, status, scheduled_for, missed_count,
		condition_detail_json, gate_snapshot_json, prompt_snapshot, spend_limit_usd, allow_unpriced,
		created_at, ended_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`,
		runID, sw.WatchID, sw.ProjectID, runKey, string(status), schedStr, missedCount,
		string(detailBytes), gateSnapshot, sw.Prompt, sw.RunSpendLimitUSD, boolToInt(sw.AllowUnpriced),
		nowStr, endedAt); err != nil {
		return err
	}
	publishWatchEventTx(ctx, e, tx, "watch_run.updated", sw.ProjectID, "", sw.WatchID, runID, sw.Revision)
	if err := tx.Commit(); err != nil {
		return err
	}
	if status != RunPending && isAtOnlyStandingWatch(sw.Conditions) {
		if next, _ := PreviewStandingSchedule(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, scheduledFor, 1); len(next) == 0 {
			_ = markStandingWatchCompleted(ctx, e, sw.WatchID, sw.ProjectID, now)
		}
	}
	return nil
}

func insertTerminalStandingTimerRun(ctx context.Context, s *Store, sw StandingWatch, scheduledFor time.Time, status WatchRunStatus, missedCount int, detail map[string]any, now time.Time) error {
	runKey := fmt.Sprintf("timer:%s:%s", sw.WatchID, scheduledFor.UTC().Format(time.RFC3339))
	nowStr, schedStr := now.UTC().Format(time.RFC3339Nano), scheduledFor.UTC().Format(time.RFC3339Nano)
	detailBytes, _ := json.Marshal(detail)
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO watch_runs (
		run_id, watch_id, project_id, run_key, status, scheduled_for, missed_count,
		condition_detail_json, prompt_snapshot, spend_limit_usd, allow_unpriced, created_at, ended_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), sw.WatchID, sw.ProjectID, runKey, string(status), schedStr, missedCount,
		string(detailBytes), sw.Prompt, sw.RunSpendLimitUSD, boolToInt(sw.AllowUnpriced), nowStr, nowStr)
	return err
}

type coalescedStandingChange struct {
	OccurrenceID string         `json:"occurrence_id"`
	SourceKind   string         `json:"source_kind"`
	Evidence     map[string]any `json:"evidence"`
	ObservedAt   string         `json:"observed_at"`
}

func recordStandingSourceTrigger(ctx context.Context, e *StandingEngine, reg Registration, match MatchInput, cursorUpdates map[string]string) (bool, error) {
	now := e.store.clock.Now()
	for k, v := range cursorUpdates {
		_ = updateWatchCursor(e.store, ctx, reg.WatchID, k, v)
	}
	sw, err := e.Get(ctx, reg.Owner.ProjectID, reg.WatchID)
	if err != nil || sw.State != StateEnabled {
		return false, err
	}

	var lastStartedStr string
	_ = e.store.db.QueryRowContext(ctx, `SELECT COALESCE(last_started_at, '') FROM standing_watch_settings WHERE watch_id = ?`, sw.WatchID).Scan(&lastStartedStr)
	if lastStartedStr != "" {
		if lastStarted, pErr := time.Parse(time.RFC3339Nano, lastStartedStr); pErr == nil {
			if gapEnd := lastStarted.Add(time.Duration(sw.MinGapMinutes) * time.Minute); now.Before(gapEnd) {
				payload, _ := json.Marshal(coalescedStandingChange{
					OccurrenceID: match.OccurrenceID, SourceKind: match.SourceKind,
					Evidence: match.Evidence, ObservedAt: now.UTC().Format(time.RFC3339Nano),
				})
				_ = updateWatchCursor(e.store, ctx, sw.WatchID, standingPendingChangeKey, string(payload))
				e.store.heap.Upsert(KindScheduleRun, standingGapPrefix+sw.WatchID, gapEnd)
				return true, nil
			}
		}
	}
	if err := enqueueStandingChangeRun(ctx, e, sw, match.OccurrenceID, match.Evidence, now); err != nil {
		return false, err
	}
	return true, e.DispatchProjectRuns(ctx, sw.ProjectID)
}

func flushPendingStandingChange(ctx context.Context, e *StandingEngine, watchID string, now time.Time) error {
	txRead, err := e.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	reg, err := loadRegistrationByIDTx(ctx, txRead, watchID)
	_ = txRead.Rollback()
	if errors.Is(err, sql.ErrNoRows) || (err == nil && reg.Lifecycle.State != StateEnabled) {
		return nil
	}
	if err != nil || reg.Cursors[standingPendingChangeKey] == "" {
		return err
	}
	sw, err := e.Get(ctx, reg.Owner.ProjectID, watchID)
	if err != nil || sw.State != StateEnabled {
		return err
	}
	var lastStartedStr string
	_ = e.store.db.QueryRowContext(ctx, `SELECT COALESCE(last_started_at, '') FROM standing_watch_settings WHERE watch_id = ?`, watchID).Scan(&lastStartedStr)
	if lastStartedStr != "" {
		if lastStarted, pErr := time.Parse(time.RFC3339Nano, lastStartedStr); pErr == nil {
			if gapEnd := lastStarted.Add(time.Duration(sw.MinGapMinutes) * time.Minute); now.Before(gapEnd) {
				e.store.heap.Upsert(KindScheduleRun, standingGapPrefix+watchID, gapEnd)
				return nil
			}
		}
	}
	var pending coalescedStandingChange
	if json.Unmarshal([]byte(reg.Cursors[standingPendingChangeKey]), &pending) != nil {
		return nil
	}
	_, _ = e.store.db.ExecContext(ctx,
		`UPDATE watch_registrations SET cursors_json = json_remove(cursors_json, '$.standing:pending_change'), updated_at = ? WHERE watch_id = ?`,
		now.UTC().Format(time.RFC3339Nano), watchID)
	if err := enqueueStandingChangeRun(ctx, e, sw, pending.OccurrenceID, pending.Evidence, now); err != nil {
		return err
	}
	return e.DispatchProjectRuns(ctx, sw.ProjectID)
}

func enqueueStandingChangeRun(ctx context.Context, e *StandingEngine, sw StandingWatch, occID string, detail map[string]any, now time.Time) error {
	runKey := fmt.Sprintf("change:%s:%s", sw.WatchID, occID)
	detailBytes, _ := json.Marshal(detail)
	nowStr := now.UTC().Format(time.RFC3339Nano)
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var existingID string
	if tx.QueryRowContext(ctx, `SELECT run_id FROM watch_runs WHERE run_key = ?`, runKey).Scan(&existingID) == nil {
		return nil
	}
	var pendingID string
	if tx.QueryRowContext(ctx, `SELECT run_id FROM watch_runs WHERE watch_id = ? AND status = 'pending' AND run_key LIKE 'change:%' LIMIT 1`, sw.WatchID).Scan(&pendingID) == nil && pendingID != "" {
		if _, hasActive := activeRunForWatchTx(ctx, tx, sw.WatchID); hasActive {
			if _, err := tx.ExecContext(ctx, `UPDATE watch_runs SET condition_detail_json = ?, scheduled_for = ? WHERE run_id = ?`, string(detailBytes), nowStr, pendingID); err != nil {
				return err
			}
			return tx.Commit()
		}
		if sw.Target.Kind == "existing_chat" {
			_, _ = tx.ExecContext(ctx, `UPDATE watch_runs SET status = 'skipped_busy', ended_at = ? WHERE run_id = ?`, nowStr, pendingID)
		}
	}

	status := RunPending
	var endedAt any
	if _, hasActive := activeRunForWatchTx(ctx, tx, sw.WatchID); !hasActive {
		if dispatchedToday, cErr := countDispatchedRunsTodayTx(ctx, tx, sw.WatchID, sw.TimeZone, now); cErr != nil {
			return cErr
		} else if dispatchedToday >= sw.DailyRunCap {
			status, endedAt = RunSkippedDailyCap, nowStr
		}
	}
	runID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_runs (
		run_id, watch_id, project_id, run_key, status, scheduled_for, missed_count,
		condition_detail_json, prompt_snapshot, spend_limit_usd, allow_unpriced, created_at, ended_at
	) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
		runID, sw.WatchID, sw.ProjectID, runKey, string(status), nowStr,
		string(detailBytes), sw.Prompt, sw.RunSpendLimitUSD, boolToInt(sw.AllowUnpriced), nowStr, endedAt); err != nil {
		return err
	}
	publishWatchEventTx(ctx, e, tx, "watch_run.updated", sw.ProjectID, "", sw.WatchID, runID, sw.Revision)
	return tx.Commit()
}

type persistedGateBaseline struct {
	Kind      string            `json:"kind"`
	GitState  string            `json:"git_state,omitempty"`
	Manifest  map[string]string `json:"manifest,omitempty"`
	LedgerOrd int64             `json:"ledger_ord,omitempty"`
}

func initStandingBaselines(ctx context.Context, e *StandingEngine, in preparedStandingInput, cursors map[string]string) (map[int]map[string]string, string, error) {
	fakeReq := RegisterRequest{
		Owner: Owner{Kind: OwnerPerson, ProjectID: in.ProjectID}, Conditions: in.Conditions,
		Authority: in.Authority, Cursors: cursors, Repeat: true,
	}
	manifests, _, sourceLost, err := initFileAndGitBaselines(ctx, e.store, &fakeReq)
	if err != nil {
		return nil, "", err
	}
	if sourceLost {
		return nil, "", &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "conditions", Message: "watch root or repository is unavailable"}
	}
	var gateBaseline string
	if in.Gate != nil {
		gb, gErr := captureStandingGateBaseline(ctx, e.store, in.ProjectID, in.Authority, *in.Gate)
		if gErr != nil {
			return nil, "", gErr
		}
		raw, _ := json.Marshal(gb)
		gateBaseline = string(raw)
	}
	return manifests, gateBaseline, nil
}

func captureStandingGateBaseline(ctx context.Context, s *Store, projectID string, auth Authority, gate awaitstore.Condition) (persistedGateBaseline, error) {
	grant, ok := resolveWatchRootGrant(auth, gate.Root)
	if !ok || grant.Path == "" {
		return persistedGateBaseline{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "gate.root", Message: "gate root is unavailable"}
	}
	switch gate.Kind {
	case "git_ref":
		oid, exists, err := git.ResolveFullRef(grant.Path, gate.Ref)
		if err != nil {
			return persistedGateBaseline{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "gate.ref", Message: err.Error()}
		}
		return persistedGateBaseline{Kind: "git_ref", GitState: formatGitRefState(oid, exists)}, nil
	case "file_change":
		snap, exceeded, reason, err := scanBoundedManifest(grant.Path, gate.Path, gate.Glob)
		if err != nil {
			return persistedGateBaseline{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "gate.path", Message: err.Error()}
		}
		if exceeded {
			return persistedGateBaseline{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "gate.path", Message: "gate scope exceeded manifest budget: " + reason}
		}
		ord, _ := currentProjectLedgerOrdinal(s, ctx, projectID)
		return persistedGateBaseline{Kind: "file_change", Manifest: snap, LedgerOrd: ord}, nil
	default:
		return persistedGateBaseline{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "gate.kind", Message: "unsupported gate kind"}
	}
}

func evaluateStandingGate(ctx context.Context, e *StandingEngine, sw StandingWatch) (bool, string, error) {
	if sw.Gate == nil {
		return true, "", nil
	}
	var rawBase string
	_ = e.store.db.QueryRowContext(ctx, `SELECT COALESCE(gate_baseline_json, '') FROM standing_watch_settings WHERE watch_id = ?`, sw.WatchID).Scan(&rawBase)
	cur, err := captureStandingGateBaseline(ctx, e.store, sw.ProjectID, sw.Authority, *sw.Gate)
	if err != nil {
		return false, "", err
	}
	curRaw, _ := json.Marshal(cur)
	if rawBase == "" {
		return false, string(curRaw), nil
	}
	var prev persistedGateBaseline
	if json.Unmarshal([]byte(rawBase), &prev) != nil {
		return true, string(curRaw), nil
	}
	if sw.Gate.Kind == "git_ref" {
		return cur.GitState != prev.GitState, string(curRaw), nil
	}
	grant, ok := resolveWatchRootGrant(sw.Authority, sw.Gate.Root)
	if !ok {
		return false, string(curRaw), nil
	}
	owner := Owner{Kind: OwnerPerson, ProjectID: sw.ProjectID, OwnerPersonID: sw.OwnerPersonID}
	effects, _, eErr := queryNewLedgerEffects(e.store, ctx, sw.WatchID, owner, grant, prev.LedgerOrd)
	if eErr != nil {
		return false, "", eErr
	}
	covered := map[string]bool{}
	for _, eff := range effects {
		if !selectorMatchesRelPath(eff.Path, sw.Gate.Path, sw.Gate.Glob) &&
			(eff.FromPath == "" || !selectorMatchesRelPath(eff.FromPath, sw.Gate.Path, sw.Gate.Glob)) {
			continue
		}
		covered[eff.Path] = true
		if eff.FromPath != "" {
			covered[eff.FromPath] = true
		}
		if !eff.IsOwn {
			return true, string(curRaw), nil
		}
	}
	diffs := diffManifests(prev.Manifest, cur.Manifest)
	for _, d := range diffs {
		if covered[d.path] || (d.fromPath != "" && covered[d.fromPath]) {
			continue
		}
		if _, _, isOwn := lookupLatestPathEffect(e.store, ctx, sw.WatchID, owner, grant, d.path, d.afterDigest); !isOwn {
			return true, string(curRaw), nil
		}
	}
	if len(diffs) > 0 || cur.LedgerOrd > prev.LedgerOrd {
		_, _ = e.store.db.ExecContext(ctx, `UPDATE standing_watch_settings SET gate_baseline_json = ? WHERE watch_id = ?`, string(curRaw), sw.WatchID)
	}
	return false, string(curRaw), nil
}

func isStandingWatchRunSession(ctx context.Context, s *Store, watchID, sessionID, jobID string) bool {
	watchID, sessionID, jobID = strings.TrimSpace(watchID), strings.TrimSpace(sessionID), strings.TrimSpace(jobID)
	if watchID == "" || (sessionID == "" && jobID == "") {
		return false
	}
	var matched int
	if sessionID != "" {
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT session_id FROM standing_watch_chat_provenance WHERE watch_id = ? AND session_id = ?
			UNION ALL
			SELECT st.id FROM sessions st JOIN standing_watch_chat_provenance p ON p.session_id = st.parent_session_id WHERE p.watch_id = ? AND st.id = ?
			UNION ALL
			SELECT r.session_id FROM watch_runs r WHERE r.watch_id = ? AND r.session_id = ? AND r.status IN ('running', 'waiting_for_owner')
		)`, watchID, sessionID, watchID, sessionID, watchID, sessionID).Scan(&matched)
		if err == nil && matched > 0 {
			return true
		}
	}
	if jobID != "" {
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worker_jobs wj
			JOIN standing_watch_chat_provenance p ON p.session_id = wj.parent_session_id
			WHERE p.watch_id = ? AND wj.id = ?`, watchID, jobID).Scan(&matched)
		if err == nil && matched > 0 {
			return true
		}
	}
	return false
}
