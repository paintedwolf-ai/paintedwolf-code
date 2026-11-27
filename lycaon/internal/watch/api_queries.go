package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/db"
)

// NotifyEvents wakes the host outbox after a watch or run transaction commits.
func NotifyEvents(s *Store) {
	if s == nil || s.standing == nil {
		return
	}
	if fn := s.standing.callbacks().EventCommitNotify; fn != nil {
		fn()
	}
}

func publishAgentWatchEventTx(ctx context.Context, s *Store, tx *sql.Tx, reg Registration, revision int) {
	if s == nil || s.standing == nil {
		return
	}
	publishWatchEventTx(ctx, s.standing, tx, "watch.updated", reg.Owner.ProjectID, reg.Owner.SessionID, reg.WatchID, "", revision)
}

// ListProjectAgentWatches returns session- and job-owned watch registrations in projectID.
func ListProjectAgentWatches(ctx context.Context, s *Store, projectID string) ([]Registration, error) {
	if !s.Configured() || strings.TrimSpace(projectID) == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+registrationColumns+`
		FROM watch_registrations
		WHERE project_id = ? AND owner_kind IN ('session', 'job')
		ORDER BY created_at ASC, seq ASC, watch_id ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Registration
	for rows.Next() {
		reg, sErr := scanRegistration(rows)
		if sErr != nil {
			return nil, sErr
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// GetProjectWatchRegistration loads any watch registration in projectID by watchID.
func GetProjectWatchRegistration(ctx context.Context, s *Store, projectID, watchID string) (Registration, error) {
	if !s.Configured() {
		return Registration{}, &UnknownHandleError{Handle: watchID}
	}
	reg, err := scanRegistration(s.db.QueryRowContext(ctx, `SELECT `+registrationColumns+`
		FROM watch_registrations WHERE watch_id = ? AND project_id = ?`,
		strings.TrimSpace(watchID), strings.TrimSpace(projectID)))
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, &UnknownHandleError{Handle: watchID}
	}
	return reg, err
}

// ListSessionAgentWatches returns session- and worker-owned watches in sessionID.
func ListSessionAgentWatches(ctx context.Context, s *Store, sessionID string) ([]Registration, error) {
	if !s.Configured() || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+registrationColumns+`
		FROM watch_registrations
		WHERE session_id = ? AND owner_kind IN ('session', 'job')
		ORDER BY seq ASC`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Registration
	for rows.Next() {
		reg, sErr := scanRegistration(rows)
		if sErr != nil {
			return nil, sErr
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// GetSessionAgentWatch loads one session or worker watch in sessionID by handle without consuming matches.
func GetSessionAgentWatch(ctx context.Context, s *Store, sessionID, handle string) (Registration, error) {
	if !s.Configured() {
		return Registration{}, &UnknownHandleError{Handle: handle}
	}
	reg, err := scanRegistration(s.db.QueryRowContext(ctx, `SELECT `+registrationColumns+`
		FROM watch_registrations
		WHERE session_id = ? AND handle = ? AND owner_kind IN ('session', 'job')`,
		strings.TrimSpace(sessionID), strings.TrimSpace(handle)))
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, &UnknownHandleError{Handle: handle}
	}
	return reg, err
}

// SetSessionAgentWatchDelivery updates delivery on a session or worker watch and returns the refreshed record.
func SetSessionAgentWatchDelivery(ctx context.Context, s *Store, sessionID, handle string, mode DeliveryMode) (Registration, error) {
	reg, err := GetSessionAgentWatch(ctx, s, sessionID, handle)
	if err != nil {
		return Registration{}, err
	}
	if _, err := s.SetDelivery(ctx, reg.Owner, handle, mode); err != nil {
		return Registration{}, err
	}
	return GetSessionAgentWatch(ctx, s, sessionID, handle)
}

// LoadFinalReceipt returns any stored cancel/keep receipt and the latest recorded match for watchID.
func LoadFinalReceipt(ctx context.Context, database db.DBTX, watchID string) (*CancelReceipt, *RecordedMatch, *time.Time, error) {
	if database == nil || strings.TrimSpace(watchID) == "" {
		return nil, nil, nil, nil
	}
	watchID = strings.TrimSpace(watchID)
	var savedReceipt, updatedStr string
	err := database.QueryRowContext(ctx,
		`SELECT COALESCE(final_receipt_json, ''), updated_at FROM watch_registrations WHERE watch_id = ?`,
		watchID).Scan(&savedReceipt, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var recordedAt *time.Time
	if parsed, pErr := time.Parse(time.RFC3339Nano, updatedStr); pErr == nil {
		u := parsed.UTC()
		recordedAt = &u
	}
	var cancelRec *CancelReceipt
	if savedReceipt != "" {
		var r CancelReceipt
		if json.Unmarshal([]byte(savedReceipt), &r) == nil && r.Handle != "" {
			cancelRec = &r
		}
	}
	var m RecordedMatch
	var evidenceStr, createdStr string
	var restrictedInt int
	mErr := database.QueryRowContext(ctx, `SELECT match_id, watch_id, seq, source_kind, source_occurrence_id,
		outcome, evidence_json, COALESCE(retained_ref, ''), restricted, created_at
		FROM watch_matches WHERE watch_id = ? ORDER BY seq DESC LIMIT 1`, watchID).Scan(
		&m.MatchID, &m.WatchID, &m.Seq, &m.SourceKind, &m.OccurrenceID,
		&m.Outcome, &evidenceStr, &m.RetainedRef, &restrictedInt, &createdStr)
	if mErr == nil {
		m.Evidence = json.RawMessage(evidenceStr)
		m.Restricted = restrictedInt == 1
		if parsed, pErr := time.Parse(time.RFC3339Nano, createdStr); pErr == nil {
			u := parsed.UTC()
			recordedAt = &u
		}
		return cancelRec, &m, recordedAt, nil
	}
	if !errors.Is(mErr, sql.ErrNoRows) {
		return cancelRec, nil, recordedAt, mErr
	}
	return cancelRec, nil, recordedAt, nil
}

// ListWatchRunsWindow returns active/queued runs and runs ended since endedSince, grouped by watch_id.
func ListWatchRunsWindow(ctx context.Context, s *Store, projectID string, endedSince time.Time) (map[string][]WatchRun, error) {
	if !s.Configured() || strings.TrimSpace(projectID) == "" {
		return nil, nil
	}
	sinceStr := endedSince.UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, watch_id, project_id, run_key, status,
		scheduled_for, COALESCE(session_id, ''), missed_count, condition_detail_json,
		COALESCE(gate_snapshot_json, ''), prompt_snapshot, spend_limit_usd, allow_unpriced,
		created_at, COALESCE(started_at, ''), COALESCE(ended_at, '')
		FROM watch_runs
		WHERE project_id = ?
		  AND (status IN ('pending', 'queued', 'running', 'waiting_for_owner')
		       OR (ended_at IS NOT NULL AND ended_at != '' AND ended_at >= ?))
		ORDER BY scheduled_for DESC, created_at DESC, run_id DESC`,
		strings.TrimSpace(projectID), sinceStr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]WatchRun{}
	for rows.Next() {
		r, sErr := scanWatchRun(rows)
		if sErr != nil {
			return nil, sErr
		}
		out[r.WatchID] = append(out[r.WatchID], r)
	}
	return out, rows.Err()
}

// LoadWatchEventRecordTx loads the current standing watch or agent watch inside tx for outbox event projection.
func LoadWatchEventRecordTx(ctx context.Context, s *Store, tx *sql.Tx, projectID, watchID string) (*StandingWatch, *Registration, error) {
	now := time.Now().UTC()
	if s != nil && s.clock != nil {
		now = s.clock.Now()
	}
	sw, err := scanStandingWatch(tx.QueryRowContext(ctx,
		`SELECT `+standingWatchColumns+`
		 FROM watch_registrations wr
		 JOIN standing_watch_settings sws ON sws.watch_id = wr.watch_id
		 WHERE wr.watch_id = ? AND wr.project_id = ?`,
		strings.TrimSpace(watchID), strings.TrimSpace(projectID)), now)
	if err == nil {
		return &sw, nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	reg, rErr := scanRegistration(tx.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE watch_id = ?`,
		strings.TrimSpace(watchID)))
	if errors.Is(rErr, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if rErr != nil {
		return nil, nil, rErr
	}
	return nil, &reg, nil
}

// LoadWatchRunEventRecordTx loads a watch run and its standing watch name inside tx.
func LoadWatchRunEventRecordTx(ctx context.Context, tx *sql.Tx, runID string) (WatchRun, string, int, error) {
	run, err := scanWatchRun(tx.QueryRowContext(ctx, `SELECT run_id, watch_id, project_id, run_key, status,
		scheduled_for, COALESCE(session_id, ''), missed_count, condition_detail_json,
		COALESCE(gate_snapshot_json, ''), prompt_snapshot, spend_limit_usd, allow_unpriced,
		created_at, COALESCE(started_at, ''), COALESCE(ended_at, '')
		FROM watch_runs WHERE run_id = ?`, strings.TrimSpace(runID)))
	if err != nil {
		return WatchRun{}, "", 0, err
	}
	var name string
	var rev int
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(sws.name, ''), COALESCE(wr.revision, 1)
		FROM watch_registrations wr
		LEFT JOIN standing_watch_settings sws ON sws.watch_id = wr.watch_id
		WHERE wr.watch_id = ?`, run.WatchID).Scan(&name, &rev)
	if rev <= 0 {
		rev = 1
	}
	return run, name, rev, nil
}

// IsAgentWatchKeepable reports whether reg is live and uses only person-ownable conditions.
func IsAgentWatchKeepable(reg Registration) bool {
	if reg.Owner.Kind == OwnerPerson || reg.Lifecycle.State != StateLive || len(reg.Conditions) == 0 {
		return false
	}
	for _, c := range reg.Conditions {
		if c.Kind != "timer" && c.Kind != "git_ref" && c.Kind != "file_change" {
			return false
		}
	}
	return true
}

// SummarizeConditions formats watch conditions for Activity rows and proposal cards.
func SummarizeConditions(conditions []awaitstore.Condition, timeZone string) string {
	if len(conditions) == 0 {
		return "Watch"
	}
	parts := make([]string, 0, len(conditions))
	for _, c := range conditions {
		parts = append(parts, summarizeOneCondition(c, timeZone))
	}
	return strings.Join(parts, " or ")
}

func summarizeOneCondition(c awaitstore.Condition, timeZone string) string {
	switch c.Kind {
	case "timer":
		if c.EveryMinutes > 0 {
			return FormatEveryMinutesSummary(c.EveryMinutes)
		}
		if c.Calendar != nil {
			dayPart := formatCalendarDaysSummary(c.Calendar.Days)
			timePart := strings.Join(c.Calendar.Times, ", ")
			if strings.TrimSpace(timeZone) != "" {
				return fmt.Sprintf("%s at %s (%s)", dayPart, timePart, strings.TrimSpace(timeZone))
			}
			return fmt.Sprintf("%s at %s", dayPart, timePart)
		}
		if strings.TrimSpace(c.At) != "" {
			if strings.TrimSpace(timeZone) != "" {
				return fmt.Sprintf("Once at %s (%s)", strings.TrimSpace(c.At), strings.TrimSpace(timeZone))
			}
			return fmt.Sprintf("Once at %s", strings.TrimSpace(c.At))
		}
		if c.DelayMS > 0 {
			mins := (c.DelayMS + 59999) / 60000
			if mins <= 1 {
				return "In 1 minute"
			}
			return fmt.Sprintf("In %d minutes", mins)
		}
		return "Timer"
	case "git_ref":
		ref := strings.TrimSpace(c.Ref)
		if ref == "" {
			ref = "HEAD"
		}
		return fmt.Sprintf("When %s changes", ref)
	case "file_change":
		p := strings.TrimSpace(c.Path)
		if p == "" || p == "." {
			if g := strings.TrimSpace(c.Glob); g != "" {
				return fmt.Sprintf("When files matching %s change", g)
			}
			return "When workspace files change"
		}
		return fmt.Sprintf("When files in %s change", p)
	case ConditionProcessDone:
		if len(c.Handles) > 0 {
			return fmt.Sprintf("When command %s finishes", c.Handles[0])
		}
		return "When command finishes"
	case "process_output":
		if len(c.Handles) > 0 {
			return fmt.Sprintf("When command %s writes output", c.Handles[0])
		}
		return "When command writes output"
	case "http_ready":
		if c.URL != "" {
			return fmt.Sprintf("When %s is ready", c.URL)
		}
		return "When HTTP endpoint is ready"
	case "port_ready":
		if c.Port > 0 {
			return fmt.Sprintf("When port %d is ready", c.Port)
		}
		return "When port is ready"
	default:
		return strings.ReplaceAll(c.Kind, "_", " ")
	}
}

func formatCalendarDaysSummary(days any) string {
	switch v := days.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "every_day":
			return "Every day"
		case "weekdays":
			return "Weekdays"
		default:
			if len(v) > 0 {
				return strings.ToUpper(v[:1]) + strings.ToLower(v[1:])
			}
		}
	case []string:
		return joinDayNames(v)
	case []any:
		strs := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				strs = append(strs, s)
			}
		}
		return joinDayNames(strs)
	}
	return "Scheduled days"
}

func joinDayNames(days []string) string {
	if len(days) == 1 {
		switch strings.ToLower(strings.TrimSpace(days[0])) {
		case "every_day":
			return "Every day"
		case "weekdays":
			return "Weekdays"
		}
	}
	out := make([]string, 0, len(days))
	for _, d := range days {
		d = strings.TrimSpace(d)
		if d != "" {
			out = append(out, strings.ToUpper(d[:1])+strings.ToLower(d[1:]))
		}
	}
	if len(out) == 0 {
		return "Scheduled days"
	}
	return strings.Join(out, ", ")
}
